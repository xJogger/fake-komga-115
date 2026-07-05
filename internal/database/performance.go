package database

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) RecordArchiveIndexBuild(
	ctx context.Context,
	bookID, version, indexJSON string,
	pageCount int,
	duration time.Duration,
) error {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO zip_indexes(
 book_id,version,page_count,index_json,index_duration_ns,created_at,updated_at
) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(book_id) DO UPDATE SET
 version=excluded.version,
 page_count=excluded.page_count,
 index_json=excluded.index_json,
 index_duration_ns=excluded.index_duration_ns,
 updated_at=excluded.updated_at`,
		bookID, version, pageCount, indexJSON, maxDurationNS(duration), now, now)
	return err
}

func (s *Store) ArchiveIndexRaw(ctx context.Context, bookID string) (string, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT index_json FROM zip_indexes WHERE book_id=?`, bookID).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return raw, true, nil
}

func (s *Store) ArchiveIndexRawForVersion(
	ctx context.Context,
	bookID, version string,
) (string, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT index_json FROM zip_indexes WHERE book_id=? AND version=?`,
		bookID, version).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return raw, true, nil
}

func (s *Store) HasArchiveIndexVersion(
	ctx context.Context,
	bookID, version string,
) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `
SELECT 1 FROM zip_indexes WHERE book_id=? AND version=?`, bookID, version).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Store) DeleteArchiveIndex(ctx context.Context, bookID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM zip_indexes WHERE book_id=?`, bookID)
	return err
}

func (s *Store) ArchiveIndexStats(
	ctx context.Context,
	bookID string,
) (ArchiveIndexStats, bool, error) {
	var stats ArchiveIndexStats
	var updated string
	var durationNS int64
	err := s.db.QueryRowContext(ctx, `
SELECT book_id,version,page_count,index_duration_ns,updated_at
FROM zip_indexes WHERE book_id=?`, bookID).Scan(
		&stats.BookID, &stats.Version, &stats.PageCount, &durationNS, &updated,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return ArchiveIndexStats{}, false, nil
		}
		return ArchiveIndexStats{}, false, err
	}
	stats.Duration = time.Duration(durationNS)
	stats.HasDuration = durationNS > 0
	stats.CompletedAt = parseTime(updated)
	return stats, true, nil
}

func (s *Store) ArchiveIndexStatsBySeries(
	ctx context.Context,
	seriesID string,
) (map[string]ArchiveIndexStats, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT z.book_id,z.version,z.page_count,z.index_duration_ns,z.updated_at,
 b.name,b.number_sort,b.series_id,b.library_id
FROM zip_indexes z
JOIN books b ON b.id=z.book_id
WHERE b.series_id=?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ArchiveIndexStats{}
	for rows.Next() {
		var stats ArchiveIndexStats
		var updated string
		var durationNS int64
		if err := rows.Scan(
			&stats.BookID, &stats.Version, &stats.PageCount, &durationNS, &updated,
			&stats.BookName, &stats.BookNumber, &stats.BookSeriesID, &stats.BookLibraryID,
		); err != nil {
			return nil, err
		}
		stats.Duration = time.Duration(durationNS)
		stats.HasDuration = durationNS > 0
		stats.CompletedAt = parseTime(updated)
		out[stats.BookID] = stats
	}
	return out, rows.Err()
}

func (s *Store) RecordBookDownload(
	ctx context.Context,
	book Book,
	bytes int64,
	duration time.Duration,
) error {
	if bytes <= 0 || duration <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO book_download_stats(book_id,series_id,bytes,duration_ns,samples,updated_at)
VALUES(?,?,?,?,1,?)
ON CONFLICT(book_id) DO UPDATE SET
 series_id=excluded.series_id,
 bytes=book_download_stats.bytes+excluded.bytes,
 duration_ns=book_download_stats.duration_ns+excluded.duration_ns,
 samples=book_download_stats.samples+1,
 updated_at=excluded.updated_at`,
		book.ID, book.SeriesID, bytes, maxDurationNS(duration), nowText())
	return err
}

func (s *Store) BookDownloadStats(
	ctx context.Context,
	bookID string,
) (DownloadStats, bool, error) {
	var stats DownloadStats
	var updated string
	var durationNS int64
	err := s.db.QueryRowContext(ctx, `
SELECT book_id,series_id,bytes,duration_ns,samples,updated_at
FROM book_download_stats WHERE book_id=?`, bookID).Scan(
		&stats.BookID, &stats.SeriesID, &stats.Bytes, &durationNS,
		&stats.Samples, &updated,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return DownloadStats{}, false, nil
		}
		return DownloadStats{}, false, err
	}
	stats.Duration = time.Duration(durationNS)
	stats.UpdatedAt = parseTime(updated)
	stats.HasDownload = stats.Bytes > 0 && stats.Duration > 0
	return stats, true, nil
}

func (s *Store) BookDownloadStatsBySeries(
	ctx context.Context,
	seriesID string,
) (map[string]DownloadStats, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT book_id,series_id,bytes,duration_ns,samples,updated_at
FROM book_download_stats WHERE series_id=?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DownloadStats{}
	for rows.Next() {
		var stats DownloadStats
		var updated string
		var durationNS int64
		if err := rows.Scan(
			&stats.BookID, &stats.SeriesID, &stats.Bytes, &durationNS,
			&stats.Samples, &updated,
		); err != nil {
			return nil, err
		}
		stats.Duration = time.Duration(durationNS)
		stats.UpdatedAt = parseTime(updated)
		stats.HasDownload = stats.Bytes > 0 && stats.Duration > 0
		out[stats.BookID] = stats
	}
	return out, rows.Err()
}

func (s *Store) SeriesPerformance(
	ctx context.Context,
	seriesID string,
	currentVersions map[string]string,
) (SeriesPerformanceStats, error) {
	var stats SeriesPerformanceStats
	var coverDurationNS sql.NullInt64
	var coverUpdated sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM books WHERE series_id=?`, seriesID).Scan(&stats.BooksCount); err != nil {
		return SeriesPerformanceStats{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT z.book_id,z.version,z.index_duration_ns,z.updated_at
FROM zip_indexes z
JOIN books b ON b.id=z.book_id
WHERE b.series_id=?`, seriesID)
	if err != nil {
		return SeriesPerformanceStats{}, err
	}
	var totalIndexDuration time.Duration
	for rows.Next() {
		var bookID, version, updated string
		var durationNS int64
		if err := rows.Scan(&bookID, &version, &durationNS, &updated); err != nil {
			rows.Close()
			return SeriesPerformanceStats{}, err
		}
		if currentVersions != nil && currentVersions[bookID] != version {
			continue
		}
		stats.IndexedBooksCount++
		if durationNS > 0 {
			stats.IndexDurationCount++
			totalIndexDuration += time.Duration(durationNS)
		}
		completedAt := parseTime(updated)
		if completedAt.After(stats.IndexLatestAt) {
			stats.IndexLatestAt = completedAt
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SeriesPerformanceStats{}, err
	}
	if err := rows.Close(); err != nil {
		return SeriesPerformanceStats{}, err
	}
	if stats.IndexDurationCount > 0 {
		stats.IndexAverage = totalIndexDuration / time.Duration(stats.IndexDurationCount)
	}

	err = s.db.QueryRowContext(ctx, `
SELECT generation_duration_ns,updated_at FROM series_thumbnails WHERE series_id=?`,
		seriesID).Scan(&coverDurationNS, &coverUpdated)
	if err != nil && err != sql.ErrNoRows {
		return SeriesPerformanceStats{}, err
	}
	if coverDurationNS.Valid && coverDurationNS.Int64 > 0 {
		stats.HasCoverDuration = true
		stats.CoverDuration = time.Duration(coverDurationNS.Int64)
	}
	if coverUpdated.Valid {
		stats.CoverCompletedAt = parseTime(coverUpdated.String)
	}

	var downloadNS int64
	var updated sql.NullString
	err = s.db.QueryRowContext(ctx, `
SELECT coalesce(sum(bytes),0),coalesce(sum(duration_ns),0),coalesce(sum(samples),0),
 max(updated_at)
FROM book_download_stats WHERE series_id=?`, seriesID).Scan(
		&stats.DownloadBytes, &downloadNS, &stats.DownloadSamples, &updated,
	)
	if err != nil {
		return SeriesPerformanceStats{}, err
	}
	stats.DownloadDuration = time.Duration(downloadNS)
	stats.HasDownload = stats.DownloadBytes > 0 && stats.DownloadDuration > 0
	if updated.Valid {
		stats.DownloadLatestAt = parseTime(updated.String)
	}
	return stats, nil
}

func (s *Store) GlobalPerformance(ctx context.Context) (GlobalPerformanceStats, error) {
	var stats GlobalPerformanceStats
	var indexAvg, coverAvg sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*),avg(index_duration_ns)
FROM zip_indexes WHERE index_duration_ns>0`).Scan(
		&stats.IndexCount, &indexAvg,
	); err != nil {
		return GlobalPerformanceStats{}, err
	}
	if indexAvg.Valid {
		stats.IndexAverageDurationNs = int64(indexAvg.Float64)
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*),avg(generation_duration_ns)
FROM series_thumbnails WHERE generation_duration_ns>0`).Scan(
		&stats.CoverCount, &coverAvg,
	); err != nil {
		return GlobalPerformanceStats{}, err
	}
	if coverAvg.Valid {
		stats.CoverAverageDurationNs = int64(coverAvg.Float64)
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT coalesce(sum(bytes),0),coalesce(sum(duration_ns),0),coalesce(sum(samples),0)
FROM book_download_stats`).Scan(
		&stats.DownloadBytes, &stats.DownloadDurationNs, &stats.DownloadSamples,
	); err != nil {
		return GlobalPerformanceStats{}, err
	}
	return stats, nil
}

func maxDurationNS(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return int64(duration)
}
