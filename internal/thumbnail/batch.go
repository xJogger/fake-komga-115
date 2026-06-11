package thumbnail

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
)

const (
	defaultBatchLimit = 50
	maxBatchLimit     = 10000
	maxStoredErrors   = 20
)

var (
	ErrBatchAlreadyQueued = errors.New("thumbnail job is already queued or running for this library")
	urlPattern            = regexp.MustCompile(`https?://\S+`)
)

type BatchRun struct {
	ID              string   `json:"id"`
	LibraryID       string   `json:"libraryId"`
	LibraryName     string   `json:"libraryName"`
	Mode            string   `json:"mode"`
	RequestedLimit  int      `json:"requestedLimit"`
	Status          string   `json:"status"`
	TotalSeries     int      `json:"totalSeries"`
	ProcessedSeries int      `json:"processedSeries"`
	GeneratedCount  int      `json:"generatedCount"`
	SkippedCount    int      `json:"skippedCount"`
	FailedCount     int      `json:"failedCount"`
	CurrentSeries   string   `json:"currentSeries"`
	Errors          []string `json:"errors"`
	CancelRequested bool     `json:"cancelRequested"`
	StartedAt       *string  `json:"startedAt"`
	CompletedAt     *string  `json:"completedAt"`
	CreatedAt       string   `json:"createdAt"`
}

type batchJob struct {
	runID     string
	libraryID string
	all       bool
	limit     int
}

type batchSeries struct {
	ID   string
	Name string
}

type ensureCoverFunc func(context.Context, string) (bool, error)

type BatchManager struct {
	store  *database.Store
	ensure ensureCoverFunc
	logger *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan batchJob

	mu          sync.Mutex
	queued      map[string]string
	activeRunID string
	activeStop  context.CancelFunc
}

func NewBatchManager(
	store *database.Store,
	archiveService *archive.Service,
	service *Service,
	logger *slog.Logger,
) *BatchManager {
	return newBatchManager(store, func(ctx context.Context, seriesID string) (bool, error) {
		valid, err := service.HasValid(ctx, seriesID)
		if err != nil || valid {
			return false, err
		}
		book, err := store.FirstBookInSeries(ctx, seriesID)
		if err != nil {
			return false, err
		}
		page, err := archiveService.ReadPage(ctx, book, 1)
		if err != nil {
			return false, err
		}
		if err := service.Generate(ctx, book, page.Data); err != nil {
			return false, err
		}
		return true, nil
	}, logger)
}

func newBatchManager(
	store *database.Store,
	ensure ensureCoverFunc,
	logger *slog.Logger,
) *BatchManager {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = store.DB().ExecContext(ctx, `
UPDATE thumbnail_runs SET status='failed',completed_at=?,
 errors_json='["service restarted before thumbnail job completed"]'
WHERE status IN ('queued','running')`, now)
	manager := &BatchManager{
		store: store, ensure: ensure, logger: logger,
		ctx: ctx, cancel: cancel, jobs: make(chan batchJob, 64),
		queued: make(map[string]string),
	}
	go manager.worker()
	return manager
}

func (m *BatchManager) Close() {
	m.cancel()
	m.mu.Lock()
	if m.activeStop != nil {
		m.activeStop()
	}
	m.mu.Unlock()
}

func (m *BatchManager) Start(
	ctx context.Context,
	libraryID string,
	all bool,
	limit int,
) (BatchRun, error) {
	if _, err := m.store.Library(ctx, libraryID); err != nil {
		return BatchRun{}, err
	}
	if all {
		limit = 0
	} else {
		if limit <= 0 {
			limit = defaultBatchLimit
		}
		if limit > maxBatchLimit {
			return BatchRun{}, fmt.Errorf("thumbnail limit cannot exceed %d", maxBatchLimit)
		}
	}

	m.mu.Lock()
	if _, exists := m.queued[libraryID]; exists {
		m.mu.Unlock()
		return BatchRun{}, ErrBatchAlreadyQueued
	}
	runID := randomBatchID()
	m.queued[libraryID] = runID
	m.mu.Unlock()

	total, err := m.selectedCount(ctx, libraryID, all, limit)
	if err != nil {
		m.releaseQueued(libraryID)
		return BatchRun{}, err
	}
	mode := "latest"
	if all {
		mode = "all"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = m.store.DB().ExecContext(ctx, `
INSERT INTO thumbnail_runs(
 id,library_id,mode,requested_limit,status,total_series,created_at
) VALUES(?,?,?,?, 'queued',?,?)`,
		runID, libraryID, mode, limit, total, now)
	if err != nil {
		m.releaseQueued(libraryID)
		return BatchRun{}, err
	}
	run, err := m.Run(ctx, runID)
	if err != nil {
		m.releaseQueued(libraryID)
		return BatchRun{}, err
	}
	select {
	case m.jobs <- batchJob{runID: runID, libraryID: libraryID, all: all, limit: limit}:
		return run, nil
	case <-m.ctx.Done():
		return BatchRun{}, context.Canceled
	}
}

func (m *BatchManager) Cancel(ctx context.Context, runID string) error {
	result, err := m.store.DB().ExecContext(ctx, `
UPDATE thumbnail_runs SET cancel_requested=1
WHERE id=? AND status IN ('queued','running')`, runID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	m.mu.Lock()
	if m.activeRunID == runID && m.activeStop != nil {
		m.activeStop()
	}
	m.mu.Unlock()
	return nil
}

func (m *BatchManager) Runs(ctx context.Context, limit int) ([]BatchRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := m.store.DB().QueryContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,''),r.mode,r.requested_limit,r.status,
 r.total_series,r.processed_series,r.generated_count,r.skipped_count,r.failed_count,
 r.current_series,r.errors_json,r.cancel_requested,r.started_at,r.completed_at,r.created_at
FROM thumbnail_runs r LEFT JOIN libraries l ON l.id=r.library_id
ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]BatchRun, 0)
	for rows.Next() {
		run, err := scanBatchRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (m *BatchManager) Run(ctx context.Context, runID string) (BatchRun, error) {
	row := m.store.DB().QueryRowContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,''),r.mode,r.requested_limit,r.status,
 r.total_series,r.processed_series,r.generated_count,r.skipped_count,r.failed_count,
 r.current_series,r.errors_json,r.cancel_requested,r.started_at,r.completed_at,r.created_at
FROM thumbnail_runs r LEFT JOIN libraries l ON l.id=r.library_id WHERE r.id=?`, runID)
	return scanBatchRun(row)
}

type rowScanner interface {
	Scan(...any) error
}

func scanBatchRun(row rowScanner) (BatchRun, error) {
	var run BatchRun
	var errorsJSON string
	err := row.Scan(
		&run.ID, &run.LibraryID, &run.LibraryName, &run.Mode, &run.RequestedLimit,
		&run.Status, &run.TotalSeries, &run.ProcessedSeries, &run.GeneratedCount,
		&run.SkippedCount, &run.FailedCount, &run.CurrentSeries, &errorsJSON,
		&run.CancelRequested, &run.StartedAt, &run.CompletedAt, &run.CreatedAt,
	)
	if err != nil {
		return BatchRun{}, err
	}
	_ = json.Unmarshal([]byte(errorsJSON), &run.Errors)
	if run.Errors == nil {
		run.Errors = []string{}
	}
	return run, nil
}

func (m *BatchManager) worker() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case item := <-m.jobs:
			m.execute(item)
		}
	}
}

func (m *BatchManager) execute(item batchJob) {
	ctx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	m.activeRunID = item.runID
	m.activeStop = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.queued, item.libraryID)
		m.activeRunID = ""
		m.activeStop = nil
		m.mu.Unlock()
	}()

	run, err := m.Run(ctx, item.runID)
	if err != nil {
		return
	}
	if run.CancelRequested {
		m.finish(item, run, "canceled")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.DB().ExecContext(ctx,
		`UPDATE thumbnail_runs SET status='running',started_at=? WHERE id=?`,
		now, item.runID)

	series, err := m.selectSeries(ctx, item.libraryID, item.all, item.limit)
	if err != nil {
		run.Errors = append(run.Errors, safeBatchError(err))
		m.finish(item, run, "failed")
		return
	}
	run.TotalSeries = len(series)
	_, _ = m.store.DB().ExecContext(ctx,
		`UPDATE thumbnail_runs SET total_series=? WHERE id=?`, run.TotalSeries, item.runID)

	for _, current := range series {
		if err := ctx.Err(); err != nil {
			m.finish(item, run, "canceled")
			return
		}
		generated, err := m.ensure(ctx, current.ID)
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			m.finish(item, run, "canceled")
			return
		}
		run.ProcessedSeries++
		run.CurrentSeries = current.Name
		switch {
		case err != nil:
			run.FailedCount++
			if len(run.Errors) < maxStoredErrors {
				run.Errors = append(run.Errors, current.Name+": "+safeBatchError(err))
			}
		case generated:
			run.GeneratedCount++
		default:
			run.SkippedCount++
		}
		if err := m.updateProgress(ctx, item.runID, run); err != nil {
			m.finish(item, run, "canceled")
			return
		}
	}
	status := "success"
	if run.FailedCount > 0 {
		status = "partial"
	}
	m.finish(item, run, status)
	if m.logger != nil {
		m.logger.Info("thumbnail job completed",
			"run", item.runID, "library", item.libraryID, "status", status,
			"generated", run.GeneratedCount, "skipped", run.SkippedCount, "failed", run.FailedCount)
	}
}

func (m *BatchManager) updateProgress(
	ctx context.Context,
	runID string,
	run BatchRun,
) error {
	errorsJSON, _ := json.Marshal(run.Errors)
	result, err := m.store.DB().ExecContext(ctx, `
UPDATE thumbnail_runs SET processed_series=?,generated_count=?,skipped_count=?,
 failed_count=?,current_series=?,errors_json=?
WHERE id=? AND cancel_requested=0`,
		run.ProcessedSeries, run.GeneratedCount, run.SkippedCount, run.FailedCount,
		run.CurrentSeries, string(errorsJSON), runID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return context.Canceled
	}
	return nil
}

func (m *BatchManager) finish(item batchJob, run BatchRun, status string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	errorsJSON, _ := json.Marshal(run.Errors)
	_, _ = m.store.DB().ExecContext(context.Background(), `
UPDATE thumbnail_runs SET status=?,completed_at=?,total_series=?,processed_series=?,
 generated_count=?,skipped_count=?,failed_count=?,current_series=?,errors_json=?
WHERE id=?`,
		status, now, run.TotalSeries, run.ProcessedSeries, run.GeneratedCount,
		run.SkippedCount, run.FailedCount, run.CurrentSeries, string(errorsJSON), item.runID)
}

func (m *BatchManager) selectedCount(
	ctx context.Context,
	libraryID string,
	all bool,
	limit int,
) (int, error) {
	var total int
	err := m.store.DB().QueryRowContext(ctx, `
SELECT count(*) FROM series s
WHERE s.library_id=? AND EXISTS (SELECT 1 FROM books b WHERE b.series_id=s.id)`,
		libraryID).Scan(&total)
	if !all && total > limit {
		total = limit
	}
	return total, err
}

func (m *BatchManager) selectSeries(
	ctx context.Context,
	libraryID string,
	all bool,
	limit int,
) ([]batchSeries, error) {
	query := `
SELECT s.id,s.name FROM series s
WHERE s.library_id=? AND EXISTS (SELECT 1 FROM books existing_book WHERE existing_book.series_id=s.id)
ORDER BY COALESCE(
 (SELECT MAX(b.file_modified_at) FROM books b WHERE b.series_id=s.id),
 s.file_modified_at,s.updated_at
) DESC,s.name COLLATE NOCASE ASC,s.id ASC`
	args := []any{libraryID}
	if !all {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := m.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []batchSeries
	for rows.Next() {
		var item batchSeries
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (m *BatchManager) releaseQueued(libraryID string) {
	m.mu.Lock()
	delete(m.queued, libraryID)
	m.mu.Unlock()
}

func safeBatchError(err error) string {
	message := urlPattern.ReplaceAllString(err.Error(), "[redacted-url]")
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		message = message[:300] + "..."
	}
	return message
}

func randomBatchID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
