package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type shelfRevision struct{ catalog, progress int64 }

type shelfCacheEntry struct {
	revision shelfRevision
	value    any
}

type shelfCache struct {
	mu           sync.Mutex
	items        map[string]shelfCacheEntry
	load         singleflight.Group
	groupSeen    bool
	hotLibraries []string
}

func createShelfRevisions(db *sql.DB) error {
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS shelf_revision (id INTEGER PRIMARY KEY CHECK(id=1), catalog INTEGER NOT NULL, progress INTEGER NOT NULL);
INSERT OR IGNORE INTO shelf_revision VALUES(1,0,0);`); err != nil {
		return err
	}
	targets := []struct {
		table, revision, update, condition string
	}{
		{"libraries", "catalog", " OF enabled,name,provider_type,one_shot", ""},
		{"series", "catalog", "", ""},
		{"books", "catalog", "", ""},
		{"series_metadata", "catalog", " OF bgm_subject_id,scrape_status,bgm_score,bgm_rank,status,title,title_sort,publisher,is_nsfw,cover_source,genres_json,tags_json,authors_json,summary,alternate_titles_json,language", ""},
		{"series_tags", "catalog", "", ""},
		{"series_genres", "catalog", "", ""},
		{"series_authors", "catalog", "", ""},
		{"book_read_progress", "progress", "", ""},
		{"zip_indexes", "progress", " OF page_count", ""},
		{"settings", "catalog", " OF value", "show_nsfw"},
	}
	for _, t := range targets {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			suffix, condition := "", ""
			if op == "UPDATE" {
				suffix = t.update
			}
			if t.condition != "" {
				record := "NEW"
				if op == "DELETE" {
					record = "OLD"
				}
				condition = fmt.Sprintf(" WHEN %s.key='%s'", record, t.condition)
			}
			if _, err := db.Exec(fmt.Sprintf(
				"CREATE TRIGGER IF NOT EXISTS fk115_shelf_%s_%s AFTER %s%s ON %s%s BEGIN UPDATE shelf_revision SET %s=%s+1 WHERE id=1; END",
				t.table, strings.ToLower(op), op, suffix, t.table, condition, t.revision, t.revision)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) shelfRevision(ctx context.Context, includesProgress bool) (shelfRevision, error) {
	var r shelfRevision
	err := s.db.QueryRowContext(ctx, "SELECT catalog,progress FROM shelf_revision WHERE id=1").Scan(&r.catalog, &r.progress)
	if !includesProgress {
		r.progress = 0
	}
	return r, err
}

// Result storage is bounded and process-local. Revisions are written in the
// same transaction as data, including writes by scanner and Agent endpoints.
func shelfCached[T any](ctx context.Context, s *Store, key string, progress bool, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	lookup := func(rev shelfRevision) (T, bool) {
		s.shelf.mu.Lock()
		defer s.shelf.mu.Unlock()
		if hit, ok := s.shelf.items[key]; ok && hit.revision == rev {
			return hit.value.(T), true
		}
		return zero, false
	}
	for tries := 0; tries < 3; tries++ {
		rev, err := s.shelfRevision(ctx, progress)
		if err != nil {
			return zero, err
		}
		if val, ok := lookup(rev); ok {
			return val, nil
		}
		ch := s.shelf.load.DoChan(fmt.Sprintf("%s/%d/%d", key, rev.catalog, rev.progress), func() (any, error) {
			if val, ok := lookup(rev); ok {
				return val, nil
			}
			calc, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			val, err := fn(calc)
			if err != nil {
				return nil, err
			}
			end, err := s.shelfRevision(calc, progress)
			if err != nil {
				return nil, err
			}
			if rev == end {
				s.shelf.mu.Lock()
				if len(s.shelf.items) >= 96 {
					clear(s.shelf.items)
				}
				if s.shelf.items == nil {
					s.shelf.items = make(map[string]shelfCacheEntry)
				}
				s.shelf.items[key] = shelfCacheEntry{rev, val}
				s.shelf.mu.Unlock()
			}
			return val, nil
		})
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case res := <-ch:
			if res.Err != nil {
				return zero, res.Err
			}
			if now, err := s.shelfRevision(ctx, progress); err == nil && now == rev {
				return res.Val.(T), nil
			}
		}
	}
	// Busy catalog updates shouldn't make requests wait forever. This direct
	// uncached load uses current visibility settings, just like a cache miss.
	return fn(ctx)
}

type seriesPageResult struct {
	items []Series
	total int64
}

type bookPageResult struct {
	items []Book
	total int64
}

func (s *Store) SeriesPage(ctx context.Context, q SeriesQuery) ([]Series, int64, error) {
	fresh := func(ctx context.Context) (seriesPageResult, error) {
		items, total, err := s.querySeriesPage(ctx, q)
		return seriesPageResult{items, total}, err
	}
	commonSort := strings.EqualFold(q.Sort, "lastModifiedDate,desc") ||
		strings.EqualFold(q.Sort, "createdDate,desc") || strings.EqualFold(q.Sort, "bgmRank,asc")
	common := commonSort && q.Page == 0 && (q.Size == 12 || q.Size == 18 || q.Size == 36) &&
		!q.IncludeHidden && q.NSFWMode == "" && q.Search == "" && !q.Empty &&
		len(q.LibraryIDs) <= 1 && len(q.ReadStatus)+len(q.Tags)+len(q.Genres)+len(q.Statuses)+
		len(q.Publishers)+len(q.Authors)+len(q.Languages) == 0 && q.OneShot == nil && q.ScrapeStatus == ""
	if common && len(q.LibraryIDs) == 1 && !s.IsLibraryEnabled(ctx, q.LibraryIDs[0]) {
		common = false
	}
	var r seriesPageResult
	var err error
	if common {
		library := ""
		if len(q.LibraryIDs) == 1 {
			library = q.LibraryIDs[0]
		}
		s.shelf.noteSeriesQuery(library, q.GroupBangumi)
		key, _ := json.Marshal([]any{strings.ToLower(q.Sort), library, q.Size, q.GroupBangumi})
		r, err = shelfCached(ctx, s, "series:"+string(key), false, fresh)
	} else {
		r, err = fresh(ctx)
	}
	return r.items, r.total, err
}

func (s *Store) BooksPage(ctx context.Context, q BookQuery) ([]Book, int64, error) {
	if !isRecentProgressQuery(q) || q.Size != 8 || q.Page != 0 || q.Unpaged ||
		q.Search != "" || q.SeriesID != "" || len(q.LibraryIDs)+len(q.Tags)+len(q.Authors) != 0 || q.OneShot != nil || q.Empty {
		return s.queryBooksPage(ctx, q)
	}
	r, err := shelfCached(ctx, s, "continue-reading", true, func(ctx context.Context) (bookPageResult, error) {
		items, total, err := s.queryBooksPage(ctx, q)
		return bookPageResult{items, total}, err
	})
	return r.items, r.total, err
}

// WarmShelf is a single low-concurrency worker. A small debounce avoids
// regenerating expensive lists for every row in a scan or scrape batch.
func (s *Store) WarmShelf(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var previous, warmed shelfRevision
	dirtySince := time.Now()
	started := false
	for {
		rev, err := s.shelfRevision(ctx, true)
		if err == nil {
			if previous != rev {
				previous = rev
				dirtySince = time.Now()
			}
			if !started || (warmed != rev && time.Since(dirtySince) >= time.Second) {
				catalog := !started || warmed.catalog != rev.catalog
				started, warmed = true, rev
				s.warmShelfQueries(ctx, catalog)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) warmShelfQueries(ctx context.Context, catalog bool) {
	_, _, _ = s.BooksPage(ctx, BookQuery{ReadStatus: []string{"IN_PROGRESS"}, Size: 8, Sort: "readProgress.readDate,desc"})
	if !catalog || ctx.Err() != nil {
		return
	}
	s.shelf.mu.Lock()
	groupSeen, libraries := s.shelf.groupSeen, slices.Clone(s.shelf.hotLibraries)
	s.shelf.mu.Unlock()
	var sorts = []SeriesQuery{
		{Size: 18, Sort: "lastModifiedDate,desc"},
		{Size: 12, Sort: "createdDate,desc"},
		{Size: 36, Sort: "lastModifiedDate,desc"},
		{Size: 36, Sort: "bgmRank,asc"},
	}
	_, _ = s.Genres(ctx, nil)
	_, _ = s.Tags(ctx, nil)
	_, _ = s.Publishers(ctx, nil)
	for _, group := range []bool{false, true} {
		if group && !groupSeen {
			continue
		}
		for _, q := range sorts {
			if ctx.Err() != nil {
				return
			}
			q.GroupBangumi = group
			_, _, _ = s.SeriesPage(ctx, q)
		}
		for i := len(libraries) - 1; i >= 0; i-- {
			if ctx.Err() != nil {
				return
			}
			_, _, _ = s.SeriesPage(ctx, SeriesQuery{
				LibraryIDs: []string{libraries[i]}, Size: 36, Sort: "lastModifiedDate,desc", GroupBangumi: group,
			})
		}
	}
}

func (c *shelfCache) noteSeriesQuery(library string, grouped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groupSeen = c.groupSeen || grouped
	if library == "" || (len(c.hotLibraries) != 0 && c.hotLibraries[0] == library) {
		return
	}
	if i := slices.Index(c.hotLibraries, library); i >= 0 {
		c.hotLibraries = slices.Delete(c.hotLibraries, i, i+1)
	}
	c.hotLibraries = slices.Insert(c.hotLibraries, 0, library)
	if len(c.hotLibraries) > 3 {
		c.hotLibraries = c.hotLibraries[:3]
	}
}
