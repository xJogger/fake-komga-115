package httpserver

import (
	"bufio"
	"compress/gzip"
	"context"
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
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func TestV22KohariaCollectionsReadListsSSEGzipAndIcons(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.Open(filepath.Join(tempDir, "v22.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if _, err := store.ClaimAdmin(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib1", Name: "Shelf", RootCID: "root1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, s := range []struct {
		id, name, title, tag, author, role string
	}{
		{"s1", "VolA", "海风之城", "治愈", "作者甲", "writer"},
		{"s2", "VolB", "星海旅人", "海风之城", "作者乙", "artist"},
	} {
		if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES(?,'lib1',?,?, ?,?,?,'scan')`, s.id, "cid-"+s.id, s.name, s.name, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().Exec(`
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES(?,?,'lib1',?,'p',?,512,'pk',1,?,?,'scan')`, s.id+"-b1", s.id, "f-"+s.id+"-1", s.name+" 01.cbz", now, now); err != nil {
			t.Fatal(err)
		}
		sr, err := store.SeriesByID(ctx, s.id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyScrapedMetadata(ctx, sr, database.ScrapedMetadata{
			SubjectID: 500 + int64(len(s.title)),
			Title:     s.title,
			TitleSort: s.title,
			Tags:      []string{s.tag},
			Authors:   []database.Author{{Name: s.author, Role: s.role}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := oneonefive.New(store, logger)
	webdavMgr := webdav.NewManager()
	scanManager := scanner.NewWithWebDAV(store, client, webdavMgr, logger)
	defer scanManager.Close()
	cacheManager, err := cache.New(store, filepath.Join(tempDir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	bangumiSvc, err := bangumi.NewService(store, filepath.Join(tempDir, "bangumi"), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer bangumiSvc.Close()
	thumbnailService, err := thumbnail.New(store, filepath.Join(tempDir, "thumbs"), logger)
	if err != nil {
		t.Fatal(err)
	}
	archiveService := archive.NewServiceWithWebDAV(store, client, webdavMgr, cacheManager, logger)
	defer archiveService.Close()
	coverManager := thumbnail.NewBatchManager(store, archiveService, thumbnailService, logger)
	defer coverManager.Close()
	maintenanceManager := maintenance.New(store, archiveService, thumbnailService, logger)
	defer maintenanceManager.Close()

	srv := New(
		store, client, scanManager, cacheManager, archiveService,
		thumbnailService, coverManager, maintenanceManager,
		RuntimeInfo{DataDir: tempDir, Host: "127.0.0.1", Port: 25600}, logger,
	).WithServices(webdavMgr, bangumiSvc)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doReq := func(method, path string, body any) *http.Response {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rdr = strings.NewReader(string(raw))
		}
		req, err := http.NewRequest(method, ts.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("admin", "password123")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 1. Agent manifest name
	resp := doReq(http.MethodGet, "/api/agent/v1/manifest", nil)
	var manifest map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if manifest["name"] != "MangaAlpha Agent API" {
		t.Fatalf("unexpected agent manifest name: %#v", manifest["name"])
	}

	// 2. Koharia "(q) OR tag:(q)" POST /api/v1/series/list + relevance sort
	resp = doReq(http.MethodPost, "/api/v1/series/list?sort=relevance,asc&size=20", map[string]any{
		"fullTextSearch": "(海风之城) OR tag:(海风之城)",
	})
	var sList struct {
		TotalElements int64            `json:"totalElements"`
		Content       []map[string]any `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sList); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sList.TotalElements != 2 || len(sList.Content) != 2 || sList.Content[0]["id"] != "s1" {
		t.Fatalf("unexpected koharia series search: total=%d content=%#v", sList.TotalElements, sList.Content)
	}

	// 3. Collections CRUD + /collections/{id}/series + /series/{id}/collections + POST /series/list collectionId condition
	resp = doReq(http.MethodPost, "/api/v1/collections", map[string]any{
		"name":      "精选收藏",
		"ordered":   true,
		"seriesIds": []string{"s2", "s1"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create collection status=%d", resp.StatusCode)
	}
	var createdCol map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&createdCol); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	colID := createdCol["id"].(string)

	resp = doReq(http.MethodGet, "/api/v1/collections/"+colID+"/series", nil)
	var colSeries struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&colSeries); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(colSeries.Content) != 2 || colSeries.Content[0]["id"] != "s2" {
		t.Fatalf("unexpected collection series order: %#v", colSeries.Content)
	}

	resp = doReq(http.MethodPost, "/api/v1/series/list", map[string]any{
		"condition": map[string]any{
			"collectionId": map[string]any{"operator": "is", "value": colID},
		},
	})
	var colFilterRes struct {
		TotalElements int64 `json:"totalElements"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&colFilterRes); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if colFilterRes.TotalElements != 2 {
		t.Fatalf("expected 2 series matching collectionId condition, got %d", colFilterRes.TotalElements)
	}

	resp = doReq(http.MethodGet, "/api/v1/series/s1/collections", nil)
	var s1Cols []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&s1Cols); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(s1Cols) != 1 || s1Cols[0]["id"] != colID {
		t.Fatalf("unexpected series collections: %#v", s1Cols)
	}

	// 4. ReadLists CRUD + books + sibling next/previous + /books/{id}/readlists
	resp = doReq(http.MethodPost, "/api/v1/readlists", map[string]any{
		"name":    "周末连读",
		"summary": "先看 s2 再看 s1",
		"ordered": true,
		"bookIds": []string{"s2-b1", "s1-b1"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create readlist status=%d", resp.StatusCode)
	}
	var createdRL map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&createdRL); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	rlID := createdRL["id"].(string)

	resp = doReq(http.MethodGet, "/api/v1/readlists/"+rlID+"/books/s2-b1/next", nil)
	var nextRLBook map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&nextRLBook); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if nextRLBook["id"] != "s1-b1" {
		t.Fatalf("expected readlist next book s1-b1, got %#v", nextRLBook["id"])
	}

	resp = doReq(http.MethodGet, "/api/v1/books/s1-b1/readlists", nil)
	var b1RLs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&b1RLs); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(b1RLs) != 1 || b1RLs[0]["id"] != rlID {
		t.Fatalf("unexpected book readlists: %#v", b1RLs)
	}

	// 5. GET /api/v2/authors paged & role-filtered
	resp = doReq(http.MethodGet, "/api/v2/authors?role=artist&page=0&size=10", nil)
	var authorsV2 struct {
		TotalElements int64             `json:"totalElements"`
		Content       []database.Author `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authorsV2); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if authorsV2.TotalElements != 1 || len(authorsV2.Content) != 1 || authorsV2.Content[0].Name != "作者乙" {
		t.Fatalf("unexpected v2/authors response: %#v", authorsV2)
	}

	// 6. Live Komga SSE stream + complete bookReadProgressDTO
	sseCtx, sseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sseCancel()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/api/v1/sse/v1/events", nil)
	sseReq.SetBasicAuth("admin", "password123")
	sseReq.Header.Set("Accept-Encoding", "gzip") // verify SSE is NEVER gzipped
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	if sseResp.Header.Get("Content-Encoding") != "" || !strings.HasPrefix(sseResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected SSE headers: ct=%q ce=%q", sseResp.Header.Get("Content-Type"), sseResp.Header.Get("Content-Encoding"))
	}
	sseReader := bufio.NewReader(sseResp.Body)
	if line, err := sseReader.ReadString('\n'); err != nil || !strings.HasPrefix(line, ": connected") {
		t.Fatalf("expected SSE connected comment, got %q err=%v", line, err)
	}
	_, _ = sseReader.ReadString('\n') // blank line

	// Trigger book read progress update (completed=true, no explicit page -> DTO must emit non-null page>=1 + timestamps)
	resp = doReq(http.MethodPatch, "/api/v1/books/s1-b1/read-progress", map[string]any{"completed": true})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch progress status=%d", resp.StatusCode)
	}
	evLine, err := sseReader.ReadString('\n')
	if err != nil || strings.TrimSpace(evLine) != "event: ReadProgressChanged" {
		t.Fatalf("expected ReadProgressChanged event, got %q err=%v", evLine, err)
	}
	dataLine, err := sseReader.ReadString('\n')
	if err != nil || !strings.Contains(dataLine, `"bookId":"s1-b1"`) {
		t.Fatalf("expected bookId in SSE data, got %q err=%v", dataLine, err)
	}
	sseCancel()

	resp = doReq(http.MethodGet, "/api/v1/books/s1-b1/read-progress", nil)
	var rp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rp["completed"] != true || rp["page"].(float64) < 1 || rp["readDate"] == "" || rp["lastModified"] == "" || rp["created"] == "" {
		t.Fatalf("incomplete bookReadProgressDTO: %#v", rp)
	}

	// 7. HTTP gzip compression for JSON & skipped for thumbnails
	rawClient := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	gzReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/series", nil)
	gzReq.SetBasicAuth("admin", "password123")
	gzReq.Header.Set("Accept-Encoding", "gzip")
	gzResp, err := rawClient.Do(gzReq)
	if err != nil {
		t.Fatal(err)
	}
	if gzResp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip Content-Encoding on JSON, got %q", gzResp.Header.Get("Content-Encoding"))
	}
	gr, err := gzip.NewReader(gzResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var gzBody map[string]any
	if err := json.NewDecoder(gr).Decode(&gzBody); err != nil {
		t.Fatal(err)
	}
	gr.Close()
	gzResp.Body.Close()

	thumbReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/collections/"+colID+"/thumbnail", nil)
	thumbReq.SetBasicAuth("admin", "password123")
	thumbReq.Header.Set("Accept-Encoding", "gzip")
	thumbResp, err := rawClient.Do(thumbReq)
	if err != nil {
		t.Fatal(err)
	}
	thumbResp.Body.Close()
	if thumbResp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("expected no gzip on thumbnail, got %q", thumbResp.Header.Get("Content-Encoding"))
	}

	// 8. Public favicon, Day & Night fish icon assets, and SPA collection/readlist routes
	for _, pubPath := range []string{"/favicon.ico", "/ui/mangaalpha-icon.png", "/ui/mangaalpha-icon-dark.png"} {
		r, err := http.Get(ts.URL + pubPath)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("public icon %s: status=%d ct=%q", pubPath, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
	for _, spaPath := range []string{"/collections", "/collections/" + colID, "/readlists", "/readlists/" + rlID} {
		r := doReq(http.MethodGet, spaPath, nil)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("SPA route %s status=%d", spaPath, r.StatusCode)
		}
	}
	r404 := doReq(http.MethodGet, "/collections/nonexistent", nil)
	r404.Body.Close()
	if r404.StatusCode != http.StatusNotFound {
		t.Fatalf("missing collection SPA route expected 404, got %d", r404.StatusCode)
	}
}
