package httpserver

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/buildinfo"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
)

func (s *Server) agentRoutes(r chi.Router) {
	r.Get("/manifest", s.agentManifest)
	r.Get("/overview", s.agentOverview)

	r.Get("/libraries", s.agentListLibraries)
	r.Post("/libraries", s.createLibrary)
	r.Patch("/libraries/{libraryID}", s.agentPatchLibrary)
	r.Delete("/libraries/{libraryID}", s.deleteLibrary)
	r.Post("/libraries/{libraryID}/scan", s.agentScanLibrary)
	r.Post("/scans", s.startScan)

	r.Get("/series", s.agentListSeries)
	r.Get("/series/{seriesID}", s.agentGetSeries)
	r.Get("/series/{seriesID}/bangumi/candidates", s.agentSearchSeriesBangumi)
	r.Post("/series/batch", s.agentBatchSeries)

	r.Post("/bangumi/scrape", s.startBangumiRun)
	r.Post("/bangumi/archive/update", s.startBangumiArchiveUpdate)

	r.Get("/jobs", s.agentListJobs)
	r.Post("/jobs/{jobType}/{runID}/cancel", s.agentCancelJob)

	r.Get("/settings", s.getSettings)
	r.Patch("/settings", s.agentPatchSettings)
}

func (s *Server) agentManifest(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":       "MangaAlpha Agent API",
		"apiVersion": "v1",
		"appVersion": buildinfo.Version,
		"basePath":   "/api/agent/v1",
		"auth": map[string]any{
			"headers": []string{
				"Authorization: Bearer <api-key>",
				"X-API-Key: <api-key>",
				"Authorization: Basic <base64(username:password)>",
			},
			"note": "Create an API key in /admin -> 账号与安全, then send it via Authorization: Bearer or X-API-Key.",
		},
		"providers":   []string{database.Provider115, database.ProviderLocal, database.ProviderWebDAV},
		"scrapeModes": []string{bangumi.ScrapeModeIncremental, bangumi.ScrapeModeRetryUnmatched, bangumi.ScrapeModeForce},
		"nsfwFilters": []string{"all", "only", "exclude"},
		"endpoints": []map[string]string{
			{"method": "GET", "path": "/api/agent/v1/manifest", "summary": "Self-describing capability and endpoint manifest"},
			{"method": "GET", "path": "/api/agent/v1/overview", "summary": "Compact library, Bangumi scrape, and NSFW visibility summary"},
			{"method": "GET", "path": "/api/agent/v1/libraries", "summary": "List libraries (includes hidden libraries by default)"},
			{"method": "POST", "path": "/api/agent/v1/libraries", "summary": "Create a 115, local/SMB, or WebDAV library"},
			{"method": "PATCH", "path": "/api/agent/v1/libraries/{libraryID}", "summary": "Update library name, hidden/enabled state, or oneShot mode"},
			{"method": "DELETE", "path": "/api/agent/v1/libraries/{libraryID}", "summary": "Delete a library"},
			{"method": "POST", "path": "/api/agent/v1/libraries/{libraryID}/scan", "summary": "Queue a directory scan for one library"},
			{"method": "POST", "path": "/api/agent/v1/scans", "summary": "Queue scans for one libraryId or all unhidden libraries"},
			{"method": "GET", "path": "/api/agent/v1/series", "summary": "Compact paginated series query (search, scrape_status, nsfw=all|only|exclude, include_hidden)"},
			{"method": "GET", "path": "/api/agent/v1/series/{seriesID}", "summary": "Full series metadata, field locks, and book list"},
			{"method": "GET", "path": "/api/agent/v1/series/{seriesID}/bangumi/candidates", "summary": "Search Bangumi candidates or subject ID for a series (?q=...)"},
			{"method": "POST", "path": "/api/agent/v1/series/batch", "summary": "Batch bind_bangumi, patch_metadata, auto_scrape, or clear_bangumi (up to 200 items)"},
			{"method": "POST", "path": "/api/agent/v1/bangumi/scrape", "summary": "Start batch Bangumi scrape (mode: incremental | retry_unmatched | force)"},
			{"method": "POST", "path": "/api/agent/v1/bangumi/archive/update", "summary": "Trigger online update of local Bangumi offline archive index"},
			{"method": "GET", "path": "/api/agent/v1/jobs", "summary": "Unified view of scan, Bangumi scrape, cover, and maintenance jobs"},
			{"method": "POST", "path": "/api/agent/v1/jobs/{jobType}/{runID}/cancel", "summary": "Cancel a queued/running job (jobType: scan | bangumi | cover)"},
			{"method": "GET", "path": "/api/agent/v1/settings", "summary": "Read runtime settings"},
			{"method": "PATCH", "path": "/api/agent/v1/settings", "summary": "Update runtime settings (including show_nsfw)"},
		},
	})
}

func (s *Server) agentOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	libraries, series, books, comicBytes, err := s.store.Counts(ctx)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	hiddenLibs, _ := s.store.HiddenLibraryCount(ctx)
	nsfwSeries, _ := s.store.NSFWSeriesCount(ctx)
	showNSFW := s.store.ShowNSFW(ctx)
	matched, unmatched, total, _ := s.store.ScrapeSummary(ctx)
	var arch bangumi.ArchiveStatus
	if s.bangumi != nil {
		arch = s.bangumi.ArchiveStatus()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  buildinfo.Version,
		"showNsfw": showNSFW,
		"counts": map[string]any{
			"activeLibraries": libraries,
			"hiddenLibraries": hiddenLibs,
			"visibleSeries":   series,
			"visibleBooks":    books,
			"comicBytes":      comicBytes,
			"nsfwSeries":      nsfwSeries,
		},
		"bangumi": map[string]any{
			"archive":   arch,
			"matched":   matched,
			"unmatched": unmatched,
			"pending":   max(int64(0), total-matched-unmatched),
			"total":     total,
		},
	})
}

func (s *Server) agentListLibraries(w http.ResponseWriter, r *http.Request) {
	enabledOnly := r.URL.Query().Get("include_hidden") == "false"
	items, err := s.store.Libraries(r.Context(), enabledOnly)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, lib := range items {
		out = append(out, map[string]any{
			"id":                  lib.ID,
			"name":                lib.Name,
			"providerType":        lib.EffectiveProvider(),
			"rootCid":             lib.RootCID,
			"rootPath":            lib.RootPath,
			"webdavAccountId":     lib.WebDAVAccountID,
			"enabled":             lib.Enabled,
			"hidden":              !lib.Enabled,
			"oneShot":             lib.OneShot,
			"seriesCount":         lib.SeriesCount,
			"bookCount":           lib.BookCount,
			"comicBytes":          lib.ComicBytes,
			"lastScanStatus":      lib.LastScanStatus,
			"lastScanCompletedAt": lib.LastScanCompletedAt,
			"lastScanError":       lib.LastScanError,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) agentPatchLibrary(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Library(r.Context(), chi.URLParam(r, "libraryID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Library not found.")
			return
		}
		s.writeDBError(w, r, err)
		return
	}
	wasEnabled := item.Enabled
	var req struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
		Hidden  *bool   `json:"hidden"`
		OneShot *bool   `json:"oneShot"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		clean := strings.TrimSpace(*req.Name)
		if clean == "" {
			writeError(w, http.StatusBadRequest, "INVALID_LIBRARY", "name cannot be empty")
			return
		}
		item.Name = clean
	}
	if req.Hidden != nil {
		item.Enabled = !*req.Hidden
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if req.OneShot != nil && *req.OneShot != item.OneShot {
		var active int
		if err := s.store.DB().QueryRowContext(r.Context(),
			`SELECT count(*) FROM scan_runs WHERE library_id=? AND status IN ('queued','running')`,
			item.ID).Scan(&active); err != nil {
			s.writeDBError(w, r, err)
			return
		}
		if active > 0 {
			writeError(w, http.StatusConflict, "SCAN_ACTIVE", "Cancel the active scan before changing oneShot mode.")
			return
		}
		item.OneShot = *req.OneShot
	}
	if err := s.store.UpsertLibrary(r.Context(), item); err != nil {
		s.writeDBError(w, r, err)
		return
	}
	if wasEnabled && !item.Enabled {
		s.cancelLibraryJobs(r.Context(), item.ID)
	}
	updated, _ := s.store.Library(r.Context(), item.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) agentScanLibrary(w http.ResponseWriter, r *http.Request) {
	libraryID := strings.TrimSpace(chi.URLParam(r, "libraryID"))
	run, err := s.scanner.StartLibrary(r.Context(), libraryID, "agent")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Library not found.")
			return
		}
		writeError(w, http.StatusConflict, "SCAN_START_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) agentListSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := max(0, intQuery(r, "page", 0))
	size := intQuery(r, "size", 50)
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	nsfwMode := strings.ToLower(strings.TrimSpace(q.Get("nsfw")))
	switch nsfwMode {
	case "only", "exclude", "all":
	case "":
		// Default to "all" on Agent API so agents can inspect and curate NSFW items even when hidden on UI.
		nsfwMode = "all"
	default:
		writeError(w, http.StatusBadRequest, "INVALID_NSFW_FILTER", "nsfw must be all, only, or exclude")
		return
	}
	includeHidden := q.Get("include_hidden") == "true"

	searchTerm := strings.TrimSpace(q.Get("search"))
	if searchTerm == "" {
		searchTerm = strings.TrimSpace(q.Get("q"))
	}
	items, total, err := s.store.SeriesPage(r.Context(), database.SeriesQuery{
		LibraryIDs:    listQuery(r, "library_id"),
		Search:        searchTerm,
		Publishers:    listQuery(r, "publisher"),
		Genres:        listQuery(r, "genre"),
		Tags:          listQuery(r, "tag"),
		Authors:       listQuery(r, "author"),
		Statuses:      listQuery(r, "status"),
		ScrapeStatus:  q.Get("scrape_status"),
		IncludeHidden: includeHidden,
		NSFWMode:      nsfwMode,
		Page:          page,
		Size:          size,
		Sort:          q.Get("sort"),
	})
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	metaMap, err := s.store.SeriesMetadatas(r.Context(), items)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	compact := make([]map[string]any, 0, len(items))
	for _, item := range items {
		meta := metaMap[item.ID]
		title := strings.TrimSpace(meta.Title)
		if title == "" {
			title = item.Name
		}
		scrapeSt := meta.ScrapeStatus
		if scrapeSt == "" {
			scrapeSt = database.ScrapeStatusNone
		}
		compact = append(compact, map[string]any{
			"id":               item.ID,
			"libraryId":        item.LibraryID,
			"folderName":       item.Name,
			"relativePath":     item.RelativePath,
			"title":            title,
			"booksCount":       item.BooksCount,
			"oneShot":          item.OneShot,
			"scrapeStatus":     scrapeSt,
			"scrapeConfidence": meta.ScrapeConfidence,
			"bgmSubjectId":     meta.BgmSubjectID,
			"bgmScore":         meta.BgmScore,
			"publisher":        meta.Publisher,
			"status":           meta.Status,
			"isNsfw":           meta.IsNSFW,
			"nsfwLock":         meta.NSFWLock,
			"authors":          meta.Authors,
			"genres":           meta.Genres,
			"tags":             meta.Tags,
			"updatedAt":        item.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(size) - 1) / int64(size))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":         compact,
		"page":          page,
		"size":          size,
		"totalElements": total,
		"totalPages":    totalPages,
	})
}

func (s *Server) agentGetSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	series, err := s.store.SeriesByIDAny(ctx, chi.URLParam(r, "seriesID"), true)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
			return
		}
		s.writeDBError(w, r, err)
		return
	}
	lib, _ := s.store.LibraryBasic(ctx, series.LibraryID)
	meta, err := s.store.SeriesMetadataByID(ctx, series)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	progress, _ := s.store.SeriesReadProgress(ctx, series.ID)
	var books []map[string]any
	bRows, qErr := s.store.DB().QueryContext(ctx, `
SELECT b.id,b.name,b.size,b.number_sort,coalesce(z.page_count,0)
FROM books b
LEFT JOIN zip_indexes z ON z.book_id=b.id
WHERE b.series_id=?
ORDER BY b.number_sort ASC,b.name COLLATE NOCASE ASC,b.id ASC`, series.ID)
	if qErr == nil {
		for bRows.Next() {
			var id, name string
			var sz int64
			var num float64
			var pages int
			if bRows.Scan(&id, &name, &sz, &num, &pages) == nil {
				books = append(books, map[string]any{
					"id": id, "name": name, "size": sz, "numberSort": num, "pageCount": pages,
				})
			}
		}
		bRows.Close()
	}
	if books == nil {
		books = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            series.ID,
		"libraryId":     series.LibraryID,
		"libraryName":   lib.Name,
		"libraryHidden": !lib.Enabled,
		"folderName":    series.Name,
		"relativePath":  series.RelativePath,
		"oneShot":       series.OneShot,
		"booksCount":    series.BooksCount,
		"progress":      progress,
		"metadata":      meta,
		"books":         books,
	})
}

func (s *Server) agentSearchSeriesBangumi(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	series, err := s.store.SeriesByIDAny(r.Context(), chi.URLParam(r, "seriesID"), false)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found in active libraries.")
		return
	}
	candidates, parsed, err := s.bangumi.SearchCandidates(r.Context(), series, r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "BANGUMI_SEARCH_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"parsed":     parsed,
		"candidates": candidates,
	})
}

type agentSeriesBatchOp struct {
	SeriesID  string                        `json:"seriesId"`
	Action    string                        `json:"action"`
	SubjectID int64                         `json:"subjectId,omitempty"`
	Patch     *database.SeriesMetadataPatch `json:"patch,omitempty"`
}

func (s *Server) agentBatchSeries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action     string                        `json:"action"`
		SeriesIDs  []string                      `json:"seriesIds"`
		SubjectID  int64                         `json:"subjectId"`
		Patch      *database.SeriesMetadataPatch `json:"patch"`
		Operations []agentSeriesBatchOp          `json:"operations"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	ops := make([]agentSeriesBatchOp, 0, len(req.Operations)+len(req.SeriesIDs))
	ops = append(ops, req.Operations...)
	for _, sid := range req.SeriesIDs {
		ops = append(ops, agentSeriesBatchOp{
			SeriesID:  sid,
			Action:    req.Action,
			SubjectID: req.SubjectID,
			Patch:     req.Patch,
		})
	}
	if len(ops) == 0 {
		writeError(w, http.StatusBadRequest, "EMPTY_BATCH", "Provide operations or seriesIds + action.")
		return
	}
	if len(ops) > 200 {
		writeError(w, http.StatusBadRequest, "BATCH_TOO_LARGE", "Maximum 200 operations per batch call.")
		return
	}

	ctx := r.Context()
	results := make([]map[string]any, 0, len(ops))
	succeeded := 0
	failed := 0

	for _, op := range ops {
		sid := strings.TrimSpace(op.SeriesID)
		action := strings.ToLower(strings.TrimSpace(op.Action))
		series, err := s.store.SeriesByIDAny(ctx, sid, false)
		if err != nil {
			failed++
			results = append(results, map[string]any{
				"seriesId": sid, "action": action, "ok": false, "error": "series not found or library is hidden",
			})
			continue
		}

		switch action {
		case "bind_bangumi":
			if s.bangumi == nil || op.SubjectID <= 0 {
				failed++
				results = append(results, map[string]any{
					"seriesId": sid, "action": action, "ok": false, "error": "positive subjectId is required",
				})
				continue
			}
			meta, bErr := s.bangumi.BindSubject(ctx, series, op.SubjectID)
			if bErr != nil {
				failed++
				results = append(results, map[string]any{
					"seriesId": sid, "action": action, "ok": false, "error": bErr.Error(),
				})
				continue
			}
			succeeded++
			results = append(results, map[string]any{
				"seriesId": sid, "action": action, "ok": true,
				"title": meta.Title, "bgmSubjectId": meta.BgmSubjectID, "isNsfw": meta.IsNSFW,
			})
		case "auto_scrape":
			if s.bangumi == nil {
				failed++
				results = append(results, map[string]any{
					"seriesId": sid, "action": action, "ok": false, "error": "bangumi service unavailable",
				})
				continue
			}
			matched, meta, aErr := s.bangumi.AutoScrapeOne(ctx, series)
			if aErr != nil {
				failed++
				results = append(results, map[string]any{
					"seriesId": sid, "action": action, "ok": false, "error": aErr.Error(),
				})
				continue
			}
			succeeded++
			results = append(results, map[string]any{
				"seriesId": sid, "action": action, "ok": true, "matched": matched,
				"title": meta.Title, "bgmSubjectId": meta.BgmSubjectID, "isNsfw": meta.IsNSFW,
			})
		case "clear_bangumi":
			meta, cErr := s.store.ClearSeriesBangumiBinding(ctx, series)
			if cErr != nil {
				failed++
				results = append(results, map[string]any{
					"seriesId": sid, "action": action, "ok": false, "error": cErr.Error(),
				})
				continue
			}
			succeeded++
			results = append(results, map[string]any{
				"seriesId": sid, "action": action, "ok": true, "title": meta.Title,
			})
		case "patch_metadata":
			if op.Patch == nil {
				failed++
				results = append(results, map[string]any{
					"seriesId": sid, "action": action, "ok": false, "error": "patch object is required",
				})
				continue
			}
			meta, pErr := s.store.PatchSeriesMetadata(ctx, series, *op.Patch)
			if pErr != nil {
				failed++
				results = append(results, map[string]any{
					"seriesId": sid, "action": action, "ok": false, "error": pErr.Error(),
				})
				continue
			}
			succeeded++
			results = append(results, map[string]any{
				"seriesId": sid, "action": action, "ok": true,
				"title": meta.Title, "isNsfw": meta.IsNSFW, "nsfwLock": meta.NSFWLock,
			})
		default:
			failed++
			results = append(results, map[string]any{
				"seriesId": sid, "action": action, "ok": false,
				"error": "unsupported action (expected bind_bangumi, auto_scrape, clear_bangumi, or patch_metadata)",
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":     len(ops),
		"succeeded": succeeded,
		"failed":    failed,
		"results":   results,
	})
}

func (s *Server) agentListJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := min(100, max(1, intQuery(r, "limit", 20)))
	scans, _ := s.scanner.Runs(ctx, limit)
	var bgmRuns []database.BangumiScrapeRun
	var arch bangumi.ArchiveStatus
	if s.bangumi != nil {
		bgmRuns, _ = s.bangumi.Runs(ctx, limit)
		arch = s.bangumi.ArchiveStatus()
	}
	coverRuns, _ := s.covers.Runs(ctx, limit)
	maintRuns, _ := s.tasks.Runs(ctx, maintenance.ListFilter{Limit: limit})
	writeJSON(w, http.StatusOK, map[string]any{
		"scans":           scans,
		"bangumiRuns":     bgmRuns,
		"bangumiArchive":  arch,
		"coverRuns":       coverRuns,
		"maintenanceRuns": maintRuns,
	})
}

func (s *Server) agentCancelJob(w http.ResponseWriter, r *http.Request) {
	jobType := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "jobType")))
	runID := strings.TrimSpace(chi.URLParam(r, "runID"))
	var err error
	switch jobType {
	case "scan", "scans":
		err = s.scanner.Cancel(r.Context(), runID)
	case "bangumi":
		if s.bangumi == nil {
			err = sql.ErrNoRows
		} else {
			err = s.bangumi.CancelBatch(r.Context(), runID)
		}
	case "cover", "covers":
		err = s.covers.Cancel(r.Context(), runID)
	default:
		writeError(w, http.StatusBadRequest, "INVALID_JOB_TYPE", "jobType must be scan, bangumi, or cover")
		return
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) agentPatchSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !decodeJSON(w, r, &raw) {
		return
	}
	converted := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "showNsfw" {
			k = "show_nsfw"
		}
		switch typed := v.(type) {
		case string:
			converted[k] = typed
		case bool:
			converted[k] = strconv.FormatBool(typed)
		case float64:
			converted[k] = strconv.FormatInt(int64(typed), 10)
		default:
			writeError(w, http.StatusBadRequest, "INVALID_SETTING", "Unsupported value type for "+k)
			return
		}
	}
	allowedBool := map[string]bool{
		"show_nsfw": true, "volume_index_prefetch_enabled": true,
		"auto_scan_enabled": true, "scan_on_startup": true,
	}
	allowedInt := map[string]bool{
		"auto_scan_interval_minutes": true, "range_block_size": true,
		"range_cache_max_bytes": true, "page_cache_max_bytes": true,
		"page_prefetch_count": true, "max_page_size": true,
		"volume_index_prefetch_remaining_pages": true, "downurl_ttl_seconds": true,
		"rar_index_block_size": true, "rar_max_dictionary_size": true,
	}
	for k, v := range converted {
		switch {
		case allowedBool[k]:
			parsed, err := strconv.ParseBool(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_SETTING", k+" must be boolean")
				return
			}
			converted[k] = strconv.FormatBool(parsed)
		case allowedInt[k]:
			num, err := strconv.ParseInt(v, 10, 64)
			if err != nil || num < 0 {
				writeError(w, http.StatusBadRequest, "INVALID_SETTING", k+" must be a non-negative integer")
				return
			}
			converted[k] = strconv.FormatInt(num, 10)
		case k == "api_rate_per_second":
			num, err := strconv.ParseFloat(v, 64)
			if err != nil || num <= 0 {
				writeError(w, http.StatusBadRequest, "INVALID_SETTING", "api_rate_per_second must be positive")
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "INVALID_SETTING", "Unknown setting: "+k)
			return
		}
	}
	for k, v := range converted {
		if err := s.store.SetSetting(r.Context(), k, v); err != nil {
			s.writeDBError(w, r, err)
			return
		}
	}
	s.client.Reload(r.Context())
	s.getSettings(w, r)
}
