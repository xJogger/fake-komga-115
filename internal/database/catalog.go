package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type SeriesQuery struct {
	Search       string
	LibraryIDs   []string
	ReadStatus   []string
	Statuses     []string
	Genres       []string
	Tags         []string
	Publishers   []string
	Authors      []string
	Languages    []string
	ScrapeStatus string
	OneShot      *bool
	Empty        bool
	Page         int
	Size         int
	Sort         string
}

type BookQuery struct {
	Search     string
	LibraryIDs []string
	SeriesID   string
	ReadStatus []string
	Tags       []string
	Authors    []string
	OneShot    *bool
	Empty      bool
	Page       int
	Size       int
	Unpaged    bool
	Sort       string
}

func (s *Store) SeriesPage(ctx context.Context, q SeriesQuery) ([]Series, int64, error) {
	if q.Page < 0 {
		q.Page = 0
	}
	if q.Size <= 0 || q.Size > 500 {
		q.Size = 20
	}
	if q.Empty {
		return nil, 0, nil
	}
	where, args := buildSeriesFilters(q)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM series s`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	dir := sortDirection(strings.ToLower(strings.TrimSpace(q.Sort)))
	sortValue := strings.ToLower(strings.TrimSpace(q.Sort))
	order := `COALESCE(NULLIF((SELECT sm.title_sort FROM series_metadata sm WHERE sm.series_id=s.id),''), s.name) COLLATE NOCASE ASC,s.id ASC`
	switch {
	case strings.Contains(sortValue, "random"):
		order = "RANDOM()"
	case strings.HasPrefix(sortValue, "lastmodifieddate"):
		order = `COALESCE(
 (SELECT MAX(b.file_modified_at) FROM books b WHERE b.series_id=s.id),
 s.file_modified_at,s.updated_at
) ` + dir + `,s.name COLLATE NOCASE ASC,s.id ASC`
	case strings.HasPrefix(sortValue, "createddate"):
		order = `COALESCE(
 (SELECT MAX(b.file_created_at) FROM books b WHERE b.series_id=s.id),
 s.created_at
) ` + dir + `,s.name COLLATE NOCASE ASC,s.id ASC`
	case strings.HasPrefix(sortValue, "bgmscore"):
		order = `COALESCE((SELECT sm.bgm_score FROM series_metadata sm WHERE sm.series_id=s.id), -1) ` + dir + `,s.name COLLATE NOCASE ASC,s.id ASC`
	case strings.Contains(sortValue, ",desc"):
		order = `COALESCE(NULLIF((SELECT sm.title_sort FROM series_metadata sm WHERE sm.series_id=s.id),''), s.name) COLLATE NOCASE DESC,s.id DESC`
	}
	query := `
SELECT s.id,s.library_id,s.cid,s.name,s.relative_path,s.one_shot,s.file_modified_at,s.created_at,s.updated_at,
 (SELECT count(*) FROM books b WHERE b.series_id=s.id)
FROM series s` + where + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, q.Size, q.Page*q.Size)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanSeriesRows(rows)
	return items, total, err
}

func sortDirection(value string) string {
	if strings.HasSuffix(value, ",desc") {
		return "DESC"
	}
	return "ASC"
}

func (s *Store) SeriesByID(ctx context.Context, id string) (Series, error) {
	var item Series
	var modified sql.NullString
	var created, updated string
	err := s.db.QueryRowContext(ctx, `
SELECT s.id,s.library_id,s.cid,s.name,s.relative_path,s.one_shot,s.file_modified_at,s.created_at,s.updated_at,
 (SELECT count(*) FROM books b WHERE b.series_id=s.id)
FROM series s WHERE s.id=?`, id).Scan(
		&item.ID, &item.LibraryID, &item.CID, &item.Name, &item.RelativePath, &item.OneShot,
		&modified, &created, &updated, &item.BooksCount,
	)
	if err != nil {
		return Series{}, err
	}
	item.FileModifiedAt = parseNullableTime(modified)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) BooksPage(ctx context.Context, q BookQuery) ([]Book, int64, error) {
	if q.Page < 0 {
		q.Page = 0
	}
	if q.Size <= 0 || q.Size > 1000 {
		q.Size = 20
	}
	if q.Empty {
		return nil, 0, nil
	}
	where, args := buildBookFilters(q)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM books b`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "b.number_sort ASC,b.name COLLATE NOCASE ASC,b.id ASC"
	sortValue := strings.ToLower(strings.TrimSpace(q.Sort))
	switch {
	case strings.Contains(sortValue, "random"):
		order = "RANDOM()"
	case strings.HasPrefix(sortValue, "readprogress.readdate"), strings.HasPrefix(sortValue, "readdate"):
		order = `COALESCE((SELECT p.updated_at FROM book_read_progress p WHERE p.book_id=b.id), '') ` +
			sortDirection(sortValue) + `,b.number_sort ASC,b.id ASC`
	case strings.HasPrefix(sortValue, "lastmodifieddate"):
		order = "COALESCE(b.file_modified_at,b.updated_at) " +
			sortDirection(sortValue) + ",b.name COLLATE NOCASE ASC,b.id ASC"
	case strings.HasPrefix(sortValue, "createddate"):
		order = "COALESCE(b.file_created_at,b.created_at) " +
			sortDirection(sortValue) + ",b.name COLLATE NOCASE ASC,b.id ASC"
	case strings.HasPrefix(sortValue, "metadata.title"):
		order = "b.name COLLATE NOCASE " +
			sortDirection(sortValue) + ",b.id " + sortDirection(sortValue)
	case strings.Contains(sortValue, "name,desc"):
		order = "b.name COLLATE NOCASE DESC,b.id DESC"
	case strings.Contains(sortValue, "name,asc"):
		order = "b.name COLLATE NOCASE ASC,b.id ASC"
	case strings.Contains(sortValue, ",desc"):
		order = "b.number_sort DESC,b.name COLLATE NOCASE DESC,b.id DESC"
	}
	query := `
SELECT b.id,b.series_id,b.library_id,b.file_id,b.parent_cid,b.name,b.size,b.pick_code,b.sha1,
 b.file_created_at,b.file_modified_at,b.number_sort,b.created_at,b.updated_at,
 coalesce(z.page_count,0)
FROM books b LEFT JOIN zip_indexes z ON z.book_id=b.id` + where + ` ORDER BY ` + order
	queryArgs := append([]any(nil), args...)
	if !q.Unpaged {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, q.Size, q.Page*q.Size)
	}
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanBookRows(rows)
	return items, total, err
}

func (s *Store) BookByID(ctx context.Context, id string) (Book, error) {
	var item Book
	var fileCreated, fileModified sql.NullString
	var created, updated string
	err := s.db.QueryRowContext(ctx, `
SELECT b.id,b.series_id,b.library_id,b.file_id,b.parent_cid,b.name,b.size,b.pick_code,b.sha1,
 b.file_created_at,b.file_modified_at,b.number_sort,b.created_at,b.updated_at,
 coalesce(z.page_count,0)
FROM books b LEFT JOIN zip_indexes z ON z.book_id=b.id WHERE b.id=?`, id).Scan(
		&item.ID, &item.SeriesID, &item.LibraryID, &item.FileID, &item.ParentCID,
		&item.Name, &item.Size, &item.PickCode, &item.SHA1, &fileCreated, &fileModified,
		&item.NumberSort, &created, &updated, &item.PageCount,
	)
	if err != nil {
		return Book{}, err
	}
	item.FileCreatedAt = parseNullableTime(fileCreated)
	item.FileModifiedAt = parseNullableTime(fileModified)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

func (s *Store) SeriesByIDs(ctx context.Context, ids []string) (map[string]Series, error) {
	out := make(map[string]Series, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	unique := cleanStringSliceCaseSensitive(ids)
	args := make([]any, 0, len(unique))
	for _, id := range unique {
		args = append(args, id)
	}
	query := `
SELECT s.id,s.library_id,s.cid,s.name,s.relative_path,s.one_shot,s.file_modified_at,s.created_at,s.updated_at,
 (SELECT count(*) FROM books b WHERE b.series_id=s.id)
FROM series s WHERE s.id IN ` + Placeholders(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanSeriesRows(rows)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out[item.ID] = item
	}
	return out, nil
}

func (s *Store) FirstBookInSeries(ctx context.Context, seriesID string) (Book, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT id FROM books WHERE series_id=?
ORDER BY number_sort ASC,name COLLATE NOCASE ASC,id ASC LIMIT 1`, seriesID).Scan(&id)
	if err != nil {
		return Book{}, err
	}
	return s.BookByID(ctx, id)
}

func (s *Store) NextBookInSeries(ctx context.Context, current Book) (Book, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
WITH ordered AS (
 SELECT id,
  LEAD(id) OVER (
   ORDER BY number_sort ASC,name COLLATE NOCASE ASC,id ASC
  ) AS next_id
 FROM books
 WHERE series_id=?
)
SELECT ordered.next_id
FROM ordered
JOIN series ON series.id=?
WHERE ordered.id=? AND ordered.next_id IS NOT NULL AND series.one_shot=0`,
		current.SeriesID, current.SeriesID, current.ID).Scan(&id)
	if err != nil {
		return Book{}, err
	}
	return s.BookByID(ctx, id)
}

func (s *Store) PreviousBookInSeries(ctx context.Context, current Book) (Book, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
WITH ordered AS (
 SELECT id,
  LAG(id) OVER (
   ORDER BY number_sort ASC,name COLLATE NOCASE ASC,id ASC
  ) AS previous_id
 FROM books
 WHERE series_id=?
)
SELECT ordered.previous_id
FROM ordered
JOIN series ON series.id=?
WHERE ordered.id=? AND ordered.previous_id IS NOT NULL AND series.one_shot=0`,
		current.SeriesID, current.SeriesID, current.ID).Scan(&id)
	if err != nil {
		return Book{}, err
	}
	return s.BookByID(ctx, id)
}

func (s *Store) Counts(
	ctx context.Context,
) (libraries, series, books, comicBytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `
SELECT
 (SELECT count(*) FROM libraries WHERE enabled=1),
 (SELECT count(*) FROM series),
 (SELECT count(*) FROM books),
 (SELECT coalesce(sum(size),0) FROM books)`).
		Scan(&libraries, &series, &books, &comicBytes)
	return
}

func buildSeriesFilters(q SeriesQuery) (string, []any) {
	clauses := []string{
		"EXISTS (SELECT 1 FROM libraries enabled_library WHERE enabled_library.id=s.library_id AND enabled_library.enabled=1)",
	}
	var args []any
	if search := strings.TrimSpace(q.Search); search != "" {
		like := "%" + escapeLike(search) + "%"
		clauses = append(clauses, `(
 s.name LIKE ? ESCAPE '\' COLLATE NOCASE
 OR EXISTS (
  SELECT 1 FROM series_metadata sm_search
  WHERE sm_search.series_id=s.id
  AND (
   sm_search.title LIKE ? ESCAPE '\' COLLATE NOCASE
   OR sm_search.alternate_titles_json LIKE ? ESCAPE '\' COLLATE NOCASE
  )
 )
)`)
		args = append(args, like, like, like)
	}
	if len(q.LibraryIDs) > 0 {
		clauses = append(clauses, "s.library_id IN "+Placeholders(len(q.LibraryIDs)))
		for _, value := range q.LibraryIDs {
			args = append(args, value)
		}
	}
	if q.OneShot != nil {
		clauses = append(clauses, "s.one_shot=?")
		args = append(args, *q.OneShot)
	}
	if len(q.Statuses) > 0 {
		var statusClauses []string
		for _, st := range cleanStringSlice(q.Statuses) {
			upper := strings.ToUpper(st)
			if upper == "ONGOING" {
				statusClauses = append(statusClauses,
					`COALESCE((SELECT sm.status FROM series_metadata sm WHERE sm.series_id=s.id),'ONGOING')='ONGOING'`)
			} else {
				statusClauses = append(statusClauses,
					`EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND sm.status=?)`)
				args = append(args, upper)
			}
		}
		if len(statusClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(statusClauses, " OR ")+")")
		}
	}
	if len(q.Genres) > 0 {
		genres := cleanStringSlice(q.Genres)
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM series_genres sg WHERE sg.series_id=s.id AND sg.genre COLLATE NOCASE IN `+Placeholders(len(genres))+`)`)
		for _, g := range genres {
			args = append(args, g)
		}
	}
	if len(q.Tags) > 0 {
		tags := cleanStringSlice(q.Tags)
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM series_tags st WHERE st.series_id=s.id AND st.tag COLLATE NOCASE IN `+Placeholders(len(tags))+`)`)
		for _, t := range tags {
			args = append(args, t)
		}
	}
	if len(q.Publishers) > 0 {
		pubs := cleanStringSlice(q.Publishers)
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND sm.publisher COLLATE NOCASE IN `+Placeholders(len(pubs))+`)`)
		for _, p := range pubs {
			args = append(args, p)
		}
	}
	if len(q.Authors) > 0 {
		authors := cleanStringSlice(q.Authors)
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM series_authors sa WHERE sa.series_id=s.id AND sa.name COLLATE NOCASE IN `+Placeholders(len(authors))+`)`)
		for _, a := range authors {
			args = append(args, a)
		}
	}
	if len(q.Languages) > 0 {
		langs := cleanStringSlice(q.Languages)
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND sm.language COLLATE NOCASE IN `+Placeholders(len(langs))+`)`)
		for _, l := range langs {
			args = append(args, l)
		}
	}
	if scrapeStatus := strings.ToLower(strings.TrimSpace(q.ScrapeStatus)); scrapeStatus != "" {
		if scrapeStatus == ScrapeStatusNone {
			clauses = append(clauses,
				`COALESCE((SELECT sm.scrape_status FROM series_metadata sm WHERE sm.series_id=s.id),'none')='none'`)
		} else {
			clauses = append(clauses,
				`EXISTS (SELECT 1 FROM series_metadata sm WHERE sm.series_id=s.id AND sm.scrape_status=?)`)
			args = append(args, scrapeStatus)
		}
	}
	if conditions := readStatusConditions(q.ReadStatus, "series"); len(conditions) > 0 {
		clauses = append(clauses, "("+strings.Join(conditions, " OR ")+")")
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func readStatusConditions(statuses []string, target string) []string {
	seen := map[string]bool{}
	var conditions []string
	for _, raw := range statuses {
		status := strings.ToUpper(strings.TrimSpace(raw))
		if seen[status] {
			continue
		}
		seen[status] = true
		switch target {
		case "series":
			switch status {
			case "READ":
				conditions = append(conditions, "("+seriesReadCondition()+")")
			case "IN_PROGRESS":
				conditions = append(conditions, "("+seriesInProgressCondition()+")")
			case "UNREAD":
				conditions = append(conditions, "("+seriesUnreadCondition()+")")
			}
		case "book":
			switch status {
			case "READ":
				conditions = append(conditions,
					`EXISTS (SELECT 1 FROM book_read_progress p WHERE p.book_id=b.id AND p.completed=1)`)
			case "IN_PROGRESS":
				conditions = append(conditions,
					`EXISTS (SELECT 1 FROM book_read_progress p WHERE p.book_id=b.id AND p.completed=0)`)
			case "UNREAD":
				conditions = append(conditions,
					`NOT EXISTS (SELECT 1 FROM book_read_progress p WHERE p.book_id=b.id)`)
			}
		}
	}
	return conditions
}

func seriesReadCondition() string {
	return `EXISTS (SELECT 1 FROM books read_book WHERE read_book.series_id=s.id)
AND NOT EXISTS (
 SELECT 1 FROM books not_read_book
 WHERE not_read_book.series_id=s.id
 AND NOT EXISTS (
  SELECT 1 FROM book_read_progress read_progress
  WHERE read_progress.book_id=not_read_book.id AND read_progress.completed=1
 )
)`
}

func seriesInProgressCondition() string {
	return `EXISTS (
 SELECT 1 FROM books progressed_book
 JOIN book_read_progress progressed ON progressed.book_id=progressed_book.id
 WHERE progressed_book.series_id=s.id
)
AND EXISTS (
 SELECT 1 FROM books incomplete_book
 WHERE incomplete_book.series_id=s.id
 AND NOT EXISTS (
  SELECT 1 FROM book_read_progress completed_progress
  WHERE completed_progress.book_id=incomplete_book.id AND completed_progress.completed=1
 )
)`
}

func seriesUnreadCondition() string {
	return `NOT EXISTS (
 SELECT 1 FROM books progressed_book
 JOIN book_read_progress progressed ON progressed.book_id=progressed_book.id
 WHERE progressed_book.series_id=s.id
)`
}

func buildBookFilters(q BookQuery) (string, []any) {
	clauses := []string{
		"EXISTS (SELECT 1 FROM libraries enabled_library WHERE enabled_library.id=b.library_id AND enabled_library.enabled=1)",
	}
	var args []any
	if search := strings.TrimSpace(q.Search); search != "" {
		like := "%" + escapeLike(search) + "%"
		clauses = append(clauses, `(
 b.name LIKE ? ESCAPE '\' COLLATE NOCASE
 OR EXISTS (
  SELECT 1 FROM series s_search
  LEFT JOIN series_metadata sm_search ON sm_search.series_id=s_search.id
  WHERE s_search.id=b.series_id
  AND (s_search.name LIKE ? ESCAPE '\' COLLATE NOCASE OR sm_search.title LIKE ? ESCAPE '\' COLLATE NOCASE)
 )
)`)
		args = append(args, like, like, like)
	}
	if q.SeriesID != "" {
		clauses = append(clauses, "b.series_id=?")
		args = append(args, q.SeriesID)
	}
	if len(q.LibraryIDs) > 0 {
		clauses = append(clauses, "b.library_id IN "+Placeholders(len(q.LibraryIDs)))
		for _, value := range q.LibraryIDs {
			args = append(args, value)
		}
	}
	if q.OneShot != nil {
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM series oneshot_series WHERE oneshot_series.id=b.series_id AND oneshot_series.one_shot=?)")
		args = append(args, *q.OneShot)
	}
	if len(q.Tags) > 0 {
		tags := cleanStringSlice(q.Tags)
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM series_tags st WHERE st.series_id=b.series_id AND st.tag COLLATE NOCASE IN `+Placeholders(len(tags))+`)`)
		for _, t := range tags {
			args = append(args, t)
		}
	}
	if len(q.Authors) > 0 {
		authors := cleanStringSlice(q.Authors)
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM series_authors sa WHERE sa.series_id=b.series_id AND sa.name COLLATE NOCASE IN `+Placeholders(len(authors))+`)`)
		for _, a := range authors {
			args = append(args, a)
		}
	}
	if conditions := readStatusConditions(q.ReadStatus, "book"); len(conditions) > 0 {
		clauses = append(clauses, "("+strings.Join(conditions, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func scanSeriesRows(rows *sql.Rows) ([]Series, error) {
	out := make([]Series, 0)
	for rows.Next() {
		var item Series
		var modified sql.NullString
		var created, updated string
		if err := rows.Scan(
			&item.ID, &item.LibraryID, &item.CID, &item.Name, &item.RelativePath, &item.OneShot,
			&modified, &created, &updated, &item.BooksCount,
		); err != nil {
			return nil, err
		}
		item.FileModifiedAt = parseNullableTime(modified)
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanBookRows(rows *sql.Rows) ([]Book, error) {
	out := make([]Book, 0)
	for rows.Next() {
		var item Book
		var fileCreated, fileModified sql.NullString
		var created, updated string
		if err := rows.Scan(
			&item.ID, &item.SeriesID, &item.LibraryID, &item.FileID, &item.ParentCID,
			&item.Name, &item.Size, &item.PickCode, &item.SHA1, &fileCreated, &fileModified,
			&item.NumberSort, &created, &updated, &item.PageCount,
		); err != nil {
			return nil, err
		}
		item.FileCreatedAt = parseNullableTime(fileCreated)
		item.FileModifiedAt = parseNullableTime(fileModified)
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func cleanStringSliceCaseSensitive(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, raw := range items {
		v := strings.TrimSpace(raw)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := parseTime(value.String)
	return &parsed
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Unix(0, 0).UTC()
	}
	return parsed
}

func Placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf("(%s)", strings.TrimSuffix(strings.Repeat("?,", count), ","))
}
