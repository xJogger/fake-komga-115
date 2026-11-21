package scanner

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
	"github.com/xJogger/fake-komga-115/internal/id"
	"github.com/xJogger/fake-komga-115/internal/localfs"
	"github.com/xJogger/fake-komga-115/internal/natsort"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

type Manager struct {
	store  *database.Store
	client *oneonefive.Client
	webdav *webdav.Manager
	logger *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan job

	mu          sync.Mutex
	queued      map[string]string
	activeRunID string
	activeStop  context.CancelFunc
	listeners   []func(libraryID, runID, trigger, status string)
}

type job struct {
	runID   string
	library database.Library
}

type queueDir struct {
	CID          string
	Name         string
	RelativePath string
	ModifiedAt   time.Time
	RealKey      string
}

type counters struct {
	directories int64
	files       int64
	series      int64
	books       int64
}

func New(store *database.Store, client *oneonefive.Client, logger *slog.Logger) *Manager {
	return NewWithWebDAV(store, client, webdav.NewManager(), logger)
}

func NewWithWebDAV(
	store *database.Store,
	client *oneonefive.Client,
	webdavManager *webdav.Manager,
	logger *slog.Logger,
) *Manager {
	if webdavManager == nil {
		webdavManager = webdav.NewManager()
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = store.DB().ExecContext(ctx, `
UPDATE scan_runs SET status='failed',completed_at=?,error='service restarted before scan completed'
WHERE status IN ('queued','running')`, now)
	_, _ = store.DB().ExecContext(ctx, `
UPDATE libraries SET last_scan_status='failed',last_scan_completed_at=?,
 last_scan_error='service restarted before scan completed'
WHERE last_scan_status='running'`, now)
	_, _ = store.DB().ExecContext(ctx, `DELETE FROM scan_books_staging`)
	_, _ = store.DB().ExecContext(ctx, `DELETE FROM scan_series_staging`)
	m := &Manager{
		store:  store,
		client: client,
		webdav: webdavManager,
		logger: logger,
		ctx:    ctx,
		cancel: cancel,
		jobs:   make(chan job, 64),
		queued: make(map[string]string),
	}
	go m.worker()
	return m
}

func (m *Manager) AddCompletionListener(fn func(libraryID, runID, trigger, status string)) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	m.listeners = append(m.listeners, fn)
	m.mu.Unlock()
}

func (m *Manager) notifyCompleted(libraryID, runID, trigger, status string) {
	m.mu.Lock()
	cbs := make([]func(string, string, string, string), len(m.listeners))
	copy(cbs, m.listeners)
	m.mu.Unlock()
	for _, fn := range cbs {
		fn(libraryID, runID, trigger, status)
	}
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	if m.activeStop != nil {
		m.activeStop()
	}
	m.mu.Unlock()
}

func (m *Manager) StartAll(ctx context.Context, trigger string) ([]database.ScanRun, error) {
	libraries, err := m.store.LibrariesBasic(ctx, false)
	if err != nil {
		return nil, err
	}
	runs := make([]database.ScanRun, 0)
	for _, library := range libraries {
		if !library.Enabled || library.EffectiveProvider() == database.ProviderUnion {
			continue
		}
		run, err := m.StartLibrary(ctx, library.ID, trigger)
		if err != nil {
			if errors.Is(err, ErrAlreadyQueued) {
				continue
			}
			return runs, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

var (
	ErrAlreadyQueued            = errors.New("library scan is already queued or running")
	ErrLibraryHidden            = errors.New("library is hidden")
	ErrUnionLibraryNotScannable = errors.New("union library cannot be scanned directly; scan member libraries or refresh the union")
)

func (m *Manager) StartLibrary(ctx context.Context, libraryID, trigger string) (database.ScanRun, error) {
	library, err := m.store.LibraryBasic(ctx, libraryID)
	if err != nil {
		return database.ScanRun{}, err
	}
	if !library.Enabled {
		return database.ScanRun{}, ErrLibraryHidden
	}
	if library.EffectiveProvider() == database.ProviderUnion {
		return database.ScanRun{}, ErrUnionLibraryNotScannable
	}
	m.mu.Lock()
	if _, exists := m.queued[libraryID]; exists {
		m.mu.Unlock()
		return database.ScanRun{}, ErrAlreadyQueued
	}
	runID := randomID()
	m.queued[libraryID] = runID
	m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if trigger == "" {
		trigger = "manual"
	}
	_, err = m.store.DB().ExecContext(ctx, `
INSERT INTO scan_runs(id,library_id,status,trigger_type,created_at)
VALUES(?,?,'queued',?,?)`, runID, library.ID, trigger, now)
	if err != nil {
		m.mu.Lock()
		delete(m.queued, libraryID)
		m.mu.Unlock()
		return database.ScanRun{}, err
	}
	run, err := m.Run(ctx, runID)
	if err != nil {
		return database.ScanRun{}, err
	}
	select {
	case m.jobs <- job{runID: runID, library: library}:
		return run, nil
	case <-m.ctx.Done():
		return database.ScanRun{}, context.Canceled
	}
}

func (m *Manager) Cancel(ctx context.Context, runID string) error {
	result, err := m.store.DB().ExecContext(ctx,
		`UPDATE scan_runs SET cancel_requested=1 WHERE id=? AND status IN ('queued','running')`, runID)
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

func (m *Manager) CancelLibrary(ctx context.Context, libraryID string) {
	rows, err := m.store.DB().QueryContext(ctx,
		`SELECT id FROM scan_runs WHERE library_id=? AND status IN ('queued','running')`, libraryID)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_ = m.Cancel(ctx, id)
	}
}

func (m *Manager) Runs(ctx context.Context, limit int) ([]database.ScanRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := m.store.DB().QueryContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,''),r.status,r.trigger_type,r.started_at,r.completed_at,
 r.directories_seen,r.files_seen,r.series_seen,r.books_seen,r.current_path,r.error,
 r.cancel_requested,r.created_at
FROM scan_runs r LEFT JOIN libraries l ON l.id=r.library_id
ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]database.ScanRun, 0)
	for rows.Next() {
		var run database.ScanRun
		if err := rows.Scan(
			&run.ID, &run.LibraryID, &run.LibraryName, &run.Status, &run.TriggerType,
			&run.StartedAt, &run.CompletedAt, &run.DirectoriesSeen, &run.FilesSeen,
			&run.SeriesSeen, &run.BooksSeen, &run.CurrentPath, &run.Error,
			&run.CancelRequested, &run.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (m *Manager) Run(ctx context.Context, id string) (database.ScanRun, error) {
	var run database.ScanRun
	err := m.store.DB().QueryRowContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,''),r.status,r.trigger_type,r.started_at,r.completed_at,
 r.directories_seen,r.files_seen,r.series_seen,r.books_seen,r.current_path,r.error,
 r.cancel_requested,r.created_at
FROM scan_runs r LEFT JOIN libraries l ON l.id=r.library_id WHERE r.id=?`, id).Scan(
		&run.ID, &run.LibraryID, &run.LibraryName, &run.Status, &run.TriggerType,
		&run.StartedAt, &run.CompletedAt, &run.DirectoriesSeen, &run.FilesSeen,
		&run.SeriesSeen, &run.BooksSeen, &run.CurrentPath, &run.Error,
		&run.CancelRequested, &run.CreatedAt,
	)
	return run, err
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
	ctx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	m.activeRunID = item.runID
	m.activeStop = cancel
	m.mu.Unlock()

	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.queued, item.library.ID)
		m.activeRunID = ""
		m.activeStop = nil
		m.mu.Unlock()
	}()

	var canceled bool
	if err := m.store.DB().QueryRowContext(ctx,
		`SELECT cancel_requested FROM scan_runs WHERE id=?`, item.runID).Scan(&canceled); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			m.finish(item, "failed", counters{}, err.Error())
		}
		return
	}
	if canceled {
		m.finish(item, "canceled", counters{}, "scan canceled before start")
		return
	}
	if !m.store.IsLibraryEnabled(ctx, item.library.ID) {
		m.finish(item, "canceled", counters{}, "library is hidden")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.DB().ExecContext(ctx, `
UPDATE scan_runs SET status='running',started_at=? WHERE id=?;
`, now, item.runID)
	_, _ = m.store.DB().ExecContext(ctx, `
UPDATE libraries SET last_scan_status='running',last_scan_started_at=?,last_scan_error='' WHERE id=?`,
		now, item.library.ID)

	startedAt := time.Now()
	provider := item.library.EffectiveProvider()
	m.logger.Info("scan started", "run", item.runID, "library", item.library.Name)
	diagnostics.Default().Record(diagnostics.Event{
		Level:         diagnostics.LevelInfo,
		Component:     "scanner",
		Stage:         "scan_start",
		Action:        "start",
		CorrelationID: item.runID,
		Meta:          map[string]any{"provider": provider},
	})
	conversion, err := m.needsModeConversion(ctx, item)
	if err != nil {
		diagnostics.Default().Record(diagnostics.Event{
			Level:         diagnostics.LevelError,
			Component:     "scanner",
			Stage:         "scan_done",
			Action:        "mode_check",
			Code:          diagnostics.ClassifyError(err),
			DurationMs:    time.Since(startedAt).Milliseconds(),
			CorrelationID: item.runID,
			Detail:        err.Error(),
			Meta:          map[string]any{"provider": provider},
		})
		m.finish(item, "failed", counters{}, err.Error())
		return
	}
	counts, err := m.scan(ctx, item, conversion)
	if errors.Is(err, context.Canceled) {
		m.discardStaging(item, conversion)
		diagnostics.Default().Record(diagnostics.Event{
			Level:         diagnostics.LevelWarn,
			Component:     "scanner",
			Stage:         "scan_done",
			Action:        "canceled",
			Code:          "CANCELED",
			DurationMs:    time.Since(startedAt).Milliseconds(),
			CorrelationID: item.runID,
			Meta:          map[string]any{"provider": provider, "directories": counts.directories, "files": counts.files},
		})
		m.finish(item, "canceled", counts, "scan canceled")
		return
	}
	if err != nil {
		m.discardStaging(item, conversion)
		m.logger.Error("scan failed", "run", item.runID, "library", item.library.Name, "error", err)
		diagnostics.Default().Record(diagnostics.Event{
			Level:         diagnostics.LevelError,
			Component:     "scanner",
			Stage:         "scan_done",
			Action:        "failed",
			Code:          diagnostics.ClassifyError(err),
			DurationMs:    time.Since(startedAt).Milliseconds(),
			CorrelationID: item.runID,
			Detail:        err.Error(),
			Meta:          map[string]any{"provider": provider, "directories": counts.directories, "files": counts.files, "series": counts.series, "books": counts.books},
		})
		m.finish(item, "failed", counts, err.Error())
		return
	}
	if err := m.commitSuccessfulScan(context.Background(), item, conversion); err != nil {
		m.discardStaging(item, conversion)
		diagnostics.Default().Record(diagnostics.Event{
			Level:         diagnostics.LevelError,
			Component:     "scanner",
			Stage:         "scan_done",
			Action:        "commit",
			Code:          diagnostics.ClassifyError(err),
			DurationMs:    time.Since(startedAt).Milliseconds(),
			CorrelationID: item.runID,
			Detail:        err.Error(),
			Meta:          map[string]any{"provider": provider},
		})
		m.finish(item, "failed", counts, err.Error())
		return
	}
	m.finish(item, "success", counts, "")
	diagnostics.Default().Record(diagnostics.Event{
		Level:         diagnostics.LevelInfo,
		Component:     "scanner",
		Stage:         "scan_done",
		Action:        "success",
		DurationMs:    time.Since(startedAt).Milliseconds(),
		CorrelationID: item.runID,
		Meta:          map[string]any{"provider": provider, "directories": counts.directories, "files": counts.files, "series": counts.series, "books": counts.books},
	})
	m.logger.Info("scan completed", "run", item.runID, "library", item.library.Name,
		"directories", counts.directories, "files", counts.files, "series", counts.series, "books", counts.books)
}

func (m *Manager) scan(ctx context.Context, item job, stage bool) (counters, error) {
	switch item.library.EffectiveProvider() {
	case database.ProviderLocal:
		return m.scanLocal(ctx, item, stage)
	case database.ProviderWebDAV:
		return m.scanWebDAV(ctx, item, stage)
	default:
		return m.scan115(ctx, item, stage)
	}
}

func (m *Manager) scan115(ctx context.Context, item job, stage bool) (counters, error) {
	rootInfo, err := m.client.FolderInfo(ctx, item.library.RootCID)
	if err != nil {
		return counters{}, fmt.Errorf("read root folder: %w", err)
	}
	rootName := rootInfo.Name
	if item.library.RootCID == "0" || strings.TrimSpace(rootName) == "" {
		rootName = item.library.Name
	}
	queue := []queueDir{{
		CID:          item.library.RootCID,
		Name:         rootName,
		RelativePath: rootName,
		ModifiedAt:   time.Now().UTC(),
	}}
	var counts counters
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return counts, err
		}
		current := queue[0]
		queue = queue[1:]
		files, err := m.client.ListDirectory(ctx, current.CID)
		if err != nil {
			return counts, fmt.Errorf("list %s: %w", current.RelativePath, err)
		}
		counts.directories++
		var comics []oneonefive.File
		for _, file := range files {
			if file.IsDir {
				queue = append(queue, queueDir{
					CID:          file.ID,
					Name:         file.Name,
					RelativePath: path.Join(current.RelativePath, file.Name),
					ModifiedAt:   file.ModifiedAt,
				})
				continue
			}
			counts.files++
			if !file.Incomplete && isComic(file.Name) {
				comics = append(comics, file)
			}
		}
		if err := m.persistDirComics(ctx, item, current, comics, stage, &counts); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

func (m *Manager) scanLocal(ctx context.Context, item job, stage bool) (counters, error) {
	rootClean, err := localfs.CleanRoot(item.library.RootPath)
	if err != nil {
		return counters{}, fmt.Errorf("local library root: %w", err)
	}
	rootResolved := rootClean
	if resolved, err := filepath.EvalSymlinks(rootClean); err == nil && resolved != "" {
		rootResolved = resolved
	}
	rootName := path.Base(strings.ReplaceAll(rootClean, "\\", "/"))
	if localfs.IsDocumentRoot(rootClean) || rootName == "" || rootName == "/" || rootName == "." {
		rootName = item.library.Name
	}
	visitedReal := map[string]bool{
		rootClean:    true,
		rootResolved: true,
	}
	queue := []queueDir{{
		CID:          ".",
		Name:         rootName,
		RelativePath: rootName,
		ModifiedAt:   time.Now().UTC(),
		RealKey:      rootResolved,
	}}
	var counts counters
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return counts, err
		}
		current := queue[0]
		queue = queue[1:]
		entries, err := localfs.ListDirectory(rootClean, current.CID)
		if err != nil {
			return counts, fmt.Errorf("list %s: %w", current.RelativePath, err)
		}
		if current.CID == "." && len(entries) == 0 {
			var existingBooks int64
			_ = m.store.DB().QueryRowContext(ctx, `SELECT count(*) FROM books WHERE library_id=?`, item.library.ID).Scan(&existingBooks)
			if existingBooks > 0 {
				return counts, fmt.Errorf("local library root appears empty (0 entries) while %d books exist in database; refusing to wipe catalog (check source availability or directory permission)", existingBooks)
			}
		}
		counts.directories++
		var comics []oneonefive.File
		for _, entry := range entries {
			if entry.IsDir {
				realKey := entry.RealDirKey
				if realKey == "" && current.RealKey != "" {
					realKey = filepath.Join(current.RealKey, entry.Name)
				}
				if realKey != "" {
					if visitedReal[realKey] {
						continue
					}
					visitedReal[realKey] = true
				}
				queue = append(queue, queueDir{
					CID:          entry.RelPath,
					Name:         entry.Name,
					RelativePath: path.Join(current.RelativePath, entry.Name),
					ModifiedAt:   entry.ModifiedAt,
					RealKey:      realKey,
				})
				continue
			}
			counts.files++
			if isComic(entry.Name) {
				comics = append(comics, oneonefive.File{
					ID:         entry.RelPath,
					ParentID:   current.CID,
					Name:       entry.Name,
					Size:       entry.Size,
					CreatedAt:  entry.ModifiedAt,
					ModifiedAt: entry.ModifiedAt,
				})
			}
		}
		if err := m.persistDirComics(ctx, item, current, comics, stage, &counts); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

func (m *Manager) scanWebDAV(ctx context.Context, item job, stage bool) (counters, error) {
	if m.webdav == nil {
		m.webdav = webdav.NewManager()
	}
	account, err := m.store.WebDAVAccountSecret(ctx, item.library.WebDAVAccountID)
	if err != nil {
		return counters{}, fmt.Errorf("load WebDAV account: %w", err)
	}
	rootRemote := webdav.CleanRemotePath(item.library.RootPath)
	rootName := path.Base(rootRemote)
	if rootName == "" || rootName == "/" || rootName == "." {
		rootName = item.library.Name
	}
	queue := []queueDir{{
		CID:          ".",
		Name:         rootName,
		RelativePath: rootName,
		ModifiedAt:   time.Now().UTC(),
	}}
	var counts counters
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return counts, err
		}
		current := queue[0]
		queue = queue[1:]
		targetRemote := webdav.JoinRemotePath(rootRemote, current.CID)
		entries, err := m.webdav.ListDirectory(ctx, account, targetRemote, rootRemote)
		if err != nil {
			return counts, fmt.Errorf("list %s: %w", current.RelativePath, err)
		}
		if current.CID == "." && len(entries) == 0 {
			var existingBooks int64
			_ = m.store.DB().QueryRowContext(ctx, `SELECT count(*) FROM books WHERE library_id=?`, item.library.ID).Scan(&existingBooks)
			if existingBooks > 0 {
				return counts, fmt.Errorf("WebDAV library root appears empty (0 entries) while %d books exist in database; refusing to wipe catalog (check WebDAV source availability)", existingBooks)
			}
		}
		counts.directories++
		var comics []oneonefive.File
		for _, entry := range entries {
			if entry.IsDir {
				queue = append(queue, queueDir{
					CID:          entry.RelPath,
					Name:         entry.Name,
					RelativePath: path.Join(current.RelativePath, entry.Name),
					ModifiedAt:   entry.ModifiedAt,
				})
				continue
			}
			counts.files++
			if isComic(entry.Name) {
				comics = append(comics, oneonefive.File{
					ID:         entry.RelPath,
					ParentID:   current.CID,
					Name:       entry.Name,
					Size:       entry.Size,
					SHA1:       entry.ETag,
					CreatedAt:  entry.ModifiedAt,
					ModifiedAt: entry.ModifiedAt,
				})
			}
		}
		if err := m.persistDirComics(ctx, item, current, comics, stage, &counts); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

func (m *Manager) persistDirComics(
	ctx context.Context,
	item job,
	current queueDir,
	comics []oneonefive.File,
	stage bool,
	counts *counters,
) error {
	if len(comics) > 0 {
		slices.SortFunc(comics, func(a, b oneonefive.File) int {
			if natsort.Less(a.Name, b.Name) {
				return -1
			}
			if natsort.Less(b.Name, a.Name) {
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
		if item.library.OneShot {
			if err := m.saveOneShots(ctx, item, current, comics, stage); err != nil {
				return err
			}
			counts.series += int64(len(comics))
			counts.books += int64(len(comics))
		} else {
			if err := m.saveSeries(ctx, item, current, comics, stage); err != nil {
				return err
			}
			counts.series++
			counts.books += int64(len(comics))
		}
	}
	return m.updateProgress(ctx, item.runID, current.RelativePath, *counts)
}

func (m *Manager) saveSeries(
	ctx context.Context,
	item job,
	dir queueDir,
	files []oneonefive.File,
	stage bool,
) error {
	return m.saveScannedSeries(ctx, item, scannedSeries{
		ID:           id.Series(item.library.ID, dir.CID),
		CID:          dir.CID,
		Name:         dir.Name,
		RelativePath: dir.RelativePath,
		ModifiedAt:   dir.ModifiedAt,
		OneShot:      false,
		ParentCID:    dir.CID,
		Files:        files,
	}, stage)
}

func (m *Manager) saveOneShot(
	ctx context.Context,
	item job,
	dir queueDir,
	file oneonefive.File,
	stage bool,
) error {
	return m.saveScannedSeries(ctx, item, oneShotSeries(item, dir, file), stage)
}

func (m *Manager) saveOneShots(
	ctx context.Context,
	item job,
	dir queueDir,
	files []oneonefive.File,
	stage bool,
) error {
	tx, err := m.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, file := range files {
		if err := m.saveScannedSeriesTx(ctx, tx, item, oneShotSeries(item, dir, file), stage); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func oneShotSeries(item job, dir queueDir, file oneonefive.File) scannedSeries {
	return scannedSeries{
		ID:           id.OneShotSeries(item.library.ID, file.ID),
		CID:          id.OneShotCID(file.ID),
		Name:         strings.TrimSuffix(file.Name, path.Ext(file.Name)),
		RelativePath: path.Join(dir.RelativePath, file.Name),
		ModifiedAt:   file.ModifiedAt,
		OneShot:      true,
		ParentCID:    dir.CID,
		Files:        []oneonefive.File{file},
	}
}

type scannedSeries struct {
	ID           string
	CID          string
	Name         string
	RelativePath string
	ModifiedAt   time.Time
	OneShot      bool
	ParentCID    string
	Files        []oneonefive.File
}

func (m *Manager) saveScannedSeries(
	ctx context.Context,
	item job,
	series scannedSeries,
	stage bool,
) error {
	tx, err := m.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := m.saveScannedSeriesTx(ctx, tx, item, series, stage); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) saveScannedSeriesTx(
	ctx context.Context,
	tx *sql.Tx,
	item job,
	series scannedSeries,
	stage bool,
) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	modified := series.ModifiedAt.UTC().Format(time.RFC3339Nano)
	var err error
	if stage {
		_, err = tx.ExecContext(ctx, `
INSERT INTO scan_series_staging(
 run_id,id,library_id,cid,name,relative_path,one_shot,file_modified_at,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(run_id,library_id,cid) DO UPDATE SET
 name=excluded.name,relative_path=excluded.relative_path,
 one_shot=excluded.one_shot,file_modified_at=excluded.file_modified_at,
 updated_at=excluded.updated_at`,
			item.runID, series.ID, item.library.ID, series.CID, series.Name, series.RelativePath,
			series.OneShot, modified, now, now)
	} else {
		_, err = tx.ExecContext(ctx, `
INSERT INTO series(
 id,library_id,cid,name,relative_path,one_shot,file_modified_at,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(library_id,cid) DO UPDATE SET
 name=excluded.name,relative_path=excluded.relative_path,
 one_shot=excluded.one_shot,file_modified_at=excluded.file_modified_at,
 updated_at=excluded.updated_at,seen_scan_id=excluded.seen_scan_id`,
			series.ID, item.library.ID, series.CID, series.Name, series.RelativePath,
			series.OneShot, modified, now, now, item.runID)
	}
	if err != nil {
		return err
	}
	for index, file := range series.Files {
		bookID := id.Book(item.library.ID, file.ID)
		if stage {
			_, err = tx.ExecContext(ctx, `
INSERT INTO scan_books_staging(
 run_id,id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(run_id,library_id,file_id) DO UPDATE SET
 series_id=excluded.series_id,parent_cid=excluded.parent_cid,
 name=excluded.name,size=excluded.size,pick_code=excluded.pick_code,sha1=excluded.sha1,
 file_created_at=excluded.file_created_at,file_modified_at=excluded.file_modified_at,
 number_sort=excluded.number_sort,updated_at=excluded.updated_at`,
				item.runID, bookID, series.ID, item.library.ID, file.ID, series.ParentCID,
				file.Name, file.Size, file.PickCode, file.SHA1,
				file.CreatedAt.UTC().Format(time.RFC3339Nano), file.ModifiedAt.UTC().Format(time.RFC3339Nano),
				float64(index+1), now, now)
		} else {
			_, err = tx.ExecContext(ctx, `
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(library_id,file_id) DO UPDATE SET
 series_id=excluded.series_id,library_id=excluded.library_id,parent_cid=excluded.parent_cid,
 name=excluded.name,size=excluded.size,pick_code=excluded.pick_code,sha1=excluded.sha1,
 file_created_at=excluded.file_created_at,file_modified_at=excluded.file_modified_at,
 number_sort=excluded.number_sort,updated_at=excluded.updated_at,seen_scan_id=excluded.seen_scan_id`,
				bookID, series.ID, item.library.ID, file.ID, series.ParentCID,
				file.Name, file.Size, file.PickCode, file.SHA1,
				file.CreatedAt.UTC().Format(time.RFC3339Nano), file.ModifiedAt.UTC().Format(time.RFC3339Nano),
				float64(index+1), now, now, item.runID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) updateProgress(ctx context.Context, runID, currentPath string, counts counters) error {
	result, err := m.store.DB().ExecContext(ctx, `
UPDATE scan_runs SET directories_seen=?,files_seen=?,series_seen=?,books_seen=?,current_path=?
WHERE id=? AND cancel_requested=0`,
		counts.directories, counts.files, counts.series, counts.books, currentPath, runID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return context.Canceled
	}
	return nil
}

func (m *Manager) needsModeConversion(ctx context.Context, item job) (bool, error) {
	var opposite int
	err := m.store.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM series WHERE library_id=? AND one_shot<>?`,
		item.library.ID, item.library.OneShot,
	).Scan(&opposite)
	return opposite > 0, err
}

func (m *Manager) discardStaging(item job, staged bool) {
	if !staged {
		return
	}
	_, _ = m.store.DB().ExecContext(context.Background(),
		`DELETE FROM scan_books_staging WHERE run_id=?`, item.runID)
	_, _ = m.store.DB().ExecContext(context.Background(),
		`DELETE FROM scan_series_staging WHERE run_id=?`, item.runID)
}

func (m *Manager) commitSuccessfulScan(ctx context.Context, item job, staged bool) error {
	tx, err := m.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if staged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE library_id=?`, item.library.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO series(
 id,library_id,cid,name,relative_path,one_shot,file_modified_at,created_at,updated_at,seen_scan_id
)
SELECT id,library_id,cid,name,relative_path,one_shot,file_modified_at,created_at,updated_at,?
FROM scan_series_staging WHERE run_id=?`, item.runID, item.runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
)
SELECT id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,?
FROM scan_books_staging WHERE run_id=?`, item.runID, item.runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM scan_books_staging WHERE run_id=?`, item.runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM scan_series_staging WHERE run_id=?`, item.runID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM books WHERE library_id=? AND seen_scan_id<>?`, item.library.ID, item.runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM series WHERE library_id=? AND seen_scan_id<>?`, item.library.ID, item.runID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) finish(item job, status string, counts counters, message string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.DB().ExecContext(context.Background(), `
UPDATE scan_runs SET status=?,completed_at=?,directories_seen=?,files_seen=?,series_seen=?,
 books_seen=?,error=? WHERE id=?`,
		status, now, counts.directories, counts.files, counts.series, counts.books, message, item.runID)
	_, _ = m.store.DB().ExecContext(context.Background(), `
UPDATE libraries SET last_scan_status=?,last_scan_completed_at=?,last_scan_error=? WHERE id=?`,
		status, now, message, item.library.ID)
	var trigger string
	_ = m.store.DB().QueryRowContext(context.Background(), `SELECT trigger_type FROM scan_runs WHERE id=?`, item.runID).Scan(&trigger)
	m.notifyCompleted(item.library.ID, item.runID, trigger, status)
}

func isComic(name string) bool {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".cbz") || strings.HasSuffix(name, ".zip") {
		return true
	}
	if !strings.HasSuffix(name, ".cbr") && !strings.HasSuffix(name, ".rar") {
		return false
	}
	return !isSecondaryRARVolume(name)
}

func isSecondaryRARVolume(name string) bool {
	extension := path.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	index := strings.LastIndex(stem, ".part")
	if index < 0 {
		return false
	}
	number := strings.TrimPrefix(stem[index:], ".part")
	if number == "" {
		return false
	}
	volume, err := strconv.Atoi(number)
	return err == nil && volume > 1
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
