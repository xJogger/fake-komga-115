package maintenance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
)

const (
	OperationBookIndex       = "book_index"
	OperationSeriesIndex     = "series_index"
	OperationSeriesThumbnail = "series_thumbnail"

	TargetBook   = "book"
	TargetSeries = "series"

	maxStoredErrors = 20
)

var (
	ErrJobAlreadyQueued = errors.New("maintenance job is already queued or running for this target")
	ErrInvalidOperation = errors.New("invalid maintenance operation")
	urlPattern          = regexp.MustCompile(`https?://\S+`)
)

type Run struct {
	ID              string   `json:"id"`
	Operation       string   `json:"operation"`
	TargetType      string   `json:"targetType"`
	TargetID        string   `json:"targetId"`
	TargetName      string   `json:"targetName"`
	SeriesID        string   `json:"seriesId,omitempty"`
	BookID          string   `json:"bookId,omitempty"`
	Force           bool     `json:"force"`
	Status          string   `json:"status"`
	TotalItems      int      `json:"totalItems"`
	ProcessedItems  int      `json:"processedItems"`
	GeneratedCount  int      `json:"generatedCount"`
	SkippedCount    int      `json:"skippedCount"`
	FailedCount     int      `json:"failedCount"`
	CurrentItem     string   `json:"currentItem"`
	Errors          []string `json:"errors"`
	CancelRequested bool     `json:"cancelRequested"`
	StartedAt       *string  `json:"startedAt"`
	CompletedAt     *string  `json:"completedAt"`
	CreatedAt       string   `json:"createdAt"`
}

type ListFilter struct {
	TargetType string
	TargetID   string
	Limit      int
}

type job struct {
	runID string
}

type indexFunc func(context.Context, database.Book, bool) (bool, error)
type coverFunc func(context.Context, string, bool) (bool, error)

type Manager struct {
	store      *database.Store
	buildIndex indexFunc
	buildCover coverFunc
	logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan job

	mu          sync.Mutex
	queued      map[string]string
	activeRunID string
}

func New(
	store *database.Store,
	archiveService *archive.Service,
	thumbnailService *thumbnail.Service,
	logger *slog.Logger,
) *Manager {
	return newManager(
		store,
		func(ctx context.Context, book database.Book, force bool) (bool, error) {
			version := archive.BookVersion(book)
			if !force {
				valid, err := store.HasArchiveIndexVersion(ctx, book.ID, version)
				if err != nil || valid {
					return false, err
				}
			}
			if force {
				if err := store.DeleteArchiveIndex(ctx, book.ID); err != nil {
					return false, err
				}
			}
			if _, err := archiveService.ListPages(ctx, book); err != nil {
				return false, err
			}
			return true, nil
		},
		func(ctx context.Context, seriesID string, force bool) (bool, error) {
			if !force {
				valid, err := thumbnailService.HasValid(ctx, seriesID)
				if err != nil || valid {
					return false, err
				}
			}
			book, err := store.FirstBookInSeries(ctx, seriesID)
			if err != nil {
				return false, err
			}
			started := time.Now()
			page, err := archiveService.ReadPage(ctx, book, 1)
			if err != nil {
				return false, err
			}
			if err := thumbnailService.GenerateWithOptions(ctx, book, page.Data, force); err != nil {
				return false, err
			}
			if err := thumbnailService.SetGenerationDuration(ctx, seriesID, time.Since(started)); err != nil {
				return false, err
			}
			return true, nil
		},
		logger,
	)
}

func newManager(
	store *database.Store,
	buildIndex indexFunc,
	buildCover coverFunc,
	logger *slog.Logger,
) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = store.DB().ExecContext(ctx, `
UPDATE maintenance_runs SET status='failed',completed_at=?,
 errors_json='["service restarted before maintenance job completed"]'
WHERE status IN ('queued','running')`, now)
	manager := &Manager{
		store: store, buildIndex: buildIndex, buildCover: buildCover, logger: logger,
		ctx: ctx, cancel: cancel, jobs: make(chan job, 64), queued: make(map[string]string),
	}
	go manager.worker()
	return manager
}

func (m *Manager) Close() {
	m.cancel()
}

func (m *Manager) StartBookIndex(
	ctx context.Context,
	bookID string,
	force bool,
) (Run, error) {
	book, err := m.store.BookByID(ctx, bookID)
	if err != nil {
		return Run{}, err
	}
	return m.start(ctx, OperationBookIndex, TargetBook, book.ID, book.Name, book.SeriesID, book.ID, force, 1)
}

func (m *Manager) StartSeriesIndex(
	ctx context.Context,
	seriesID string,
	force bool,
) (Run, error) {
	series, err := m.store.SeriesByID(ctx, seriesID)
	if err != nil {
		return Run{}, err
	}
	books, _, err := m.store.BooksPage(ctx, database.BookQuery{
		SeriesID: seriesID, Unpaged: true,
	})
	if err != nil {
		return Run{}, err
	}
	return m.start(ctx, OperationSeriesIndex, TargetSeries, series.ID, series.Name, series.ID, "", force, len(books))
}

func (m *Manager) StartSeriesThumbnail(
	ctx context.Context,
	seriesID string,
	force bool,
) (Run, error) {
	series, err := m.store.SeriesByID(ctx, seriesID)
	if err != nil {
		return Run{}, err
	}
	if _, err := m.store.FirstBookInSeries(ctx, seriesID); err != nil {
		return Run{}, err
	}
	return m.start(ctx, OperationSeriesThumbnail, TargetSeries, series.ID, series.Name, series.ID, "", force, 1)
}

func (m *Manager) start(
	ctx context.Context,
	operation, targetType, targetID, targetName, seriesID, bookID string,
	force bool,
	totalItems int,
) (Run, error) {
	if operation != OperationBookIndex &&
		operation != OperationSeriesIndex &&
		operation != OperationSeriesThumbnail {
		return Run{}, ErrInvalidOperation
	}
	key := operation + ":" + targetType + ":" + targetID
	m.mu.Lock()
	if _, exists := m.queued[key]; exists {
		m.mu.Unlock()
		return Run{}, ErrJobAlreadyQueued
	}
	runID := randomRunID()
	m.queued[key] = runID
	m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := m.store.DB().ExecContext(ctx, `
INSERT INTO maintenance_runs(
 id,operation,target_type,target_id,target_name,series_id,book_id,force,status,total_items,created_at
) VALUES(?,?,?,?,?,?,?,?, 'queued',?,?)`,
		runID, operation, targetType, targetID, targetName,
		nullIfEmpty(seriesID), nullIfEmpty(bookID), force, totalItems, now)
	if err != nil {
		m.releaseQueued(key)
		return Run{}, err
	}
	run, err := m.Run(ctx, runID)
	if err != nil {
		m.releaseQueued(key)
		return Run{}, err
	}
	select {
	case m.jobs <- job{runID: runID}:
		return run, nil
	case <-m.ctx.Done():
		m.releaseQueued(key)
		return Run{}, context.Canceled
	}
}

func (m *Manager) Cancel(ctx context.Context, runID string) error {
	result, err := m.store.DB().ExecContext(ctx, `
UPDATE maintenance_runs SET cancel_requested=1
WHERE id=? AND status IN ('queued','running')`, runID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (m *Manager) Runs(ctx context.Context, filter ListFilter) ([]Run, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 20
	}
	query := `
SELECT id,operation,target_type,target_id,target_name,
 coalesce(series_id,''),coalesce(book_id,''),force,status,total_items,processed_items,
 generated_count,skipped_count,failed_count,current_item,errors_json,cancel_requested,
 started_at,completed_at,created_at
FROM maintenance_runs`
	var clauses []string
	var args []any
	if filter.TargetType != "" {
		clauses = append(clauses, "target_type=?")
		args = append(args, filter.TargetType)
	}
	if filter.TargetID != "" {
		clauses = append(clauses, "target_id=?")
		args = append(args, filter.TargetID)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, filter.Limit)
	rows, err := m.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (m *Manager) Run(ctx context.Context, runID string) (Run, error) {
	row := m.store.DB().QueryRowContext(ctx, `
SELECT id,operation,target_type,target_id,target_name,
 coalesce(series_id,''),coalesce(book_id,''),force,status,total_items,processed_items,
 generated_count,skipped_count,failed_count,current_item,errors_json,cancel_requested,
 started_at,completed_at,created_at
FROM maintenance_runs WHERE id=?`, runID)
	return scanRun(row)
}

type rowScanner interface {
	Scan(...any) error
}

func scanRun(row rowScanner) (Run, error) {
	var run Run
	var errorsJSON string
	err := row.Scan(
		&run.ID, &run.Operation, &run.TargetType, &run.TargetID, &run.TargetName,
		&run.SeriesID, &run.BookID, &run.Force, &run.Status, &run.TotalItems,
		&run.ProcessedItems, &run.GeneratedCount, &run.SkippedCount, &run.FailedCount,
		&run.CurrentItem, &errorsJSON, &run.CancelRequested, &run.StartedAt,
		&run.CompletedAt, &run.CreatedAt,
	)
	if err != nil {
		return Run{}, err
	}
	_ = json.Unmarshal([]byte(errorsJSON), &run.Errors)
	if run.Errors == nil {
		run.Errors = []string{}
	}
	return run, nil
}

func (m *Manager) worker() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case item := <-m.jobs:
			m.execute(item)
		}
	}
}

func (m *Manager) execute(item job) {
	ctx := m.ctx
	m.mu.Lock()
	m.activeRunID = item.runID
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.activeRunID = ""
		m.mu.Unlock()
	}()

	run, err := m.Run(ctx, item.runID)
	if err != nil {
		return
	}
	defer m.releaseQueued(run.Operation + ":" + run.TargetType + ":" + run.TargetID)
	if run.CancelRequested {
		m.finish(run, "canceled")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.DB().ExecContext(ctx,
		`UPDATE maintenance_runs SET status='running',started_at=? WHERE id=?`,
		now, run.ID)

	switch run.Operation {
	case OperationBookIndex:
		m.executeBookIndex(ctx, &run)
	case OperationSeriesIndex:
		m.executeSeriesIndex(ctx, &run)
	case OperationSeriesThumbnail:
		m.executeSeriesThumbnail(ctx, &run)
	default:
		run.Errors = append(run.Errors, "invalid operation")
		m.finish(run, "failed")
	}
}

func (m *Manager) executeBookIndex(ctx context.Context, run *Run) {
	book, err := m.store.BookByID(ctx, run.BookID)
	if err != nil {
		run.Errors = append(run.Errors, safeError(err))
		m.finish(*run, "failed")
		return
	}
	m.processBook(ctx, run, book)
	m.finishWithCounts(*run)
}

func (m *Manager) executeSeriesIndex(ctx context.Context, run *Run) {
	books, _, err := m.store.BooksPage(ctx, database.BookQuery{
		SeriesID: run.SeriesID, Unpaged: true,
	})
	if err != nil {
		run.Errors = append(run.Errors, safeError(err))
		m.finish(*run, "failed")
		return
	}
	run.TotalItems = len(books)
	if err := m.updateProgress(ctx, run); err != nil {
		m.finish(*run, "canceled")
		return
	}
	for _, book := range books {
		if m.cancelRequested(ctx, run.ID) {
			m.finish(*run, "canceled")
			return
		}
		m.processBook(ctx, run, book)
		if err := m.updateProgress(ctx, run); err != nil {
			m.finish(*run, "canceled")
			return
		}
	}
	m.finishWithCounts(*run)
}

func (m *Manager) executeSeriesThumbnail(ctx context.Context, run *Run) {
	if m.cancelRequested(ctx, run.ID) {
		m.finish(*run, "canceled")
		return
	}
	run.CurrentItem = run.TargetName
	generated, err := m.buildCover(ctx, run.SeriesID, run.Force)
	run.ProcessedItems++
	switch {
	case err != nil:
		run.FailedCount++
		run.Errors = append(run.Errors, run.TargetName+": "+safeError(err))
	case generated:
		run.GeneratedCount++
	default:
		run.SkippedCount++
	}
	if err := m.updateProgress(ctx, run); err != nil {
		m.finish(*run, "canceled")
		return
	}
	m.finishWithCounts(*run)
}

func (m *Manager) processBook(ctx context.Context, run *Run, book database.Book) {
	run.CurrentItem = book.Name
	generated, err := m.buildIndex(ctx, book, run.Force)
	run.ProcessedItems++
	switch {
	case err != nil:
		run.FailedCount++
		if len(run.Errors) < maxStoredErrors {
			run.Errors = append(run.Errors, book.Name+": "+safeError(err))
		}
	case generated:
		run.GeneratedCount++
	default:
		run.SkippedCount++
	}
}

func (m *Manager) updateProgress(ctx context.Context, run *Run) error {
	errorsJSON, _ := json.Marshal(run.Errors)
	result, err := m.store.DB().ExecContext(ctx, `
UPDATE maintenance_runs SET total_items=?,processed_items=?,generated_count=?,
 skipped_count=?,failed_count=?,current_item=?,errors_json=?
WHERE id=? AND cancel_requested=0`,
		run.TotalItems, run.ProcessedItems, run.GeneratedCount, run.SkippedCount,
		run.FailedCount, run.CurrentItem, string(errorsJSON), run.ID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return context.Canceled
	}
	return nil
}

func (m *Manager) finishWithCounts(run Run) {
	status := "success"
	if run.FailedCount > 0 {
		status = "partial"
		if run.GeneratedCount == 0 && run.SkippedCount == 0 {
			status = "failed"
		}
	}
	m.finish(run, status)
	if m.logger != nil {
		m.logger.Info(
			"maintenance job completed",
			"run", run.ID, "operation", run.Operation, "target", run.TargetID,
			"status", status, "generated", run.GeneratedCount,
			"skipped", run.SkippedCount, "failed", run.FailedCount,
		)
	}
}

func (m *Manager) finish(run Run, status string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	errorsJSON, _ := json.Marshal(run.Errors)
	_, _ = m.store.DB().ExecContext(context.Background(), `
UPDATE maintenance_runs SET status=?,completed_at=?,total_items=?,processed_items=?,
 generated_count=?,skipped_count=?,failed_count=?,current_item=?,errors_json=?
WHERE id=?`,
		status, now, run.TotalItems, run.ProcessedItems, run.GeneratedCount,
		run.SkippedCount, run.FailedCount, run.CurrentItem, string(errorsJSON), run.ID)
}

func (m *Manager) cancelRequested(ctx context.Context, runID string) bool {
	var cancel bool
	err := m.store.DB().QueryRowContext(ctx,
		`SELECT cancel_requested FROM maintenance_runs WHERE id=?`, runID).Scan(&cancel)
	return err != nil || cancel
}

func (m *Manager) releaseQueued(key string) {
	m.mu.Lock()
	delete(m.queued, key)
	m.mu.Unlock()
}

func safeError(err error) string {
	message := urlPattern.ReplaceAllString(err.Error(), "[redacted-url]")
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		message = message[:300] + "..."
	}
	return message
}

func randomRunID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
