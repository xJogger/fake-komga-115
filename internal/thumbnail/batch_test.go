package thumbnail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestBatchManagerLatestLimitAndSkip(t *testing.T) {
	store, libraryID := batchTestStore(t)
	insertBatchSeries(t, store, libraryID, "old", "Old", "2024-01-01T00:00:00Z")
	insertBatchSeries(t, store, libraryID, "middle", "Middle", "2025-01-01T00:00:00Z")
	insertBatchSeries(t, store, libraryID, "new", "New", "2026-01-01T00:00:00Z")

	var mu sync.Mutex
	var selected []string
	manager := newBatchManager(store, func(_ context.Context, seriesID string) (bool, error) {
		mu.Lock()
		selected = append(selected, seriesID)
		mu.Unlock()
		return seriesID == "new", nil
	}, batchTestLogger())
	defer manager.Close()

	started, err := manager.Start(context.Background(), libraryID, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	run := waitBatchRun(t, manager, started.ID)
	if run.Status != "success" || run.TotalSeries != 2 || run.ProcessedSeries != 2 {
		t.Fatalf("unexpected run: %#v", run)
	}
	if run.GeneratedCount != 1 || run.SkippedCount != 1 || run.FailedCount != 0 {
		t.Fatalf("unexpected counts: %#v", run)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(selected) != 2 || selected[0] != "new" || selected[1] != "middle" {
		t.Fatalf("latest series selection/order = %#v", selected)
	}
}

func TestBatchManagerContinuesAfterFailure(t *testing.T) {
	store, libraryID := batchTestStore(t)
	insertBatchSeries(t, store, libraryID, "one", "One", "2024-01-01T00:00:00Z")
	insertBatchSeries(t, store, libraryID, "two", "Two", "2025-01-01T00:00:00Z")
	insertBatchSeries(t, store, libraryID, "three", "Three", "2026-01-01T00:00:00Z")

	manager := newBatchManager(store, func(_ context.Context, seriesID string) (bool, error) {
		if seriesID == "two" {
			return false, errors.New("cannot read https://secret.example/page")
		}
		return true, nil
	}, batchTestLogger())
	defer manager.Close()

	started, err := manager.Start(context.Background(), libraryID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	run := waitBatchRun(t, manager, started.ID)
	if run.Status != "partial" || run.ProcessedSeries != 3 ||
		run.GeneratedCount != 2 || run.FailedCount != 1 {
		t.Fatalf("unexpected partial run: %#v", run)
	}
	if len(run.Errors) != 1 || run.Errors[0] != "Two: cannot read [redacted-url]" {
		t.Fatalf("unexpected safe errors: %#v", run.Errors)
	}
}

func TestBatchManagerCancel(t *testing.T) {
	store, libraryID := batchTestStore(t)
	insertBatchSeries(t, store, libraryID, "one", "One", "2026-01-01T00:00:00Z")

	entered := make(chan struct{})
	manager := newBatchManager(store, func(ctx context.Context, _ string) (bool, error) {
		close(entered)
		<-ctx.Done()
		return false, ctx.Err()
	}, batchTestLogger())
	defer manager.Close()

	started, err := manager.Start(context.Background(), libraryID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("thumbnail job did not begin")
	}
	if err := manager.Cancel(context.Background(), started.ID); err != nil {
		t.Fatal(err)
	}
	run := waitBatchRun(t, manager, started.ID)
	if run.Status != "canceled" || run.ProcessedSeries != 0 || run.FailedCount != 0 {
		t.Fatalf("unexpected canceled run: %#v", run)
	}
}

func batchTestStore(t *testing.T) (*database.Store, string) {
	t.Helper()
	store, err := database.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	libraryID := "library"
	if err := store.UpsertLibrary(context.Background(), database.Library{
		ID: libraryID, Name: "Library", RootCID: "root", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	return store, libraryID
}

func insertBatchSeries(
	t *testing.T,
	store *database.Store,
	libraryID string,
	seriesID string,
	name string,
	modified string,
) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(
 id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,?,'scan')`,
		seriesID, libraryID, "cid-"+seriesID, name, name, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'scan')`,
		"book-"+seriesID, seriesID, libraryID, "file-"+seriesID, "cid-"+seriesID,
		"001.cbz", 100, "pick-"+seriesID, "", modified, modified, 1, now, now); err != nil {
		t.Fatal(err)
	}
}

func waitBatchRun(t *testing.T, manager *BatchManager, runID string) BatchRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := manager.Run(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		switch run.Status {
		case "success", "partial", "failed", "canceled":
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("thumbnail job did not finish")
	return BatchRun{}
}

func batchTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
