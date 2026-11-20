package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
)

var (
	ErrAlreadyQueued = errors.New("a pipeline job is already queued or running for this library")
	ErrLibraryHidden = errors.New("library is disabled")
)

type pipelineJob struct {
	runID     string
	libraryID string
}

type Manager struct {
	store   *database.Store
	scanner *scanner.Manager
	bangumi *bangumi.Service
	covers  *thumbnail.BatchManager
	logger  *slog.Logger

	nowFunc      func() time.Time
	pollInterval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan pipelineJob

	mu                sync.Mutex
	queued            map[string]string
	activeRunID       string
	activeStop        context.CancelFunc
	activeChildCancel func()
}

func New(
	store *database.Store,
	scanMgr *scanner.Manager,
	bgmSvc *bangumi.Service,
	coverMgr *thumbnail.BatchManager,
	logger *slog.Logger,
) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = store.DB().ExecContext(ctx, `
UPDATE pipeline_runs SET status='failed',completed_at=?,
 error='service restarted before pipeline completed'
WHERE status IN ('queued','running')`, now)

	m := &Manager{
		store:        store,
		scanner:      scanMgr,
		bangumi:      bgmSvc,
		covers:       coverMgr,
		logger:       logger,
		nowFunc:      time.Now,
		pollInterval: 250 * time.Millisecond,
		ctx:          ctx,
		cancel:       cancel,
		jobs:         make(chan pipelineJob, 64),
		queued:       make(map[string]string),
	}
	go m.worker()
	go m.scheduler()
	return m
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	stop := m.activeStop
	childCancel := m.activeChildCancel
	m.mu.Unlock()
	if childCancel != nil {
		childCancel()
	}
	if stop != nil {
		stop()
	}
}

func (m *Manager) Start(ctx context.Context, libraryID, trigger string) (database.PipelineRun, error) {
	lib, err := m.store.LibraryBasic(ctx, libraryID)
	if err != nil {
		return database.PipelineRun{}, err
	}
	if !lib.Enabled {
		return database.PipelineRun{}, ErrLibraryHidden
	}
	m.mu.Lock()
	if _, exists := m.queued[libraryID]; exists {
		m.mu.Unlock()
		return database.PipelineRun{}, ErrAlreadyQueued
	}
	runID := randomPipelineID()
	m.queued[libraryID] = runID
	m.mu.Unlock()

	if strings.TrimSpace(trigger) == "" {
		trigger = "manual"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := m.store.DB().ExecContext(ctx, `
INSERT INTO pipeline_runs(id,library_id,trigger_type,status,stage,created_at)
VALUES(?,?,?,'queued','queued',?)`, runID, libraryID, trigger, now); err != nil {
		m.releaseQueued(libraryID)
		return database.PipelineRun{}, err
	}
	run, err := m.store.PipelineRunByID(ctx, runID)
	if err != nil {
		m.releaseQueued(libraryID)
		return database.PipelineRun{}, err
	}
	select {
	case m.jobs <- pipelineJob{runID: runID, libraryID: libraryID}:
		return run, nil
	case <-m.ctx.Done():
		m.releaseQueued(libraryID)
		return database.PipelineRun{}, context.Canceled
	}
}

func (m *Manager) Cancel(ctx context.Context, runID string) error {
	res, err := m.store.DB().ExecContext(ctx, `
UPDATE pipeline_runs SET cancel_requested=1
WHERE id=? AND status IN ('queued','running')`, strings.TrimSpace(runID))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	m.mu.Lock()
	var stop context.CancelFunc
	var childCancel func()
	if m.activeRunID == runID {
		stop = m.activeStop
		childCancel = m.activeChildCancel
	}
	m.mu.Unlock()
	if childCancel != nil {
		childCancel()
	}
	if stop != nil {
		stop()
	}
	return nil
}

func (m *Manager) CancelLibrary(ctx context.Context, libraryID string) {
	rows, err := m.store.DB().QueryContext(ctx,
		`SELECT id FROM pipeline_runs WHERE library_id=? AND status IN ('queued','running')`, libraryID)
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

func (m *Manager) Runs(ctx context.Context, limit int) ([]database.PipelineRun, error) {
	return m.store.PipelineRuns(ctx, limit)
}

func (m *Manager) Run(ctx context.Context, runID string) (database.PipelineRun, error) {
	return m.store.PipelineRunByID(ctx, runID)
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

func (m *Manager) scheduler() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.CheckSchedulesOnce(m.ctx)
		}
	}
}

// ScheduleSlotDue evaluates whether a library schedule should fire at `now` (in server local time).
func ScheduleSlotDue(sched database.LibrarySchedule, now time.Time) (string, bool) {
	if !sched.Enabled {
		return "", false
	}
	local := now.Local()
	parts := strings.Split(strings.TrimSpace(sched.TimeOfDay), ":")
	if len(parts) != 2 {
		return "", false
	}
	hour, err1 := strconv.Atoi(parts[0])
	minute, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return "", false
	}
	freq := strings.ToLower(strings.TrimSpace(sched.Frequency))
	if freq == "weekly" && int(local.Weekday()) != sched.Weekday {
		return "", false
	}
	target := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, local.Location())
	delta := local.Sub(target)
	if delta < 0 || delta >= 10*time.Minute {
		return "", false
	}
	var slotKey string
	if freq == "weekly" {
		slotKey = fmt.Sprintf("%s-w%d@%02d:%02d", local.Format("2006-01-02"), sched.Weekday, hour, minute)
	} else {
		slotKey = fmt.Sprintf("%s@%02d:%02d", local.Format("2006-01-02"), hour, minute)
	}
	if sched.LastSlotKey == slotKey {
		return "", false
	}
	return slotKey, true
}

func (m *Manager) CheckSchedulesOnce(ctx context.Context) {
	schedules, err := m.store.LibrarySchedules(ctx)
	if err != nil {
		if m.logger != nil {
			m.logger.Error("load library schedules", "error", err)
		}
		return
	}
	now := m.nowFunc()
	for _, sched := range schedules {
		slotKey, due := ScheduleSlotDue(sched, now)
		if !due {
			continue
		}
		if !m.store.IsLibraryEnabled(ctx, sched.LibraryID) {
			continue
		}
		if _, err := m.Start(ctx, sched.LibraryID, "scheduled"); err != nil {
			if !errors.Is(err, ErrAlreadyQueued) && m.logger != nil {
				m.logger.Error("trigger scheduled pipeline", "library", sched.LibraryID, "error", err)
			}
			continue
		}
		_ = m.store.MarkScheduleSlotTriggered(ctx, sched.LibraryID, slotKey)
	}
}

func (m *Manager) setChildCancel(fn func()) {
	m.mu.Lock()
	m.activeChildCancel = fn
	m.mu.Unlock()
}

func (m *Manager) execute(item pipelineJob) {
	ctx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	m.activeRunID = item.runID
	m.activeStop = cancel
	m.activeChildCancel = nil
	m.mu.Unlock()

	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.queued, item.libraryID)
		m.activeRunID = ""
		m.activeStop = nil
		m.activeChildCancel = nil
		m.mu.Unlock()
	}()

	run, err := m.store.PipelineRunByID(ctx, item.runID)
	if err != nil {
		return
	}
	if run.CancelRequested {
		m.finish(item.runID, "canceled", "queued", run.ScanRunIDs, "", "", "任务启动前已取消", "pipeline canceled")
		return
	}
	lib, err := m.store.LibraryBasic(ctx, item.libraryID)
	if err != nil || !lib.Enabled {
		msg := "library is disabled"
		if err != nil {
			msg = err.Error()
		}
		m.finish(item.runID, "canceled", "queued", run.ScanRunIDs, "", "", "", msg)
		return
	}
	sched, err := m.store.LibrarySchedule(ctx, item.libraryID)
	if err != nil {
		sched = database.DefaultLibrarySchedule(item.libraryID)
	}

	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.DB().ExecContext(ctx,
		`UPDATE pipeline_runs SET status='running',stage='scan',started_at=? WHERE id=?`,
		startedAt, item.runID)

	var (
		scanRunIDs   []string
		bgmRunID     string
		coverRunID   string
		summaryParts []string
		hasPartial   bool
	)

	// Stage 1: Scan (physical library or selected sub-libraries of a union library)
	if lib.EffectiveProvider() == database.ProviderUnion {
		selected := sched.ScanMemberLibraryIDs
		validMember := map[string]bool{}
		for _, layer := range lib.UnionLayers {
			validMember[layer.MemberLibraryID] = true
		}
		scannedMembers := 0
		for _, memberID := range selected {
			if !validMember[memberID] || !m.store.IsLibraryEnabled(ctx, memberID) {
				continue
			}
			m.updateProgress(item.runID, "scan", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"))
			sRun, sErr := m.runAndWaitScan(ctx, memberID)
			if sRun.ID != "" {
				scanRunIDs = append(scanRunIDs, sRun.ID)
			}
			if sErr != nil {
				if errors.Is(sErr, context.Canceled) || ctx.Err() != nil {
					m.finish(item.runID, "canceled", "scan", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "pipeline canceled during scan")
					return
				}
				m.finish(item.runID, "failed", "scan", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), sErr.Error())
				return
			}
			scannedMembers++
		}
		if scannedMembers > 0 {
			summaryParts = append(summaryParts, fmt.Sprintf("扫描 %d 个子库完成", scannedMembers))
		} else {
			summaryParts = append(summaryParts, "未勾选扫子库，直接刷新联合库")
		}

		// Stage 2 (union): Refresh materialized union library
		m.updateProgress(item.runID, "union_refresh", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"))
		uStats, uErr := m.store.RefreshUnionLibrary(ctx, lib.ID)
		if uErr != nil {
			status := "failed"
			if errors.Is(uErr, context.Canceled) || ctx.Err() != nil {
				status = "canceled"
			}
			m.finish(item.runID, status, "union_refresh", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), uErr.Error())
			return
		}
		summaryParts = append(summaryParts, fmt.Sprintf("联合库合并 %d 部/%d 册", uStats.SeriesCount, uStats.BooksCount))
	} else {
		// Physical library scan
		sRun, sErr := m.runAndWaitScan(ctx, lib.ID)
		if sRun.ID != "" {
			scanRunIDs = append(scanRunIDs, sRun.ID)
		}
		if sErr != nil {
			if errors.Is(sErr, context.Canceled) || ctx.Err() != nil {
				m.finish(item.runID, "canceled", "scan", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "pipeline canceled during scan")
				return
			}
			m.finish(item.runID, "failed", "scan", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), sErr.Error())
			return
		}
		summaryParts = append(summaryParts, fmt.Sprintf("扫库完成（%d 部/%d 册）", sRun.SeriesSeen, sRun.BooksSeen))

		// If this physical library is a union sub-library, refresh parent union and finish (no duplicate scrape/cover on sub-lib).
		if parentID, isSub, _ := m.store.UnionParentLibraryID(ctx, lib.ID); isSub && parentID != "" {
			m.updateProgress(item.runID, "union_refresh", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"))
			uStats, uErr := m.store.RefreshUnionLibrary(ctx, parentID)
			if uErr != nil {
				m.finish(item.runID, "failed", "union_refresh", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), uErr.Error())
				return
			}
			summaryParts = append(summaryParts, fmt.Sprintf("已刷新所属联合库（%d 部/%d 册）", uStats.SeriesCount, uStats.BooksCount))
			m.finish(item.runID, "success", "done", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "")
			return
		}
	}

	if ctx.Err() != nil {
		m.finish(item.runID, "canceled", "scan", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "pipeline canceled")
		return
	}

	// Stage 3 & 4: Bangumi scrape + missing-unbound cover generation
	// Per spec: if Bangumi offline archive is NOT ready, skip BOTH Bangumi scrape AND auto cover generation.
	bgmOfflineReady := m.bangumi != nil && m.bangumi.ArchiveStatus().Ready
	if !bgmOfflineReady {
		summaryParts = append(summaryParts, "未就绪 Bangumi 离线库，已跳过刮削与补封面")
		m.finish(item.runID, "success", "done", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "")
		return
	}

	if sched.AutoBangumi {
		m.updateProgress(item.runID, "bangumi", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"))
		bRun, bErr := m.runAndWaitBangumi(ctx, lib.ID)
		bgmRunID = bRun.ID
		if bErr != nil {
			if errors.Is(bErr, context.Canceled) || ctx.Err() != nil {
				m.finish(item.runID, "canceled", "bangumi", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "pipeline canceled during Bangumi scrape")
				return
			}
			hasPartial = true
			summaryParts = append(summaryParts, "Bangumi 刮削异常: "+bErr.Error())
		} else {
			if bRun.Status == "partial" {
				hasPartial = true
			}
			summaryParts = append(summaryParts, fmt.Sprintf("Bangumi 命中 %d / 未命中 %d", bRun.MatchedCount, bRun.UnmatchedCount))
		}
	}

	if ctx.Err() != nil {
		m.finish(item.runID, "canceled", "bangumi", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "pipeline canceled")
		return
	}

	if sched.AutoCoverLimit != 0 && m.covers != nil {
		m.updateProgress(item.runID, "cover", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"))
		all := sched.AutoCoverLimit < 0
		limit := sched.AutoCoverLimit
		if all {
			limit = 0
		}
		cRun, cErr := m.runAndWaitCover(ctx, lib.ID, all, limit)
		coverRunID = cRun.ID
		if cErr != nil {
			if errors.Is(cErr, context.Canceled) || ctx.Err() != nil {
				m.finish(item.runID, "canceled", "cover", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "pipeline canceled during cover generation")
				return
			}
			hasPartial = true
			summaryParts = append(summaryParts, "补封面异常: "+cErr.Error())
		} else {
			if cRun.Status == "partial" {
				hasPartial = true
			}
			summaryParts = append(summaryParts, fmt.Sprintf("补封面生成 %d / 跳过 %d", cRun.GeneratedCount, cRun.SkippedCount))
		}
	}

	finalStatus := "success"
	if hasPartial {
		finalStatus = "partial"
	}
	m.finish(item.runID, finalStatus, "done", scanRunIDs, bgmRunID, coverRunID, strings.Join(summaryParts, "；"), "")
}

func (m *Manager) runAndWaitScan(ctx context.Context, libraryID string) (database.ScanRun, error) {
	if m.scanner == nil {
		return database.ScanRun{}, errors.New("scanner service unavailable")
	}
	run, err := m.scanner.StartLibrary(ctx, libraryID, "pipeline")
	if err != nil {
		return database.ScanRun{}, err
	}
	m.setChildCancel(func() {
		_ = m.scanner.Cancel(context.Background(), run.ID)
	})
	defer m.setChildCancel(nil)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		cur, err := m.scanner.Run(context.Background(), run.ID)
		if err != nil {
			return run, err
		}
		switch cur.Status {
		case "success":
			return cur, nil
		case "canceled":
			return cur, context.Canceled
		case "failed":
			msg := cur.Error
			if msg == "" {
				msg = "library scan failed"
			}
			return cur, errors.New(msg)
		}
		select {
		case <-ctx.Done():
			_ = m.scanner.Cancel(context.Background(), run.ID)
			return cur, context.Canceled
		case <-ticker.C:
		}
	}
}

func (m *Manager) runAndWaitBangumi(ctx context.Context, libraryID string) (database.BangumiScrapeRun, error) {
	run, err := m.bangumi.StartBatchWithMode(ctx, libraryID, bangumi.ScrapeModeIncremental)
	if err != nil {
		return database.BangumiScrapeRun{}, err
	}
	m.setChildCancel(func() {
		_ = m.bangumi.CancelBatch(context.Background(), run.ID)
	})
	defer m.setChildCancel(nil)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		cur, err := m.bangumi.Run(context.Background(), run.ID)
		if err != nil {
			return run, err
		}
		switch cur.Status {
		case "success", "partial":
			return cur, nil
		case "canceled":
			return cur, context.Canceled
		case "failed":
			msg := "Bangumi scrape failed"
			if len(cur.Errors) > 0 {
				msg = cur.Errors[0]
			}
			return cur, errors.New(msg)
		}
		select {
		case <-ctx.Done():
			_ = m.bangumi.CancelBatch(context.Background(), run.ID)
			return cur, context.Canceled
		case <-ticker.C:
		}
	}
}

func (m *Manager) runAndWaitCover(ctx context.Context, libraryID string, all bool, limit int) (thumbnail.BatchRun, error) {
	run, err := m.covers.StartMissingUnbound(ctx, libraryID, all, limit)
	if err != nil {
		return thumbnail.BatchRun{}, err
	}
	m.setChildCancel(func() {
		_ = m.covers.Cancel(context.Background(), run.ID)
	})
	defer m.setChildCancel(nil)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		cur, err := m.covers.Run(context.Background(), run.ID)
		if err != nil {
			return run, err
		}
		switch cur.Status {
		case "success", "partial":
			return cur, nil
		case "canceled":
			return cur, context.Canceled
		case "failed":
			msg := "cover batch job failed"
			if len(cur.Errors) > 0 {
				msg = cur.Errors[0]
			}
			return cur, errors.New(msg)
		}
		select {
		case <-ctx.Done():
			_ = m.covers.Cancel(context.Background(), run.ID)
			return cur, context.Canceled
		case <-ticker.C:
		}
	}
}

func (m *Manager) updateProgress(runID, stage string, scanRunIDs []string, bgmRunID, coverRunID, summary string) {
	if scanRunIDs == nil {
		scanRunIDs = []string{}
	}
	scanJSON, _ := json.Marshal(scanRunIDs)
	_, _ = m.store.DB().ExecContext(context.Background(), `
UPDATE pipeline_runs SET stage=?,scan_run_ids_json=?,bangumi_run_id=?,cover_run_id=?,summary=?
WHERE id=?`, stage, string(scanJSON), bgmRunID, coverRunID, summary, runID)
}

func (m *Manager) finish(runID, status, stage string, scanRunIDs []string, bgmRunID, coverRunID, summary, errMsg string) {
	if scanRunIDs == nil {
		scanRunIDs = []string{}
	}
	scanJSON, _ := json.Marshal(scanRunIDs)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.DB().ExecContext(context.Background(), `
UPDATE pipeline_runs
SET status=?,stage=?,scan_run_ids_json=?,bangumi_run_id=?,cover_run_id=?,summary=?,error=?,completed_at=?
WHERE id=?`, status, stage, string(scanJSON), bgmRunID, coverRunID, summary, errMsg, now, runID)
	if m.logger != nil {
		m.logger.Info("pipeline completed", "run", runID, "status", status, "stage", stage, "summary", summary, "error", errMsg)
	}
}

func (m *Manager) releaseQueued(libraryID string) {
	m.mu.Lock()
	delete(m.queued, libraryID)
	m.mu.Unlock()
}

func randomPipelineID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("pl_%d", time.Now().UnixNano())
	}
	return "pl_" + hex.EncodeToString(value[:])
}
