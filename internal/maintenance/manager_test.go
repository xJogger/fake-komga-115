package maintenance

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestManagerRunsSeriesIndexAndSkipsExisting(t *testing.T) {
	store, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	insertMaintenanceCatalog(t, store)

	calls := 0
	manager := newManager(
		store,
		func(ctx context.Context, book database.Book, force bool) (bool, error) {
			calls++
			if !force && book.ID == "book-1" {
				return false, nil
			}
			return true, nil
		},
		func(context.Context, string, bool) (bool, error) {
			return true, nil
		},
		slog.Default(),
	)
	defer manager.Close()

	run, err := manager.StartSeriesIndex(ctx, "series", false)
	if err != nil {
		t.Fatal(err)
	}
	run = waitRun(t, manager, run.ID)
	if run.Status != "success" || run.TotalItems != 2 || run.ProcessedItems != 2 ||
		run.GeneratedCount != 1 || run.SkippedCount != 1 || run.FailedCount != 0 ||
		calls != 2 {
		t.Fatalf("run=%#v calls=%d", run, calls)
	}
}

func TestManagerStartsAndCancelsQueuedBookIndex(t *testing.T) {
	store, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	insertMaintenanceCatalog(t, store)

	block := make(chan struct{})
	manager := newManager(
		store,
		func(ctx context.Context, book database.Book, force bool) (bool, error) {
			<-block
			return true, nil
		},
		func(context.Context, string, bool) (bool, error) {
			return true, nil
		},
		slog.Default(),
	)
	defer manager.Close()

	first, err := manager.StartBookIndex(ctx, "book-1", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.StartBookIndex(ctx, "book-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Cancel(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	close(block)
	first = waitRun(t, manager, first.ID)
	second = waitRun(t, manager, second.ID)
	if first.Status != "success" || second.Status != "canceled" {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}

func insertMaintenanceCatalog(t *testing.T, store *database.Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "library", Name: "Library", RootCID: "root", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES('series','library','series-cid','Series','Series',?,?,'scan');
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,
 number_sort,created_at,updated_at,seen_scan_id
) VALUES
 ('book-1','series','library','file-1','series-cid','Book 1.cbz',1,'pick-1',1,?,?,'scan'),
 ('book-2','series','library','file-2','series-cid','Book 2.cbz',1,'pick-2',2,?,?,'scan')`,
		now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
}

func waitRun(t *testing.T, manager *Manager, id string) Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := manager.Run(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		switch run.Status {
		case "success", "partial", "failed", "canceled":
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ := manager.Run(context.Background(), id)
	t.Fatalf("run %s did not finish: %#v", id, run)
	return Run{}
}
