package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

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
	WinPriority    int
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
	ID                   string
	CID                  string
	Name                 string
	RelativePath         string
	OneShot              bool
	FileModifiedAt       sql.NullString
	BooksCount           int
	LatestBookModifiedAt sql.NullString
	LatestBookCreatedAt  sql.NullString
	SortModifiedAt       string
	SortCreatedAt        string
	CreatedAt            string
	UpdatedAt            string
	Sources              []unionSeriesSrcRef
	BooksByKey           map[string]*unionMergedBook
	Books                []*unionMergedBook
}

type existingUnionSeries struct {
	ID                   string
	CID                  string
	Name                 string
	RelativePath         string
	OneShot              bool
	FileModifiedAt       sql.NullString
	BooksCount           int
	LatestBookModifiedAt sql.NullString
	LatestBookCreatedAt  sql.NullString
	SortModifiedAt       string
	SortCreatedAt        string
	CreatedAt            string
	UpdatedAt            string
}

type existingUnionBook struct {
	ID             string
	SeriesID       string
	FileID         string
	ParentCID      string
	Name           string
	Size           int64
	PickCode       string
	SHA1           string
	FileCreatedAt  sql.NullString
	FileModifiedAt sql.NullString
	NumberSort     float64
}

type unionBookRow struct {
	SeriesID  string
	ParentCID string
	Book      *unionMergedBook
}

type unionSeriesSrcRow struct {
	UnionSeriesID string
	Src           unionSeriesSrcRef
}

type unionBookSrcRow struct {
	UnionBookID string
	Src         unionBookSrcRef
}

const (
	unionBulkChunkSeries      = 2500
	unionBulkCacheBoostOps    = 5000
	unionBulkCacheSizePragmas = -131072 // 128 MiB temporary page cache during large union writes
	unionSeriesBatchRows      = 50
	unionBookBatchRows        = 50
	unionSeriesSrcBatchRows   = 100
	unionBookSrcBatchRows     = 150
)

func buildMultiInsertSQL(prefix, rowTuple string, rows int) string {
	var sb strings.Builder
	sb.Grow(len(prefix) + rows*(len(rowTuple)+1))
	sb.WriteString(prefix)
	for i := 0; i < rows; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(rowTuple)
	}
	return sb.String()
}

func nullStrEqual(a, b sql.NullString) bool {
	if a.Valid != b.Valid {
		return false
	}
	if !a.Valid {
		return true
	}
	return a.String == b.String
}

func unionSeriesSourcesEqual(a, b []unionSeriesSrcRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func unionBookSourcesEqual(a, b []unionBookSrcRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func computeUnionSeriesRollup(ms *unionMergedSeries, createdAt, updatedAt string) {
	ms.BooksCount = len(ms.Books)
	var maxMod, maxCre string
	for _, mb := range ms.Books {
		if mb.FileModifiedAt.Valid && mb.FileModifiedAt.String != "" && mb.FileModifiedAt.String > maxMod {
			maxMod = mb.FileModifiedAt.String
		}
		if mb.FileCreatedAt.Valid && mb.FileCreatedAt.String != "" && mb.FileCreatedAt.String > maxCre {
			maxCre = mb.FileCreatedAt.String
		}
	}
	if maxMod != "" {
		ms.LatestBookModifiedAt = sql.NullString{String: maxMod, Valid: true}
	} else {
		ms.LatestBookModifiedAt = sql.NullString{}
	}
	if maxCre != "" {
		ms.LatestBookCreatedAt = sql.NullString{String: maxCre, Valid: true}
	} else {
		ms.LatestBookCreatedAt = sql.NullString{}
	}
	ms.CreatedAt = createdAt
	ms.UpdatedAt = updatedAt
	switch {
	case maxMod != "":
		ms.SortModifiedAt = maxMod
	case ms.FileModifiedAt.Valid && ms.FileModifiedAt.String != "":
		ms.SortModifiedAt = ms.FileModifiedAt.String
	default:
		ms.SortModifiedAt = updatedAt
	}
	if maxCre != "" {
		ms.SortCreatedAt = maxCre
	} else {
		ms.SortCreatedAt = createdAt
	}
}

func execChunkedDelete(ctx context.Context, tx *sql.Tx, queryPrefix string, ids []string) error {
	for _, chunk := range chunkStrings(ids, sqlInChunkSize) {
		args := make([]any, len(chunk))
		for i, idVal := range chunk {
			args[i] = idVal
		}
		if _, err := tx.ExecContext(ctx, queryPrefix+Placeholders(len(chunk)), args...); err != nil {
			return err
		}
	}
	return nil
}

// RefreshUnionLibrary materializes an OverlayFS-style volume-level merge of all configured
// member libraries into the union library's series/books rows, preserving deterministic IDs,
// inheriting metadata/progress, and migrating collections/readlists from enrolled member libraries.
// Unchanged rows are skipped via in-memory diffing to avoid redundant trigger/index amplification.
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

	// 1. Load all physical member series and existing union series outside any write lock.
	seriesMap := make(map[string]*unionMergedSeries)
	physSeriesKey := make(map[string]string) // physical series ID -> normalized series key
	memberPrio := make(map[string]int, len(unionLib.UnionLayers))
	scanLibIDs := make([]string, 0, len(unionLib.UnionLayers)+1)
	for _, layer := range unionLib.UnionLayers {
		memberPrio[layer.MemberLibraryID] = layer.Priority
		scanLibIDs = append(scanLibIDs, layer.MemberLibraryID)
	}
	scanLibIDs = append(scanLibIDs, unionLibraryID)

	if len(unionLib.UnionLayers) > 0 {
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
	}

	// Load existing union series before scanning books.
	existingSeries := make(map[string]existingUnionSeries)
	esRows, err := s.db.QueryContext(ctx, `
SELECT id,cid,name,relative_path,one_shot,file_modified_at,
 books_count,latest_book_modified_at,latest_book_created_at,sort_modified_at,sort_created_at,
 created_at,updated_at
FROM series WHERE library_id=?`, unionLibraryID)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	for esRows.Next() {
		var es existingUnionSeries
		var oneShotInt int
		if err := esRows.Scan(
			&es.ID, &es.CID, &es.Name, &es.RelativePath, &oneShotInt, &es.FileModifiedAt,
			&es.BooksCount, &es.LatestBookModifiedAt, &es.LatestBookCreatedAt, &es.SortModifiedAt, &es.SortCreatedAt,
			&es.CreatedAt, &es.UpdatedAt,
		); err != nil {
			esRows.Close()
			return UnionRefreshStats{}, err
		}
		es.OneShot = oneShotInt != 0
		existingSeries[es.ID] = es
	}
	if err := esRows.Err(); err != nil {
		esRows.Close()
		return UnionRefreshStats{}, err
	}
	esRows.Close()

	// 2. Load physical member books AND existing union books in a single scan (no ORDER BY temp sort).
	// Use a sequential table scan when scanning a large catalog to avoid hundreds of thousands of random index lookups.
	existingBooks := make(map[string]existingUnionBook)
	if len(unionLib.UnionLayers) > 0 || len(existingSeries) > 0 {
		libArgs := make([]any, len(scanLibIDs))
		for i, lid := range scanLibIDs {
			libArgs[i] = lid
		}
		var targetBookCount int64
		_ = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM books WHERE library_id IN `+Placeholders(len(libArgs)),
			libArgs...,
		).Scan(&targetBookCount)

		whereCol := "b.library_id"
		if targetBookCount > 15000 {
			whereCol = "+b.library_id"
		}
		bRows, err := s.db.QueryContext(ctx, `
SELECT b.id,b.series_id,b.library_id,b.file_id,b.parent_cid,b.name,b.size,b.pick_code,b.sha1,
 b.file_created_at,b.file_modified_at,b.number_sort
FROM books b
WHERE `+whereCol+` IN `+Placeholders(len(libArgs)), libArgs...)
		if err != nil {
			return UnionRefreshStats{}, err
		}
		for bRows.Next() {
			var (
				bid, sid, lid, fid, pcid, name, pick, sha1 string
				size                                       int64
				fcre, fmod                                 sql.NullString
				numSort                                    float64
			)
			if err := bRows.Scan(&bid, &sid, &lid, &fid, &pcid, &name, &size, &pick, &sha1, &fcre, &fmod, &numSort); err != nil {
				bRows.Close()
				return UnionRefreshStats{}, err
			}
			if lid == unionLibraryID {
				existingBooks[bid] = existingUnionBook{
					ID: bid, SeriesID: sid, FileID: fid, ParentCID: pcid, Name: name,
					Size: size, PickCode: pick, SHA1: sha1, FileCreatedAt: fcre, FileModifiedAt: fmod, NumberSort: numSort,
				}
				continue
			}
			prio, isMember := memberPrio[lid]
			if !isMember {
				continue
			}
			sKey, ok := physSeriesKey[sid]
			if !ok {
				continue
			}
			ms := seriesMap[sKey]
			if ms == nil {
				continue
			}
			bKey := sKey + "\x00" + name
			if ms.OneShot {
				bKey = sKey
			}
			mb, exists := ms.BooksByKey[bKey]
			if !exists {
				mb = &unionMergedBook{
					ID:             id.Book(unionLibraryID, bKey),
					FileID:         bKey,
					Name:           name,
					Size:           size,
					PickCode:       pick,
					SHA1:           sha1,
					FileCreatedAt:  fcre,
					FileModifiedAt: fmod,
					WinPriority:    prio,
				}
				ms.BooksByKey[bKey] = mb
			} else if prio < mb.WinPriority {
				mb.Size = size
				mb.PickCode = pick
				mb.SHA1 = sha1
				mb.FileCreatedAt = fcre
				mb.FileModifiedAt = fmod
				mb.WinPriority = prio
			}
			hasLayer := false
			for _, existingSrc := range mb.Sources {
				if existingSrc.Priority == prio {
					hasLayer = true
					break
				}
			}
			if !hasLayer {
				mb.Sources = append(mb.Sources, unionBookSrcRef{
					Priority:  prio,
					BookID:    bid,
					LibraryID: lid,
					Size:      size,
				})
			}
		}
		if err := bRows.Err(); err != nil {
			bRows.Close()
			return UnionRefreshStats{}, err
		}
		bRows.Close()
	}

	// Sort series by ID, books naturally per series, and each book's sources by priority; drop empty series.
	mergedList := make([]*unionMergedSeries, 0, len(seriesMap))
	totalMergedBooks := 0
	for _, ms := range seriesMap {
		if len(ms.BooksByKey) == 0 {
			continue
		}
		books := make([]*unionMergedBook, 0, len(ms.BooksByKey))
		for _, mb := range ms.BooksByKey {
			if len(mb.Sources) > 1 {
				slices.SortFunc(mb.Sources, func(a, b unionBookSrcRef) int {
					return a.Priority - b.Priority
				})
			}
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
		ms.BooksByKey = nil
		totalMergedBooks += len(books)
		mergedList = append(mergedList, ms)
	}
	slices.SortFunc(mergedList, func(a, b *unionMergedSeries) int {
		return strings.Compare(a.ID, b.ID)
	})
	stats.SeriesCount = len(mergedList)
	stats.BooksCount = totalMergedBooks

	// Load existing union_series_sources & union_book_sources without ORDER BY temp sorts.
	// Both tables are WITHOUT ROWID keyed on (union_*_id, priority), so direct scans are already ordered.
	existingSeriesSrc := make(map[string][]unionSeriesSrcRef)
	existingBookSrc := make(map[string][]unionBookSrcRef)
	if len(existingSeries) > 0 {
		var ssRows *sql.Rows
		if len(existingSeries) > 2000 {
			ssRows, err = s.db.QueryContext(ctx, `
SELECT union_series_id,priority,source_series_id,source_library_id
FROM union_series_sources`)
		} else {
			ssRows, err = s.db.QueryContext(ctx, `
SELECT uss.union_series_id,uss.priority,uss.source_series_id,uss.source_library_id
FROM series us
JOIN union_series_sources uss ON uss.union_series_id=us.id
WHERE us.library_id=?`, unionLibraryID)
		}
		if err != nil {
			return UnionRefreshStats{}, err
		}
		for ssRows.Next() {
			var uid string
			var ref unionSeriesSrcRef
			if err := ssRows.Scan(&uid, &ref.Priority, &ref.SeriesID, &ref.LibraryID); err != nil {
				ssRows.Close()
				return UnionRefreshStats{}, err
			}
			if _, ok := existingSeries[uid]; ok {
				existingSeriesSrc[uid] = append(existingSeriesSrc[uid], ref)
			}
		}
		if err := ssRows.Err(); err != nil {
			ssRows.Close()
			return UnionRefreshStats{}, err
		}
		ssRows.Close()

		var bsRows *sql.Rows
		if len(existingBooks) > 10000 {
			bsRows, err = s.db.QueryContext(ctx, `
SELECT union_book_id,priority,source_book_id,source_library_id,source_size
FROM union_book_sources`)
		} else {
			bsRows, err = s.db.QueryContext(ctx, `
SELECT ubs.union_book_id,ubs.priority,ubs.source_book_id,ubs.source_library_id,ubs.source_size
FROM books ub
JOIN union_book_sources ubs ON ubs.union_book_id=ub.id
WHERE ub.library_id=?`, unionLibraryID)
		}
		if err != nil {
			return UnionRefreshStats{}, err
		}
		for bsRows.Next() {
			var uid string
			var ref unionBookSrcRef
			if err := bsRows.Scan(&uid, &ref.Priority, &ref.BookID, &ref.LibraryID, &ref.Size); err != nil {
				bsRows.Close()
				return UnionRefreshStats{}, err
			}
			if _, ok := existingBooks[uid]; ok {
				existingBookSrc[uid] = append(existingBookSrc[uid], ref)
			}
		}
		if err := bsRows.Err(); err != nil {
			bsRows.Close()
			return UnionRefreshStats{}, err
		}
		bsRows.Close()
	}

	// 3. Diff desired vs existing state in memory.
	now := nowText()
	desiredSeriesIDs := make(map[string]struct{}, len(mergedList))
	desiredBookIDs := make(map[string]struct{}, totalMergedBooks)
	for _, ms := range mergedList {
		desiredSeriesIDs[ms.ID] = struct{}{}
		for _, mb := range ms.Books {
			desiredBookIDs[mb.ID] = struct{}{}
		}
	}

	var bookIDsToDelete []string
	var seriesIDsToDelete []string
	seriesWithBookChange := make(map[string]bool)
	for bid, eb := range existingBooks {
		if _, ok := desiredBookIDs[bid]; !ok {
			bookIDsToDelete = append(bookIDsToDelete, bid)
			if _, sKeep := desiredSeriesIDs[eb.SeriesID]; sKeep {
				seriesWithBookChange[eb.SeriesID] = true
			}
		}
	}
	for sid := range existingSeries {
		if _, ok := desiredSeriesIDs[sid]; !ok {
			seriesIDsToDelete = append(seriesIDsToDelete, sid)
		}
	}
	stats.RemovedStaleBooks = len(bookIDsToDelete)
	stats.RemovedStaleSeries = len(seriesIDsToDelete)

	type newSeriesBundle struct {
		Series   *unionMergedSeries
		NewBooks []unionBookRow
	}
	var (
		newSeriesBundles        []newSeriesBundle
		seriesToUpdate          []*unionMergedSeries
		booksToInsertStandalone []unionBookRow
		booksToUpdate           []unionBookRow
		seriesSrcDeleteIDs      []string
		seriesSrcInsert         []unionSeriesSrcRow
		bookSrcDeleteIDs        []string
		bookSrcInsert           []unionBookSrcRow
		winningSrcChangedIDs    []string
		totalNewBooksInBundles  int
	)

	for _, ms := range mergedList {
		oldS, sExists := existingSeries[ms.ID]
		var bundleNewBooks []unionBookRow
		for _, mb := range ms.Books {
			oldB, bExists := existingBooks[mb.ID]
			if !bExists {
				row := unionBookRow{SeriesID: ms.ID, ParentCID: ms.CID, Book: mb}
				if !sExists {
					bundleNewBooks = append(bundleNewBooks, row)
				} else {
					booksToInsertStandalone = append(booksToInsertStandalone, row)
					for _, src := range mb.Sources {
						bookSrcInsert = append(bookSrcInsert, unionBookSrcRow{UnionBookID: mb.ID, Src: src})
					}
				}
				seriesWithBookChange[ms.ID] = true
				continue
			}
			if oldB.SeriesID != ms.ID || oldB.FileID != mb.FileID || oldB.ParentCID != ms.CID ||
				oldB.Name != mb.Name || oldB.Size != mb.Size || oldB.PickCode != mb.PickCode ||
				oldB.SHA1 != mb.SHA1 || !nullStrEqual(oldB.FileCreatedAt, mb.FileCreatedAt) ||
				!nullStrEqual(oldB.FileModifiedAt, mb.FileModifiedAt) || oldB.NumberSort != mb.NumberSort {
				booksToUpdate = append(booksToUpdate, unionBookRow{SeriesID: ms.ID, ParentCID: ms.CID, Book: mb})
				seriesWithBookChange[ms.ID] = true
			}
			oldBSrc := existingBookSrc[mb.ID]
			if !unionBookSourcesEqual(oldBSrc, mb.Sources) {
				if len(oldBSrc) > 0 {
					bookSrcDeleteIDs = append(bookSrcDeleteIDs, mb.ID)
				}
				for _, src := range mb.Sources {
					bookSrcInsert = append(bookSrcInsert, unionBookSrcRow{UnionBookID: mb.ID, Src: src})
				}
				if len(oldBSrc) > 0 && len(mb.Sources) > 0 &&
					(oldBSrc[0].BookID != mb.Sources[0].BookID || oldBSrc[0].Size != mb.Sources[0].Size) {
					winningSrcChangedIDs = append(winningSrcChangedIDs, mb.ID)
				}
			}
		}

		if !sExists {
			computeUnionSeriesRollup(ms, now, now)
			newSeriesBundles = append(newSeriesBundles, newSeriesBundle{Series: ms, NewBooks: bundleNewBooks})
			totalNewBooksInBundles += len(bundleNewBooks)
			continue
		}

		computeUnionSeriesRollup(ms, oldS.CreatedAt, oldS.UpdatedAt)
		seriesChanged := oldS.CID != ms.CID || oldS.Name != ms.Name || oldS.RelativePath != ms.RelativePath ||
			oldS.OneShot != ms.OneShot || !nullStrEqual(oldS.FileModifiedAt, ms.FileModifiedAt) ||
			oldS.BooksCount != ms.BooksCount || !nullStrEqual(oldS.LatestBookModifiedAt, ms.LatestBookModifiedAt) ||
			!nullStrEqual(oldS.LatestBookCreatedAt, ms.LatestBookCreatedAt) ||
			oldS.SortModifiedAt != ms.SortModifiedAt || oldS.SortCreatedAt != ms.SortCreatedAt ||
			seriesWithBookChange[ms.ID]
		if seriesChanged {
			computeUnionSeriesRollup(ms, oldS.CreatedAt, now)
			seriesToUpdate = append(seriesToUpdate, ms)
		}
		oldSSrc := existingSeriesSrc[ms.ID]
		if !unionSeriesSourcesEqual(oldSSrc, ms.Sources) {
			if len(oldSSrc) > 0 {
				seriesSrcDeleteIDs = append(seriesSrcDeleteIDs, ms.ID)
			}
			for _, src := range ms.Sources {
				seriesSrcInsert = append(seriesSrcInsert, unionSeriesSrcRow{UnionSeriesID: ms.ID, Src: src})
			}
		}
	}

	// Free large existing maps before entering write phase.
	existingSeries = nil
	existingBooks = nil
	existingSeriesSrc = nil
	existingBookSrc = nil

	totalCatalogOps := len(newSeriesBundles) + len(seriesToUpdate) + len(seriesIDsToDelete) +
		totalNewBooksInBundles + len(booksToInsertStandalone) + len(booksToUpdate) + len(bookIDsToDelete) +
		len(seriesSrcDeleteIDs) + len(seriesSrcInsert) + len(bookSrcDeleteIDs) + len(bookSrcInsert)

	// 4. Execute minimal writes using a dedicated connection (with temporary cache boost for large writes).
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	defer conn.Close()

	if totalCatalogOps > unionBulkCacheBoostOps {
		_, _ = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA cache_size = %d", unionBulkCacheSizePragmas))
		defer conn.ExecContext(context.Background(), "PRAGMA cache_size = -16384")
	}

	const insertSeriesPrefix = `INSERT INTO series(
 id,library_id,cid,name,relative_path,one_shot,file_modified_at,
 books_count,latest_book_modified_at,latest_book_created_at,sort_modified_at,sort_created_at,
 created_at,updated_at,seen_scan_id
) VALUES `
	const insertSeriesTuple = `(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'union')`
	const insertBookPrefix = `INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES `
	const insertBookTuple = `(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'union')`
	const insertBookSQL = insertBookPrefix + insertBookTuple
	const insertSeriesSrcPrefix = `INSERT INTO union_series_sources(union_series_id,priority,source_series_id,source_library_id) VALUES `
	const insertSeriesSrcTuple = `(?,?,?,?)`
	const insertSeriesSrcSQL = insertSeriesSrcPrefix + insertSeriesSrcTuple
	const insertBookSrcPrefix = `INSERT INTO union_book_sources(union_book_id,priority,source_book_id,source_library_id,source_size) VALUES `
	const insertBookSrcTuple = `(?,?,?,?,?)`
	const insertBookSrcSQL = insertBookSrcPrefix + insertBookSrcTuple

	writeNewBundlesTx := func(tx *sql.Tx, bundles []newSeriesBundle) error {
		if len(bundles) == 0 {
			return nil
		}
		var (
			chunkBooks      []unionBookRow
			chunkSeriesSrcs []unionSeriesSrcRow
			chunkBookSrcs   []unionBookSrcRow
		)

		// 1. Multi-row batch insert series
		batchSStmt, err := tx.PrepareContext(ctx, buildMultiInsertSQL(insertSeriesPrefix, insertSeriesTuple, unionSeriesBatchRows))
		if err != nil {
			return err
		}
		defer batchSStmt.Close()
		sArgs := make([]any, 0, unionSeriesBatchRows*14)
		for i := 0; i < len(bundles); {
			end := i + unionSeriesBatchRows
			if end > len(bundles) {
				end = len(bundles)
			}
			sArgs = sArgs[:0]
			for _, b := range bundles[i:end] {
				ms := b.Series
				sArgs = append(sArgs,
					ms.ID, unionLibraryID, ms.CID, ms.Name, ms.RelativePath,
					ms.OneShot, ms.FileModifiedAt,
					ms.BooksCount, ms.LatestBookModifiedAt, ms.LatestBookCreatedAt,
					ms.SortModifiedAt, ms.SortCreatedAt,
					ms.CreatedAt, ms.UpdatedAt,
				)
				for _, src := range ms.Sources {
					chunkSeriesSrcs = append(chunkSeriesSrcs, unionSeriesSrcRow{UnionSeriesID: ms.ID, Src: src})
				}
				chunkBooks = append(chunkBooks, b.NewBooks...)
			}
			if end-i == unionSeriesBatchRows {
				if _, err := batchSStmt.ExecContext(ctx, sArgs...); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, buildMultiInsertSQL(insertSeriesPrefix, insertSeriesTuple, end-i), sArgs...); err != nil {
					return err
				}
			}
			i = end
		}

		// 2. Multi-row batch insert books
		if len(chunkBooks) > 0 {
			batchBStmt, err := tx.PrepareContext(ctx, buildMultiInsertSQL(insertBookPrefix, insertBookTuple, unionBookBatchRows))
			if err != nil {
				return err
			}
			defer batchBStmt.Close()
			bArgs := make([]any, 0, unionBookBatchRows*14)
			for i := 0; i < len(chunkBooks); {
				end := i + unionBookBatchRows
				if end > len(chunkBooks) {
					end = len(chunkBooks)
				}
				bArgs = bArgs[:0]
				for _, br := range chunkBooks[i:end] {
					mb := br.Book
					bArgs = append(bArgs,
						mb.ID, br.SeriesID, unionLibraryID, mb.FileID, br.ParentCID, mb.Name,
						mb.Size, mb.PickCode, mb.SHA1, mb.FileCreatedAt, mb.FileModifiedAt,
						mb.NumberSort, now, now,
					)
					for _, src := range mb.Sources {
						chunkBookSrcs = append(chunkBookSrcs, unionBookSrcRow{UnionBookID: mb.ID, Src: src})
					}
				}
				if end-i == unionBookBatchRows {
					if _, err := batchBStmt.ExecContext(ctx, bArgs...); err != nil {
						return err
					}
				} else {
					if _, err := tx.ExecContext(ctx, buildMultiInsertSQL(insertBookPrefix, insertBookTuple, end-i), bArgs...); err != nil {
						return err
					}
				}
				i = end
			}
		}

		// 3. Multi-row batch insert union_series_sources (already sorted by union_series_id, priority)
		if len(chunkSeriesSrcs) > 0 {
			batchSSStmt, err := tx.PrepareContext(ctx, buildMultiInsertSQL(insertSeriesSrcPrefix, insertSeriesSrcTuple, unionSeriesSrcBatchRows))
			if err != nil {
				return err
			}
			defer batchSSStmt.Close()
			ssArgs := make([]any, 0, unionSeriesSrcBatchRows*4)
			for i := 0; i < len(chunkSeriesSrcs); {
				end := i + unionSeriesSrcBatchRows
				if end > len(chunkSeriesSrcs) {
					end = len(chunkSeriesSrcs)
				}
				ssArgs = ssArgs[:0]
				for _, row := range chunkSeriesSrcs[i:end] {
					ssArgs = append(ssArgs, row.UnionSeriesID, row.Src.Priority, row.Src.SeriesID, row.Src.LibraryID)
				}
				if end-i == unionSeriesSrcBatchRows {
					if _, err := batchSSStmt.ExecContext(ctx, ssArgs...); err != nil {
						return err
					}
				} else {
					if _, err := tx.ExecContext(ctx, buildMultiInsertSQL(insertSeriesSrcPrefix, insertSeriesSrcTuple, end-i), ssArgs...); err != nil {
						return err
					}
				}
				i = end
			}
		}

		// 4. Multi-row batch insert union_book_sources (sorted by WITHOUT ROWID primary key)
		if len(chunkBookSrcs) > 0 {
			slices.SortFunc(chunkBookSrcs, func(a, b unionBookSrcRow) int {
				if c := strings.Compare(a.UnionBookID, b.UnionBookID); c != 0 {
					return c
				}
				return a.Src.Priority - b.Src.Priority
			})
			batchBSStmt, err := tx.PrepareContext(ctx, buildMultiInsertSQL(insertBookSrcPrefix, insertBookSrcTuple, unionBookSrcBatchRows))
			if err != nil {
				return err
			}
			defer batchBSStmt.Close()
			bsArgs := make([]any, 0, unionBookSrcBatchRows*5)
			for i := 0; i < len(chunkBookSrcs); {
				end := i + unionBookSrcBatchRows
				if end > len(chunkBookSrcs) {
					end = len(chunkBookSrcs)
				}
				bsArgs = bsArgs[:0]
				for _, row := range chunkBookSrcs[i:end] {
					bsArgs = append(bsArgs, row.UnionBookID, row.Src.Priority, row.Src.BookID, row.Src.LibraryID, row.Src.Size)
				}
				if end-i == unionBookSrcBatchRows {
					if _, err := batchBSStmt.ExecContext(ctx, bsArgs...); err != nil {
						return err
					}
				} else {
					if _, err := tx.ExecContext(ctx, buildMultiInsertSQL(insertBookSrcPrefix, insertBookSrcTuple, end-i), bsArgs...); err != nil {
						return err
					}
				}
				i = end
			}
		}
		return nil
	}

	// For large cold/bulk inserts, delete stale rows first and write new series in bounded chunks
	// so each write transaction holds the lock briefly and WAL stays compact.
	wroteBundlesInChunks := false
	if len(newSeriesBundles) > unionBulkChunkSeries {
		if len(bookIDsToDelete) > 0 || len(seriesIDsToDelete) > 0 {
			delTx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return UnionRefreshStats{}, err
			}
			if _, err := delTx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=1 WHERE id=1`); err != nil {
				delTx.Rollback()
				return UnionRefreshStats{}, err
			}
			if err := execChunkedDelete(ctx, delTx, `DELETE FROM books WHERE id IN `, bookIDsToDelete); err != nil {
				delTx.Rollback()
				return UnionRefreshStats{}, err
			}
			if err := execChunkedDelete(ctx, delTx, `DELETE FROM series WHERE id IN `, seriesIDsToDelete); err != nil {
				delTx.Rollback()
				return UnionRefreshStats{}, err
			}
			if _, err := delTx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=0, catalog=catalog+1 WHERE id=1`); err != nil {
				delTx.Rollback()
				return UnionRefreshStats{}, err
			}
			if err := delTx.Commit(); err != nil {
				return UnionRefreshStats{}, err
			}
			bookIDsToDelete = nil
			seriesIDsToDelete = nil
		}

		for start := 0; start < len(newSeriesBundles); start += unionBulkChunkSeries {
			if err := ctx.Err(); err != nil {
				return UnionRefreshStats{}, err
			}
			end := start + unionBulkChunkSeries
			if end > len(newSeriesBundles) {
				end = len(newSeriesBundles)
			}
			chunkTx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return UnionRefreshStats{}, err
			}
			if _, err := chunkTx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=1 WHERE id=1`); err != nil {
				chunkTx.Rollback()
				return UnionRefreshStats{}, err
			}
			if err := writeNewBundlesTx(chunkTx, newSeriesBundles[start:end]); err != nil {
				chunkTx.Rollback()
				return UnionRefreshStats{}, err
			}
			if _, err := chunkTx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=0, catalog=catalog+1 WHERE id=1`); err != nil {
				chunkTx.Rollback()
				return UnionRefreshStats{}, err
			}
			if err := chunkTx.Commit(); err != nil {
				return UnionRefreshStats{}, err
			}
		}
		newSeriesBundles = nil
		wroteBundlesInChunks = true
	}

	// Final transaction: remaining catalog diffs + metadata/progress/zip/collection inheritance + library status.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	defer tx.Rollback()

	hasPendingCatalogWrites := len(newSeriesBundles) > 0 || len(seriesToUpdate) > 0 ||
		len(seriesIDsToDelete) > 0 || len(booksToInsertStandalone) > 0 || len(booksToUpdate) > 0 ||
		len(bookIDsToDelete) > 0 || len(seriesSrcDeleteIDs) > 0 || len(seriesSrcInsert) > 0 ||
		len(bookSrcDeleteIDs) > 0 || len(bookSrcInsert) > 0

	bulkEnabled := false
	enableBulk := func() error {
		if bulkEnabled {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=1 WHERE id=1`); err != nil {
			return err
		}
		bulkEnabled = true
		return nil
	}

	catalogMutated := false
	progressMutated := false

	if hasPendingCatalogWrites {
		if err := enableBulk(); err != nil {
			return UnionRefreshStats{}, err
		}
		catalogMutated = true

		if err := execChunkedDelete(ctx, tx, `DELETE FROM union_book_sources WHERE union_book_id IN `, bookSrcDeleteIDs); err != nil {
			return UnionRefreshStats{}, err
		}
		if err := execChunkedDelete(ctx, tx, `DELETE FROM union_series_sources WHERE union_series_id IN `, seriesSrcDeleteIDs); err != nil {
			return UnionRefreshStats{}, err
		}
		if err := execChunkedDelete(ctx, tx, `DELETE FROM books WHERE id IN `, bookIDsToDelete); err != nil {
			return UnionRefreshStats{}, err
		}
		if err := execChunkedDelete(ctx, tx, `DELETE FROM series WHERE id IN `, seriesIDsToDelete); err != nil {
			return UnionRefreshStats{}, err
		}
		if err := writeNewBundlesTx(tx, newSeriesBundles); err != nil {
			return UnionRefreshStats{}, err
		}

		if len(seriesToUpdate) > 0 {
			stmtUpdS, err := tx.PrepareContext(ctx, `
UPDATE series SET
 cid=?,name=?,relative_path=?,one_shot=?,file_modified_at=?,
 books_count=?,latest_book_modified_at=?,latest_book_created_at=?,
 sort_modified_at=?,sort_created_at=?,updated_at=?
WHERE id=?`)
			if err != nil {
				return UnionRefreshStats{}, err
			}
			for _, ms := range seriesToUpdate {
				if _, err := stmtUpdS.ExecContext(ctx,
					ms.CID, ms.Name, ms.RelativePath, ms.OneShot, ms.FileModifiedAt,
					ms.BooksCount, ms.LatestBookModifiedAt, ms.LatestBookCreatedAt,
					ms.SortModifiedAt, ms.SortCreatedAt, ms.UpdatedAt, ms.ID,
				); err != nil {
					stmtUpdS.Close()
					return UnionRefreshStats{}, err
				}
			}
			stmtUpdS.Close()
		}

		if len(booksToInsertStandalone) > 0 {
			stmtInsB, err := tx.PrepareContext(ctx, insertBookSQL)
			if err != nil {
				return UnionRefreshStats{}, err
			}
			for _, br := range booksToInsertStandalone {
				mb := br.Book
				if _, err := stmtInsB.ExecContext(ctx,
					mb.ID, br.SeriesID, unionLibraryID, mb.FileID, br.ParentCID, mb.Name,
					mb.Size, mb.PickCode, mb.SHA1, mb.FileCreatedAt, mb.FileModifiedAt,
					mb.NumberSort, now, now,
				); err != nil {
					stmtInsB.Close()
					return UnionRefreshStats{}, err
				}
			}
			stmtInsB.Close()
		}

		if len(booksToUpdate) > 0 {
			stmtUpdB, err := tx.PrepareContext(ctx, `
UPDATE books SET
 series_id=?,file_id=?,parent_cid=?,name=?,size=?,pick_code=?,sha1=?,
 file_created_at=?,file_modified_at=?,number_sort=?,updated_at=?
WHERE id=?`)
			if err != nil {
				return UnionRefreshStats{}, err
			}
			for _, br := range booksToUpdate {
				mb := br.Book
				if _, err := stmtUpdB.ExecContext(ctx,
					br.SeriesID, mb.FileID, br.ParentCID, mb.Name, mb.Size, mb.PickCode, mb.SHA1,
					mb.FileCreatedAt, mb.FileModifiedAt, mb.NumberSort, now, mb.ID,
				); err != nil {
					stmtUpdB.Close()
					return UnionRefreshStats{}, err
				}
			}
			stmtUpdB.Close()
		}

		if len(seriesSrcInsert) > 0 {
			stmtSS, err := tx.PrepareContext(ctx, insertSeriesSrcSQL)
			if err != nil {
				return UnionRefreshStats{}, err
			}
			for _, row := range seriesSrcInsert {
				if _, err := stmtSS.ExecContext(ctx,
					row.UnionSeriesID, row.Src.Priority, row.Src.SeriesID, row.Src.LibraryID,
				); err != nil {
					stmtSS.Close()
					return UnionRefreshStats{}, err
				}
			}
			stmtSS.Close()
		}

		if len(bookSrcInsert) > 0 {
			slices.SortFunc(bookSrcInsert, func(a, b unionBookSrcRow) int {
				if c := strings.Compare(a.UnionBookID, b.UnionBookID); c != 0 {
					return c
				}
				return a.Src.Priority - b.Src.Priority
			})
			stmtBS, err := tx.PrepareContext(ctx, insertBookSrcSQL)
			if err != nil {
				return UnionRefreshStats{}, err
			}
			for _, row := range bookSrcInsert {
				if _, err := stmtBS.ExecContext(ctx,
					row.UnionBookID, row.Src.Priority, row.Src.BookID, row.Src.LibraryID, row.Src.Size,
				); err != nil {
					stmtBS.Close()
					return UnionRefreshStats{}, err
				}
			}
			stmtBS.Close()
		}
	}

	if len(winningSrcChangedIDs) > 0 {
		if err := enableBulk(); err != nil {
			return UnionRefreshStats{}, err
		}
		if err := execChunkedDelete(ctx, tx, `DELETE FROM zip_indexes WHERE book_id IN `, winningSrcChangedIDs); err != nil {
			return UnionRefreshStats{}, err
		}
		progressMutated = true
	}

	// 5. Inherit series_metadata (and genres/tags/authors) for union series lacking metadata yet.
	inheritedMeta, err := inheritUnionMetadataTx(ctx, tx, unionLibraryID, now, enableBulk)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	stats.InheritedMetadata = inheritedMeta
	if inheritedMeta > 0 {
		catalogMutated = true
	}

	// 6. Inherit book_read_progress & book_page_progress for union books without progress (and without reset tombstone).
	inheritedProg, err := inheritUnionProgressTx(ctx, tx, unionLibraryID, enableBulk)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	stats.InheritedProgress = inheritedProg
	if inheritedProg > 0 {
		progressMutated = true
	}

	// 7. Inherit cached zip_indexes page_count from winning physical book when union book has no zip_indexes row yet.
	zipCopied, err := syncUnionZipIndexesTx(ctx, tx, unionLibraryID, enableBulk)
	if err != nil {
		return UnionRefreshStats{}, err
	}
	if zipCopied > 0 {
		progressMutated = true
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

	if bulkEnabled {
		catBump, progBump := 0, 0
		if catalogMutated || wroteBundlesInChunks {
			catBump = 1
		}
		if progressMutated {
			progBump = 1
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE shelf_revision
SET bulk_mode=0, catalog=catalog+?, progress=progress+?
WHERE id=1`, catBump, progBump); err != nil {
			return UnionRefreshStats{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return UnionRefreshStats{}, err
	}
	return stats, nil
}

func inheritUnionMetadataTx(
	ctx context.Context,
	tx *sql.Tx,
	unionLibraryID, now string,
	enableBulk func() error,
) (int, error) {
	var hasMissing int
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM series us
  LEFT JOIN series_metadata um ON um.series_id=us.id
  WHERE us.library_id=? AND um.series_id IS NULL
)`, unionLibraryID).Scan(&hasMissing); err != nil {
		return 0, err
	}
	if hasMissing == 0 {
		return 0, nil
	}

	if _, err := tx.ExecContext(ctx, `
CREATE TEMP TABLE IF NOT EXISTS _fk115_union_meta_pick (
  union_series_id TEXT PRIMARY KEY,
  source_series_id TEXT NOT NULL
)`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM temp._fk115_union_meta_pick`); err != nil {
		return 0, err
	}
	resPick, err := tx.ExecContext(ctx, `
INSERT INTO temp._fk115_union_meta_pick(union_series_id, source_series_id)
SELECT union_series_id, source_series_id FROM (
  SELECT uss.union_series_id, uss.source_series_id,
         ROW_NUMBER() OVER (
           PARTITION BY uss.union_series_id
           ORDER BY CASE WHEN sm.scrape_status='matched' THEN 0 WHEN sm.title<>'' OR sm.summary<>'' THEN 1 ELSE 2 END ASC,
                    uss.priority ASC
         ) AS rn
  FROM union_series_sources uss
  JOIN series us ON us.id=uss.union_series_id AND us.library_id=?
  JOIN series_metadata sm ON sm.series_id=uss.source_series_id
  LEFT JOIN series_metadata um ON um.series_id=uss.union_series_id
  WHERE um.series_id IS NULL
) WHERE rn=1`, unionLibraryID)
	if err != nil {
		return 0, err
	}
	picked, _ := resPick.RowsAffected()
	if picked == 0 {
		return 0, nil
	}
	if err := enableBulk(); err != nil {
		return 0, err
	}

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
 p.union_series_id,sm.bgm_subject_id,sm.scrape_status,sm.scrape_confidence,sm.scrape_synced_at,
 sm.cover_source,sm.bgm_cover_url,sm.bgm_cover_path,sm.bgm_score,sm.bgm_rank,
 sm.status,sm.status_lock,sm.title,sm.title_lock,sm.title_sort,sm.title_sort_lock,
 sm.summary,sm.summary_lock,sm.reading_direction,sm.reading_direction_lock,
 sm.publisher,sm.publisher_lock,sm.age_rating,sm.age_rating_lock,
 sm.language,sm.language_lock,sm.total_book_count,sm.total_book_count_lock,
 sm.release_date,sm.release_date_lock,sm.genres_json,sm.genres_lock,
 sm.tags_json,sm.tags_lock,sm.authors_json,sm.authors_lock,
 sm.alternate_titles_json,sm.alternate_titles_lock,sm.links_json,sm.links_lock,
 sm.sharing_labels_json,sm.sharing_labels_lock,sm.is_nsfw,sm.nsfw_lock,?
FROM temp._fk115_union_meta_pick p
JOIN series_metadata sm ON sm.series_id=p.source_series_id
ORDER BY p.union_series_id`, now); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO series_genres(series_id,genre)
SELECT p.union_series_id,g.genre
FROM temp._fk115_union_meta_pick p
JOIN series_genres g ON g.series_id=p.source_series_id
ORDER BY p.union_series_id`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO series_tags(series_id,tag)
SELECT p.union_series_id,t.tag
FROM temp._fk115_union_meta_pick p
JOIN series_tags t ON t.series_id=p.source_series_id
ORDER BY p.union_series_id`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO series_authors(series_id,name,role)
SELECT p.union_series_id,a.name,a.role
FROM temp._fk115_union_meta_pick p
JOIN series_authors a ON a.series_id=p.source_series_id
ORDER BY p.union_series_id`); err != nil {
		return 0, err
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM temp._fk115_union_meta_pick`)
	return int(picked), nil
}

func inheritUnionProgressTx(
	ctx context.Context,
	tx *sql.Tx,
	unionLibraryID string,
	enableBulk func() error,
) (int, error) {
	// Drive from book_read_progress (small) via idx_union_book_sources_src instead of scanning all union books.
	rows, err := tx.QueryContext(ctx, `
SELECT ub.id, ub.series_id, sp.completed, sp.page, sp.read_date, sp.completed_at, sp.updated_at
FROM book_read_progress sp
CROSS JOIN union_book_sources ubs ON ubs.source_book_id=sp.book_id
CROSS JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?
LEFT JOIN book_read_progress up ON up.book_id=ub.id
LEFT JOIN book_read_progress_tombstones ut ON ut.book_id=ub.id
WHERE up.book_id IS NULL AND ut.book_id IS NULL
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

	if len(chosen) > 0 {
		if err := enableBulk(); err != nil {
			return 0, err
		}
		stmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO book_read_progress(book_id,series_id,completed,page,read_date,completed_at,updated_at)
VALUES(?,?,?,?,?,?,?)`)
		if err != nil {
			return 0, err
		}
		for _, r := range chosen {
			if _, err := stmt.ExecContext(ctx, r.bookID, r.seriesID, r.completed, r.page, r.readDate, r.completedAt, r.updatedAt); err != nil {
				stmt.Close()
				return 0, err
			}
		}
		stmt.Close()
	}

	// Also inherit inferred book_page_progress where missing on union books (driven from book_page_progress).
	if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO book_page_progress(book_id,series_id,last_loaded_page,max_loaded_page,page_count,updated_at)
SELECT ub.id, ub.series_id, sp.last_loaded_page, sp.max_loaded_page, sp.page_count, sp.updated_at
FROM book_page_progress sp
CROSS JOIN union_book_sources ubs ON ubs.source_book_id=sp.book_id
CROSS JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?
LEFT JOIN book_page_progress up ON up.book_id=ub.id
WHERE up.book_id IS NULL
ORDER BY sp.updated_at DESC, ubs.priority ASC`, unionLibraryID); err != nil {
		return 0, err
	}
	return len(chosen), nil
}

func syncUnionZipIndexesTx(
	ctx context.Context,
	tx *sql.Tx,
	unionLibraryID string,
	enableBulk func() error,
) (int, error) {
	// Drive from zip_indexes (small) via idx_union_book_sources_src so 0-change refreshes stay sub-millisecond.
	// Allow inheriting cached page_count from lower layers ONLY when source_size matches the winning union book size,
	// keeping same-name different-size files strictly isolated.
	rows, err := tx.QueryContext(ctx, `
SELECT ubs.union_book_id, z.version, z.page_count, z.index_duration_ns, z.created_at, z.updated_at
FROM zip_indexes z
CROSS JOIN union_book_sources ubs ON ubs.source_book_id=z.book_id
CROSS JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?
LEFT JOIN zip_indexes uz ON uz.book_id=ub.id
WHERE uz.book_id IS NULL
  AND ubs.source_size=ub.size
ORDER BY ubs.union_book_id ASC, ubs.priority ASC`, unionLibraryID)
	if err != nil {
		return 0, err
	}
	type zipRow struct {
		bookID, version, createdAt, updatedAt string
		pageCount                             int
		durationNS                            int64
	}
	var toCopy []zipRow
	seen := make(map[string]struct{})
	for rows.Next() {
		var r zipRow
		if err := rows.Scan(&r.bookID, &r.version, &r.pageCount, &r.durationNS, &r.createdAt, &r.updatedAt); err != nil {
			rows.Close()
			return 0, err
		}
		if _, dup := seen[r.bookID]; dup {
			continue
		}
		seen[r.bookID] = struct{}{}
		toCopy = append(toCopy, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(toCopy) == 0 {
		return 0, nil
	}
	if err := enableBulk(); err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT OR IGNORE INTO zip_indexes(book_id,version,page_count,index_json,index_duration_ns,created_at,updated_at)
VALUES(?,?,?,'[]',?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, r := range toCopy {
		if _, err := stmt.ExecContext(ctx, r.bookID, r.version, r.pageCount, r.durationNS, r.createdAt, r.updatedAt); err != nil {
			return 0, err
		}
	}
	return len(toCopy), nil
}

func migrateCollectionsAndReadListsTx(ctx context.Context, tx *sql.Tx, unionLibraryID string) (int, int, error) {
	var migCol, migRL int64
	var hasCol int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collection_series)`).Scan(&hasCol); err != nil {
		return 0, 0, err
	}
	if hasCol != 0 {
		resColIns, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO collection_series(collection_id,series_id,position)
SELECT cs.collection_id, uss.union_series_id, cs.position
FROM collection_series cs
CROSS JOIN union_series_sources uss ON uss.source_series_id=cs.series_id
CROSS JOIN series us ON us.id=uss.union_series_id AND us.library_id=?`, unionLibraryID)
		if err != nil {
			return 0, 0, err
		}
		migCol, _ = resColIns.RowsAffected()
		if migCol > 0 {
			if _, err := tx.ExecContext(ctx, `
DELETE FROM collection_series
WHERE series_id IN (
  SELECT uss.source_series_id FROM union_series_sources uss
  JOIN series us ON us.id=uss.union_series_id AND us.library_id=?
)`, unionLibraryID); err != nil {
				return 0, 0, err
			}
		}
	}

	var hasRL int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM readlist_books)`).Scan(&hasRL); err != nil {
		return 0, 0, err
	}
	if hasRL != 0 {
		resRLIns, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO readlist_books(readlist_id,book_id,position)
SELECT rb.readlist_id, ubs.union_book_id, rb.position
FROM readlist_books rb
CROSS JOIN union_book_sources ubs ON ubs.source_book_id=rb.book_id
CROSS JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?`, unionLibraryID)
		if err != nil {
			return 0, 0, err
		}
		migRL, _ = resRLIns.RowsAffected()
		if migRL > 0 {
			if _, err := tx.ExecContext(ctx, `
DELETE FROM readlist_books
WHERE book_id IN (
  SELECT ubs.source_book_id FROM union_book_sources ubs
  JOIN books ub ON ub.id=ubs.union_book_id AND ub.library_id=?
)`, unionLibraryID); err != nil {
				return 0, 0, err
			}
		}
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
ORDER BY s.books_count DESC,
 CASE WHEN l.provider_type='union' THEN 0 ELSE 1 END ASC,
 l.name COLLATE NOCASE ASC, s.name COLLATE NOCASE ASC, s.id ASC`
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
