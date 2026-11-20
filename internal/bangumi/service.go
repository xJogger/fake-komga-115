package bangumi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

var (
	ErrScrapeAlreadyQueued = errors.New("a Bangumi scrape job is already queued or running for this target")
	ErrLibraryHidden       = errors.New("library is hidden")
)

const (
	maxScrapeStoredErrors = 20

	ScrapeModeIncremental    = "incremental"
	ScrapeModeRetryUnmatched = "retry_unmatched"
	ScrapeModeForce          = "force"
)

func NormalizeScrapeMode(raw string, legacyForce bool) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ScrapeModeForce:
		return ScrapeModeForce
	case ScrapeModeRetryUnmatched, "retry", "unmatched":
		return ScrapeModeRetryUnmatched
	case ScrapeModeIncremental:
		return ScrapeModeIncremental
	default:
		if legacyForce {
			return ScrapeModeForce
		}
		return ScrapeModeIncremental
	}
}

type Service struct {
	store   *database.Store
	archive *ArchiveIndex
	live    *LiveClient
	logger  *slog.Logger

	ctx      context.Context
	cancel   context.CancelFunc
	jobs     chan scrapeJob
	rankDone chan struct{}

	mu          sync.Mutex
	queued      map[string]string
	activeRunID string
	activeStop  context.CancelFunc
}

type scrapeJob struct {
	runID     string
	libraryID string // empty means all enabled libraries
	mode      string
}

func NewService(store *database.Store, dataDir string, logger *slog.Logger) (*Service, error) {
	archiveIdx, err := NewArchiveIndex(dataDir, logger)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = store.DB().ExecContext(ctx, `
UPDATE bangumi_scrape_runs SET status='failed',completed_at=?,
 errors_json='["service restarted before scrape job completed"]'
WHERE status IN ('queued','running')`, now)

	liveClient := NewLiveClient()
	if savedProxy, pErr := store.GetSetting(ctx, SettingProxyURL); pErr == nil && strings.TrimSpace(savedProxy) != "" {
		if _, normalized, vErr := ValidateProxyURL(savedProxy); vErr == nil {
			_ = archiveIdx.SetProxyURL(normalized)
			_ = liveClient.SetProxyURL(normalized)
		}
	}

	svc := &Service{
		store:    store,
		archive:  archiveIdx,
		live:     liveClient,
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
		jobs:     make(chan scrapeJob, 32),
		queued:   make(map[string]string),
		rankDone: make(chan struct{}),
	}
	ranksPending := make(chan struct{}, 1)
	archiveIdx.onImport = func() {
		select {
		case ranksPending <- struct{}{}:
		default:
		}
	}
	go svc.rankWorker(ranksPending)
	archiveIdx.onImport()
	go svc.worker()
	return svc, nil
}

func (s *Service) Close() error {
	s.cancel()
	s.mu.Lock()
	if s.activeStop != nil {
		s.activeStop()
	}
	s.mu.Unlock()
	if s.rankDone != nil {
		<-s.rankDone
	}
	return s.archive.Close()
}

func (s *Service) ArchiveStatus() ArchiveStatus {
	return s.archive.Status()
}

func (s *Service) StartArchiveUpdate() (ArchiveStatus, error) {
	return s.archive.StartOnlineUpdate()
}

func (s *Service) CancelArchiveUpdate() bool {
	return s.archive.CancelUpdate()
}

func (s *Service) ImportArchiveZip(ctx context.Context, zipPath, dumpName, createdAt string) error {
	return s.archive.ImportFromZipFile(ctx, zipPath, dumpName, createdAt)
}

func (s *Service) FetchCoverBytes(ctx context.Context, coverURL string, subjectID int64) ([]byte, error) {
	return s.live.FetchCoverBytes(ctx, coverURL, subjectID)
}

func (s *Service) RedactedProxyURL(ctx context.Context) string {
	val, err := s.store.GetSetting(ctx, SettingProxyURL)
	if err != nil {
		return ""
	}
	return RedactProxyURL(val)
}

func (s *Service) SetProxyURL(ctx context.Context, inputRaw string) (string, error) {
	existing, _ := s.store.GetSetting(ctx, SettingProxyURL)
	merged, err := MergeProxyURLUpdate(inputRaw, existing)
	if err != nil {
		return "", err
	}
	if err := s.archive.SetProxyURL(merged); err != nil {
		return "", err
	}
	if err := s.live.SetProxyURL(merged); err != nil {
		return "", err
	}
	if err := s.store.SetSetting(ctx, SettingProxyURL, merged); err != nil {
		return "", err
	}
	return RedactProxyURL(merged), nil
}

func (s *Service) TestConnectivity(ctx context.Context, candidateRaw *string) (ConnectivityReport, error) {
	existing, _ := s.store.GetSetting(ctx, SettingProxyURL)
	targetProxy := existing
	if candidateRaw != nil {
		merged, err := MergeProxyURLUpdate(*candidateRaw, existing)
		if err != nil {
			return ConnectivityReport{}, err
		}
		targetProxy = merged
	}
	return ProbeConnectivity(ctx, targetProxy, s.live.baseURL, s.archive.metaURL)
}

// SearchCandidates searches Bangumi for manual matching in the UI.
// Supports both keyword search and direct Subject ID / bgm.tv URL input.
func (s *Service) SearchCandidates(
	ctx context.Context,
	series database.Series,
	queryText string,
) ([]SubjectCandidate, ParsedQuery, error) {
	queryText = strings.TrimSpace(queryText)
	useSeriesName := queryText == "" || queryText == series.Name
	var parsed ParsedQuery
	if useSeriesName {
		queryText = series.Name
		parsed = ParseSeriesWithPath(series.Name, series.RelativePath)
	} else {
		parsed = ParseSeriesName(queryText)
	}

	if id, ok := ParseSubjectIDInput(queryText); ok {
		cand, err := s.live.SubjectByID(ctx, id)
		if err == nil {
			return []SubjectCandidate{cand}, parsed, nil
		}
		if localCand, ok, lErr := s.archive.SubjectByID(ctx, id); lErr == nil && ok {
			return []SubjectCandidate{localCand}, parsed, nil
		}
	}

	searchKeyword := queryText
	terms := parsed.lookupTitles()
	useLookupTerms := useSeriesName || len(parsed.compoundTitles) > 0
	if useLookupTerms && len(terms) > 0 {
		searchKeyword = terms[0]
	}

	seen := map[int64]bool{}
	var merged []SubjectCandidate
	if liveResults, err := s.live.Search(ctx, searchKeyword, parsed, series.OneShot, 15); err == nil {
		for _, item := range liveResults {
			if !seen[item.ID] {
				seen[item.ID] = true
				merged = append(merged, item)
			}
		}
	}
	if useLookupTerms && len(terms) > 1 && len(merged) < 5 {
		if liveAlt, err := s.live.Search(ctx, terms[1], parsed, series.OneShot, 10); err == nil {
			for _, item := range liveAlt {
				if !seen[item.ID] {
					seen[item.ID] = true
					merged = append(merged, item)
				}
			}
		}
	}
	if s.archive.Ready() {
		if localResults, err := s.archive.Search(ctx, parsed, series.OneShot, 15); err == nil {
			for _, item := range localResults {
				if !seen[item.ID] {
					seen[item.ID] = true
					merged = append(merged, item)
				}
			}
		}
	}
	return merged, parsed, nil
}

// BindSubject binds a specific Bangumi Subject ID to a Series (preferring live API details, falling back to local archive).
func (s *Service) BindSubject(
	ctx context.Context,
	series database.Series,
	subjectID int64,
) (database.SeriesMetadata, error) {
	cand, err := s.live.SubjectByID(ctx, subjectID)
	if err != nil {
		localCand, ok, localErr := s.archive.SubjectByID(ctx, subjectID)
		if localErr != nil || !ok {
			return database.SeriesMetadata{}, fmt.Errorf("fetch Bangumi subject %d: %w", subjectID, err)
		}
		cand = localCand
	}
	cand.Confidence = 1.0
	parsed := ParseSeriesWithPath(series.Name, series.RelativePath)
	scraped := ToScrapedMetadataWithQuery(series.Name, parsed, cand)
	return s.store.ApplyScrapedMetadata(ctx, series, scraped)
}

// AutoScrapeOne runs automatic title parsing & matching on a single Series.
func (s *Service) AutoScrapeOne(
	ctx context.Context,
	series database.Series,
) (matched bool, meta database.SeriesMetadata, err error) {
	parsed := ParseSeriesWithPath(series.Name, series.RelativePath)
	terms := parsed.lookupTitles()
	var candidates []SubjectCandidate
	if s.archive.Ready() {
		candidates, err = s.archive.Search(ctx, parsed, series.OneShot, 10)
		if err != nil {
			return false, database.SeriesMetadata{}, err
		}
	} else if len(terms) > 0 {
		maxTries := min(2, len(terms))
		for i := 0; i < maxTries; i++ {
			res, sErr := s.live.Search(ctx, terms[i], parsed, series.OneShot, 10)
			if sErr != nil {
				if i == 0 {
					return false, database.SeriesMetadata{}, sErr
				}
				break
			}
			if len(res) > 0 && (len(candidates) == 0 || res[0].Confidence > candidates[0].Confidence) {
				candidates = res
			}
			if len(candidates) > 0 && candidates[0].Confidence >= AutoMatchMinConfidence {
				break
			}
		}
	}
	if len(candidates) == 0 || candidates[0].Confidence < AutoMatchMinConfidence {
		bestConf := 0.0
		if len(candidates) > 0 {
			bestConf = candidates[0].Confidence
		}
		inferredNSFW := InferPathNSFW(series.Name, series.RelativePath)
		if mErr := s.store.MarkSeriesScrapeStatusWithNSFW(ctx, series, database.ScrapeStatusUnmatched, bestConf, inferredNSFW); mErr != nil {
			return false, database.SeriesMetadata{}, mErr
		}
		cur, _ := s.store.SeriesMetadataByID(ctx, series)
		return false, cur, nil
	}
	best := candidates[0]
	// When matching via online search (which lacks full infobox persons), enrich via SubjectByID if needed.
	if !s.archive.Ready() && len(best.Authors) == 0 {
		if full, fErr := s.live.SubjectByID(ctx, best.ID); fErr == nil {
			full.Confidence = best.Confidence
			best = full
		}
	}
	scraped := ToScrapedMetadataWithQuery(series.Name, parsed, best)
	meta, err = s.store.ApplyScrapedMetadata(ctx, series, scraped)
	if err != nil {
		return false, database.SeriesMetadata{}, err
	}
	return true, meta, nil
}

func (s *Service) StartBatch(
	ctx context.Context,
	libraryID string,
	force bool,
) (database.BangumiScrapeRun, error) {
	return s.StartBatchWithMode(ctx, libraryID, NormalizeScrapeMode("", force))
}

func (s *Service) StartBatchWithMode(
	ctx context.Context,
	libraryID string,
	rawMode string,
) (database.BangumiScrapeRun, error) {
	baseMode := NormalizeScrapeMode(rawMode, false)
	force := baseMode == ScrapeModeForce
	libraryID = strings.TrimSpace(libraryID)
	if libraryID != "" {
		lib, err := s.store.LibraryBasic(ctx, libraryID)
		if err != nil {
			return database.BangumiScrapeRun{}, err
		}
		if !lib.Enabled || lib.UnionParentID != "" {
			return database.BangumiScrapeRun{}, ErrLibraryHidden
		}
	}
	queueKey := libraryID
	if queueKey == "" {
		queueKey = "__all__"
	}
	s.mu.Lock()
	if _, exists := s.queued[queueKey]; exists {
		s.mu.Unlock()
		return database.BangumiScrapeRun{}, ErrScrapeAlreadyQueued
	}
	runID := randomScrapeID()
	s.queued[queueKey] = runID
	s.mu.Unlock()

	modeLabel := baseMode
	if s.archive.Ready() {
		modeLabel += ":offline"
	} else {
		modeLabel += ":online"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var libParam any
	if libraryID != "" {
		libParam = libraryID
	}
	forceInt := 0
	if force {
		forceInt = 1
	}
	if _, err := s.store.DB().ExecContext(ctx, `
INSERT INTO bangumi_scrape_runs(id,library_id,mode,force,status,created_at)
VALUES(?,?,?,?,'queued',?)`, runID, libParam, modeLabel, forceInt, now); err != nil {
		s.releaseQueued(queueKey)
		return database.BangumiScrapeRun{}, err
	}
	run, err := s.Run(ctx, runID)
	if err != nil {
		s.releaseQueued(queueKey)
		return database.BangumiScrapeRun{}, err
	}
	select {
	case s.jobs <- scrapeJob{runID: runID, libraryID: libraryID, mode: baseMode}:
		return run, nil
	case <-s.ctx.Done():
		s.releaseQueued(queueKey)
		return database.BangumiScrapeRun{}, context.Canceled
	}
}

func (s *Service) CancelBatch(ctx context.Context, runID string) error {
	res, err := s.store.DB().ExecContext(ctx, `
UPDATE bangumi_scrape_runs SET cancel_requested=1
WHERE id=? AND status IN ('queued','running')`, strings.TrimSpace(runID))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	s.mu.Lock()
	if s.activeRunID == runID && s.activeStop != nil {
		s.activeStop()
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) CancelLibrary(ctx context.Context, libraryID string) {
	rows, err := s.store.DB().QueryContext(ctx,
		`SELECT id FROM bangumi_scrape_runs WHERE library_id=? AND status IN ('queued','running')`, libraryID)
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
		_ = s.CancelBatch(ctx, id)
	}
}

func (s *Service) Runs(ctx context.Context, limit int) ([]database.BangumiScrapeRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := s.store.DB().QueryContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,'全部未隐藏书库'),r.mode,r.force,r.status,
 r.total_series,r.processed_series,r.matched_count,r.unmatched_count,r.skipped_count,r.failed_count,
 r.current_series,r.errors_json,r.cancel_requested,r.started_at,r.completed_at,r.created_at
FROM bangumi_scrape_runs r
LEFT JOIN libraries l ON l.id=r.library_id
ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]database.BangumiScrapeRun, 0)
	for rows.Next() {
		run, err := scanScrapeRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Service) Run(ctx context.Context, runID string) (database.BangumiScrapeRun, error) {
	row := s.store.DB().QueryRowContext(ctx, `
SELECT r.id,r.library_id,coalesce(l.name,'全部未隐藏书库'),r.mode,r.force,r.status,
 r.total_series,r.processed_series,r.matched_count,r.unmatched_count,r.skipped_count,r.failed_count,
 r.current_series,r.errors_json,r.cancel_requested,r.started_at,r.completed_at,r.created_at
FROM bangumi_scrape_runs r
LEFT JOIN libraries l ON l.id=r.library_id
WHERE r.id=?`, runID)
	return scanScrapeRun(row)
}

func scanScrapeRun(scanner interface{ Scan(...any) error }) (database.BangumiScrapeRun, error) {
	var (
		run        database.BangumiScrapeRun
		forceInt   int
		cancelInt  int
		errorsJSON string
	)
	if err := scanner.Scan(
		&run.ID, &run.LibraryID, &run.LibraryName, &run.Mode, &forceInt, &run.Status,
		&run.TotalSeries, &run.ProcessedSeries, &run.MatchedCount, &run.UnmatchedCount,
		&run.SkippedCount, &run.FailedCount, &run.CurrentSeries, &errorsJSON,
		&cancelInt, &run.StartedAt, &run.CompletedAt, &run.CreatedAt,
	); err != nil {
		return database.BangumiScrapeRun{}, err
	}
	run.Force = forceInt != 0
	run.CancelRequested = cancelInt != 0
	_ = json.Unmarshal([]byte(errorsJSON), &run.Errors)
	if run.Errors == nil {
		run.Errors = []string{}
	}
	return run, nil
}

func (s *Service) worker() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case item := <-s.jobs:
			s.executeBatch(item)
		}
	}
}

func (s *Service) executeBatch(item scrapeJob) {
	queueKey := item.libraryID
	if queueKey == "" {
		queueKey = "__all__"
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.activeRunID = item.runID
	s.activeStop = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.queued, queueKey)
		s.activeRunID = ""
		s.activeStop = nil
		s.mu.Unlock()
	}()

	run, err := s.Run(ctx, item.runID)
	if err != nil {
		return
	}
	if run.CancelRequested || (item.libraryID != "" && !s.store.IsLibraryVisible(ctx, item.libraryID)) {
		s.finishRun(item.runID, run, "canceled")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.store.DB().ExecContext(ctx,
		`UPDATE bangumi_scrape_runs SET status='running',started_at=? WHERE id=?`, now, item.runID)

	targets, err := s.loadTargetSeries(ctx, item.libraryID)
	if err != nil {
		run.Errors = append(run.Errors, err.Error())
		s.finishRun(item.runID, run, "failed")
		return
	}
	run.TotalSeries = len(targets)
	_, _ = s.store.DB().ExecContext(ctx,
		`UPDATE bangumi_scrape_runs SET total_series=? WHERE id=?`, run.TotalSeries, item.runID)

	libEnabledCache := map[string]bool{}
	lastFlush := time.Now()
	for i, target := range targets {
		if ctx.Err() != nil {
			s.finishRun(item.runID, run, "canceled")
			return
		}
		libOK, seenLib := libEnabledCache[target.series.LibraryID]
		if !seenLib {
			libOK = s.store.IsLibraryVisible(ctx, target.series.LibraryID)
			libEnabledCache[target.series.LibraryID] = libOK
		}
		if !libOK {
			if item.libraryID != "" {
				s.finishRun(item.runID, run, "canceled")
				return
			}
			run.ProcessedSeries++
			run.SkippedCount++
		} else if shouldSkipByMode(item.mode, target.scrapeStatus) {
			run.ProcessedSeries++
			run.SkippedCount++
		} else {
			run.CurrentSeries = target.series.Name
			matched, _, sErr := s.AutoScrapeOne(ctx, target.series)
			if errors.Is(sErr, context.Canceled) || ctx.Err() != nil {
				s.finishRun(item.runID, run, "canceled")
				return
			}
			run.ProcessedSeries++
			switch {
			case sErr != nil:
				run.FailedCount++
				if len(run.Errors) < maxScrapeStoredErrors {
					run.Errors = append(run.Errors, target.series.Name+": "+sErr.Error())
				}
			case matched:
				run.MatchedCount++
			default:
				run.UnmatchedCount++
			}
		}

		// Flush progress every 25 items or 400ms so 60k-series offline runs don't bottleneck on progress writes!
		if i == len(targets)-1 || run.ProcessedSeries%25 == 0 || time.Since(lastFlush) >= 400*time.Millisecond {
			lastFlush = time.Now()
			clear(libEnabledCache)
			if err := s.flushRunProgress(ctx, item.runID, run); err != nil {
				s.finishRun(item.runID, run, "canceled")
				return
			}
		}
	}

	status := "success"
	if run.FailedCount > 0 {
		status = "partial"
	}
	s.finishRun(item.runID, run, status)
	if s.logger != nil {
		s.logger.Info("Bangumi scrape job completed",
			"run", item.runID, "total", run.TotalSeries,
			"matched", run.MatchedCount, "unmatched", run.UnmatchedCount,
			"skipped", run.SkippedCount, "failed", run.FailedCount)
	}
}

type scrapeTarget struct {
	series       database.Series
	scrapeStatus string
}

func shouldSkipByMode(mode string, scrapeStatus string) bool {
	switch mode {
	case ScrapeModeForce:
		return false
	case ScrapeModeRetryUnmatched:
		return scrapeStatus == database.ScrapeStatusMatched
	default:
		return scrapeStatus == database.ScrapeStatusMatched || scrapeStatus == database.ScrapeStatusUnmatched
	}
}

func (s *Service) loadTargetSeries(ctx context.Context, libraryID string) ([]scrapeTarget, error) {
	query := `
SELECT s.id,s.library_id,s.cid,s.name,s.relative_path,s.one_shot,coalesce(sm.scrape_status,'none')
FROM series s
JOIN libraries l ON l.id=s.library_id
LEFT JOIN series_metadata sm ON sm.series_id=s.id`
	var args []any
	if libraryID != "" {
		query += ` WHERE s.library_id=? AND l.enabled=1 AND NOT EXISTS (SELECT 1 FROM union_layers ul WHERE ul.member_library_id=l.id)`
		args = append(args, libraryID)
	} else {
		query += ` WHERE l.enabled=1 AND NOT EXISTS (SELECT 1 FROM union_layers ul WHERE ul.member_library_id=l.id)`
	}
	query += ` ORDER BY s.updated_at DESC,s.id ASC`
	rows, err := s.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scrapeTarget
	for rows.Next() {
		var t scrapeTarget
		var oneShotInt int
		if err := rows.Scan(
			&t.series.ID, &t.series.LibraryID, &t.series.CID, &t.series.Name,
			&t.series.RelativePath, &oneShotInt, &t.scrapeStatus,
		); err != nil {
			return nil, err
		}
		t.series.OneShot = oneShotInt != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) flushRunProgress(ctx context.Context, runID string, run database.BangumiScrapeRun) error {
	errorsJSON, _ := json.Marshal(run.Errors)
	res, err := s.store.DB().ExecContext(ctx, `
UPDATE bangumi_scrape_runs SET total_series=?,processed_series=?,matched_count=?,
 unmatched_count=?,skipped_count=?,failed_count=?,current_series=?,errors_json=?
WHERE id=? AND cancel_requested=0`,
		run.TotalSeries, run.ProcessedSeries, run.MatchedCount, run.UnmatchedCount,
		run.SkippedCount, run.FailedCount, run.CurrentSeries, string(errorsJSON), runID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return context.Canceled
	}
	return nil
}

func (s *Service) finishRun(runID string, run database.BangumiScrapeRun, status string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	errorsJSON, _ := json.Marshal(run.Errors)
	_, _ = s.store.DB().ExecContext(context.Background(), `
UPDATE bangumi_scrape_runs SET status=?,completed_at=?,total_series=?,processed_series=?,
 matched_count=?,unmatched_count=?,skipped_count=?,failed_count=?,current_series=?,errors_json=?
WHERE id=?`,
		status, now, run.TotalSeries, run.ProcessedSeries, run.MatchedCount,
		run.UnmatchedCount, run.SkippedCount, run.FailedCount, run.CurrentSeries, string(errorsJSON), runID)
}

func (s *Service) releaseQueued(key string) {
	s.mu.Lock()
	delete(s.queued, key)
	s.mu.Unlock()
}

func randomScrapeID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
