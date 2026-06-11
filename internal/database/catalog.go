package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type SeriesQuery struct {
	Search     string
	LibraryIDs []string
	OneShot    *bool
	Page       int
	Size       int
	Sort       string
}

type BookQuery struct {
	Search     string
	LibraryIDs []string
	SeriesID   string
	OneShot    *bool
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
	where, args := buildFilters(q.Search, q.LibraryIDs, "", q.OneShot)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM series s`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "s.name COLLATE NOCASE ASC,s.id ASC"
	sortValue := strings.ToLower(q.Sort)
	switch {
	case strings.Contains(sortValue, "random"):
		order = "RANDOM()"
	case strings.Contains(sortValue, "lastmodifieddate,desc"):
		order = "s.updated_at DESC,s.name COLLATE NOCASE ASC"
	case strings.Contains(sortValue, "createddate,desc"):
		order = "s.created_at DESC,s.name COLLATE NOCASE ASC"
	case strings.Contains(sortValue, ",desc"):
		order = "s.name COLLATE NOCASE DESC,s.id DESC"
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
	where, args := buildBookFilters(q)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM books b`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "b.number_sort ASC,b.name COLLATE NOCASE ASC,b.id ASC"
	sortValue := strings.ToLower(q.Sort)
	switch {
	case strings.Contains(sortValue, "random"):
		order = "RANDOM()"
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

func buildFilters(search string, libraries []string, alias string, oneShot *bool) (string, []any) {
	prefix := alias
	if prefix == "" {
		prefix = "s"
	}
	clauses := []string{
		"EXISTS (SELECT 1 FROM libraries enabled_library WHERE enabled_library.id=" +
			prefix + ".library_id AND enabled_library.enabled=1)",
	}
	var args []any
	if search = strings.TrimSpace(search); search != "" {
		clauses = append(clauses, prefix+`.name LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, "%"+escapeLike(search)+"%")
	}
	if len(libraries) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(libraries)), ",")
		clauses = append(clauses, prefix+".library_id IN ("+placeholders+")")
		for _, value := range libraries {
			args = append(args, value)
		}
	}
	if oneShot != nil {
		clauses = append(clauses, prefix+".one_shot=?")
		args = append(args, *oneShot)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func buildBookFilters(q BookQuery) (string, []any) {
	clauses := []string{
		"EXISTS (SELECT 1 FROM libraries enabled_library WHERE enabled_library.id=b.library_id AND enabled_library.enabled=1)",
	}
	var args []any
	if search := strings.TrimSpace(q.Search); search != "" {
		clauses = append(clauses, `b.name LIKE ? ESCAPE '\' COLLATE NOCASE`)
		args = append(args, "%"+escapeLike(search)+"%")
	}
	if q.SeriesID != "" {
		clauses = append(clauses, "b.series_id=?")
		args = append(args, q.SeriesID)
	}
	if len(q.LibraryIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(q.LibraryIDs)), ",")
		clauses = append(clauses, "b.library_id IN ("+placeholders+")")
		for _, value := range q.LibraryIDs {
			args = append(args, value)
		}
	}
	if q.OneShot != nil {
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM series oneshot_series WHERE oneshot_series.id=b.series_id AND oneshot_series.one_shot=?)")
		args = append(args, *q.OneShot)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func scanSeriesRows(rows *sql.Rows) ([]Series, error) {
	var out []Series
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
	var out []Book
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
