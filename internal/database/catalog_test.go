package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSeriesPageSortsByRemoteBookTimes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	library := Library{ID: "library", Name: "Library", RootCID: "root", Enabled: true}
	if err := store.UpsertLibrary(ctx, library); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, library.ID, "a", "Alpha", []catalogBookTime{
		{created: "2020-01-01T00:00:00Z", modified: "2021-01-01T00:00:00Z"},
		{created: "2024-01-01T00:00:00Z", modified: "2025-01-01T00:00:00Z"},
	})
	insertCatalogSeries(t, store, library.ID, "b", "Beta", []catalogBookTime{
		{created: "2022-01-01T00:00:00Z", modified: "2023-01-01T00:00:00Z"},
	})
	insertCatalogSeries(t, store, library.ID, "c", "Gamma", []catalogBookTime{
		{created: "2023-01-01T00:00:00Z", modified: "2024-01-01T00:00:00Z"},
	})

	tests := []struct {
		sort string
		want []string
	}{
		{"createdDate,asc", []string{"Beta", "Gamma", "Alpha"}},
		{"createdDate,desc", []string{"Alpha", "Gamma", "Beta"}},
		{"lastModifiedDate,asc", []string{"Beta", "Gamma", "Alpha"}},
		{"lastModifiedDate,desc", []string{"Alpha", "Gamma", "Beta"}},
	}
	for _, test := range tests {
		t.Run(test.sort, func(t *testing.T) {
			items, total, err := store.SeriesPage(ctx, SeriesQuery{
				LibraryIDs: []string{library.ID}, Size: 20, Sort: test.sort,
			})
			if err != nil {
				t.Fatal(err)
			}
			if total != 3 || len(items) != 3 {
				t.Fatalf("total=%d len=%d", total, len(items))
			}
			for index, want := range test.want {
				if items[index].Name != want {
					t.Fatalf("position %d=%q want %q", index, items[index].Name, want)
				}
			}
		})
	}
}

func TestNextBookInSeries(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{
		ID: "library", Name: "Library", RootCID: "root", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES
 ('normal','library','normal-cid','Normal','Normal',0,?,?,'scan'),
 ('single','library','single-cid','Single','Single',0,?,?,'scan'),
 ('oneshot','library','oneshot-cid','One Shot','One Shot',1,?,?,'scan')`,
		now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, seriesID, name string
		number             float64
	}{
		{"normal-a", "normal", "Vol 2.cbz", 1},
		{"normal-b", "normal", "Vol 10.cbz", 2},
		{"normal-c", "normal", "Vol 11.cbz", 2},
		{"single-a", "single", "Only.cbz", 1},
		{"oneshot-a", "oneshot", "A.cbz", 1},
		{"oneshot-b", "oneshot", "B.cbz", 2},
	} {
		if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,
 number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,1,?,?,?,?,'scan')`,
			item.id, item.seriesID, "library", "file-"+item.id, "parent", item.name,
			"pick-"+item.id, item.number, now, now); err != nil {
			t.Fatal(err)
		}
	}

	current, err := store.BookByID(ctx, "normal-a")
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.NextBookInSeries(ctx, current)
	if err != nil || next.ID != "normal-b" {
		t.Fatalf("next after normal-a = %q, err=%v", next.ID, err)
	}
	next, err = store.NextBookInSeries(ctx, next)
	if err != nil || next.ID != "normal-c" {
		t.Fatalf("next after normal-b = %q, err=%v", next.ID, err)
	}
	for _, id := range []string{"normal-c", "single-a", "oneshot-a"} {
		book, err := store.BookByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.NextBookInSeries(ctx, book); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("NextBookInSeries(%q) err=%v, want sql.ErrNoRows", id, err)
		}
	}
}

type catalogBookTime struct {
	created  string
	modified string
}

func insertCatalogSeries(
	t *testing.T,
	store *Store,
	libraryID, seriesID, name string,
	bookTimes []catalogBookTime,
) {
	t.Helper()
	now := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,?,?,'scan')`,
		seriesID, libraryID, "cid-"+seriesID, name, name, now, now); err != nil {
		t.Fatal(err)
	}
	for index, item := range bookTimes {
		if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,1,?,?,?,?,?,?,'scan')`,
			seriesID+"-book-"+string(rune('a'+index)), seriesID, libraryID,
			seriesID+"-file-"+string(rune('a'+index)), "parent", "book.cbz", "pick",
			item.created, item.modified, float64(index+1), now, now); err != nil {
			t.Fatal(err)
		}
	}
}
