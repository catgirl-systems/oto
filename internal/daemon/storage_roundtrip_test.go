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
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ctx, q := context.Background(), owner.Queries()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	for _, value := range []uint64{0, 1, 1<<63 - 1, 1 << 63, ^uint64(0)} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			d := Download{ID: "d-1", Username: "peer", Filename: "file", Size: value, Offset: value, FilterBypass: true, State: "paused", CreatedAt: stamp, UpdatedAt: stamp, RetryAt: time.Unix(0, 0).UTC()}
			if err := q.UpsertDownload(ctx, downloadParams(d)); err != nil {
				t.Fatal(err)
			}
			row, err := q.GetDownload(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := fromDownload(row)
			if err != nil || !reflect.DeepEqual(got, d) {
				t.Fatalf("download: %+v != %+v (%v)", got, d, err)
			}
			u := Upload{Transfer: Transfer{ID: "upload:1", Username: "peer", Filename: "file", Direction: "upload", State: "completed", Done: value, Total: value, SpeedBPS: value, ElapsedMS: &value, ETASeconds: &value, Queue: ^uint32(0)}, QueueOrder: value, CreatedAt: stamp, QueuedAt: stamp, UpdatedAt: stamp}
			if err := q.UpsertUpload(ctx, uploadParams(u)); err != nil {
				t.Fatal(err)
			}
			urow, err := q.GetUpload(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			ugot, err := fromUpload(urow)
			if err != nil || !reflect.DeepEqual(ugot, u) {
				t.Fatalf("upload: %+v != %+v (%v)", ugot, u, err)
			}
			if err := q.SetDownloadSequence(ctx, storage.EncodeUint64(value)); err != nil {
				t.Fatal(err)
			}
			meta, err := q.GetStateMeta(ctx)
			if err != nil {
				t.Fatal(err)
			}
			sequence, err := storage.DecodeUint64(meta.DownloadSequence)
			if err != nil || sequence != value {
				t.Fatalf("sequence: %d != %d (%v)", sequence, value, err)
			}
		})
	}
}
