package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/buildinfo"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/id"
	"github.com/xJogger/fake-komga-115/internal/localfs"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func (s *Server) adminRoutes(r chi.Router) {
	// Auth & API Key routes
	r.Get("/auth/status", s.authStatusHandler)
	r.Post("/auth/setup", s.authSetupHandler)
	r.Post("/auth/login", s.authLoginHandler)
	r.Post("/auth/logout", s.authLogoutHandler)
	r.Put("/auth/credentials", s.updateCredentialsHandler)
	r.Get("/auth/sessions", s.listSessionsHandler)
	r.Delete("/auth/sessions/{sessionID}", s.deleteSessionHandler)
	r.Get("/auth/api-keys", s.listAPIKeysHandler)
	r.Post("/auth/api-keys", s.createAPIKeyHandler)
	r.Delete("/auth/api-keys/{keyID}", s.deleteAPIKeyHandler)

	// Status & 115 account
	r.Get("/status", s.adminStatus)
	r.Get("/account", s.getAccount)
	r.Put("/account", s.putAccount)
	r.Post("/account/test", s.testAccount)
	r.Post("/account/refresh", s.refreshAccount)
	r.Get("/115/folders", s.list115Folders)

	// Local & WebDAV storage sources
	r.Get("/local/folders", s.listLocalFolders)
	r.Get("/webdav-accounts", s.listWebDAVAccounts)
	r.Post("/webdav-accounts", s.createWebDAVAccount)
	r.Put("/webdav-accounts/{accountID}", s.updateWebDAVAccount)
	r.Delete("/webdav-accounts/{accountID}", s.deleteWebDAVAccount)
	r.Post("/webdav-accounts/test", s.testWebDAVAccount)
	r.Get("/webdav-accounts/{accountID}/folders", s.listWebDAVFolders)

	// Libraries, settings, scans, covers, maintenance, cache
	r.Get("/libraries", s.listLibraries)
	r.Post("/libraries", s.createLibrary)
	r.Put("/libraries/{libraryID}", s.updateLibrary)
	r.Delete("/libraries/{libraryID}", s.deleteLibrary)
	r.Get("/settings", s.getSettings)
	r.Put("/settings", s.putSettings)
	r.Get("/scans", s.listScans)
	r.Post("/scans", s.startScan)
	r.Post("/scans/{runID}/cancel", s.cancelScan)
	r.Get("/cover-jobs", s.listCoverJobs)
	r.Post("/cover-jobs", s.startCoverJob)
	r.Post("/cover-jobs/{runID}/cancel", s.cancelCoverJob)
	r.Get("/maintenance-jobs", s.listMaintenanceJobs)
	r.Post("/maintenance-jobs", s.startMaintenanceJob)
	r.Post("/maintenance-jobs/{runID}/cancel", s.cancelMaintenanceJob)
	r.Get("/cache", s.cacheStats)
	r.Delete("/cache/{cacheType}", s.clearCache)

	// Rich detail endpoints for built-in Bookshelf & WebView
	r.Get("/series/{seriesID}/detail", s.seriesRichDetail)
	r.Get("/books/{bookID}/detail", s.bookRichDetail)

	// Bangumi scraping & archive index management
	r.Get("/bangumi/status", s.bangumiStatus)
	r.Put("/bangumi/config", s.putBangumiConfig)
	r.Post("/bangumi/proxy/test", s.testBangumiProxy)
	r.Get("/bangumi/subjects/{subjectID}/cover", s.bangumiSubjectCover)
	r.Post("/bangumi/archive/update", s.startBangumiArchiveUpdate)
	r.Post("/bangumi/archive/cancel", s.cancelBangumiArchiveUpdate)
	r.Get("/bangumi/runs", s.listBangumiRuns)
	r.Post("/bangumi/runs", s.startBangumiRun)
	r.Post("/bangumi/runs/{runID}/cancel", s.cancelBangumiRun)
	r.Get("/series/{seriesID}/bangumi/candidates", s.searchSeriesBangumi)
	r.Post("/series/{seriesID}/bangumi/bind", s.bindSeriesBangumi)
	r.Post("/series/{seriesID}/bangumi/auto", s.autoMatchSeriesBangumi)
	r.Delete("/series/{seriesID}/bangumi/bind", s.clearSeriesBangumi)
}

func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	account, _ := s.store.Account(r.Context())
	webdavAccounts, _ := s.store.WebDAVAccounts(r.Context())
	libraries, series, books, comicBytes, err := s.store.Counts(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	hiddenLibraries, _ := s.store.HiddenLibraryCount(r.Context())
	performance, err := s.store.GlobalPerformance(r.Context())
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	matched, unmatched, _, _ := s.store.ScrapeSummary(r.Context())
	writeJSON(w, 200, map[string]any{
		"configured":      account.RefreshToken != "" || len(webdavAccounts) > 0 || (libraries+hiddenLibraries) > 0,
		"configured115":   account.RefreshToken != "",
		"webdavAccounts":  len(webdavAccounts),
		"libraries":       libraries,
		"hiddenLibraries": hiddenLibraries,
		"series":          series,
		"books":           books,
		"comicBytes":      comicBytes,
		"bgmMatched":      matched,
		"bgmUnmatched":    unmatched,
		"version":         buildinfo.Version,
		"dataDir":         s.runtime.DataDir,
		"adminUrl":        localServiceURL(s.runtime.Host, s.runtime.Port),
		"mihonAddresses":  mihonAddresses(s.runtime.Host, s.runtime.Port),
		"performance":     performance,
	})
}

func localServiceURL(host string, port int) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func mihonAddresses(host string, port int) []string {
	host = strings.TrimSpace(host)
	if host != "" && host != "0.0.0.0" && host != "::" && host != "[::]" {
		return []string{localServiceURL(host, port)}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var result []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 ||
			isVirtualInterface(iface.Name) {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil {
				continue
			}
			ip = ip.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			url := "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
			if _, ok := seen[url]; ok {
				continue
			}
			seen[url] = struct{}{}
			result = append(result, url)
		}
	}
	sort.Strings(result)
	return result
}

func isVirtualInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{
		"br-", "docker", "veth", "virbr", "vmnet", "vboxnet", "vethernet", "wsl",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
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
	folders := make([]any, 0)
	for _, file := range files {
		if file.IsDir {
			folders = append(folders, file)
		}
	}
	writeJSON(w, 200, map[string]any{"cid": cid, "folders": folders})
}

func (s *Server) listLocalFolders(w http.ResponseWriter, r *http.Request) {
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	current, parent, folders, err := localfs.BrowseDirectories(rawPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "LOCAL_BROWSE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current": current,
		"parent":  parent,
		"folders": folders,
	})
}

// --- WebDAV Account Management ---

func (s *Server) listWebDAVAccounts(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.WebDAVAccounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

type webdavAccountRequest struct {
	Name          string  `json:"name"`
	BaseURL       string  `json:"baseUrl"`
	Username      string  `json:"username"`
	Password      string  `json:"password"`
	InsecureTLS   bool    `json:"insecureTls"`
	RatePerSecond float64 `json:"ratePerSecond"`
}

func (s *Server) createWebDAVAccount(w http.ResponseWriter, r *http.Request) {
	var req webdavAccountRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cleanURL, err := webdav.ValidateBaseURL(req.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WEBDAV_URL", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_WEBDAV_NAME", "name is required")
		return
	}
	item, err := s.store.UpsertWebDAVAccount(r.Context(), database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{
			Name:          strings.TrimSpace(req.Name),
			BaseURL:       cleanURL,
			Username:      strings.TrimSpace(req.Username),
			InsecureTLS:   req.InsecureTLS,
			RatePerSecond: req.RatePerSecond,
		},
		Password: req.Password,
	}, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) updateWebDAVAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	if _, err := s.store.WebDAVAccount(r.Context(), accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "WebDAV account not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	var req webdavAccountRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cleanURL, err := webdav.ValidateBaseURL(req.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WEBDAV_URL", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_WEBDAV_NAME", "name is required")
		return
	}
	item, err := s.store.UpsertWebDAVAccount(r.Context(), database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{
			ID:            accountID,
			Name:          strings.TrimSpace(req.Name),
			BaseURL:       cleanURL,
			Username:      strings.TrimSpace(req.Username),
			InsecureTLS:   req.InsecureTLS,
			RatePerSecond: req.RatePerSecond,
		},
		Password: req.Password,
	}, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteWebDAVAccount(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteWebDAVAccount(r.Context(), chi.URLParam(r, "accountID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "WebDAV account not found.")
			return
		}
		writeError(w, http.StatusConflict, "WEBDAV_IN_USE", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testWebDAVAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID            string  `json:"id"`
		BaseURL       string  `json:"baseUrl"`
		Username      string  `json:"username"`
		Password      string  `json:"password"`
		InsecureTLS   bool    `json:"insecureTls"`
		RatePerSecond float64 `json:"ratePerSecond"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	var secret database.WebDAVAccountSecret
	if req.ID != "" && req.BaseURL == "" {
		var err error
		secret, err = s.store.WebDAVAccountSecret(r.Context(), req.ID)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "WebDAV account not found.")
			return
		}
	} else {
		cleanURL, err := webdav.ValidateBaseURL(req.BaseURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_WEBDAV_URL", err.Error())
			return
		}
		password := req.Password
		if password == "" && req.ID != "" {
			if existing, err := s.store.WebDAVAccountSecret(r.Context(), req.ID); err == nil {
				password = existing.Password
			}
		}
		secret = database.WebDAVAccountSecret{
			WebDAVAccount: database.WebDAVAccount{
				BaseURL:       cleanURL,
				Username:      strings.TrimSpace(req.Username),
				InsecureTLS:   req.InsecureTLS,
				RatePerSecond: req.RatePerSecond,
			},
			Password: password,
		}
	}
	if err := s.webdav.TestConnection(r.Context(), secret); err != nil {
		writeError(w, http.StatusBadGateway, "WEBDAV_TEST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) listWebDAVFolders(w http.ResponseWriter, r *http.Request) {
	secret, err := s.store.WebDAVAccountSecret(r.Context(), chi.URLParam(r, "accountID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "WebDAV account not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	remoteDir := r.URL.Query().Get("path")
	current, parent, folders, err := s.webdav.ListFolders(r.Context(), secret, remoteDir)
	if err != nil {
		writeError(w, http.StatusBadGateway, "WEBDAV_LIST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current": current,
		"parent":  parent,
		"folders": folders,
	})
}

// --- Libraries ---

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
		Name            string `json:"name"`
		ProviderType    string `json:"providerType"`
		RootCID         string `json:"rootCid"`
		WebDAVAccountID string `json:"webdavAccountId"`
		RootPath        string `json:"rootPath"`
		Enabled         *bool  `json:"enabled"`
		OneShot         bool   `json:"oneShot"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		writeError(w, 400, "INVALID_LIBRARY", "name is required")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(request.ProviderType))
	if provider == "" {
		provider = database.Provider115
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}

	var item database.Library
	switch provider {
	case database.Provider115:
		rootCID := strings.TrimSpace(request.RootCID)
		if rootCID == "" {
			writeError(w, 400, "INVALID_LIBRARY", "rootCid is required for 115 libraries")
			return
		}
		item = database.Library{
			ID:           id.Library(rootCID),
			Name:         request.Name,
			ProviderType: database.Provider115,
			RootCID:      rootCID,
			Enabled:      enabled,
			OneShot:      request.OneShot,
		}
	case database.ProviderLocal:
		cleanRoot, err := localfs.CleanRoot(request.RootPath)
		if err != nil {
			writeError(w, 400, "INVALID_LOCAL_PATH", err.Error())
			return
		}
		locator := "local:" + cleanRoot
		item = database.Library{
			ID:           id.Library(locator),
			Name:         request.Name,
			ProviderType: database.ProviderLocal,
			RootCID:      locator,
			RootPath:     cleanRoot,
			Enabled:      enabled,
			OneShot:      request.OneShot,
		}
	case database.ProviderWebDAV:
		accountID := strings.TrimSpace(request.WebDAVAccountID)
		if accountID == "" {
			writeError(w, 400, "INVALID_LIBRARY", "webdavAccountId is required for WebDAV libraries")
			return
		}
		if _, err := s.store.WebDAVAccount(r.Context(), accountID); err != nil {
			writeError(w, 400, "INVALID_WEBDAV_ACCOUNT", "Selected WebDAV account does not exist")
			return
		}
		cleanRemote := webdav.CleanRemotePath(request.RootPath)
		locator := "webdav:" + accountID + ":" + cleanRemote
		item = database.Library{
			ID:              id.Library(locator),
			Name:            request.Name,
			ProviderType:    database.ProviderWebDAV,
			RootCID:         locator,
			WebDAVAccountID: accountID,
			RootPath:        cleanRemote,
			Enabled:         enabled,
			OneShot:         request.OneShot,
		}
	default:
		writeError(w, 400, "INVALID_PROVIDER", "Unsupported providerType: "+provider)
		return
	}

	var existing int
	_ = s.store.DB().QueryRowContext(r.Context(), `SELECT count(*) FROM libraries WHERE id=?`, item.ID).Scan(&existing)
	if existing > 0 {
		writeError(w, 409, "LIBRARY_EXISTS", "This library root is already configured.")
		return
	}

	if err := s.store.UpsertLibrary(r.Context(), item); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, 409, "LIBRARY_EXISTS", "This library root is already configured.")
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
	wasEnabled := item.Enabled
	var request struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
		OneShot *bool   `json:"oneShot"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Name != nil {
		cleanName := strings.TrimSpace(*request.Name)
		if cleanName == "" {
			writeError(w, 400, "INVALID_LIBRARY", "name is required")
			return
		}
		item.Name = cleanName
	}
	if request.Enabled != nil {
		item.Enabled = *request.Enabled
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
	if err := s.store.UpsertLibrary(r.Context(), item); err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	if wasEnabled && !item.Enabled {
		s.cancelLibraryJobs(r.Context(), item.ID)
	}
	item, _ = s.store.Library(r.Context(), item.ID)
	writeJSON(w, 200, item)
}

func (s *Server) cancelLibraryJobs(ctx context.Context, libraryID string) {
	if s.scanner != nil {
		s.scanner.CancelLibrary(ctx, libraryID)
	}
	if s.covers != nil {
		s.covers.CancelLibrary(ctx, libraryID)
	}
	if s.bangumi != nil {
		s.bangumi.CancelLibrary(ctx, libraryID)
	}
	if s.tasks != nil {
		s.tasks.CancelLibrary(ctx, libraryID)
	}
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
		"auto_scan_enabled":                     true,
		"auto_scan_interval_minutes":            true,
		"scan_on_startup":                       true,
		"api_rate_per_second":                   true,
		"range_block_size":                      true,
		"range_cache_max_bytes":                 true,
		"page_cache_max_bytes":                  true,
		"page_prefetch_count":                   true,
		"max_page_size":                         true,
		"volume_index_prefetch_enabled":         true,
		"volume_index_prefetch_remaining_pages": true,
		"downurl_ttl_seconds":                   true,
		"rar_index_block_size":                  true,
		"rar_max_dictionary_size":               true,
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
		if key == "volume_index_prefetch_enabled" || key == "auto_scan_enabled" || key == "scan_on_startup" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				writeError(w, 400, "INVALID_SETTING", key+" must be true or false")
				return
			}
			request[key] = strconv.FormatBool(parsed)
			continue
		}
		if key == "volume_index_prefetch_remaining_pages" {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < 1 || number > 100 {
				writeError(w, 400, "INVALID_SETTING",
					"Volume index prefetch remaining pages must be between 1 and 100.")
				return
			}
			continue
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

func (s *Server) listCoverJobs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.covers.Runs(r.Context(), intQuery(r, "limit", 50))
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, runs)
}

func (s *Server) startCoverJob(w http.ResponseWriter, r *http.Request) {
	var request struct {
		LibraryID string `json:"libraryId"`
		All       bool   `json:"all"`
		Limit     int    `json:"limit"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.LibraryID = strings.TrimSpace(request.LibraryID)
	if request.LibraryID == "" {
		writeError(w, 400, "INVALID_LIBRARY", "libraryId is required")
		return
	}
	run, err := s.covers.Start(r.Context(), request.LibraryID, request.All, request.Limit)
	if err != nil {
		status := http.StatusConflict
		code := "COVER_JOB_START_FAILED"
		if errors.Is(err, sql.ErrNoRows) {
			status, code = http.StatusNotFound, "NOT_FOUND"
		} else if !errors.Is(err, thumbnail.ErrBatchAlreadyQueued) &&
			strings.Contains(err.Error(), "limit") {
			status, code = http.StatusBadRequest, "INVALID_LIMIT"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) cancelCoverJob(w http.ResponseWriter, r *http.Request) {
	if err := s.covers.Cancel(r.Context(), chi.URLParam(r, "runID")); err != nil {
		writeError(w, 404, "NOT_FOUND", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMaintenanceJobs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.tasks.Runs(r.Context(), maintenance.ListFilter{
		TargetType: strings.TrimSpace(r.URL.Query().Get("targetType")),
		TargetID:   strings.TrimSpace(r.URL.Query().Get("targetId")),
		Limit:      intQuery(r, "limit", 20),
	})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, runs)
}

func (s *Server) startMaintenanceJob(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Operation string `json:"operation"`
		SeriesID  string `json:"seriesId"`
		BookID    string `json:"bookId"`
		Force     bool   `json:"force"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Operation = strings.TrimSpace(request.Operation)
	request.SeriesID = strings.TrimSpace(request.SeriesID)
	request.BookID = strings.TrimSpace(request.BookID)
	var (
		run maintenance.Run
		err error
	)
	switch request.Operation {
	case maintenance.OperationBookIndex:
		if request.BookID == "" {
			writeError(w, http.StatusBadRequest, "INVALID_TARGET", "bookId is required")
			return
		}
		run, err = s.tasks.StartBookIndex(r.Context(), request.BookID, request.Force)
	case maintenance.OperationSeriesIndex:
		if request.SeriesID == "" {
			writeError(w, http.StatusBadRequest, "INVALID_TARGET", "seriesId is required")
			return
		}
		run, err = s.tasks.StartSeriesIndex(r.Context(), request.SeriesID, request.Force)
	case maintenance.OperationSeriesThumbnail:
		if request.SeriesID == "" {
			writeError(w, http.StatusBadRequest, "INVALID_TARGET", "seriesId is required")
			return
		}
		run, err = s.tasks.StartSeriesThumbnail(r.Context(), request.SeriesID, request.Force)
	default:
		writeError(w, http.StatusBadRequest, "INVALID_OPERATION", "Unknown maintenance operation.")
		return
	}
	if err != nil {
		status := http.StatusConflict
		code := "MAINTENANCE_JOB_START_FAILED"
		if errors.Is(err, sql.ErrNoRows) {
			status, code = http.StatusNotFound, "NOT_FOUND"
		} else if errors.Is(err, maintenance.ErrJobAlreadyQueued) {
			status, code = http.StatusConflict, "JOB_ALREADY_RUNNING"
		} else if errors.Is(err, maintenance.ErrInvalidOperation) {
			status, code = http.StatusBadRequest, "INVALID_OPERATION"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) cancelMaintenanceJob(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.Cancel(r.Context(), chi.URLParam(r, "runID")); err != nil {
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
	bgmThumbStats, _ := s.thumbs.BangumiStats(r.Context())
	writeJSON(w, 200, map[string]any{
		"entries":          stats,
		"archiveIndexes":   indexes,
		"zipIndexes":       indexes,
		"seriesThumbnails": thumbnailStats,
		"bangumiCovers":    bgmThumbStats,
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
	if cacheType == "bangumi" {
		if err := s.thumbs.ClearBangumiCovers(r.Context()); err != nil {
			writeError(w, 500, "CACHE_CLEAR_FAILED", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if cacheType != cache.TypeRange && cacheType != cache.TypePage && cacheType != "all" {
		writeError(w, 400, "INVALID_CACHE_TYPE", "Use range, page, archive, thumbnail, bangumi, or all.")
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
		_ = s.thumbs.ClearBangumiCovers(r.Context())
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Rich Detail & Bangumi Handlers ---

func (s *Server) seriesRichDetail(w http.ResponseWriter, r *http.Request) {
	series, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	library, _ := s.store.LibraryBasic(r.Context(), series.LibraryID)
	meta, _ := s.store.SeriesMetadataByID(r.Context(), series)
	progress, _ := s.store.SeriesReadProgress(r.Context(), series.ID)
	books, _, err := s.store.BooksPage(r.Context(), database.BookQuery{SeriesID: series.ID, Unpaged: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	bookDTOList, err := s.bookDTOs(r, books)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	pageProgresses, _ := s.store.BookPageProgressesBySeries(r.Context(), series.ID)
	indexStats, _ := s.store.ArchiveIndexStatsBySeries(r.Context(), series.ID)
	downloadStats, _ := s.store.BookDownloadStatsBySeries(r.Context(), series.ID)

	var totalSize int64
	currentVersions := make(map[string]string, len(books))
	bookExtras := make(map[string]any, len(books))
	for _, b := range books {
		totalSize += b.Size
		ver := archive.BookVersion(b)
		currentVersions[b.ID] = ver
		bookExtras[b.ID] = map[string]any{
			"inferredProgress": pageProgressInline(pageProgresses[b.ID]),
			"indexStatus":      indexStatusInline(indexStats[b.ID], ver),
			"downloadStat":     downloadStatInline(downloadStats[b.ID]),
		}
	}
	perf, _ := s.store.SeriesPerformance(r.Context(), series.ID, currentVersions)
	if coverStats, hasCover, _ := s.thumbs.SeriesStats(r.Context(), series.ID); hasCover && coverStats.GenerationDuration > 0 {
		perf.HasCoverDuration = true
		perf.CoverDuration = coverStats.GenerationDuration
		perf.CoverCompletedAt = coverStats.UpdatedAt
	}
	var inferred map[string]any
	if latest, hasLatest, _ := s.store.LatestBookPageProgressInSeries(r.Context(), series.ID); hasLatest {
		inferred = map[string]any{
			"bookId":   latest.BookID,
			"bookName": latest.BookName,
			"last":     inferredProgressText(latest.BookName, latest.LastLoadedPage, latest.PageCount),
			"max":      inferredProgressText(latest.BookName, latest.MaxLoadedPage, latest.PageCount),
			"updated":  latest.UpdatedAt.UTC().Format(time.RFC3339),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"series":       seriesDTOFull(series, progress, meta),
		"rawMeta":      meta,
		"originalName": series.Name,
		"relativePath": series.RelativePath,
		"library": map[string]any{
			"id":           library.ID,
			"name":         library.Name,
			"providerType": library.ProviderType,
			"oneShot":      library.OneShot,
		},
		"totalSize":    formatBytes(totalSize),
		"books":        bookDTOList,
		"bookExtras":   bookExtras,
		"inferred":     inferred,
		"indexSummary": seriesIndexSummary(perf),
		"coverSummary": seriesCoverSummary(perf),
		"downloadStat": seriesDownloadSummary(perf),
	})
}

func (s *Server) bookRichDetail(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Book not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	series, err := s.store.SeriesByID(r.Context(), book.SeriesID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	library, _ := s.store.LibraryBasic(r.Context(), book.LibraryID)
	seriesMeta, _ := s.store.SeriesMetadataByID(r.Context(), series)
	progress, hasProgress, _ := s.store.BookReadProgress(r.Context(), book.ID)
	pageProgress, hasPageProgress, _ := s.store.BookPageProgress(r.Context(), book.ID)
	indexStats, hasIndexStats, _ := s.store.ArchiveIndexStats(r.Context(), book.ID)
	downloadStats, hasDownloadStats, _ := s.store.BookDownloadStats(r.Context(), book.ID)

	var prevBookID, nextBookID string
	if prev, err := s.store.PreviousBookInSeries(r.Context(), book); err == nil {
		prevBookID = prev.ID
	}
	if next, err := s.store.NextBookInSeries(r.Context(), book); err == nil {
		nextBookID = next.ID
	}

	currentVer := archive.BookVersion(book)
	indexText := "尚未建立当前文件版本索引"
	indexDuration := ""
	indexUpdated := ""
	if hasIndexStats && indexStats.Version == currentVer {
		indexText = strconv.Itoa(indexStats.PageCount) + " 页（已建索引）"
		if indexStats.HasDuration {
			indexDuration = formatDuration(indexStats.Duration)
		}
		indexUpdated = webTime(indexStats.CompletedAt)
	} else if hasIndexStats {
		indexText = "已有旧文件版本索引，打开或重建后自动更新"
	}

	dlSpeed := "尚无真实 Range 下载统计"
	dlBytes := ""
	dlUpdated := ""
	if hasDownloadStats && downloadStats.HasDownload {
		dlSpeed = formatSpeed(downloadStats.Bytes, downloadStats.Duration)
		dlBytes = formatBytes(downloadStats.Bytes)
		dlUpdated = webTime(downloadStats.UpdatedAt)
	}

	var inferred map[string]any
	if hasPageProgress {
		inferred = map[string]any{
			"last":    pageNumberText(pageProgress.LastLoadedPage, pageProgress.PageCount),
			"max":     pageNumberText(pageProgress.MaxLoadedPage, pageProgress.PageCount),
			"updated": webTime(pageProgress.UpdatedAt),
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"book":           bookDTOWithProgressAndMeta(book, series, seriesMeta, progress, hasProgress),
		"seriesId":       series.ID,
		"seriesTitle":    seriesMeta.Title,
		"originalSeries": series.Name,
		"libraryName":    library.Name,
		"providerType":   library.ProviderType,
		"relativePath":   webBookPath(series, book),
		"archiveType":    webArchiveType(book.Name),
		"previousBookId": prevBookID,
		"nextBookId":     nextBookID,
		"indexStatus":    indexText,
		"indexDuration":  indexDuration,
		"indexUpdated":   indexUpdated,
		"downloadSpeed":  dlSpeed,
		"downloadBytes":  dlBytes,
		"downloadAt":     dlUpdated,
		"inferred":       inferred,
	})
}

func (s *Server) bangumiStatus(w http.ResponseWriter, r *http.Request) {
	matched, unmatched, total, _ := s.store.ScrapeSummary(r.Context())
	var arch bangumi.ArchiveStatus
	proxyURL := ""
	if s.bangumi != nil {
		arch = s.bangumi.ArchiveStatus()
		proxyURL = s.bangumi.RedactedProxyURL(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"archive":        arch,
		"proxyUrl":       proxyURL,
		"matchedCount":   matched,
		"unmatchedCount": unmatched,
		"pendingCount":   max(int64(0), total-matched-unmatched),
		"totalSeries":    total,
	})
}

func (s *Server) putBangumiConfig(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	var req struct {
		ProxyURL string `json:"proxyUrl"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	redacted, err := s.bangumi.SetProxyURL(r.Context(), req.ProxyURL)
	if err != nil {
		if errors.Is(err, bangumi.ErrInvalidProxyURL) {
			writeError(w, http.StatusBadRequest, "INVALID_PROXY_URL", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "BANGUMI_CONFIG_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"proxyUrl": redacted})
}

func (s *Server) testBangumiProxy(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	var req struct {
		ProxyURL *string `json:"proxyUrl"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	report, err := s.bangumi.TestConnectivity(r.Context(), req.ProxyURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PROXY_URL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) bangumiSubjectCover(w http.ResponseWriter, r *http.Request) {
	subjectID, err := strconv.ParseInt(chi.URLParam(r, "subjectID"), 10, 64)
	if err != nil || subjectID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_SUBJECT", "Invalid Bangumi subject ID.")
		return
	}
	coverURL := strings.TrimSpace(r.URL.Query().Get("u"))
	if !bangumi.IsAllowedBangumiCoverURL(coverURL) {
		coverURL = ""
	}
	data, err := s.thumbs.GetOrFetchBangumiCover(r.Context(), subjectID, coverURL)
	if err != nil {
		s.writeUncachedPlaceholder(w)
		return
	}
	w.Header().Set("Content-Type", data.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(data.Bytes)))
	_, _ = w.Write(data.Bytes)
}

func (s *Server) startBangumiArchiveUpdate(w http.ResponseWriter, _ *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	st, err := s.bangumi.StartArchiveUpdate()
	if err != nil {
		writeError(w, http.StatusConflict, "ARCHIVE_UPDATE_ACTIVE", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) cancelBangumiArchiveUpdate(w http.ResponseWriter, _ *http.Request) {
	if s.bangumi == nil || !s.bangumi.CancelArchiveUpdate() {
		writeError(w, http.StatusNotFound, "NOT_RUNNING", "No active Bangumi archive download.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listBangumiRuns(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	runs, err := s.bangumi.Runs(r.Context(), intQuery(r, "limit", 30))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) startBangumiRun(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	var req struct {
		LibraryID string `json:"libraryId"`
		Force     bool   `json:"force"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	run, err := s.bangumi.StartBatch(r.Context(), req.LibraryID, req.Force)
	if err != nil {
		status := http.StatusConflict
		code := "BANGUMI_SCRAPE_CONFLICT"
		if errors.Is(err, sql.ErrNoRows) {
			status, code = http.StatusNotFound, "NOT_FOUND"
		}
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) cancelBangumiRun(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Bangumi service unavailable.")
		return
	}
	if err := s.bangumi.CancelBatch(r.Context(), chi.URLParam(r, "runID")); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) searchSeriesBangumi(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	series, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
		return
	}
	candidates, parsed, err := s.bangumi.SearchCandidates(r.Context(), series, r.URL.Query().Get("q"))
	if err != nil {
		if errors.Is(err, bangumi.ErrNSFWBlocked) {
			writeError(w, http.StatusUnprocessableEntity, "NSFW_BLOCKED", "NSFW Bangumi 条目已被禁用。")
			return
		}
		writeError(w, http.StatusBadGateway, "BANGUMI_SEARCH_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"parsed":     parsed,
		"candidates": candidates,
	})
}

func (s *Server) bindSeriesBangumi(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	series, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
		return
	}
	var req struct {
		SubjectID int64 `json:"subjectId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.SubjectID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_SUBJECT", "subjectId must be positive.")
		return
	}
	meta, err := s.bangumi.BindSubject(r.Context(), series, req.SubjectID)
	if err != nil {
		if errors.Is(err, bangumi.ErrNSFWBlocked) {
			writeError(w, http.StatusUnprocessableEntity, "NSFW_BLOCKED", "不支持绑定 NSFW 条目。")
			return
		}
		writeError(w, http.StatusBadGateway, "BANGUMI_BIND_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) autoMatchSeriesBangumi(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	series, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
		return
	}
	matched, meta, err := s.bangumi.AutoScrapeOne(r.Context(), series)
	if err != nil {
		writeError(w, http.StatusBadGateway, "BANGUMI_SCRAPE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"matched":  matched,
		"metadata": meta,
	})
}

func (s *Server) clearSeriesBangumi(w http.ResponseWriter, r *http.Request) {
	series, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
		return
	}
	meta, err := s.store.ClearSeriesBangumiBinding(r.Context(), series)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meta)
}
