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
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/updater"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func TestV25ArchiveSourceLoadStatusAndUpdaterEndpoints(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := database.Open(filepath.Join(dir, "v25.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Seed a library/series/book for load-status endpoint testing.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`INSERT INTO libraries(id,name,provider_type,root_cid,root_path,enabled,one_shot,created_at,updated_at) VALUES('lib1','Lib','local','local:/tmp','/tmp',1,0,?,?)`, now, now); err != nil {
		t.Fatalf("insert lib: %v", err)
	}
	if _, err := store.DB().Exec(`INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,books_count,created_at,updated_at,seen_scan_id) VALUES('ser1','lib1','s1','TestSeries','TestSeries',0,1,?,?,'scan1')`, now, now); err != nil {
		t.Fatalf("insert series: %v", err)
	}
	if _, err := store.DB().Exec(`INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,number_sort,created_at,updated_at,seen_scan_id) VALUES('book1','ser1','lib1','f1','s1','Vol01.cbz',1024,'','sha1',1,?,?,'scan1')`, now, now); err != nil {
		t.Fatalf("insert book: %v", err)
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
	updSvc := updater.NewService(store, logger)
	defer updSvc.Close()
	thumbSvc, _ := thumbnail.New(store, filepath.Join(dir, "thumbs"), logger)
	coverMgr := thumbnail.NewBatchManager(store, archSvc, thumbSvc, logger)
	defer coverMgr.Close()
	maintMgr := maintenance.New(store, archSvc, thumbSvc, logger)
	defer maintMgr.Close()

	srv := New(store, client, scanMgr, cacheMgr, archSvc, thumbSvc, coverMgr, maintMgr, RuntimeInfo{DataDir: dir}, logger).
		WithServices(wdMgr, bgmSvc).
		WithUpdater(updSvc)

	setupReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/setup", bytes.NewBufferString(`{"username":"admin","password":"password123"}`))
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

	// 1. Bangumi archive source defaults to zh_cn, switches to official/custom, rejects bad custom URL.
	rec := doAuthed(http.MethodGet, "/admin/api/bangumi/archive/source", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET archive/source code=%d body=%s", rec.Code, rec.Body.String())
	}
	var srcCfg bangumi.ArchiveSourceConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &srcCfg); err != nil {
		t.Fatalf("decode archive/source: %v", err)
	}
	if srcCfg.Source != bangumi.ArchiveSourceZH || srcCfg.MetaURL != bangumi.ChineseArchiveMetaURL {
		t.Fatalf("unexpected default archiveSource: %+v", srcCfg)
	}

	if bad := doAuthed(http.MethodPut, "/admin/api/bangumi/archive/source", `{"source":"custom","customUrl":"not-a-url"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad customUrl, got %d", bad.Code)
	}

	putRec := doAuthed(http.MethodPut, "/admin/api/bangumi/archive/source", `{"source":"official"}`)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT archive/source official code=%d body=%s", putRec.Code, putRec.Body.String())
	}
	bgmStatusRec := doAuthed(http.MethodGet, "/admin/api/bangumi/status", "")
	if bgmStatusRec.Code != http.StatusOK || !strings.Contains(bgmStatusRec.Body.String(), `"archiveSource"`) || !strings.Contains(bgmStatusRec.Body.String(), `"official"`) {
		t.Fatalf("GET bangumi/status missing official archiveSource: %s", bgmStatusRec.Body.String())
	}
	if agentSrc := doAuthed(http.MethodGet, "/api/agent/v1/bangumi/archive/source", ""); agentSrc.Code != http.StatusOK {
		t.Fatalf("Agent GET archive/source code=%d", agentSrc.Code)
	}

	// 2. Book streaming load-status endpoint (standard /pages contract untouched).
	if miss := doAuthed(http.MethodGet, "/api/v1/books/nonexistent/load-status", ""); miss.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown book load-status, got %d", miss.Code)
	}
	lsRec := doAuthed(http.MethodGet, "/api/v1/books/book1/load-status?page=2", "")
	if lsRec.Code != http.StatusOK {
		t.Fatalf("GET load-status code=%d body=%s", lsRec.Code, lsRec.Body.String())
	}
	if cc := lsRec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("expected Cache-Control: no-store, got %q", cc)
	}
	var lp archive.LoadProgress
	if err := json.Unmarshal(lsRec.Body.Bytes(), &lp); err != nil {
		t.Fatalf("decode load-status: %v", err)
	}
	if lp.BookID != "book1" || lp.Stage != "idle" || lp.Active {
		t.Fatalf("unexpected idle load-status: %+v", lp)
	}

	// 3. Cross-platform update endpoints (Admin + Agent).
	updRec := doAuthed(http.MethodGet, "/admin/api/update", "")
	if updRec.Code != http.StatusOK {
		t.Fatalf("GET /admin/api/update code=%d body=%s", updRec.Code, updRec.Body.String())
	}
	var updSt updater.Status
	if err := json.Unmarshal(updRec.Body.Bytes(), &updSt); err != nil {
		t.Fatalf("decode update status: %v", err)
	}
	if !updSt.AutoCheckEnabled || updSt.Platform == "" {
		t.Fatalf("unexpected default update status: %+v", updSt)
	}

	updCfgRec := doAuthed(http.MethodPut, "/admin/api/update/config", `{"autoCheckEnabled":false,"dismissVersion":"v9.9.9"}`)
	if updCfgRec.Code != http.StatusOK {
		t.Fatalf("PUT /admin/api/update/config code=%d body=%s", updCfgRec.Code, updCfgRec.Body.String())
	}
	if err := json.Unmarshal(updCfgRec.Body.Bytes(), &updSt); err != nil {
		t.Fatalf("decode updated config: %v", err)
	}
	if updSt.AutoCheckEnabled || updSt.DismissedVersion != "v9.9.9" {
		t.Fatalf("update config did not apply: %+v", updSt)
	}

	if agentUpd := doAuthed(http.MethodGet, "/api/agent/v1/update", ""); agentUpd.Code != http.StatusOK {
		t.Fatalf("Agent GET /api/agent/v1/update code=%d", agentUpd.Code)
	}
}
