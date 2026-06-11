package httpserver

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/id"
)

func (s *Server) adminRoutes(r chi.Router) {
	r.Get("/status", s.adminStatus)
	r.Get("/account", s.getAccount)
	r.Put("/account", s.putAccount)
	r.Post("/account/test", s.testAccount)
	r.Post("/account/refresh", s.refreshAccount)
	r.Get("/115/folders", s.list115Folders)
	r.Get("/libraries", s.listLibraries)
	r.Post("/libraries", s.createLibrary)
	r.Put("/libraries/{libraryID}", s.updateLibrary)
	r.Delete("/libraries/{libraryID}", s.deleteLibrary)
	r.Get("/settings", s.getSettings)
	r.Put("/settings", s.putSettings)
	r.Get("/scans", s.listScans)
	r.Post("/scans", s.startScan)
	r.Post("/scans/{runID}/cancel", s.cancelScan)
	r.Get("/cache", s.cacheStats)
	r.Delete("/cache/{cacheType}", s.clearCache)
}

func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	account, _ := s.store.Account(r.Context())
	libraries, series, books, comicBytes, err := s.store.Counts(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"configured": account.RefreshToken != "",
		"libraries":  libraries,
		"series":     series,
		"books":      books,
		"comicBytes": comicBytes,
		"version":    "0.1.0",
	})
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.Account(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"hasAccessToken":  account.AccessToken != "",
		"hasRefreshToken": account.RefreshToken != "",
		"updatedAt":       account.UpdatedAt,
	})
}

func (s *Server) putAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	existing, _ := s.store.Account(r.Context())
	newRefreshProvided := strings.TrimSpace(request.RefreshToken) != ""
	if strings.TrimSpace(request.AccessToken) == "" && !newRefreshProvided {
		request.AccessToken = existing.AccessToken
	}
	if strings.TrimSpace(request.RefreshToken) == "" {
		request.RefreshToken = existing.RefreshToken
	}
	info, err := s.client.SaveAndTest(r.Context(), request.AccessToken, request.RefreshToken)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ONEONEFIVE_AUTH_FAILED", err.Error())
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) testAccount(w http.ResponseWriter, r *http.Request) {
	info, err := s.client.Test(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "ONEONEFIVE_AUTH_FAILED", err.Error())
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) refreshAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.client.RefreshToken(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "ONEONEFIVE_AUTH_FAILED", err.Error())
		return
	}
	info, err := s.client.Test(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "ONEONEFIVE_AUTH_FAILED", err.Error())
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) list115Folders(w http.ResponseWriter, r *http.Request) {
	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	if cid == "" {
		cid = "0"
	}
	files, err := s.client.ListDirectory(r.Context(), cid)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ONEONEFIVE_LIST_FAILED", err.Error())
		return
	}
	var folders []any
	for _, file := range files {
		if file.IsDir {
			folders = append(folders, file)
		}
	}
	writeJSON(w, 200, map[string]any{"cid": cid, "folders": folders})
}

func (s *Server) listLibraries(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Libraries(r.Context(), false)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) createLibrary(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name    string `json:"name"`
		RootCID string `json:"rootCid"`
		Enabled *bool  `json:"enabled"`
		OneShot bool   `json:"oneShot"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.RootCID = strings.TrimSpace(request.RootCID)
	if request.Name == "" || request.RootCID == "" {
		writeError(w, 400, "INVALID_LIBRARY", "name and rootCid are required")
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	item := database.Library{
		ID: id.Library(request.RootCID), Name: request.Name, RootCID: request.RootCID,
		Enabled: enabled, OneShot: request.OneShot,
	}
	if err := s.store.UpsertLibrary(r.Context(), item); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, 409, "LIBRARY_EXISTS", "This root CID is already configured.")
		} else {
			writeError(w, 500, "DATABASE_ERROR", err.Error())
		}
		return
	}
	item, _ = s.store.Library(r.Context(), item.ID)
	writeJSON(w, 201, item)
}

func (s *Server) updateLibrary(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Library(r.Context(), chi.URLParam(r, "libraryID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "NOT_FOUND", "Library not found.")
			return
		}
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	var request struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		OneShot *bool  `json:"oneShot"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.Name) == "" {
		writeError(w, 400, "INVALID_LIBRARY", "name is required")
		return
	}
	if request.OneShot != nil && *request.OneShot != item.OneShot {
		var active int
		if err := s.store.DB().QueryRowContext(r.Context(), `
SELECT count(*) FROM scan_runs WHERE library_id=? AND status IN ('queued','running')`,
			item.ID).Scan(&active); err != nil {
			writeError(w, 500, "DATABASE_ERROR", err.Error())
			return
		}
		if active > 0 {
			writeError(w, 409, "SCAN_ACTIVE",
				"Cancel the active or queued scan before changing One-Shots mode.")
			return
		}
		item.OneShot = *request.OneShot
	}
	item.Name, item.Enabled = strings.TrimSpace(request.Name), request.Enabled
	if err := s.store.UpsertLibrary(r.Context(), item); err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	item, _ = s.store.Library(r.Context(), item.ID)
	writeJSON(w, 200, item)
}

func (s *Server) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	libraryID := chi.URLParam(r, "libraryID")
	var active int
	if err := s.store.DB().QueryRowContext(r.Context(), `
SELECT count(*) FROM scan_runs WHERE library_id=? AND status IN ('queued','running')`,
		libraryID).Scan(&active); err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	if active > 0 {
		writeError(w, 409, "SCAN_ACTIVE", "Cancel the active or queued scan before deleting this Library.")
		return
	}
	if err := s.store.DeleteLibrary(r.Context(), libraryID); err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, settings)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var request map[string]string
	if !decodeJSON(w, r, &request) {
		return
	}
	allowed := map[string]bool{
		"auto_scan_enabled": true, "auto_scan_interval_minutes": true, "scan_on_startup": true,
		"api_rate_per_second": true, "range_block_size": true, "range_cache_max_bytes": true,
		"page_cache_max_bytes": true, "page_prefetch_count": true, "max_page_size": true,
		"downurl_ttl_seconds": true, "rar_index_block_size": true,
		"rar_max_dictionary_size": true,
	}
	nonNegativeIntegers := map[string]bool{
		"range_block_size": true, "range_cache_max_bytes": true,
		"page_cache_max_bytes": true, "page_prefetch_count": true,
		"max_page_size": true, "downurl_ttl_seconds": true,
		"rar_index_block_size": true, "rar_max_dictionary_size": true,
	}
	for key, value := range request {
		if !allowed[key] {
			writeError(w, 400, "INVALID_SETTING", "Unknown setting: "+key)
			return
		}
		if strings.TrimSpace(value) == "" {
			writeError(w, 400, "INVALID_SETTING", "Setting value cannot be empty: "+key)
			return
		}
		if nonNegativeIntegers[key] {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < 0 {
				writeError(w, 400, "INVALID_SETTING", "Setting must be a non-negative integer: "+key)
				return
			}
		}
	}
	for key, value := range request {
		if err := s.store.SetSetting(r.Context(), key, value); err != nil {
			writeError(w, 500, "DATABASE_ERROR", err.Error())
			return
		}
	}
	if value, ok := request["range_cache_max_bytes"]; ok {
		limit, _ := strconv.ParseInt(value, 10, 64)
		if err := s.cache.EnforceLimit(r.Context(), cache.TypeRange, limit); err != nil {
			writeError(w, 500, "CACHE_LIMIT_FAILED", err.Error())
			return
		}
	}
	if value, ok := request["page_cache_max_bytes"]; ok {
		limit, _ := strconv.ParseInt(value, 10, 64)
		if err := s.cache.EnforceLimit(r.Context(), cache.TypePage, limit); err != nil {
			writeError(w, 500, "CACHE_LIMIT_FAILED", err.Error())
			return
		}
	}
	s.client.Reload(r.Context())
	s.getSettings(w, r)
}

func (s *Server) listScans(w http.ResponseWriter, r *http.Request) {
	runs, err := s.scanner.Runs(r.Context(), intQuery(r, "limit", 50))
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, runs)
}

func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		LibraryID string `json:"libraryId"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.LibraryID == "" {
		runs, err := s.scanner.StartAll(r.Context(), "manual")
		if err != nil {
			writeError(w, 409, "SCAN_START_FAILED", err.Error())
			return
		}
		writeJSON(w, 202, runs)
		return
	}
	run, err := s.scanner.StartLibrary(r.Context(), request.LibraryID, "manual")
	if err != nil {
		writeError(w, 409, "SCAN_START_FAILED", err.Error())
		return
	}
	writeJSON(w, 202, run)
}

func (s *Server) cancelScan(w http.ResponseWriter, r *http.Request) {
	if err := s.scanner.Cancel(r.Context(), chi.URLParam(r, "runID")); err != nil {
		writeError(w, 404, "NOT_FOUND", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cacheStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.cache.Stats(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	var indexes int64
	_ = s.store.DB().QueryRowContext(r.Context(), `SELECT count(*) FROM zip_indexes`).Scan(&indexes)
	thumbnailStats, err := s.thumbs.Stats(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"entries": stats, "archiveIndexes": indexes, "zipIndexes": indexes,
		"seriesThumbnails": thumbnailStats,
	})
}

func (s *Server) clearCache(w http.ResponseWriter, r *http.Request) {
	cacheType := chi.URLParam(r, "cacheType")
	if cacheType == "archive" || cacheType == "zip" {
		_, err := s.store.DB().ExecContext(r.Context(), `DELETE FROM zip_indexes`)
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if cacheType == "thumbnail" {
		if err := s.thumbs.Clear(r.Context()); err != nil {
			writeError(w, 500, "CACHE_CLEAR_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if cacheType != cache.TypeRange && cacheType != cache.TypePage && cacheType != "all" {
		writeError(w, 400, "INVALID_CACHE_TYPE", "Use range, page, archive, thumbnail, or all.")
		return
	}
	if err := s.cache.Clear(r.Context(), cacheType); err != nil {
		writeError(w, 500, "CACHE_CLEAR_FAILED", err.Error())
		return
	}
	if cacheType == "all" {
		_, _ = s.store.DB().ExecContext(r.Context(), `DELETE FROM zip_indexes`)
		_, _ = s.store.DB().ExecContext(r.Context(), `DELETE FROM downurl_cache`)
		if err := s.thumbs.Clear(r.Context()); err != nil {
			writeError(w, 500, "CACHE_CLEAR_FAILED", err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseBool(value string) bool {
	parsed, _ := strconv.ParseBool(value)
	return parsed
}
