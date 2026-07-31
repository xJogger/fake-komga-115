package database

import (
	"context"
	"database/sql"
)

func (s *Store) SeriesReadProgress(ctx context.Context, seriesID string) (SeriesReadProgress, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT b.number_sort, CASE WHEN p.book_id IS NULL THEN 0 ELSE 1 END AS has_progress,
 coalesce(p.completed,0)
FROM books b
LEFT JOIN book_read_progress p ON p.book_id=b.id
WHERE b.series_id=?
ORDER BY b.number_sort ASC,b.name COLLATE NOCASE ASC,b.id ASC`, seriesID)
	if err != nil {
		return SeriesReadProgress{}, err
	}
	defer rows.Close()

	var progress SeriesReadProgress
	continuous := true
	for rows.Next() {
		var numberSort float64
		var hasProgress, completed int
		if err := rows.Scan(&numberSort, &hasProgress, &completed); err != nil {
			return SeriesReadProgress{}, err
		}
		progress.BooksCount++
		progress.MaxNumberSort = numberSort
		switch {
		case hasProgress != 0 && completed != 0:
			progress.BooksReadCount++
			if continuous {
				progress.LastReadContinuousNumberSort = numberSort
			}
		case hasProgress != 0:
			progress.BooksInProgressCount++
			continuous = false
		default:
			continuous = false
		}
	}
	if err := rows.Err(); err != nil {
		return SeriesReadProgress{}, err
	}
	progress.BooksUnreadCount = progress.BooksCount -
		progress.BooksReadCount - progress.BooksInProgressCount
	return progress, nil
}

func (s *Store) UpdateSeriesReadProgress(
	ctx context.Context,
	seriesID string,
	lastBookNumberSortRead float64,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := nowText()
	if _, err := tx.ExecContext(ctx, `
DELETE FROM book_read_progress
WHERE series_id=? AND book_id IN (
 SELECT id FROM books WHERE series_id=? AND number_sort>?
)`, seriesID, seriesID, lastBookNumberSortRead); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO book_read_progress(book_id,series_id,completed,read_date,completed_at,updated_at)
SELECT id,series_id,1,?,?,?
FROM books
WHERE series_id=? AND number_sort<=?
ON CONFLICT(book_id) DO UPDATE SET
 series_id=excluded.series_id,
 completed=1,
 completed_at=excluded.completed_at,
 read_date=excluded.read_date,
 updated_at=excluded.updated_at`, now, now, now, seriesID, lastBookNumberSortRead); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompletedBookIDsBySeries(ctx context.Context, seriesID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT book_id FROM book_read_progress WHERE series_id=? AND completed=1`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *Store) BookReadCompleted(ctx context.Context, bookID string) (bool, error) {
	var completed int
	err := s.db.QueryRowContext(ctx, `
SELECT completed FROM book_read_progress WHERE book_id=?`, bookID).Scan(&completed)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return completed != 0, nil
}

func (s *Store) BookReadProgress(
	ctx context.Context,
	bookID string,
) (BookReadProgress, bool, error) {
	var progress BookReadProgress
	var completed int
	var page sql.NullInt64
	var readDate, completedAt, updatedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT book_id,series_id,completed,page,read_date,completed_at,updated_at
FROM book_read_progress WHERE book_id=?`, bookID).Scan(
		&progress.BookID, &progress.SeriesID, &completed,
		&page, &readDate, &completedAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return BookReadProgress{}, false, nil
		}
		return BookReadProgress{}, false, err
	}
	progress.Completed = completed != 0
	if page.Valid {
		value := int(page.Int64)
		progress.Page = &value
	}
	progress.ReadDate = parseNullableTime(readDate)
	progress.CompletedAt = parseNullableTime(completedAt)
	progress.UpdatedAt = parseTime(updatedAt.String)
	return progress, true, nil
}

func (s *Store) BookReadProgresses(
	ctx context.Context,
	bookIDs []string,
) (map[string]BookReadProgress, error) {
	out := map[string]BookReadProgress{}
	if len(bookIDs) == 0 {
		return out, nil
	}
	query := `
SELECT book_id,series_id,completed,page,read_date,completed_at,updated_at
FROM book_read_progress WHERE book_id IN ` + Placeholders(len(bookIDs))
	args := make([]any, 0, len(bookIDs))
	for _, id := range bookIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var progress BookReadProgress
		var completed int
		var page sql.NullInt64
		var readDate, completedAt, updatedAt sql.NullString
		if err := rows.Scan(
			&progress.BookID, &progress.SeriesID, &completed,
			&page, &readDate, &completedAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		progress.Completed = completed != 0
		if page.Valid {
			value := int(page.Int64)
			progress.Page = &value
		}
		progress.ReadDate = parseNullableTime(readDate)
		progress.CompletedAt = parseNullableTime(completedAt)
		progress.UpdatedAt = parseTime(updatedAt.String)
		out[progress.BookID] = progress
	}
	return out, rows.Err()
}

func (s *Store) UpdateBookReadProgress(
	ctx context.Context,
	book Book,
	completed *bool,
	page *int,
) error {
	now := nowText()
	completedValue := false
	if completed != nil {
		completedValue = *completed
	}
	completedInt := 0
	if completedValue {
		completedInt = 1
	}
	var completedAt any
	if completedValue {
		completedAt = now
	}
	var pageValue any
	if page != nil && *page > 0 {
		pageValue = *page
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO book_read_progress(
 book_id,series_id,completed,page,read_date,completed_at,updated_at
) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(book_id) DO UPDATE SET
 series_id=excluded.series_id,
 completed=excluded.completed,
 page=coalesce(excluded.page, book_read_progress.page),
 read_date=excluded.read_date,
 completed_at=excluded.completed_at,
 updated_at=excluded.updated_at`,
		book.ID, book.SeriesID, completedInt, pageValue, now, completedAt, now)
	return err
}

func (s *Store) RecordBookPageProgress(
	ctx context.Context,
	book Book,
	pageNumber, pageCount int,
) error {
	if pageNumber < 1 || pageCount < 1 {
		return nil
	}
	if pageNumber > pageCount {
		pageCount = pageNumber
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO book_page_progress(
 book_id,series_id,last_loaded_page,max_loaded_page,page_count,updated_at
) VALUES(?,?,?,?,?,?)
ON CONFLICT(book_id) DO UPDATE SET
 series_id=excluded.series_id,
 last_loaded_page=excluded.last_loaded_page,
 max_loaded_page=MAX(book_page_progress.max_loaded_page, excluded.max_loaded_page),
 page_count=excluded.page_count,
 updated_at=excluded.updated_at`,
		book.ID, book.SeriesID, pageNumber, pageNumber, pageCount, nowText())
	return err
}

func (s *Store) BookPageProgress(
	ctx context.Context,
	bookID string,
) (BookPageProgress, bool, error) {
	var progress BookPageProgress
	var updated string
	err := s.db.QueryRowContext(ctx, `
SELECT book_id,series_id,last_loaded_page,max_loaded_page,page_count,updated_at
FROM book_page_progress WHERE book_id=?`, bookID).Scan(
		&progress.BookID, &progress.SeriesID, &progress.LastLoadedPage,
		&progress.MaxLoadedPage, &progress.PageCount, &updated,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return BookPageProgress{}, false, nil
		}
		return BookPageProgress{}, false, err
	}
	progress.UpdatedAt = parseTime(updated)
	return progress, true, nil
}

func (s *Store) BookPageProgressesBySeries(
	ctx context.Context,
	seriesID string,
) (map[string]BookPageProgress, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT book_id,series_id,last_loaded_page,max_loaded_page,page_count,updated_at
FROM book_page_progress WHERE series_id=?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]BookPageProgress{}
	for rows.Next() {
		var progress BookPageProgress
		var updated string
		if err := rows.Scan(
			&progress.BookID, &progress.SeriesID, &progress.LastLoadedPage,
			&progress.MaxLoadedPage, &progress.PageCount, &updated,
		); err != nil {
			return nil, err
		}
		progress.UpdatedAt = parseTime(updated)
		out[progress.BookID] = progress
	}
	return out, rows.Err()
}

func (s *Store) LatestBookPageProgressInSeries(
	ctx context.Context,
	seriesID string,
) (BookPageProgressView, bool, error) {
	var progress BookPageProgressView
	var updated string
	err := s.db.QueryRowContext(ctx, `
SELECT p.book_id,p.series_id,p.last_loaded_page,p.max_loaded_page,p.page_count,p.updated_at,
 b.name,b.number_sort
FROM book_page_progress p
JOIN books b ON b.id=p.book_id
WHERE p.series_id=?
ORDER BY p.updated_at DESC,b.number_sort ASC,b.name COLLATE NOCASE ASC,b.id ASC
LIMIT 1`, seriesID).Scan(
		&progress.BookID, &progress.SeriesID, &progress.LastLoadedPage,
		&progress.MaxLoadedPage, &progress.PageCount, &updated,
		&progress.BookName, &progress.NumberSort,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return BookPageProgressView{}, false, nil
		}
		return BookPageProgressView{}, false, err
	}
	progress.UpdatedAt = parseTime(updated)
	return progress, true, nil
}
