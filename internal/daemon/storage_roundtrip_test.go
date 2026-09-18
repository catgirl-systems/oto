package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/catgirl-systems/oto/internal/storage"
)

func TestStorageUnsignedOperationalRoundTrips(t *testing.T) {
	owner, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	must(t, err)
	defer owner.Close()
	ctx, q := context.Background(), owner.Queries()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	for _, value := range []uint64{0, 1, 1<<63 - 1, 1 << 63, ^uint64(0)} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			d := Download{ID: "d-1", Username: "peer", Filename: "file", Size: value, Offset: value, FilterBypass: true, State: "paused", CreatedAt: stamp, UpdatedAt: stamp, RetryAt: time.Unix(0, 0).UTC()}
			must(t, q.UpsertDownload(ctx, downloadParams(d)))
			row, err := q.GetDownload(ctx, d.ID)
			must(t, err)
			got, err := fromDownload(row)
			failIfFmt(t, err != nil || !reflect.DeepEqual(got, d), "download: %+v != %+v (%v)", got, d, err)
			u := Upload{Transfer: Transfer{ID: "upload:1", Username: "peer", Filename: "file", Direction: "upload", State: "completed", Done: value, Total: value, SpeedBPS: value, ElapsedMS: &value, ETASeconds: &value, Queue: ^uint32(0)}, QueueOrder: value, CreatedAt: stamp, QueuedAt: stamp, UpdatedAt: stamp}
			must(t, q.UpsertUpload(ctx, uploadParams(u)))
			urow, err := q.GetUpload(ctx, u.ID)
			must(t, err)
			ugot, err := fromUpload(urow)
			failIfFmt(t, err != nil || !reflect.DeepEqual(ugot, u), "upload: %+v != %+v (%v)", ugot, u, err)
			must(t, q.SetDownloadSequence(ctx, storage.EncodeUint64(value)))
			meta, err := q.GetStateMeta(ctx)
			must(t, err)
			sequence, err := storage.DecodeUint64(meta.DownloadSequence)
			failIfFmt(t, err != nil || sequence != value, "sequence: %d != %d (%v)", sequence, value, err)
		})
	}
}
