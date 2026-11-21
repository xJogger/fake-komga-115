package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func TestV24DiagnosticsAndBangumiArchiveScheduleEndpoints(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := database.Open(filepath.Join(dir, "v24.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	diagRec, err := diagnostics.New(dir, false)
	if err != nil {
		t.Fatalf("diagnostics.New: %v", err)
	}
	cacheMgr, _ := cache.New(store, filepath.Join(dir, "cache"))
	client := oneonefive.New(store, logger)
	wdMgr := webdav.NewManager()
	scanMgr := scanner.NewWithWebDAV(store, client, wdMgr, logger)
	defer scanMgr.Close()
	archSvc := archive.NewServiceWithWebDAV(store, client, wdMgr, cacheMgr, logger)
	defer archSvc.Close()
	bgmSvc, err := bangumi.NewService(store, filepath.Join(dir, "bgm"), logger)
	if err != nil {
		t.Fatalf("bangumi.NewService: %v", err)
	}
	defer bgmSvc.Close()
	thumbSvc, _ := thumbnail.New(store, filepath.Join(dir, "thumbs"), logger)
	coverMgr := thumbnail.NewBatchManager(store, archSvc, thumbSvc, logger)
	defer coverMgr.Close()
	maintMgr := maintenance.New(store, archSvc, thumbSvc, logger)
	defer maintMgr.Close()

	srv := New(store, client, scanMgr, cacheMgr, archSvc, thumbSvc, coverMgr, maintMgr, RuntimeInfo{DataDir: dir}, logger).
		WithServices(wdMgr, bgmSvc).
		WithDiagnostics(diagRec)

	// Setup admin user so authenticated routes work via Basic Auth.
	setupBody := bytes.NewBufferString(`{"username":"admin","password":"password123"}`)
	setupReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/setup", setupBody)
	setupRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(setupRec, setupReq)
	if setupRec.Code != http.StatusOK && setupRec.Code != http.StatusCreated {
		t.Fatalf("setup code=%d body=%s", setupRec.Code, setupRec.Body.String())
	}

	doAuthed := func(method, target, body string) *httptest.ResponseRecorder {
		var rBody io.Reader
		if body != "" {
			rBody = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, rBody)
		req.SetBasicAuth("admin", "password123")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// 1. Diagnostics toggle, preview, export & clear.
	if rec := doAuthed(http.MethodPut, "/admin/api/diagnostics/config", `{"verbose":true}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT diagnostics/config code=%d body=%s", rec.Code, rec.Body.String())
	}
	diagRec.Record(diagnostics.Event{Level: diagnostics.LevelError, Component: "webdav", Stage: "range", Detail: "failed http://user:pass@10.0.0.1/dav/a.cbz"})

	listRec := doAuthed(http.MethodGet, "/admin/api/diagnostics?limit=20", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET diagnostics code=%d", listRec.Code)
	}
	if strings.Contains(listRec.Body.String(), "user:pass") || strings.Contains(listRec.Body.String(), "10.0.0.1") {
		t.Fatalf("diagnostics JSON leaked sensitive data: %s", listRec.Body.String())
	}

	expRec := doAuthed(http.MethodGet, "/admin/api/diagnostics/export", "")
	if expRec.Code != http.StatusOK || !strings.Contains(expRec.Body.String(), "# MangaAlpha Diagnostic Report") {
		t.Fatalf("GET diagnostics/export code=%d body=%s", expRec.Code, expRec.Body.String())
	}

	if rec := doAuthed(http.MethodDelete, "/admin/api/diagnostics", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE diagnostics code=%d", rec.Code)
	}

	// 2. Bangumi archive schedule GET/PUT + status inclusion.
	putSched := doAuthed(http.MethodPut, "/admin/api/bangumi/archive/schedule",
		`{"enabled":true,"frequency":"weekly","dayOfWeek":3,"dayOfMonth":1,"timeOfDay":"06:00"}`)
	if putSched.Code != http.StatusOK {
		t.Fatalf("PUT archive/schedule code=%d body=%s", putSched.Code, putSched.Body.String())
	}
	var saved bangumi.ArchiveSchedule
	if err := json.Unmarshal(putSched.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode schedule: %v", err)
	}
	if !saved.Enabled || saved.Frequency != "weekly" || saved.DayOfWeek != 3 || saved.TimeOfDay != "06:00" || saved.NextRunAt == "" {
		t.Fatalf("unexpected saved schedule: %+v", saved)
	}

	bgmSt := doAuthed(http.MethodGet, "/admin/api/bangumi/status", "")
	if bgmSt.Code != http.StatusOK || !strings.Contains(bgmSt.Body.String(), `"archiveSchedule"`) {
		t.Fatalf("GET bangumi/status missing archiveSchedule: code=%d body=%s", bgmSt.Code, bgmSt.Body.String())
	}

	// 3. Agent API mirrors diagnostics and archive schedule endpoints.
	if rec := doAuthed(http.MethodGet, "/api/agent/v1/bangumi/archive/schedule", ""); rec.Code != http.StatusOK {
		t.Fatalf("Agent GET archive/schedule code=%d", rec.Code)
	}
	if rec := doAuthed(http.MethodGet, "/api/agent/v1/diagnostics", ""); rec.Code != http.StatusOK {
		t.Fatalf("Agent GET diagnostics code=%d", rec.Code)
	}
}
