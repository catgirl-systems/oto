package daemon

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/catgirl-systems/oto/internal/config"
	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage"
	storageDB "github.com/catgirl-systems/oto/internal/storage/db"
)

func shareBool(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func shareEntryRow(snapshotID, ordinal int64, kind, root, path, name string, entry soulseek.ShareEntry, file *soulseek.ShareFile) storageDB.InsertShareEntryParams {
	row := storageDB.InsertShareEntryParams{SnapshotID: snapshotID, Ordinal: ordinal, Kind: kind, Root: root, Path: path, Name: name, Size: storage.EncodeUint64(entry.Size), Directory: shareBool(entry.Directory), Private: shareBool(entry.Private), Vbr: shareBool(entry.VBR), VbrKnown: shareBool(entry.VBRKnown), Extension: entry.Extension, Bitrate: int64(entry.Bitrate), Duration: int64(entry.Duration), SampleRate: int64(entry.SampleRate), BitDepth: int64(entry.BitDepth)}
	if file != nil {
		row.Root, row.Path, row.Directory = file.Root, file.Path, shareBool(file.Directory)
		row.AudioSource = file.AudioSource
		row.FingerprintSize = storage.EncodeUint64(file.AudioFingerprint.Size)
		row.FingerprintMtime = file.AudioFingerprint.MTimeUnixNano
		row.FingerprintCtime = file.AudioFingerprint.CTimeUnixNano
		row.ExtractorVersion = file.AudioFingerprint.ExtractorVersion
		row.Size = storage.EncodeUint64(file.Size)
		row.Private = 0
	}
	return row
}

func localEntryRow(snapshotID, ordinal int64, file soulseek.ShareFile) storageDB.InsertShareEntryParams {
	return shareEntryRow(snapshotID, ordinal, "local", file.Root, file.Path, "", soulseek.ShareEntry{Size: file.Size, Directory: file.Directory, Bitrate: file.Bitrate, Duration: file.Duration, SampleRate: file.SampleRate, BitDepth: file.BitDepth}, &file)
}

func remoteEntryRow(snapshotID, ordinal int64, entry soulseek.ShareEntry) storageDB.InsertShareEntryParams {
	return shareEntryRow(snapshotID, ordinal, "remote", "", entry.Name, "", entry, nil)
}

func insertShareSnapshot(ctx context.Context, db *storage.DB, source, username string, createdAt int64, roots []soulseek.ShareRoot, exclusions []string) (int64, error) {
	var id int64
	err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		queries := db.Queries().WithTx(tx)
		var err error
		id, err = queries.CreateShareSnapshot(ctx, storageDB.CreateShareSnapshotParams{Source: source, NormalizedUsername: strings.ToLower(strings.TrimSpace(username)), Username: strings.TrimSpace(username), SavedAt: createdAt, CreatedAt: createdAt})
		if err != nil {
			return err
		}
		for ordinal, root := range roots {
			if err := queries.InsertShareRoot(ctx, storageDB.InsertShareRootParams{SnapshotID: id, Ordinal: int64(ordinal), Name: root.Name, Path: root.Path}); err != nil {
				return err
			}
		}
		for ordinal, exclusion := range exclusions {
			if err := queries.InsertShareExclusion(ctx, storageDB.InsertShareExclusionParams{SnapshotID: id, Ordinal: int64(ordinal), Pattern: exclusion}); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

func insertShareEntryBatch(ctx context.Context, db *storage.DB, snapshotID int64, rows []storageDB.InsertShareEntryParams) error {
	return db.WriteTx(ctx, func(tx *sql.Tx) error {
		queries := db.Queries().WithTx(tx)
		for _, row := range rows {
			if err := queries.InsertShareEntry(ctx, row); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteShareSnapshotRows(ctx context.Context, db *storage.DB, id int64) error {
	for {
		var deleted bool
		err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			queries := db.Queries().WithTx(tx)
			if err := queries.DeleteShareEntriesBatch(ctx, storageDB.DeleteShareEntriesBatchParams{SnapshotID: id, Limit: storage.ShareBatchSize}); err != nil {
				return err
			}
			if err := queries.DeleteShareRootsBatch(ctx, storageDB.DeleteShareRootsBatchParams{SnapshotID: id, Limit: storage.ShareBatchSize}); err != nil {
				return err
			}
			if err := queries.DeleteShareExclusionsBatch(ctx, storageDB.DeleteShareExclusionsBatchParams{SnapshotID: id, Limit: storage.ShareBatchSize}); err != nil {
				return err
			}
			entries, err := queries.CountShareEntries(ctx, id)
			if err != nil {
				return err
			}
			roots, err := queries.CountShareRoots(ctx, id)
			if err != nil {
				return err
			}
			exclusions, err := queries.CountShareExclusions(ctx, id)
			if err != nil {
				return err
			}
			deleted = entries != 0 || roots != 0 || exclusions != 0
			return nil
		})
		if err != nil {
			return err
		}
		if !deleted {
			return db.WriteTx(ctx, func(tx *sql.Tx) error {
				return db.Queries().WithTx(tx).DeleteStagingShareSnapshot(ctx, id)
			})
		}
	}
}

func stageShareSnapshot(ctx context.Context, db *storage.DB, source, username string, roots []soulseek.ShareRoot, exclusions []string, files []soulseek.ShareFile, entries []soulseek.ShareEntry) (int64, error) {
	return stageShareSnapshotAt(ctx, db, source, username, time.Now().UTC().UnixNano(), roots, exclusions, files, entries)
}

func stageShareSnapshotAt(ctx context.Context, db *storage.DB, source, username string, createdAt int64, roots []soulseek.ShareRoot, exclusions []string, files []soulseek.ShareFile, entries []soulseek.ShareEntry) (int64, error) {
	if db == nil {
		return 0, errors.New("daemon: state database is not open")
	}
	if len(files) > 0 && len(entries) > 0 {
		return 0, errors.New("daemon: mixed share snapshot kinds")
	}
	id, err := insertShareSnapshot(ctx, db, source, username, createdAt, roots, exclusions)
	if err != nil {
		return 0, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = deleteShareSnapshotRows(cleanupCtx, db, id)
		}
	}()
	rows := make([]storageDB.InsertShareEntryParams, 0, storage.ShareBatchSize)
	if len(files) > 0 {
		for ordinal, file := range files {
			rows = append(rows, localEntryRow(id, int64(ordinal), file))
			if len(rows) == storage.ShareBatchSize {
				if err := insertShareEntryBatch(ctx, db, id, rows); err != nil {
					return 0, err
				}
				rows = rows[:0]
			}
		}
	} else {
		for ordinal, entry := range entries {
			rows = append(rows, remoteEntryRow(id, int64(ordinal), entry))
			if len(rows) == storage.ShareBatchSize {
				if err := insertShareEntryBatch(ctx, db, id, rows); err != nil {
					return 0, err
				}
				rows = rows[:0]
			}
		}
	}
	if len(rows) > 0 {
		if err := insertShareEntryBatch(ctx, db, id, rows); err != nil {
			return 0, err
		}
	}
	cleanup = false
	return id, nil
}

func publishShareSnapshot(ctx context.Context, db *storage.DB, id int64, source, username string) error {
	return db.WriteTx(ctx, func(tx *sql.Tx) error {
		queries := db.Queries().WithTx(tx)
		result, err := queries.PublishShareSnapshot(ctx, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("daemon: share snapshot is no longer staging")
		}
		return queries.SetShareHead(ctx, storageDB.SetShareHeadParams{Source: source, NormalizedUsername: username, SnapshotID: id})
	})
}

func gcShareSnapshots(ctx context.Context, db *storage.DB) error {
	for {
		var ids []int64
		err := db.ReadSnapshot(ctx, func(snapshot *storage.ReadTx) error {
			rows, err := snapshot.Queries().ListCollectableShareSnapshots(ctx)
			if err != nil {
				return err
			}
			for _, row := range rows {
				ids = append(ids, row.ID)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := deleteShareSnapshotRows(ctx, db, id); err != nil {
				return err
			}
			// Published obsolete snapshots need the same bounded child cleanup,
			// then may be removed only when no head points at them.
			if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
				return db.Queries().WithTx(tx).DeleteShareSnapshot(ctx, id)
			}); err != nil {
				return err
			}
		}
	}
}

func (s *Service) initShareStorage() error {
	if s.stateDB == nil {
		return errors.New("daemon: state database is not open")
	}
	s.shareStorageMu.Lock()
	defer s.shareStorageMu.Unlock()
	return gcShareSnapshots(context.Background(), s.stateDB)
}

func (s *Service) persistShareIndex(index *soulseek.ShareIndex) {
	if s.stateDB == nil || index == nil {
		return
	}
	s.mu.RLock()
	current := !s.closed && (s.scanCtx == nil || s.scanCtx.Err() == nil) && s.shares == index
	s.mu.RUnlock()
	if !current {
		return
	}
	roots, files, exclusions := index.Roots(), index.Files(), index.Exclusions()
	s.shareStorageMu.Lock()
	defer s.shareStorageMu.Unlock()
	ctx := s.scanCtx
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := stageShareSnapshot(ctx, s.stateDB, "local", "", roots, exclusions, files, nil)
	if err == nil {
		s.mu.RLock()
		current = !s.closed && (s.scanCtx == nil || s.scanCtx.Err() == nil) && s.shares == index
		if current {
			err = publishShareSnapshot(ctx, s.stateDB, id, "local", "")
		}
		s.mu.RUnlock()
		if !current {
			_ = deleteShareSnapshotRows(ctx, s.stateDB, id)
			err = errShareScanDiscarded
		}
	}
	if err == nil {
		err = gcShareSnapshots(ctx, s.stateDB)
	}
	if err != nil {
		logShareStorage(err)
	}
}

func (s *Service) loadShareIndexCache(shares []config.Share, rules []string) (*soulseek.ShareIndex, error) {
	if s.stateDB == nil {
		return nil, errors.New("daemon: state database is not open")
	}
	rules, err := config.NormalizeShareExclusions(rules)
	if err != nil {
		return nil, err
	}
	var roots []soulseek.ShareRoot
	var files []soulseek.ShareFile
	var storedRules []string
	err = s.stateDB.ReadSnapshot(context.Background(), func(snapshot *storage.ReadTx) error {
		queries := snapshot.Queries()
		head, err := queries.GetShareHead(context.Background(), storageDB.GetShareHeadParams{Source: "local", NormalizedUsername: ""})
		if errors.Is(err, sql.ErrNoRows) {
			return os.ErrNotExist
		}
		if err != nil {
			return err
		}
		snapshotRow, err := queries.GetShareSnapshot(context.Background(), head.SnapshotID)
		if err != nil {
			return err
		}
		if snapshotRow.State != "published" || snapshotRow.Source != "local" {
			return errors.New("daemon: local share snapshot is not published")
		}
		rootRows, err := queries.ListShareRoots(context.Background(), head.SnapshotID)
		if err != nil {
			return err
		}
		roots = make([]soulseek.ShareRoot, len(rootRows))
		for i, row := range rootRows {
			roots[i] = soulseek.ShareRoot{Name: row.Name, Path: row.Path}
		}
		exclusionRows, err := queries.ListShareExclusions(context.Background(), head.SnapshotID)
		if err != nil {
			return err
		}
		storedRules = make([]string, len(exclusionRows))
		for i, row := range exclusionRows {
			storedRules[i] = row.Pattern
		}
		entryRows, err := queries.ListShareEntries(context.Background(), head.SnapshotID)
		if err != nil {
			return err
		}
		files = make([]soulseek.ShareFile, len(entryRows))
		for i, row := range entryRows {
			if row.Kind != "local" {
				return errors.New("daemon: local snapshot contains remote entry")
			}
			size, err := storage.DecodeUint64(row.Size)
			if err != nil {
				return err
			}
			fpSize, err := storage.DecodeUint64(row.FingerprintSize)
			if err != nil && len(row.FingerprintSize) != 0 {
				return err
			}
			files[i] = soulseek.ShareFile{Root: row.Root, Path: row.Path, Size: size, Directory: row.Directory != 0, AudioSource: row.AudioSource, AudioFingerprint: soulseek.AudioFingerprint{Size: fpSize, MTimeUnixNano: row.FingerprintMtime, CTimeUnixNano: row.FingerprintCtime, ExtractorVersion: row.ExtractorVersion}, AudioMetadata: soulseek.AudioMetadata{Bitrate: uint32(row.Bitrate), Duration: uint32(row.Duration), SampleRate: uint32(row.SampleRate), BitDepth: uint32(row.BitDepth)}}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !slices.Equal(storedRules, rules) {
		return nil, errors.New("share index cache exclusions changed")
	}
	index, err := soulseek.RestoreShareIndexWithExclusions(roots, files, rules)
	if err != nil {
		return nil, err
	}
	expectedRoots := make([]soulseek.ShareRoot, len(shares))
	for i, share := range shares {
		expectedRoots[i] = soulseek.ShareRoot{Name: share.Name, Path: share.Path}
	}
	slices.SortFunc(expectedRoots, func(a, b soulseek.ShareRoot) int { return strings.Compare(a.Name, b.Name) })
	if !slices.Equal(index.Roots(), expectedRoots) {
		return nil, errors.New("share index cache roots changed")
	}
	return index, nil
}

func loadRemoteEntries(row storageDB.ShareEntry) (soulseek.ShareEntry, error) {
	size, err := storage.DecodeUint64(row.Size)
	if err != nil {
		return soulseek.ShareEntry{}, err
	}
	return soulseek.ShareEntry{Name: row.Path, Size: size, Directory: row.Directory != 0, Private: row.Private != 0, VBR: row.Vbr != 0, VBRKnown: row.VbrKnown != 0, Extension: row.Extension, Bitrate: uint32(row.Bitrate), Duration: uint32(row.Duration), SampleRate: uint32(row.SampleRate), BitDepth: uint32(row.BitDepth)}, nil
}

func (s *Service) loadRemoteShareCache(username string) (remoteShareCache, error) {
	if s.stateDB == nil {
		return remoteShareCache{}, errors.New("daemon: state database is not open")
	}
	key, err := browseUsername(username)
	if err != nil {
		return remoteShareCache{}, err
	}
	var cache remoteShareCache
	err = s.stateDB.ReadSnapshot(context.Background(), func(snapshot *storage.ReadTx) error {
		queries := snapshot.Queries()
		head, err := queries.GetShareHead(context.Background(), storageDB.GetShareHeadParams{Source: "remote", NormalizedUsername: key})
		if err != nil {
			return err
		}
		snapshotRow, err := queries.GetShareSnapshot(context.Background(), head.SnapshotID)
		if err != nil {
			return err
		}
		if snapshotRow.State != "published" || snapshotRow.Source != "remote" {
			return errors.New("daemon: remote share snapshot is not published")
		}
		rows, err := queries.ListShareEntries(context.Background(), head.SnapshotID)
		if err != nil {
			return err
		}
		cache.Username, cache.SavedAt = snapshotRow.Username, time.Unix(0, snapshotRow.SavedAt).UTC()
		if snapshotRow.SavedAt == 0 {
			cache.SavedAt = time.Unix(0, snapshotRow.CreatedAt).UTC()
		}
		cache.Entries = make([]soulseek.ShareEntry, len(rows))
		for i, row := range rows {
			if row.Kind != "remote" {
				return errors.New("daemon: remote snapshot contains local entry")
			}
			cache.Entries[i], err = loadRemoteEntries(row)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return remoteShareCache{}, err
	}
	if cache.Username == "" {
		cache.Username = key
	}
	return cache, nil
}

func (s *Service) saveRemoteShareCache(cache remoteShareCache, revision uint64) error {
	key, err := browseUsername(cache.Username)
	if err != nil {
		return err
	}
	if s.stateDB == nil {
		return errors.New("daemon: state database is not open")
	}
	entries := append([]soulseek.ShareEntry(nil), cache.Entries...)
	ctx := s.scanCtx
	if ctx == nil {
		ctx = context.Background()
	}
	createdAt := time.Now().UTC().UnixNano()
	if !cache.SavedAt.IsZero() {
		createdAt = cache.SavedAt.UnixNano()
	}
	s.shareStorageMu.Lock()
	defer s.shareStorageMu.Unlock()
	id, err := stageShareSnapshotAt(ctx, s.stateDB, "remote", strings.TrimSpace(cache.Username), createdAt, nil, nil, nil, entries)
	if err != nil {
		return err
	}

	// Keep the revision check and head publication under s.mu. This prevents a
	// newer browse from being published between the check and the final head write.
	s.mu.Lock()
	loaded, ok := s.browses[key]
	current := ok && loaded.result.Revision == revision
	if current {
		err = publishShareSnapshot(ctx, s.stateDB, id, "remote", key)
	}
	s.mu.Unlock()
	if !current {
		_ = deleteShareSnapshotRows(ctx, s.stateDB, id)
		return ErrBrowseRevision
	}
	if err != nil {
		_ = deleteShareSnapshotRows(ctx, s.stateDB, id)
		return err
	}
	if gcErr := gcShareSnapshots(context.Background(), s.stateDB); gcErr != nil {
		log.Printf("WARN share storage garbage collection: %v", gcErr)
	}
	return nil
}

func logShareStorage(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("share storage: %v", err)
	}
}
