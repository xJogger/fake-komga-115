package database

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestProgressTombstonesAndLargeLibraryUnpagedBooks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "tombstone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{ID: "lib1", Name: "Main", RootCID: "r1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib1", "s-active", "Active Series", []catalogBookTime{
		{created: "2024-01-01T00:00:00Z", modified: "2024-01-01T00:00:00Z"},
		{created: "2024-01-02T00:00:00Z", modified: "2024-01-02T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s-reset", "Reset Series", []catalogBookTime{
		{created: "2024-01-03T00:00:00Z", modified: "2024-01-03T00:00:00Z"},
		{created: "2024-01-04T00:00:00Z", modified: "2024-01-04T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s-untouched", "Untouched Series", []catalogBookTime{
		{created: "2024-01-05T00:00:00Z", modified: "2024-01-05T00:00:00Z"},
		{created: "2024-01-06T00:00:00Z", modified: "2024-01-06T00:00:00Z"},
	})

	// Total visible books = 6.
	// 1. Small library (default threshold = 2000): unpaged=true returns all 6 books.
	allBooks, total, err := store.BooksPage(ctx, BookQuery{Unpaged: true})
	if err != nil || total != 6 || len(allBooks) != 6 {
		t.Fatalf("small library unpaged: total=%d len=%d err=%v", total, len(allBooks), err)
	}

	// Mark s-active-book-a in progress, and mark+delete s-reset-book-a to create a tombstone.
	activeBook, _ := store.BookByID(ctx, "s-active-book-a")
	resetBook, _ := store.BookByID(ctx, "s-reset-book-a")
	page := 3
	completedFalse := false
	completedTrue := true
	if err := store.UpdateBookReadProgress(ctx, activeBook, &completedFalse, &page); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateBookReadProgress(ctx, resetBook, &completedTrue, &page); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBookReadProgress(ctx, resetBook.ID); err != nil {
		t.Fatal(err)
	}

	// 2. Large library (threshold = 3 < 6): returns ALL books of s-active (2) + s-reset (2) = 4 books,
	// skipping untouched s-untouched (2).
	store.unpagedBookThreshold = 3
	scoped, scopedTotal, err := store.BooksPage(ctx, BookQuery{Unpaged: true})
	if err != nil {
		t.Fatal(err)
	}
	if scopedTotal != 4 || len(scoped) != 4 {
		t.Fatalf("large library scoped: total=%d len=%d want 4", scopedTotal, len(scoped))
	}
	for _, b := range scoped {
		if b.SeriesID == "s-untouched" {
			t.Fatalf("untouched series book %s leaked into scoped result", b.ID)
		}
	}

	// 3. Expired (>90d) tombstone is excluded from scoped result.
	expired := time.Now().UTC().Add(-95 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`UPDATE book_read_progress_tombstones SET deleted_at=? WHERE series_id='s-reset'`, expired); err != nil {
		t.Fatal(err)
	}
	scoped2, scopedTotal2, err := store.BooksPage(ctx, BookQuery{Unpaged: true})
	if err != nil {
		t.Fatal(err)
	}
	if scopedTotal2 != 2 || len(scoped2) != 2 {
		t.Fatalf("after tombstone expiry: total=%d len=%d want 2", scopedTotal2, len(scoped2))
	}
	for _, b := range scoped2 {
		if b.SeriesID != "s-active" {
			t.Fatalf("unexpected series %s after tombstone expiry", b.SeriesID)
		}
	}
}

func TestChunkedBatchLookupsExceedingSQLiteVarLimit(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "chunk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{ID: "lib1", Name: "Main", RootCID: "r1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	const count = 1100
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seriesIDs := make([]string, 0, count)
	bookIDs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		sid := fmt.Sprintf("s_%04d", i)
		bid := fmt.Sprintf("b_%04d", i)
		seriesIDs = append(seriesIDs, sid)
		bookIDs = append(bookIDs, bid)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES(?,'lib1',?,?, ?,?,?,'scan')`, sid, "cid-"+sid, sid, sid, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES(?,?,'lib1',?,'p',?,1,'pk',1,?,?,'scan')`, bid, sid, "f-"+bid, bid+".cbz", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	sMap, err := store.SeriesByIDs(ctx, seriesIDs)
	if err != nil || len(sMap) != count {
		t.Fatalf("SeriesByIDs len=%d err=%v", len(sMap), err)
	}
	sList := make([]Series, 0, len(sMap))
	for _, s := range sMap {
		sList = append(sList, s)
	}
	mMap, err := store.SeriesMetadatas(ctx, sList)
	if err != nil || len(mMap) != count {
		t.Fatalf("SeriesMetadatas len=%d err=%v", len(mMap), err)
	}
	spMap, err := store.SeriesReadProgresses(ctx, seriesIDs)
	if err != nil || len(spMap) != count {
		t.Fatalf("SeriesReadProgresses len=%d err=%v", len(spMap), err)
	}
	bpMap, err := store.BookReadProgresses(ctx, bookIDs)
	if err != nil || bpMap == nil {
		t.Fatalf("BookReadProgresses err=%v", err)
	}
}
