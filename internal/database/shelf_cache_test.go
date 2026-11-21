package database

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestShelfCacheGroupingRankAndCatalogVisibility(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "shelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a := Library{ID: "local", Name: "Local", ProviderType: ProviderLocal, RootPath: "/fixture", Enabled: true}
	b := Library{ID: "remote", Name: "Remote", RootCID: "fixture", Enabled: true}
	for _, lib := range []Library{a, b} {
		if err := s.UpsertLibrary(ctx, lib); err != nil {
			t.Fatal(err)
		}
	}
	insertCatalogSeries(t, s, a.ID, "a-anchor", "Anchor", []catalogBookTime{{modified: "2023-01-01T00:00:00Z"}})
	insertCatalogSeries(t, s, b.ID, "b-edition", "Edition", []catalogBookTime{{modified: "2025-01-01T00:00:00Z"}})
	insertCatalogSeries(t, s, a.ID, "c-ranked", "Ranked", []catalogBookTime{{modified: "2024-01-01T00:00:00Z"}})
	insertCatalogSeries(t, s, a.ID, "d-unknown", "Unknown", []catalogBookTime{{modified: "2022-01-01T00:00:00Z"}})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.DB().Exec(sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO series_metadata(series_id,bgm_subject_id,bgm_rank,updated_at)
VALUES ('a-anchor',100,NULL,''),('b-edition',100,1,''),('c-ranked',200,20,'')`)
	check := func(q SeriesQuery, total int64, want ...string) {
		t.Helper()
		items, n, err := s.SeriesPage(ctx, q)
		got := make([]string, 0, len(items))
		for _, item := range items {
			got = append(got, item.ID)
		}
		if err != nil || n != total || !reflect.DeepEqual(got, want) {
			t.Fatalf("query %+v: total %d items %v err %v; want total %d, items %v", q, n, got, err, total, want)
		}
	}
	q := SeriesQuery{Size: 36, Sort: "bgmRank,asc", GroupBangumi: true}
	check(q, 3, "a-anchor", "c-ranked", "d-unknown")
	check(q, 3, "a-anchor", "c-ranked", "d-unknown") // cache hit
	date := SeriesQuery{Size: 36, Sort: "lastModifiedDate,desc", GroupBangumi: true}
	check(date, 3, "a-anchor", "c-ranked", "d-unknown")
	check(SeriesQuery{Size: 1, Page: 1, Sort: q.Sort, GroupBangumi: true}, 3, "c-ranked")
	check(SeriesQuery{Size: 36, Sort: q.Sort}, 4, "b-edition", "c-ranked", "a-anchor", "d-unknown")
	check(SeriesQuery{Size: 36, Sort: q.Sort, GroupBangumi: true, LibraryIDs: []string{b.ID}}, 1, "b-edition")

	// Hidden versions must stop contributing to sorting without exposing the hidden row.
	b.Enabled = false
	if err := s.UpsertLibrary(ctx, b); err != nil {
		t.Fatal(err)
	}
	check(q, 3, "c-ranked", "a-anchor", "d-unknown")
	check(date, 3, "c-ranked", "a-anchor", "d-unknown")

	exec(`UPDATE series_metadata SET is_nsfw=1 WHERE series_id='c-ranked'`)
	check(q, 2, "a-anchor", "d-unknown")
	if err := s.SetSetting(ctx, "show_nsfw", "true"); err != nil {
		t.Fatal(err)
	}
	check(q, 3, "c-ranked", "a-anchor", "d-unknown")
}

func TestGroupBangumiPrefersMostBooksAndUnionTieBreak(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "group_most_books.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	unionLib := Library{ID: "lib-union", Name: "AAA Union", ProviderType: ProviderUnion, RootCID: "union:lib-union", Enabled: true}
	normalLib := Library{ID: "lib-normal", Name: "ZZZ Normal", ProviderType: ProviderLocal, RootCID: "local:/fixture", RootPath: "/fixture", Enabled: true}
	for _, lib := range []Library{unionLib, normalLib} {
		if err := s.UpsertLibrary(ctx, lib); err != nil {
			t.Fatal(err)
		}
	}

	// Same bgm_subject_id=500: normal library has MORE books (3 vs 1) -> must beat union library.
	insertCatalogSeries(t, s, unionLib.ID, "a-union-short", "Short Union", []catalogBookTime{
		{modified: "2025-05-01T00:00:00Z"},
	})
	insertCatalogSeries(t, s, normalLib.ID, "z-normal-full", "Full Normal", []catalogBookTime{
		{modified: "2024-01-01T00:00:00Z"},
		{modified: "2024-01-02T00:00:00Z"},
		{modified: "2024-01-03T00:00:00Z"},
	})

	// Same bgm_subject_id=600: equal book counts (2 vs 2) -> union library wins tie-break.
	insertCatalogSeries(t, s, normalLib.ID, "a-normal-tie", "Tie Normal", []catalogBookTime{
		{modified: "2024-02-01T00:00:00Z"},
		{modified: "2024-02-02T00:00:00Z"},
	})
	insertCatalogSeries(t, s, unionLib.ID, "z-union-tie", "Tie Union", []catalogBookTime{
		{modified: "2024-03-01T00:00:00Z"},
		{modified: "2024-03-02T00:00:00Z"},
	})

	if _, err := s.DB().Exec(`INSERT INTO series_metadata(series_id,bgm_subject_id,bgm_rank,updated_at) VALUES
		('a-union-short',500,5,''),
		('z-normal-full',500,NULL,''),
		('a-normal-tie',600,20,''),
		('z-union-tie',600,20,'')`); err != nil {
		t.Fatal(err)
	}

	items, total, err := s.SeriesPage(ctx, SeriesQuery{Size: 20, Sort: "bgmRank,asc", GroupBangumi: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d len=%d, want 2", total, len(items))
	}
	// Group 500 still gets group-level bgm_rank=5 from a-union-short, but picks z-normal-full (3 books).
	if items[0].ID != "z-normal-full" || items[0].BooksCount != 3 {
		t.Fatalf("items[0] = %s (books=%d), want z-normal-full (books=3)", items[0].ID, items[0].BooksCount)
	}
	// Group 600 tie-breaks 2 vs 2 to union edition z-union-tie.
	if items[1].ID != "z-union-tie" || items[1].BooksCount != 2 {
		t.Fatalf("items[1] = %s (books=%d), want z-union-tie (books=2)", items[1].ID, items[1].BooksCount)
	}

	// Bangumi lookup must follow the exact same order (books_count DESC, then union).
	matches500, err := s.LookupSeriesByBangumiSubject(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches500) != 2 || matches500[0].SeriesID != "z-normal-full" || matches500[1].SeriesID != "a-union-short" {
		t.Fatalf("LookupSeriesByBangumiSubject(500) = %+v, want z-normal-full first", matches500)
	}
	matches600, err := s.LookupSeriesByBangumiSubject(ctx, 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches600) != 2 || matches600[0].SeriesID != "z-union-tie" || matches600[1].SeriesID != "a-normal-tie" {
		t.Fatalf("LookupSeriesByBangumiSubject(600) = %+v, want z-union-tie first", matches600)
	}
}

func TestShelfCacheContinueReadingInvalidatedOnCommit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "shelf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	lib := Library{ID: "library", Name: "Library", RootCID: "fixture", Enabled: true}
	if err := s.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, s, lib.ID, "series", "Example", []catalogBookTime{{}, {}})
	q := BookQuery{Size: 8, Sort: "readProgress.readDate,desc", ReadStatus: []string{"IN_PROGRESS"}}
	count := func(n int64) {
		t.Helper()
		rows, total, err := s.BooksPage(ctx, q)
		if err != nil || total != n || int64(len(rows)) != n {
			t.Fatalf("continue reading = %v / %d / %v; want %d", rows, total, err, n)
		}
	}
	count(0)
	book := Book{ID: "series-book-a", SeriesID: "series"}
	page := 3
	complete := false
	if err := s.UpdateBookReadProgress(ctx, book, &complete, &page); err != nil {
		t.Fatal(err)
	}
	count(1)
	complete = true
	if err := s.UpdateBookReadProgress(ctx, book, &complete, &page); err != nil {
		t.Fatal(err)
	}
	count(0)
	complete = false
	if err := s.UpdateBookReadProgress(ctx, book, &complete, &page); err != nil {
		t.Fatal(err)
	}
	count(1)
	lib.Enabled = false
	if err := s.UpsertLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	count(0)
}
