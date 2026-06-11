package scanner

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/id"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
)

func TestIsComicArchive(t *testing.T) {
	tests := map[string]bool{
		"book.cbz":        true,
		"book.zip":        true,
		"book.cbr":        true,
		"book.RAR":        true,
		"book.part1.rar":  true,
		"book.part01.rar": true,
		"book.part2.rar":  false,
		"book.part10.rar": false,
		"book.r00":        false,
		"book.7z":         false,
	}
	for name, want := range tests {
		if got := isComic(name); got != want {
			t.Fatalf("isComic(%q)=%v want %v", name, got, want)
		}
	}
}

func TestSaveOneShotCreatesOneSeriesPerFile(t *testing.T) {
	store, library, item := scannerTestStore(t, true)
	manager := &Manager{store: store}
	now := time.Now().UTC()
	file := oneonefive.File{
		ID: "file-1", Name: "My Comic.cbz", Size: 42, PickCode: "pick", SHA1: "sha",
		CreatedAt: now.Add(-time.Hour), ModifiedAt: now,
	}
	second := file
	second.ID = "file-2"
	second.PickCode = "pick-2"
	if err := manager.saveOneShots(context.Background(), item, queueDir{
		CID: "parent", Name: "Folder", RelativePath: "Root/Folder", ModifiedAt: now,
	}, []oneonefive.File{file, second}, false); err != nil {
		t.Fatal(err)
	}

	series, err := store.SeriesByID(context.Background(), id.OneShotSeries(library.ID, file.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !series.OneShot || series.Name != "My Comic" ||
		series.RelativePath != "Root/Folder/My Comic.cbz" || series.BooksCount != 1 {
		t.Fatalf("unexpected series: %+v", series)
	}
	book, err := store.BookByID(context.Background(), id.Book(library.ID, file.ID))
	if err != nil {
		t.Fatal(err)
	}
	if book.Name != file.Name || book.ParentCID != "parent" || book.SeriesID != series.ID {
		t.Fatalf("unexpected book: %+v", book)
	}
	secondSeries, err := store.SeriesByID(context.Background(), id.OneShotSeries(library.ID, second.ID))
	if err != nil || secondSeries.Name != series.Name || secondSeries.ID == series.ID {
		t.Fatalf("same-name file was not kept as a distinct series: %+v, %v", secondSeries, err)
	}
}

func TestModeConversionIsStagedUntilSuccessfulCommit(t *testing.T) {
	store, library, item := scannerTestStore(t, true)
	manager := &Manager{store: store}
	ctx := context.Background()
	now := time.Now().UTC()
	oldSeriesID := id.Series(library.ID, "old-folder")
	oldBookID := id.Book(library.ID, "file-1")
	nowText := now.Format(time.RFC3339Nano)
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,0,?,?,?)`,
		oldSeriesID, library.ID, "old-folder", "Old", "Root/Old", nowText, nowText, "old-scan"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,
 created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,?,?,1,?,?,?)`,
		oldBookID, oldSeriesID, library.ID, "file-1", "old-folder", "My Comic.cbz", 42, "pick",
		nowText, nowText, "old-scan"); err != nil {
		t.Fatal(err)
	}
	staged, err := manager.needsModeConversion(ctx, item)
	if err != nil || !staged {
		t.Fatalf("staged=%v err=%v", staged, err)
	}
	file := oneonefive.File{
		ID: "file-1", Name: "My Comic.cbz", Size: 42, PickCode: "pick",
		CreatedAt: now, ModifiedAt: now,
	}
	dir := queueDir{CID: "old-folder", RelativePath: "Root/Old", ModifiedAt: now}
	if err := manager.saveOneShot(ctx, item, dir, file, staged); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeriesByID(ctx, oldSeriesID); err != nil {
		t.Fatalf("old series changed before commit: %v", err)
	}
	if book, err := store.BookByID(ctx, oldBookID); err != nil || book.SeriesID != oldSeriesID {
		t.Fatalf("old book changed before commit: %+v, %v", book, err)
	}

	manager.discardStaging(item, staged)
	if _, err := store.SeriesByID(ctx, oldSeriesID); err != nil {
		t.Fatalf("discard removed old series: %v", err)
	}
	if err := manager.saveOneShot(ctx, item, dir, file, staged); err != nil {
		t.Fatal(err)
	}
	if err := manager.commitSuccessfulScan(ctx, item, staged); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeriesByID(ctx, oldSeriesID); err != sql.ErrNoRows {
		t.Fatalf("old series remains after conversion: %v", err)
	}
	newSeries, err := store.SeriesByID(ctx, id.OneShotSeries(library.ID, file.ID))
	if err != nil || !newSeries.OneShot {
		t.Fatalf("new one-shot series: %+v, %v", newSeries, err)
	}
}

func scannerTestStore(t *testing.T, oneShot bool) (*database.Store, database.Library, job) {
	t.Helper()
	store, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	library := database.Library{
		ID: id.Library("root"), Name: "Library", RootCID: "root", Enabled: true, OneShot: oneShot,
	}
	if err := store.UpsertLibrary(context.Background(), library); err != nil {
		t.Fatal(err)
	}
	runID := "scan-run"
	if _, err := store.DB().ExecContext(context.Background(), `
INSERT INTO scan_runs(id,library_id,status,trigger_type,created_at)
VALUES(?,?,'running','test',?)`, runID, library.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return store, library, job{runID: runID, library: library}
}
