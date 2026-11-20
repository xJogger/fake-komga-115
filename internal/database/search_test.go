package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSearchParserKohariaAndFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib1", Name: "Main", RootCID: "r1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib1", "s1", "Aria Orig", []catalogBookTime{
		{created: "2024-01-01T00:00:00Z", modified: "2024-01-01T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s2", "Space Story", []catalogBookTime{
		{created: "2024-01-02T00:00:00Z", modified: "2024-01-02T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s3", "Water Planet Chronicle", []catalogBookTime{
		{created: "2024-01-03T00:00:00Z", modified: "2024-01-03T00:00:00Z"},
	})

	s1, _ := store.SeriesByID(ctx, "s1")
	s2, _ := store.SeriesByID(ctx, "s2")
	s3, _ := store.SeriesByID(ctx, "s3")

	if _, err := store.ApplyScrapedMetadata(ctx, s1, ScrapedMetadata{
		SubjectID: 101,
		Title:     "水星领航员",
		TitleSort: "水星领航员",
		Publisher: "Mag Garden",
		Status:    "ENDED",
		Genres:    []string{"治愈"},
		Tags:      []string{"科幻", "日常"},
		Authors:   []Author{{Name: "天野梢", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, s2, ScrapedMetadata{
		SubjectID: 102,
		Title:     "星际漫游",
		TitleSort: "星际漫游",
		Publisher: "集英社",
		Status:    "ONGOING",
		Genres:    []string{"冒险"},
		Tags:      []string{"水星领航员", "治愈"},
		Authors:   []Author{{Name: "示例作者", Role: "artist"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScrapedMetadata(ctx, s3, ScrapedMetadata{
		SubjectID: 103,
		Title:     "水星领航员 特别篇",
		TitleSort: "水星领航员 特别篇",
		Publisher: "Mag Garden",
		Status:    "ENDED",
		Genres:    []string{"治愈"},
		Tags:      []string{"日常"},
		Authors:   []Author{{Name: "天野梢", Role: "writer"}},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Koharia exact "(q) OR tag:(q)" union query + relevance sorting
	items, total, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("koharia union total=%d len=%d, want 3", total, len(items))
	}
	if items[0].ID != "s1" || items[1].ID != "s3" || items[2].ID != "s2" {
		t.Fatalf("unexpected relevance order: %s, %s, %s (want s1, s3, s2)", items[0].ID, items[1].ID, items[2].ID)
	}

	// 2. Koharia BooksPage union query
	books, bTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bTotal != 3 || len(books) != 3 {
		t.Fatalf("koharia books union total=%d len=%d, want 3", bTotal, len(books))
	}

	// 3. Field prefixes, boolean AND / NOT, role prefix
	tests := []struct {
		query string
		want  []string
	}{
		{`tag:科幻`, []string{"s1"}},
		{`publisher:"Mag Garden" NOT 特别篇`, []string{"s1"}},
		{`writer:天野梢`, []string{"s1", "s3"}},
		{`artist:示例作者`, []string{"s2"}},
		{`status:ONGOING`, []string{"s2"}},
		{`genre:冒险 OR tag:科幻`, []string{"s1", "s2"}},
		{`水星领航员 -特别篇`, []string{"s1"}},
	}
	for _, tc := range tests {
		got, cnt, err := store.SeriesPage(ctx, SeriesQuery{Search: tc.query, Size: 20, Sort: "metadata.titleSort,asc"})
		if err != nil {
			t.Fatalf("query %q err=%v", tc.query, err)
		}
		if int(cnt) != len(tc.want) || len(got) != len(tc.want) {
			t.Fatalf("query %q got count=%d items=%d want %v", tc.query, cnt, len(got), tc.want)
		}
		gotSet := map[string]bool{}
		for _, item := range got {
			gotSet[item.ID] = true
		}
		for _, id := range tc.want {
			if !gotSet[id] {
				t.Fatalf("query %q missing %s in %#v", tc.query, id, gotSet)
			}
		}
	}

	// 4. Out-of-range page (> 0) must still return exact total count on both SeriesPage & BooksPage
	emptySeries, outTotal, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Page:   5,
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(emptySeries) != 0 || outTotal != 3 {
		t.Fatalf("out-of-range SeriesPage: len=%d total=%d, want 0/3", len(emptySeries), outTotal)
	}
	emptyBooks, outBookTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Page:   5,
		Size:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyBooks) != 0 || outBookTotal != 3 {
		t.Fatalf("out-of-range BooksPage: len=%d total=%d, want 0/3", len(emptyBooks), outBookTotal)
	}

	// 5. BooksPage boolean AND NOT, standalone NOT, extra tag filter, and unpaged search
	bNot, bNotTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "水星领航员 NOT 特别篇",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || bNotTotal != 1 || len(bNot) != 1 || bNot[0].SeriesID != "s1" {
		t.Fatalf("BooksPage AND NOT: err=%v total=%d items=%#v", err, bNotTotal, bNot)
	}
	bPureNot, bPureNotTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "-特别篇",
		Size:   20,
	})
	if err != nil || bPureNotTotal != 2 || len(bPureNot) != 2 {
		t.Fatalf("BooksPage standalone NOT: err=%v total=%d len=%d", err, bPureNotTotal, len(bPureNot))
	}
	bFiltered, bFilteredTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Tags:   []string{"科幻"},
		Size:   20,
	})
	if err != nil || bFilteredTotal != 1 || len(bFiltered) != 1 || bFiltered[0].SeriesID != "s1" {
		t.Fatalf("BooksPage search+tag filter: err=%v total=%d items=%#v", err, bFilteredTotal, bFiltered)
	}
	bUnpaged, bUnpagedTotal, err := store.BooksPage(ctx, BookQuery{
		Search:  "(水星领航员) OR tag:(水星领航员)",
		Unpaged: true,
	})
	if err != nil || bUnpagedTotal != 3 || len(bUnpaged) != 3 {
		t.Fatalf("BooksPage unpaged search: err=%v total=%d len=%d", err, bUnpagedTotal, len(bUnpaged))
	}

	// 6. NSFW exclusion + disabled library exclusion in candidate-set searches
	if err := store.UpsertLibrary(ctx, Library{
		ID: "lib_hidden", Name: "Hidden", RootCID: "r2", Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	insertCatalogSeries(t, store, "lib_hidden", "s_hidden", "水星领航员 Hidden", []catalogBookTime{
		{created: "2024-01-04T00:00:00Z", modified: "2024-01-04T00:00:00Z"},
	})
	insertCatalogSeries(t, store, "lib1", "s_nsfw", "水星领航员 R18", []catalogBookTime{
		{created: "2024-01-05T00:00:00Z", modified: "2024-01-05T00:00:00Z"},
	})
	sNSFW, _ := store.SeriesByIDAny(ctx, "s_nsfw", true)
	if _, err := store.ApplyScrapedMetadata(ctx, sNSFW, ScrapedMetadata{
		SubjectID: 104,
		Title:     "水星领航员 R18",
		TitleSort: "水星领航员 R18",
		IsNSFW:    true,
	}); err != nil {
		t.Fatal(err)
	}
	// Default (ShowNSFW=false): neither hidden library nor NSFW series/book appears
	sVis, sVisTotal, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || sVisTotal != 3 || len(sVis) != 3 {
		t.Fatalf("SeriesPage visibility filter: err=%v total=%d len=%d", err, sVisTotal, len(sVis))
	}
	bVis, bVisTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || bVisTotal != 3 || len(bVis) != 3 {
		t.Fatalf("BooksPage visibility filter: err=%v total=%d len=%d", err, bVisTotal, len(bVis))
	}
	// With ShowNSFW enabled, NSFW series & book appear, hidden library stays excluded
	if err := store.SetSetting(ctx, "show_nsfw", "true"); err != nil {
		t.Fatal(err)
	}
	_, sNSFWTotal, err := store.SeriesPage(ctx, SeriesQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || sNSFWTotal != 4 {
		t.Fatalf("SeriesPage showNSFW=true: err=%v total=%d want 4", err, sNSFWTotal)
	}
	_, bNSFWTotal, err := store.BooksPage(ctx, BookQuery{
		Search: "(水星领航员) OR tag:(水星领航员)",
		Sort:   "relevance,asc",
		Size:   20,
	})
	if err != nil || bNSFWTotal != 4 {
		t.Fatalf("BooksPage showNSFW=true: err=%v total=%d want 4", err, bNSFWTotal)
	}
	if err := store.SetSetting(ctx, "show_nsfw", "false"); err != nil {
		t.Fatal(err)
	}

	// 7. Shelf cache covers Koharia browse sorts & sizes (metadata.titleSort,asc size=20; lastModifiedDate,desc size=25)
	for _, q := range []SeriesQuery{
		{Page: 0, Size: 20, Sort: "metadata.titleSort,asc"},
		{Page: 0, Size: 25, Sort: "lastModifiedDate,desc"},
	} {
		first, t1, err := store.SeriesPage(ctx, q)
		if err != nil || t1 != 3 || len(first) != 3 {
			t.Fatalf("shelf cache cold %+v: err=%v total=%d len=%d", q, err, t1, len(first))
		}
		second, t2, err := store.SeriesPage(ctx, q)
		if err != nil || t2 != t1 || len(second) != len(first) || second[0].ID != first[0].ID {
			t.Fatalf("shelf cache warm %+v mismatch: err=%v t=%d/%d", q, err, t2, t1)
		}
	}
}
