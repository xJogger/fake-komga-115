package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/id"
	"github.com/xJogger/fake-komga-115/internal/natsort"
)

var (
	ErrInvalidUnionMembers  = errors.New("union library requires at least one physical member library")
	ErrUnionNestedMember    = errors.New("a union library cannot include another union library")
	ErrUnionMemberOwned     = errors.New("a member library is already enrolled in another union library")
	ErrUnionOneShotMismatch = errors.New("all member libraries of a union library must have the same One-Shot mode")
)

// NormalizeUnionSeriesPath strips the library's own root folder name (the first path segment
// recorded by the scanner) and normalizes separators so backups stored under differently named
// root folders match strictly by their path relative to each library root.
func NormalizeUnionSeriesPath(relPath string) string {
	cleaned := strings.ReplaceAll(strings.TrimSpace(relPath), "\\", "/")
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return "."
	}
	cleaned = path.Clean("/" + cleaned)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "."
	}
	slash := strings.IndexByte(cleaned, '/')
	if slash < 0 {
		return "."
	}
	rest := strings.TrimPrefix(cleaned[slash+1:], "/")
	if rest == "" || rest == "." {
		return "."
	}
	return rest
}

func (s *Store) validateUnionMembers(ctx context.Context, unionID string, memberIDs []string) ([]Library, bool, error) {
	cleanIDs := cleanStringSliceCaseSensitive(memberIDs)
	if len(cleanIDs) == 0 {
		return nil, false, ErrInvalidUnionMembers
	}
	members := make([]Library, 0, len(cleanIDs))
	var oneShot bool
	for idx, mid := range cleanIDs {
		lib, err := s.LibraryBasic(ctx, mid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, false, fmt.Errorf("member library %s not found", mid)
			}
			return nil, false, err
		}
		if lib.EffectiveProvider() == ProviderUnion {
			return nil, false, ErrUnionNestedMember
		}
		if lib.UnionParentID != "" && lib.UnionParentID != unionID {
			return nil, false, fmt.Errorf("%w: %s (already in %s)", ErrUnionMemberOwned, lib.Name, lib.UnionParentName)
		}
		if idx == 0 {
			oneShot = lib.OneShot
		} else if lib.OneShot != oneShot {
			return nil, false, ErrUnionOneShotMismatch
		}
		members = append(members, lib)
	}
	return members, oneShot, nil
}

func (s *Store) CreateUnionLibrary(
	ctx context.Context,
	name string,
	enabled bool,
	memberLibraryIDs []string,
) (Library, UnionRefreshStats, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Library{}, UnionRefreshStats{}, errors.New("union library name is required")
	}
	members, oneShot, err := s.validateUnionMembers(ctx, "", memberLibraryIDs)
	if err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	hexPart, err := randomHexID(8)
	if err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	locator := "union:" + hexPart
	unionID := id.Library(locator)
	now := nowText()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO libraries(
 id,name,provider_type,root_cid,webdav_account_id,root_path,enabled,one_shot,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		unionID, name, ProviderUnion, locator, "", "", enabled, oneShot, now, now); err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	for prio, m := range members {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO union_layers(union_library_id,member_library_id,priority,created_at)
VALUES(?,?,?,?)`, unionID, m.ID, prio, now); err != nil {
			return Library{}, UnionRefreshStats{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Library{}, UnionRefreshStats{}, err
	}

	stats, err := s.RefreshUnionLibrary(ctx, unionID)
	if err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	lib, err := s.Library(ctx, unionID)
	return lib, stats, err
}

func (s *Store) UpdateUnionLibrary(
	ctx context.Context,
	unionID string,
	name *string,
	enabled *bool,
	memberLibraryIDs []string,
) (Library, UnionRefreshStats, error) {
	existing, err := s.LibraryBasic(ctx, unionID)
	if err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	if existing.EffectiveProvider() != ProviderUnion {
		return Library{}, UnionRefreshStats{}, errors.New("target library is not a union library")
	}
	newName := existing.Name
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return Library{}, UnionRefreshStats{}, errors.New("union library name is required")
		}
		newName = trimmed
	}
	newEnabled := existing.Enabled
	if enabled != nil {
		newEnabled = *enabled
	}

	membersChanged := memberLibraryIDs != nil
	var members []Library
	newOneShot := existing.OneShot
	if membersChanged {
		members, newOneShot, err = s.validateUnionMembers(ctx, unionID, memberLibraryIDs)
		if err != nil {
			return Library{}, UnionRefreshStats{}, err
		}
	}

	now := nowText()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
UPDATE libraries SET name=?,enabled=?,one_shot=?,updated_at=? WHERE id=?`,
		newName, newEnabled, newOneShot, now, unionID); err != nil {
		return Library{}, UnionRefreshStats{}, err
	}
	if membersChanged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM union_layers WHERE union_library_id=?`, unionID); err != nil {
			return Library{}, UnionRefreshStats{}, err
		}
		for prio, m := range members {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO union_layers(union_library_id,member_library_id,priority,created_at)
VALUES(?,?,?,?)`, unionID, m.ID, prio, now); err != nil {
				return Library{}, UnionRefreshStats{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Library{}, UnionRefreshStats{}, err
	}

	var stats UnionRefreshStats
	if membersChanged || newName != existing.Name {
		stats, err = s.RefreshUnionLibrary(ctx, unionID)
		if err != nil {
			return Library{}, UnionRefreshStats{}, err
		}
	}
	lib, err := s.Library(ctx, unionID)
	return lib, stats, err
}

func (s *Store) UnionParentLibraryID(ctx context.Context, memberLibraryID string) (string, bool, error) {
	var unionID string
	err := s.db.QueryRowContext(ctx,
		`SELECT union_library_id FROM union_layers WHERE member_library_id=?`, memberLibraryID,
	).Scan(&unionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return unionID, true, nil
}

type unionPhysSeries struct {
	ID             string
	LibraryID      string
	Priority       int
	Name           string
	RelativePath   string
	OneShot        bool
	FileModifiedAt sql.NullString
}

type unionPhysBook struct {
	ID             string
	SeriesID       string
	LibraryID      string
	Priority       int
	Name           string
	Size           int64
	PickCode       string
	SHA1           string
	FileCreatedAt  sql.NullString
	FileModifiedAt sql.NullString
}

type unionMergedBook struct {
	ID             string
	FileID         string
	Name           string
	Size           int64
	PickCode       string
	SHA1           string
	FileCreatedAt  sql.NullString
	FileModifiedAt sql.NullString
	NumberSort     float64
	Sources        []unionBookSrcRef
}

type unionBookSrcRef struct {
	Priority  int
	BookID    string
	LibraryID string
	Size      int64
}

type unionSeriesSrcRef struct {
	Priority  int
	SeriesID  string
	LibraryID string
}

type unionMergedSeries struct {
	ID             string
	CID            string
	Name           string
	RelativePath   string
	OneShot        bool
	FileModifiedAt sql.NullString
	Sources        []unionSeriesSrcRef
	BooksByKey     map[string]*unionMergedBook
	Books          []*unionMergedBook
}

// RefreshUnionLibrary materializes an OverlayFS-style volume-level merge of all configured
// member libraries into the union library's series/books rows, preserving deterministic IDs,
// inheriting metadata/progress, and migrating collections/readlists from enrolled member libraries.
func (s *Store) RefreshUnionLibrary(ctx context.Context, unionLibraryID string) (UnionRefreshStats, error) {
	s.unionMu.Lock()
	defer s.unionMu.Unlock()

	unionLib, err := s.LibraryBasic(ctx, unionLibraryID)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	if unionLib.EffectiveProvider() != ProviderUnion {
		return UnionRefreshStats{}, errors.New("library is not a union library")
	}

	stats := UnionRefreshStats{
		UnionLibraryID: unionLibraryID,
		MemberCount:    len(unionLib.UnionLayers),
	}

	memberIDs := make([]string, 0, len(unionLib.UnionLayers))
	prioByLib := make(map[string]int, len(unionLib.UnionLayers))
	for _, layer := range unionLib.UnionLayers {
		memberIDs = append(memberIDs, layer.MemberLibraryID)
		prioByLib[layer.MemberLibraryID] = layer.Priority
	}

	// 1. Load all physical series & books from member libraries ordered by layer priority.
	seriesMap := make(map[string]*unionMergedSeries)
	physSeriesKey := make(map[string]string) // physical series ID -> normalized series key

	if len(memberIDs) > 0 {
		args := make([]any, len(memberIDs))
		for i, id := range memberIDs {
			args[i] = id
		}
		sRows, err := s.db.QueryContext(ctx, `
SELECT s.id,s.library_id,ul.priority,s.name,s.relative_path,s.one_shot,s.file_modified_at
FROM series s
JOIN union_layers ul ON ul.member_library_id=s.library_id
WHERE ul.union_library_id=?
ORDER BY ul.priority ASC,s.id ASC`, unionLibraryID)
		if err != nil {
			return UnionRefreshStats{}, err
		}
		for sRows.Next() {
			var ps unionPhysSeries
			var oneShotInt int
			if err := sRows.Scan(
				&ps.ID, &ps.LibraryID, &ps.Priority, &ps.Name, &ps.RelativePath, &oneShotInt, &ps.FileModifiedAt,
			); err != nil {
				sRows.Close()
				return UnionRefreshStats{}, err
			}
			ps.OneShot = oneShotInt != 0
			sKey := NormalizeUnionSeriesPath(ps.RelativePath)
			physSeriesKey[ps.ID] = sKey
			ms, exists := seriesMap[sKey]
			if !exists {
				var unionSeriesID, unionCID string
				if unionLib.OneShot || ps.OneShot {
					unionSeriesID = id.OneShotSeries(unionLibraryID, sKey)
					unionCID = id.OneShotCID(sKey)
				} else {
					unionSeriesID = id.Series(unionLibraryID, sKey)
					unionCID = sKey
				}
				displayPath := unionLib.Name
				if sKey != "." {
					displayPath = path.Join(unionLib.Name, sKey)
				}
				ms = &unionMergedSeries{
					ID:             unionSeriesID,
					CID:            unionCID,
					Name:           ps.Name,
					RelativePath:   displayPath,
					OneShot:        unionLib.OneShot || ps.OneShot,
					FileModifiedAt: ps.FileModifiedAt,
					BooksByKey:     make(map[string]*unionMergedBook),
				}
				seriesMap[sKey] = ms
			}
			// Keep at most one source series per layer priority
			hasLayer := false
			for _, existingSrc := range ms.Sources {
				if existingSrc.Priority == ps.Priority {
					hasLayer = true
					break
				}
			}
			if !hasLayer {
				ms.Sources = append(ms.Sources, unionSeriesSrcRef{
					Priority:  ps.Priority,
					SeriesID:  ps.ID,
					LibraryID: ps.LibraryID,
				})
			}
		}
		if err := sRows.Err(); err != nil {
			sRows.Close()
			return UnionRefreshStats{}, err
		}
		sRows.Close()

		bRows, err := s.db.QueryContext(ctx, `
SELECT b.id,b.series_id,b.library_id,ul.priority,b.name,b.size,b.pick_code,b.sha1,b.file_created_at,b.file_modified_at
FROM books b
JOIN union_layers ul ON ul.member_library_id=b.library_id
WHERE ul.union_library_id=?
ORDER BY ul.priority ASC,b.number_sort ASC,b.name COLLATE NOCASE ASC,b.id ASC`, unionLibraryID)
		if err != nil {
			return UnionRefreshStats{}, err
		}
		for bRows.Next() {
			var pb unionPhysBook
			if err := bRows.Scan(
				&pb.ID, &pb.SeriesID, &pb.LibraryID, &pb.Priority, &pb.Name,
				&pb.Size, &pb.PickCode, &pb.SHA1, &pb.FileCreatedAt, &pb.FileModifiedAt,
			); err != nil {
				bRows.Close()
				return UnionRefreshStats{}, err
			}
			sKey, ok := physSeriesKey[pb.SeriesID]
			if !ok {
				continue
			}
			ms := seriesMap[sKey]
			if ms == nil {
				continue
			}
			bKey := sKey + "\x00" + pb.Name
			if ms.OneShot {
				bKey = sKey
			}
			mb, exists := ms.BooksByKey[bKey]
			if !exists {
				mb = &unionMergedBook{
					ID:             id.Book(unionLibraryID, bKey),
					FileID:         bKey,
					Name:           pb.Name,
					Size:           pb.Size,
					PickCode:       pb.PickCode,
					SHA1:           pb.SHA1,
					FileCreatedAt:  pb.FileCreatedAt,
					FileModifiedAt: pb.FileModifiedAt,
				}
				ms.BooksByKey[bKey] = mb
			}
			hasLayer := false
			for _, existingSrc := range mb.Sources {
				if existingSrc.Priority == pb.Priority {
					hasLayer = true
					break
				}
			}
			if !hasLayer {
				mb.Sources = append(mb.Sources, unionBookSrcRef{
					Priority:  pb.Priority,
					BookID:    pb.ID,
					LibraryID: pb.LibraryID,
					Size:      pb.Size,
				})
			}
		}
		if err := bRows.Err(); err != nil {
			bRows.Close()
			return UnionRefreshStats{}, err
		}
		bRows.Close()
	}

	// Sort books naturally per series and drop empty series
	mergedList := make([]*unionMergedSeries, 0, len(seriesMap))
	for _, ms := range seriesMap {
		if len(ms.BooksByKey) == 0 {
			continue
		}
		books := make([]*unionMergedBook, 0, len(ms.BooksByKey))
		for _, mb := range ms.BooksByKey {
			books = append(books, mb)
		}
		slices.SortFunc(books, func(a, b *unionMergedBook) int {
			if natsort.Less(a.Name, b.Name) {
				return -1
			}
			if natsort.Less(b.Name, a.Name) {
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
		for idx, mb := range books {
			mb.NumberSort = float64(idx + 1)
		}
		ms.Books = books
		mergedList = append(mergedList, ms)
	}

	refreshTag, err := randomHexID(8)
	if err != nil {
		refreshTag = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	seenScanID := "union_" + refreshTag
	now := nowText()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	defer tx.Rollback()

	// 2. Materialize series & books into standard catalog tables
	stmtSeries, err := tx.PrepareContext(ctx, `
INSERT INTO series(
 id,library_id,cid,name,relative_path,one_shot,file_modified_at,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(library_id,cid) DO UPDATE SET
 name=excluded.name,
 relative_path=excluded.relative_path,
 one_shot=excluded.one_shot,
 file_modified_at=excluded.file_modified_at,
 updated_at=excluded.updated_at,
 seen_scan_id=excluded.seen_scan_id`)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	defer stmtSeries.Close()

	stmtBook, err := tx.PrepareContext(ctx, `
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(library_id,file_id) DO UPDATE SET
 series_id=excluded.series_id,
 parent_cid=excluded.parent_cid,
 name=excluded.name,
 size=excluded.size,
 pick_code=excluded.pick_code,
 sha1=excluded.sha1,
 file_created_at=excluded.file_created_at,
 file_modified_at=excluded.file_modified_at,
 number_sort=excluded.number_sort,
 updated_at=excluded.updated_at,
 seen_scan_id=excluded.seen_scan_id`)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	defer stmtBook.Close()

	for _, ms := range mergedList {
		if _, err := stmtSeries.ExecContext(ctx,
			ms.ID, unionLibraryID, ms.CID, ms.Name, ms.RelativePath,
			ms.OneShot, ms.FileModifiedAt, now, now, seenScanID,
		); err != nil {
			return UnionRefreshStats{}, err
		}
		stats.SeriesCount++
		for _, mb := range ms.Books {
			if _, err := stmtBook.ExecContext(ctx,
				mb.ID, ms.ID, unionLibraryID, mb.FileID, ms.CID, mb.Name,
				mb.Size, mb.PickCode, mb.SHA1, mb.FileCreatedAt, mb.FileModifiedAt,
				mb.NumberSort, now, now, seenScanID,
			); err != nil {
				return UnionRefreshStats{}, err
			}
			stats.BooksCount++
		}
	}

	// 3. Remove stale union books & series no longer present in any member layer
	resDelBooks, err := tx.ExecContext(ctx, `DELETE FROM books WHERE library_id=? AND seen_scan_id<>?`, unionLibraryID, seenScanID)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	if n, _ := resDelBooks.RowsAffected(); n > 0 {
		stats.RemovedStaleBooks = int(n)
	}
	resDelSeries, err := tx.ExecContext(ctx, `DELETE FROM series WHERE library_id=? AND seen_scan_id<>?`, unionLibraryID, seenScanID)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	if n, _ := resDelSeries.RowsAffected(); n > 0 {
		stats.RemovedStaleSeries = int(n)
	}

	// 4. Rebuild union_series_sources & union_book_sources mappings
	if _, err := tx.ExecContext(ctx, `
DELETE FROM union_series_sources
WHERE union_series_id IN (SELECT id FROM series WHERE library_id=?)`, unionLibraryID); err != nil {
		return UnionRefreshStats{}, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM union_book_sources
WHERE union_book_id IN (SELECT id FROM books WHERE library_id=?)`, unionLibraryID); err != nil {
		return UnionRefreshStats{}, err
	}

	stmtSerSrc, err := tx.PrepareContext(ctx, `
INSERT INTO union_series_sources(union_series_id,priority,source_series_id,source_library_id)
VALUES(?,?,?,?)`)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	defer stmtSerSrc.Close()

	stmtBookSrc, err := tx.PrepareContext(ctx, `
INSERT INTO union_book_sources(union_book_id,priority,source_book_id,source_library_id,source_size)
VALUES(?,?,?,?,?)`)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	defer stmtBookSrc.Close()

	for _, ms := range mergedList {
		for _, src := range ms.Sources {
			if _, err := stmtSerSrc.ExecContext(ctx, ms.ID, src.Priority, src.SeriesID, src.LibraryID); err != nil {
				return UnionRefreshStats{}, err
			}
		}
		for _, mb := range ms.Books {
			for _, src := range mb.Sources {
				if _, err := stmtBookSrc.ExecContext(ctx, mb.ID, src.Priority, src.BookID, src.LibraryID, src.Size); err != nil {
					return UnionRefreshStats{}, err
				}
			}
		}
	}

	// 5. Inherit series_metadata (and genres/tags/authors) for union series lacking metadata yet.
	inheritedMeta, err := inheritUnionMetadataTx(ctx, tx, unionLibraryID, now)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	stats.InheritedMetadata = inheritedMeta

	// 6. Inherit book_read_progress & book_page_progress for union books without progress (and without reset tombstone).
	inheritedProg, err := inheritUnionProgressTx(ctx, tx, unionLibraryID)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	stats.InheritedProgress = inheritedProg

	// 7. Inherit cached zip_indexes page_count from winning physical book when union book has no zip_indexes row yet.
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO zip_indexes(book_id,version,page_count,index_json,index_duration_ns,created_at,updated_at)
SELECT ubs.union_book_id, z.version, z.page_count, '[]', z.index_duration_ns, z.created_at, z.updated_at
FROM union_book_sources ubs
JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?
JOIN zip_indexes z ON z.book_id=ubs.source_book_id
WHERE ubs.priority = (
  SELECT MIN(u2.priority) FROM union_book_sources u2 WHERE u2.union_book_id=ubs.union_book_id
)`, unionLibraryID); err != nil {
		return UnionRefreshStats{}, err
	}

	// 8. Migrate collections & readlists referencing member physical libraries to the union library IDs.
	migCol, migRL, err := migrateCollectionsAndReadListsTx(ctx, tx, unionLibraryID)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	stats.MigratedCollections = migCol
	stats.MigratedReadLists = migRL

	// 9. Record last refresh status on the union library.
	if _, err := tx.ExecContext(ctx, `
UPDATE libraries
SET last_scan_status='success',last_scan_completed_at=?,last_scan_error='',updated_at=?
WHERE id=?`, now, now, unionLibraryID); err != nil {
		return UnionRefreshStats{}, err
	}

	if err := tx.Commit(); err != nil {
		return UnionRefreshStats{}, err
	}
	return stats, nil
}

func inheritUnionMetadataTx(ctx context.Context, tx *sql.Tx, unionLibraryID, now string) (int, error) {
	// Pick best source series per union series that doesn't already have series_metadata:
	// prefer scrape_status='matched', then lowest priority number (highest layer priority).
	rows, err := tx.QueryContext(ctx, `
SELECT uss.union_series_id, uss.source_series_id
FROM union_series_sources uss
JOIN series us ON us.id=uss.union_series_id AND us.library_id=?
JOIN series_metadata sm ON sm.series_id=uss.source_series_id
LEFT JOIN series_metadata um ON um.series_id=uss.union_series_id
WHERE um.series_id IS NULL
ORDER BY uss.union_series_id ASC,
 CASE WHEN sm.scrape_status='matched' THEN 0 WHEN sm.title<>'' OR sm.summary<>'' THEN 1 ELSE 2 END ASC,
 uss.priority ASC`, unionLibraryID)
	if err != nil {
		return 0, err
	}
	type pair struct{ unionID, srcID string }
	var chosen []pair
	seen := map[string]bool{}
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.unionID, &p.srcID); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[p.unionID] {
			seen[p.unionID] = true
			chosen = append(chosen, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, p := range chosen {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO series_metadata(
 series_id,bgm_subject_id,scrape_status,scrape_confidence,scrape_synced_at,
 cover_source,bgm_cover_url,bgm_cover_path,bgm_score,bgm_rank,
 status,status_lock,title,title_lock,title_sort,title_sort_lock,
 summary,summary_lock,reading_direction,reading_direction_lock,
 publisher,publisher_lock,age_rating,age_rating_lock,
 language,language_lock,total_book_count,total_book_count_lock,
 release_date,release_date_lock,genres_json,genres_lock,
 tags_json,tags_lock,authors_json,authors_lock,
 alternate_titles_json,alternate_titles_lock,links_json,links_lock,
 sharing_labels_json,sharing_labels_lock,is_nsfw,nsfw_lock,updated_at
)
SELECT
 ?,bgm_subject_id,scrape_status,scrape_confidence,scrape_synced_at,
 cover_source,bgm_cover_url,bgm_cover_path,bgm_score,bgm_rank,
 status,status_lock,title,title_lock,title_sort,title_sort_lock,
 summary,summary_lock,reading_direction,reading_direction_lock,
 publisher,publisher_lock,age_rating,age_rating_lock,
 language,language_lock,total_book_count,total_book_count_lock,
 release_date,release_date_lock,genres_json,genres_lock,
 tags_json,tags_lock,authors_json,authors_lock,
 alternate_titles_json,alternate_titles_lock,links_json,links_lock,
 sharing_labels_json,sharing_labels_lock,is_nsfw,nsfw_lock,?
FROM series_metadata WHERE series_id=?`, p.unionID, now, p.srcID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO series_genres(series_id,genre)
SELECT ?,genre FROM series_genres WHERE series_id=?`, p.unionID, p.srcID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO series_tags(series_id,tag)
SELECT ?,tag FROM series_tags WHERE series_id=?`, p.unionID, p.srcID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO series_authors(series_id,name,role)
SELECT ?,name,role FROM series_authors WHERE series_id=?`, p.unionID, p.srcID); err != nil {
			return 0, err
		}
	}
	return len(chosen), nil
}

func inheritUnionProgressTx(ctx context.Context, tx *sql.Tx, unionLibraryID string) (int, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT ub.id, ub.series_id, sp.completed, sp.page, sp.read_date, sp.completed_at, sp.updated_at
FROM books ub
JOIN union_book_sources ubs ON ubs.union_book_id=ub.id
JOIN book_read_progress sp ON sp.book_id=ubs.source_book_id
LEFT JOIN book_read_progress up ON up.book_id=ub.id
LEFT JOIN book_read_progress_tombstones ut ON ut.book_id=ub.id
WHERE ub.library_id=? AND up.book_id IS NULL AND ut.book_id IS NULL
ORDER BY ub.id ASC, sp.completed DESC, sp.updated_at DESC, ubs.priority ASC`, unionLibraryID)
	if err != nil {
		return 0, err
	}
	type progRow struct {
		bookID, seriesID      string
		completed             int
		page                  sql.NullInt64
		readDate, completedAt sql.NullString
		updatedAt             string
	}
	var chosen []progRow
	seen := map[string]bool{}
	for rows.Next() {
		var r progRow
		if err := rows.Scan(&r.bookID, &r.seriesID, &r.completed, &r.page, &r.readDate, &r.completedAt, &r.updatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[r.bookID] {
			seen[r.bookID] = true
			chosen = append(chosen, r)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, r := range chosen {
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO book_read_progress(book_id,series_id,completed,page,read_date,completed_at,updated_at)
VALUES(?,?,?,?,?,?,?)`, r.bookID, r.seriesID, r.completed, r.page, r.readDate, r.completedAt, r.updatedAt); err != nil {
			return 0, err
		}
	}

	// Also inherit inferred book_page_progress where missing on union books
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO book_page_progress(book_id,series_id,last_loaded_page,max_loaded_page,page_count,updated_at)
SELECT ub.id, ub.series_id, sp.last_loaded_page, sp.max_loaded_page, sp.page_count, sp.updated_at
FROM books ub
JOIN union_book_sources ubs ON ubs.union_book_id=ub.id
JOIN book_page_progress sp ON sp.book_id=ubs.source_book_id
LEFT JOIN book_page_progress up ON up.book_id=ub.id
WHERE ub.library_id=? AND up.book_id IS NULL
ORDER BY sp.updated_at DESC, ubs.priority ASC`, unionLibraryID); err != nil {
		return 0, err
	}
	return len(chosen), nil
}

func migrateCollectionsAndReadListsTx(ctx context.Context, tx *sql.Tx, unionLibraryID string) (int, int, error) {
	// Migrate collection_series rows referencing member physical series -> union series
	resColIns, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO collection_series(collection_id,series_id,position)
SELECT cs.collection_id, uss.union_series_id, cs.position
FROM collection_series cs
JOIN union_series_sources uss ON uss.source_series_id=cs.series_id
JOIN series us ON us.id=uss.union_series_id AND us.library_id=?`, unionLibraryID)
	if err != nil {
		return 0, 0, err
	}
	migCol, _ := resColIns.RowsAffected()
	if _, err := tx.ExecContext(ctx, `
DELETE FROM collection_series
WHERE series_id IN (
  SELECT uss.source_series_id FROM union_series_sources uss
  JOIN series us ON us.id=uss.union_series_id AND us.library_id=?
)`, unionLibraryID); err != nil {
		return 0, 0, err
	}

	// Migrate readlist_books rows referencing member physical books -> union books
	resRLIns, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO readlist_books(readlist_id,book_id,position)
SELECT rb.readlist_id, ubs.union_book_id, rb.position
FROM readlist_books rb
JOIN union_book_sources ubs ON ubs.source_book_id=rb.book_id
JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?`, unionLibraryID)
	if err != nil {
		return 0, 0, err
	}
	migRL, _ := resRLIns.RowsAffected()
	if _, err := tx.ExecContext(ctx, `
DELETE FROM readlist_books
WHERE book_id IN (
  SELECT ubs.source_book_id FROM union_book_sources ubs
  JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?
)`, unionLibraryID); err != nil {
		return 0, 0, err
	}

	return int(migCol), int(migRL), nil
}

// UnionBookSourceBooks resolves ordered physical replica Books (highest priority first)
// backing a materialized union book, plus their source metadata for UI display.
func (s *Store) UnionBookSourceBooks(ctx context.Context, unionBookID string) ([]Book, []UnionBookSource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT b.id,b.series_id,b.library_id,b.file_id,b.parent_cid,b.name,b.size,b.pick_code,b.sha1,
 b.file_created_at,b.file_modified_at,b.number_sort,b.created_at,b.updated_at,coalesce(z.page_count,0),
 ubs.priority,l.name,l.provider_type,coalesce(w.name,''),ubs.source_size
FROM union_book_sources ubs
JOIN books b ON b.id=ubs.source_book_id
JOIN libraries l ON l.id=ubs.source_library_id
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
LEFT JOIN zip_indexes z ON z.book_id=b.id
WHERE ubs.union_book_id=?
ORDER BY ubs.priority ASC`, unionBookID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var books []Book
	var sources []UnionBookSource
	for rows.Next() {
		var (
			b                         Book
			fileCreated, fileModified sql.NullString
			created, updated          string
			src                       UnionBookSource
		)
		src.UnionBookID = unionBookID
		if err := rows.Scan(
			&b.ID, &b.SeriesID, &b.LibraryID, &b.FileID, &b.ParentCID,
			&b.Name, &b.Size, &b.PickCode, &b.SHA1, &fileCreated, &fileModified,
			&b.NumberSort, &created, &updated, &b.PageCount,
			&src.Priority, &src.SourceLibraryName, &src.ProviderType, &src.WebDAVAccountName, &src.SourceSize,
		); err != nil {
			return nil, nil, err
		}
		b.FileCreatedAt = parseNullableTime(fileCreated)
		b.FileModifiedAt = parseNullableTime(fileModified)
		b.CreatedAt = parseTime(created)
		b.UpdatedAt = parseTime(updated)
		src.SourceBookID = b.ID
		src.SourceLibraryID = b.LibraryID
		src.Active = len(sources) == 0
		books = append(books, b)
		sources = append(sources, src)
	}
	return books, sources, rows.Err()
}

// UnionBookSourceIDs returns all physical source book IDs backing a union book (empty for non-union books).
func (s *Store) UnionBookSourceIDs(ctx context.Context, unionBookID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT source_book_id FROM union_book_sources WHERE union_book_id=? ORDER BY priority ASC`, unionBookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UnionSeriesSourceDetails returns ordered physical source series AND per-book replica sources
// for a union series (used by seriesRichDetail to annotate each volume's active & fallback layers).
func (s *Store) UnionSeriesSourceDetails(
	ctx context.Context,
	unionSeriesID string,
) ([]UnionSeriesSource, map[string][]UnionBookSource, error) {
	sRows, err := s.db.QueryContext(ctx, `
SELECT uss.union_series_id,uss.priority,uss.source_series_id,uss.source_library_id,
 l.name,l.provider_type,coalesce(w.name,'')
FROM union_series_sources uss
JOIN libraries l ON l.id=uss.source_library_id
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
WHERE uss.union_series_id=?
ORDER BY uss.priority ASC`, unionSeriesID)
	if err != nil {
		return nil, nil, err
	}
	var seriesSources []UnionSeriesSource
	for sRows.Next() {
		var item UnionSeriesSource
		if err := sRows.Scan(
			&item.UnionSeriesID, &item.Priority, &item.SourceSeriesID, &item.SourceLibraryID,
			&item.SourceLibraryName, &item.ProviderType, &item.WebDAVAccountName,
		); err != nil {
			sRows.Close()
			return nil, nil, err
		}
		seriesSources = append(seriesSources, item)
	}
	if err := sRows.Err(); err != nil {
		sRows.Close()
		return nil, nil, err
	}
	sRows.Close()

	bRows, err := s.db.QueryContext(ctx, `
SELECT ubs.union_book_id,ubs.priority,ubs.source_book_id,ubs.source_library_id,
 l.name,l.provider_type,coalesce(w.name,''),ubs.source_size
FROM union_book_sources ubs
JOIN books ub ON ub.id=ubs.union_book_id
JOIN libraries l ON l.id=ubs.source_library_id
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
WHERE ub.series_id=?
ORDER BY ubs.union_book_id ASC, ubs.priority ASC`, unionSeriesID)
	if err != nil {
		return nil, nil, err
	}
	defer bRows.Close()
	bookSources := make(map[string][]UnionBookSource)
	for bRows.Next() {
		var item UnionBookSource
		if err := bRows.Scan(
			&item.UnionBookID, &item.Priority, &item.SourceBookID, &item.SourceLibraryID,
			&item.SourceLibraryName, &item.ProviderType, &item.WebDAVAccountName, &item.SourceSize,
		); err != nil {
			return nil, nil, err
		}
		item.Active = len(bookSources[item.UnionBookID]) == 0
		bookSources[item.UnionBookID] = append(bookSources[item.UnionBookID], item)
	}
	return seriesSources, bookSources, bRows.Err()
}

// UnionSeriesCoverFallback returns ordered physical source series IDs backing a union series
// so thumbnail.Service can reuse an already-extracted physical cover read-only without copying files.
func (s *Store) UnionSeriesCoverFallback(ctx context.Context, unionSeriesID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT source_series_id FROM union_series_sources WHERE union_series_id=? ORDER BY priority ASC`, unionSeriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// LookupSeriesByBangumiSubject returns all visible series matched to a Bangumi subject ID
// (respecting enabled libraries, union sub-library hiding, and ShowNSFW).
func (s *Store) LookupSeriesByBangumiSubject(ctx context.Context, subjectID int64) ([]BangumiMatch, error) {
	if subjectID <= 0 {
		return []BangumiMatch{}, nil
	}
	query := `
SELECT s.id, COALESCE(NULLIF(sm.title,''), s.name), s.name, s.library_id,
 l.name, l.provider_type, coalesce(w.name,''), s.books_count, s.one_shot,
 (SELECT count(*) FROM book_read_progress p WHERE p.series_id=s.id AND p.completed=1)
FROM series_metadata sm
JOIN series s ON s.id=sm.series_id
JOIN libraries l ON l.id=s.library_id AND l.enabled=1
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
WHERE sm.bgm_subject_id=? AND ul.union_library_id IS NULL`
	if !s.ShowNSFW(ctx) {
		query += ` AND sm.is_nsfw=0`
	}
	query += `
ORDER BY CASE WHEN l.provider_type='union' THEN 0 ELSE 1 END ASC,
 s.books_count DESC, l.name COLLATE NOCASE ASC, s.id ASC`
	rows, err := s.db.QueryContext(ctx, query, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BangumiMatch, 0)
	for rows.Next() {
		var item BangumiMatch
		var oneShotInt int
		if err := rows.Scan(
			&item.SeriesID, &item.Title, &item.OriginalName, &item.LibraryID,
			&item.LibraryName, &item.ProviderType, &item.WebDAVAccountName,
			&item.BooksCount, &oneShotInt, &item.BooksReadCount,
		); err != nil {
			return nil, err
		}
		item.OneShot = oneShotInt != 0
		item.URL = "/series/" + item.SeriesID
		out = append(out, item)
	}
	return out, rows.Err()
}
