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
}
