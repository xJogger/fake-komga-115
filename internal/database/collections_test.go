package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestCollectionsReadListsAndAuthorsPage(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "org.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{ID: "lib-vis", Name: "Visible", RootCID: "r1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, Library{ID: "lib-hid", Name: "Hidden", RootCID: "r2", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib-vis", "s1", "Series One", []catalogBookTime{
		{created: "2024-01-01T00:00:00Z", modified: "2024-01-01T00:00:00Z"},
		{created: "2024-01-02T00:00:00Z", modified: "2024-01-02T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib-vis", "s2", "Series Two", []catalogBookTime{
		{created: "2024-01-03T00:00:00Z", modified: "2024-01-03T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib-vis", "s-nsfw", "Series NSFW", []catalogBookTime{
		{created: "2024-01-04T00:00:00Z", modified: "2024-01-04T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib-hid", "s-hid", "Series Hidden", []catalogBookTime{
		{created: "2024-01-05T00:00:00Z", modified: "2024-01-05T00:00:00Z"},
	})

	s1, _ := store.SeriesByID(ctx, "s1")
	s2, _ := store.SeriesByID(ctx, "s2")
	sNSFW, _ := store.SeriesByIDAny(ctx, "s-nsfw", true)
	if _, err := store.ApplyScrapedMetadata(ctx, s1, ScrapedMetadata{
		SubjectID: 1, Title: "作品一", Authors: []Author{{Name: "Alice", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, s2, ScrapedMetadata{
		SubjectID: 2, Title: "作品二", Authors: []Author{{Name: "Bob", Role: "artist"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, sNSFW, ScrapedMetadata{
		SubjectID: 3, Title: "成人作品", IsNSFW: true, Authors: []Author{{Name: "Charlie", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}

	// --- Collections CRUD & visibility isolation ---
	col, err := store.CreateCollection(ctx, "Favorites", true, []string{"s-hid", "s2", "s-nsfw", "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if !col.Filtered {
		t.Fatalf("expected Filtered=true when hidden/nsfw members exist")
	}
	if len(col.SeriesIDs) != 2 || col.SeriesIDs[0] != "s2" || col.SeriesIDs[1] != "s1" {
		t.Fatalf("unexpected visible seriesIDs: %#v", col.SeriesIDs)
	}
	if _, err := store.CreateCollection(ctx, "favorites", false, nil); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("expected ErrDuplicateName, got %v", err)
	}
	firstVis, err := store.CollectionFirstVisibleSeriesID(ctx, col.ID)
	if err != nil || firstVis != "s2" {
		t.Fatalf("first visible series=%q err=%v, want s2", firstVis, err)
	}

	colSeries, total, err := store.CollectionSeriesPage(ctx, col.ID, SeriesQuery{Size: 20}, false)
	if err != nil || total != 2 || len(colSeries) != 2 || colSeries[0].ID != "s2" || colSeries[1].ID != "s1" {
		t.Fatalf("CollectionSeriesPage ordered failed: total=%d items=%#v err=%v", total, colSeries, err)
	}

	// Filter SeriesPage by CollectionIDs
	byCol, byColTotal, err := store.SeriesPage(ctx, SeriesQuery{CollectionIDs: []string{col.ID}, Size: 20})
	if err != nil || byColTotal != 2 || len(byCol) != 2 {
		t.Fatalf("SeriesPage(CollectionIDs) total=%d len=%d err=%v", byColTotal, len(byCol), err)
	}

	// Series -> Collections reverse lookup
	forS1, err := store.CollectionsForSeries(ctx, "s1")
	if err != nil || len(forS1) != 1 || forS1[0].ID != col.ID {
		t.Fatalf("CollectionsForSeries=%#v err=%v", forS1, err)
	}

	// Update collection order & name
	newName := "Top Picks"
	reordered := []string{"s1", "s2"}
	updated, err := store.UpdateCollection(ctx, col.ID, &newName, nil, &reordered)
	if err != nil || updated.Name != "Top Picks" || updated.Filtered || len(updated.SeriesIDs) != 2 || updated.SeriesIDs[0] != "s1" {
		t.Fatalf("UpdateCollection failed: %#v err=%v", updated, err)
	}

	// --- ReadLists CRUD, sibling navigation & isolation ---
	rl, err := store.CreateReadList(ctx, "Weekend Queue", "Reading order", true, []string{"s-hid-book-a", "s2-book-a", "s-nsfw-book-a", "s1-book-a", "s1-book-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !rl.Filtered || len(rl.BookIDs) != 3 || rl.BookIDs[0] != "s2-book-a" || rl.BookIDs[1] != "s1-book-a" || rl.BookIDs[2] != "s1-book-b" {
		t.Fatalf("unexpected readlist: %#v", rl)
	}
	rlBooks, rlTotal, err := store.ReadListBooksPage(ctx, rl.ID, BookQuery{Size: 20})
	if err != nil || rlTotal != 3 || len(rlBooks) != 3 || rlBooks[0].ID != "s2-book-a" {
		t.Fatalf("ReadListBooksPage failed: total=%d len=%d err=%v", rlTotal, len(rlBooks), err)
	}
	nextBook, err := store.ReadListSiblingBook(ctx, rl.ID, "s2-book-a", true)
	if err != nil || nextBook.ID != "s1-book-a" {
		t.Fatalf("ReadListSiblingBook next=%q err=%v", nextBook.ID, err)
	}
	prevBook, err := store.ReadListSiblingBook(ctx, rl.ID, "s1-book-a", false)
	if err != nil || prevBook.ID != "s2-book-a" {
		t.Fatalf("ReadListSiblingBook prev=%q err=%v", prevBook.ID, err)
	}
	if _, err := store.ReadListSiblingBook(ctx, rl.ID, "s2-book-a", false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows before first book, got %v", err)
	}
	forBook, err := store.ReadListsForBook(ctx, "s1-book-a")
	if err != nil || len(forBook) != 1 || forBook[0].ID != rl.ID {
		t.Fatalf("ReadListsForBook=%#v err=%v", forBook, err)
	}

	// --- AuthorsPage (v2/authors) ---
	authors, aTotal, err := store.AuthorsPage(ctx, AuthorQuery{Page: 0, Size: 1})
	if err != nil || aTotal != 2 || len(authors) != 1 || authors[0].Name != "Alice" {
		t.Fatalf("AuthorsPage page 0: total=%d items=%#v err=%v", aTotal, authors, err)
	}
	roleAuthors, rTotal, err := store.AuthorsPage(ctx, AuthorQuery{Role: "artist", Page: 0, Size: 10})
	if err != nil || rTotal != 1 || len(roleAuthors) != 1 || roleAuthors[0].Name != "Bob" {
		t.Fatalf("AuthorsPage role=artist: total=%d items=%#v err=%v", rTotal, roleAuthors, err)
	}

	// --- Delete ---
	if err := store.DeleteCollection(ctx, col.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteReadList(ctx, rl.ID); err != nil {
		t.Fatal(err)
	}
}
