package archive

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestVolumeIndexPrefetchThresholdAndDisabledSetting(t *testing.T) {
	store, current := volumePrefetchTestStore(t)
	var calls atomic.Int64
	built := make(chan string, 2)
	prefetcher := newVolumeIndexPrefetcher(
		store,
		func(_ context.Context, book database.Book) ([]PageEntry, error) {
			calls.Add(1)
			built <- book.ID
			return []PageEntry{{Number: 1}}, nil
		},
		testLogger(),
	)
	defer prefetcher.Close()

	prefetcher.Trigger(current, 10, 20)
	assertNoVolumePrefetch(t, built)
	if err := store.SetSetting(
		context.Background(), "volume_index_prefetch_enabled", "true",
	); err != nil {
		t.Fatal(err)
	}
	prefetcher.Trigger(current, 9, 20)
	assertNoVolumePrefetch(t, built)
	prefetcher.Trigger(current, 10, 20)
	select {
	case id := <-built:
		if id != "book-b" {
			t.Fatalf("prefetched %q, want book-b", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("next volume index was not prefetched at the configured threshold")
	}
	if calls.Load() != 1 {
		t.Fatalf("build calls=%d, want 1", calls.Load())
	}
}

func TestVolumeIndexPrefetchMergesDuplicateRequests(t *testing.T) {
	store, current := volumePrefetchTestStore(t)
	if err := store.SetSetting(
		context.Background(), "volume_index_prefetch_enabled", "true",
	); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	var calls atomic.Int64
	prefetcher := newVolumeIndexPrefetcher(
		store,
		func(ctx context.Context, book database.Book) ([]PageEntry, error) {
			calls.Add(1)
			started <- book.ID
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []PageEntry{{Number: 1}}, nil
		},
		testLogger(),
	)
	defer prefetcher.Close()

	prefetcher.Trigger(current, 10, 20)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("prefetch did not start")
	}
	prefetcher.Trigger(current, 11, 20)
	prefetcher.Trigger(current, 20, 20)
	assertNoVolumePrefetch(t, started)
	close(release)
	if calls.Load() != 1 {
		t.Fatalf("duplicate requests caused %d builds, want 1", calls.Load())
	}
}

func TestVolumeIndexPrefetchShortBookTriggersOnFirstPage(t *testing.T) {
	store, current := volumePrefetchTestStore(t)
	if err := store.SetSetting(
		context.Background(), "volume_index_prefetch_enabled", "true",
	); err != nil {
		t.Fatal(err)
	}
	built := make(chan string, 1)
	prefetcher := newVolumeIndexPrefetcher(
		store,
		func(_ context.Context, book database.Book) ([]PageEntry, error) {
			built <- book.ID
			return []PageEntry{{Number: 1}}, nil
		},
		testLogger(),
	)
	defer prefetcher.Close()

	prefetcher.Trigger(current, 1, 5)
	select {
	case id := <-built:
		if id != "book-b" {
			t.Fatalf("prefetched %q, want book-b", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("short Book did not prefetch its next volume from the first page")
	}
}

func TestVolumeIndexPrefetchSerializesDifferentBooks(t *testing.T) {
	store, first := volumePrefetchTestStore(t)
	ctx := context.Background()
	if err := store.SetSetting(ctx, "volume_index_prefetch_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES('series-2','library','cid-2','Series 2','Series 2',?,?,'scan')`,
		now, now); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"book-c", "book-d"} {
		if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,
 number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,'series-2','library',?,'cid-2',?,1,?,?,?,?,'scan')`,
			id, "file-"+id, id+".cbz", "pick-"+id, index+1, now, now); err != nil {
			t.Fatal(err)
		}
	}
	second, err := store.BookByID(ctx, "book-c")
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan string, 2)
	release := make(chan struct{})
	prefetcher := newVolumeIndexPrefetcher(
		store,
		func(ctx context.Context, book database.Book) ([]PageEntry, error) {
			started <- book.ID
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []PageEntry{{Number: 1}}, nil
		},
		testLogger(),
	)
	defer prefetcher.Close()

	prefetcher.Trigger(first, 10, 20)
	prefetcher.Trigger(second, 10, 20)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first volume prefetch did not start")
	}
	assertNoVolumePrefetch(t, started)
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("second volume prefetch did not start after the first completed")
	}
	release <- struct{}{}
}

func volumePrefetchTestStore(t *testing.T) (*database.Store, database.Book) {
	t.Helper()
	store, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "library", Name: "Library", RootCID: "root", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES('series','library','cid','Series','Series',?,?,'scan')`, now, now); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"book-a", "book-b"} {
		if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,
 number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,'series','library',?,'cid',?,1,?,?,?,?,'scan')`,
			id, "file-"+id, id+".cbz", "pick-"+id, index+1, now, now); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.BookByID(ctx, "book-a")
	if err != nil {
		t.Fatal(err)
	}
	return store, current
}

func assertNoVolumePrefetch(t *testing.T, started <-chan string) {
	t.Helper()
	select {
	case id := <-started:
		t.Fatalf("unexpected volume prefetch for %q", id)
	case <-time.After(150 * time.Millisecond):
	}
}
