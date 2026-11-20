package httpserver

import (
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
	"github.com/xJogger/fake-komga-115/internal/pipeline"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func TestV23UnionSourcesSchedulesBangumiJumpAndUserscript(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.Open(filepath.Join(tempDir, "v23.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if _, err := store.ClaimAdmin(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	rawAPIKey, _, err := store.CreateAPIKey(ctx, "Bangumi Script")
	if err != nil {
		t.Fatal(err)
	}
	wdAcc, err := store.UpsertWebDAVAccount(ctx, database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{Name: "NAS-WebDAV", BaseURL: "https://example.invalid/dav", Username: "u"},
		Password:      "top-secret-password",
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_local", Name: "本地漫画", ProviderType: database.ProviderLocal,
		RootCID: "local:/mnt/comics", RootPath: "/mnt/comics", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_wd", Name: "远程备份", ProviderType: database.ProviderWebDAV,
		RootCID: "webdav:" + wdAcc.ID + ":/comics", WebDAVAccountID: wdAcc.ID, RootPath: "/comics", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_other", Name: "独立115", ProviderType: database.Provider115,
		RootCID: "cid_other", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Same series in lib_local & lib_wd + another version in lib_other
	for _, row := range []struct {
		sid, libID, relPath, bid, bname string
		size                            int64
	}{
		{"s_loc", "lib_local", "LocalRoot/WorkA", "b_loc_1", "Vol 01.cbz", 2048},
		{"s_wd", "lib_wd", "DavRoot/WorkA", "b_wd_1", "Vol 01.cbz", 1024},
		{"s_wd", "lib_wd", "DavRoot/WorkA", "b_wd_2", "Vol 02.cbz", 1536},
		{"s_oth", "lib_other", "CloudRoot/WorkB", "b_oth_1", "Vol 01.cbz", 1024},
	} {
		_, _ = store.DB().Exec(`
INSERT OR IGNORE INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,?,?,'sc')`, row.sid, row.libID, row.sid, "Work", row.relPath, now, now)
		if _, err := store.DB().Exec(`
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,number_sort,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,?,?,'pk',1,?,?,'sc')`, row.bid, row.sid, row.libID, row.bid, row.sid, row.bname, row.size, now, now); err != nil {
			t.Fatal(err)
		}
	}
	sLoc, _ := store.SeriesByID(ctx, "s_loc")
	if _, err := store.ApplyScrapedMetadata(ctx, sLoc, database.ScrapedMetadata{
		SubjectID: 8888, Title: "星辰旅途", TitleSort: "星辰旅途",
	}); err != nil {
		t.Fatal(err)
	}
	sOth, _ := store.SeriesByID(ctx, "s_oth")
	if _, err := store.ApplyScrapedMetadata(ctx, sOth, database.ScrapedMetadata{
		SubjectID: 9999, Title: "双版本漫画", TitleSort: "双版本漫画",
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := oneonefive.New(store, logger)
	wdMgr := webdav.NewManager()
	scanMgr := scanner.NewWithWebDAV(store, client, wdMgr, logger)
	defer scanMgr.Close()
	cacheMgr, err := cache.New(store, filepath.Join(tempDir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	bgmSvc, err := bangumi.NewService(store, filepath.Join(tempDir, "bangumi"), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer bgmSvc.Close()
	thumbSvc, err := thumbnail.New(store, filepath.Join(tempDir, "thumbs"), logger)
	if err != nil {
		t.Fatal(err)
	}
	archSvc := archive.NewServiceWithWebDAV(store, client, wdMgr, cacheMgr, logger)
	defer archSvc.Close()
	coverMgr := thumbnail.NewBatchManager(store, archSvc, thumbSvc, logger)
	defer coverMgr.Close()
	maintMgr := maintenance.New(store, archSvc, thumbSvc, logger)
	defer maintMgr.Close()
	pipeMgr := pipeline.New(store, scanMgr, bgmSvc, coverMgr, logger)
	defer pipeMgr.Close()

	srv := New(store, client, scanMgr, cacheMgr, archSvc, thumbSvc, coverMgr, maintMgr,
		RuntimeInfo{DataDir: tempDir, Host: "127.0.0.1", Port: 25600}, logger).
		WithServices(wdMgr, bgmSvc).WithPipeline(pipeMgr)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	noRedirectClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	doReq := func(method, path, body string, useAPIKey bool) *http.Response {
		var rBody io.Reader
		if body != "" {
			rBody = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, rBody)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if useAPIKey {
			req.Header.Set("X-API-Key", rawAPIKey)
		} else {
			req.SetBasicAuth("admin", "password123")
		}
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 1. Legacy global auto-scan settings are removed and rejected
	resp := doReq(http.MethodGet, "/admin/api/settings", "", false)
	var settings map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&settings)
	resp.Body.Close()
	for _, removed := range []string{"auto_scan_enabled", "auto_scan_interval_minutes", "scan_on_startup"} {
		if _, exists := settings[removed]; exists {
			t.Fatalf("legacy setting %s should be removed from settings", removed)
		}
		rPut := doReq(http.MethodPut, "/admin/api/settings", `{"`+removed+`":"true"}`, false)
		rPut.Body.Close()
		if rPut.StatusCode != http.StatusBadRequest {
			t.Fatalf("PUT removed setting %s: status=%d want 400", removed, rPut.StatusCode)
		}
	}

	// 2. Create Union Library via Admin API
	rUnion := doReq(http.MethodPost, "/admin/api/libraries",
		`{"name":"主联合库","providerType":"union","memberLibraryIds":["lib_local","lib_wd"]}`, false)
	if rUnion.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(rUnion.Body)
		t.Fatalf("create union library status=%d body=%s", rUnion.StatusCode, b)
	}
	var unionCreated struct {
		Library database.Library           `json:"library"`
		Stats   database.UnionRefreshStats `json:"stats"`
	}
	_ = json.NewDecoder(rUnion.Body).Decode(&unionCreated)
	rUnion.Body.Close()
	if unionCreated.Stats.SeriesCount != 1 || unionCreated.Stats.BooksCount != 2 {
		t.Fatalf("unexpected union stats: %+v", unionCreated.Stats)
	}

	// 3. Komga /api/v1/libraries hides sub-libraries, exposes unionLayers + webdavAccountName without secrets
	rLibs := doReq(http.MethodGet, "/api/v1/libraries", "", false)
	rawLibs, _ := io.ReadAll(rLibs.Body)
	rLibs.Body.Close()
	if strings.Contains(string(rawLibs), "top-secret-password") {
		t.Fatalf("WebDAV password leaked in /api/v1/libraries")
	}
	if strings.Contains(string(rawLibs), `"id":"lib_local"`) || strings.Contains(string(rawLibs), `"id":"lib_wd"`) {
		t.Fatalf("sub-libraries should not appear as top-level entries in /api/v1/libraries: %s", rawLibs)
	}
	if !strings.Contains(string(rawLibs), "NAS-WebDAV") {
		t.Fatalf("expected WebDAV account name in unionLayers metadata: %s", rawLibs)
	}

	// 4. Direct delete of enrolled sub-library returns 409 LIBRARY_IN_UNION
	rDelSub := doReq(http.MethodDelete, "/admin/api/libraries/lib_local", "", false)
	rDelSub.Body.Close()
	if rDelSub.StatusCode != http.StatusConflict {
		t.Fatalf("deleting union sub-library status=%d want 409", rDelSub.StatusCode)
	}

	// 5. Series & Book rich details expose unionSeriesSources & unionSources
	uSeriesList, _, _ := store.SeriesPage(ctx, database.SeriesQuery{LibraryIDs: []string{unionCreated.Library.ID}, Size: 5})
	uBooks, _, _ := store.BooksPage(ctx, database.BookQuery{SeriesID: uSeriesList[0].ID, Unpaged: true})
	rSerDet := doReq(http.MethodGet, "/admin/api/series/"+uSeriesList[0].ID+"/detail", "", false)
	rawSerDet, _ := io.ReadAll(rSerDet.Body)
	rSerDet.Body.Close()
	if rSerDet.StatusCode != 200 || !strings.Contains(string(rawSerDet), "unionSeriesSources") || !strings.Contains(string(rawSerDet), "NAS-WebDAV") {
		t.Fatalf("seriesRichDetail missing union source annotations: status=%d body=%s", rSerDet.StatusCode, rawSerDet)
	}
	rBookDet := doReq(http.MethodGet, "/admin/api/books/"+uBooks[0].ID+"/detail", "", false)
	rawBookDet, _ := io.ReadAll(rBookDet.Body)
	rBookDet.Body.Close()
	if rBookDet.StatusCode != 200 || !strings.Contains(string(rawBookDet), "unionSources") || !strings.Contains(string(rawBookDet), "NAS-WebDAV") {
		t.Fatalf("bookRichDetail missing unionSources: status=%d body=%s", rBookDet.StatusCode, rawBookDet)
	}

	// 6. Per-library schedule PUT/GET (default cover limit = 0)
	rSched := doReq(http.MethodPut, "/admin/api/libraries/"+unionCreated.Library.ID+"/schedule",
		`{"enabled":true,"frequency":"weekly","weekday":0,"timeOfDay":"03:30","scanMemberLibraryIds":["lib_local"],"autoBangumi":true,"autoCoverLimit":0}`, false)
	if rSched.StatusCode != 200 {
		b, _ := io.ReadAll(rSched.Body)
		t.Fatalf("PUT schedule status=%d body=%s", rSched.StatusCode, b)
	}
	rSched.Body.Close()

	// 7. Bangumi lookup via X-API-Key + /bgm/{subjectID} jump (unauthed redirect, 1-match, multi-match, 0-match)
	rLookup := doReq(http.MethodGet, "/api/v1/bangumi/lookup?subject_id=8888", "", true)
	var lookupResp struct {
		Found   bool                    `json:"found"`
		Count   int                     `json:"count"`
		JumpURL string                  `json:"jumpUrl"`
		Matches []database.BangumiMatch `json:"matches"`
	}
	_ = json.NewDecoder(rLookup.Body).Decode(&lookupResp)
	rLookup.Body.Close()
	if rLookup.StatusCode != 200 || !lookupResp.Found || lookupResp.Count != 1 || lookupResp.Matches[0].SeriesID != uSeriesList[0].ID {
		t.Fatalf("bangumi lookup 8888 unexpected: status=%d body=%+v", rLookup.StatusCode, lookupResp)
	}

	// Unauthenticated /bgm/8888 -> redirect to /login?redirect=%2Fbgm%2F8888
	rUnauth, err := noRedirectClient.Get(ts.URL + "/bgm/8888")
	if err != nil {
		t.Fatal(err)
	}
	rUnauth.Body.Close()
	if rUnauth.StatusCode != http.StatusTemporaryRedirect || !strings.Contains(rUnauth.Header.Get("Location"), "/login?redirect=") {
		t.Fatalf("unauthed /bgm/8888: status=%d loc=%q", rUnauth.StatusCode, rUnauth.Header.Get("Location"))
	}

	// Authenticated /bgm/8888 (1 match) -> 302 /series/{unionSeriesID}
	rJump1 := doReq(http.MethodGet, "/bgm/8888", "", false)
	rJump1.Body.Close()
	if rJump1.StatusCode != http.StatusFound || rJump1.Header.Get("Location") != "/series/"+uSeriesList[0].ID {
		t.Fatalf("authed /bgm/8888: status=%d loc=%q", rJump1.StatusCode, rJump1.Header.Get("Location"))
	}

	// Multi-match: bind union series to 9999 too so 9999 has 2 visible matches (union + lib_other) -> 302 ?versions=1
	if _, err := store.ApplyScrapedMetadata(ctx, uSeriesList[0], database.ScrapedMetadata{
		SubjectID: 9999, Title: "双版本漫画", TitleSort: "双版本漫画",
	}); err != nil {
		t.Fatal(err)
	}
	rJumpMulti := doReq(http.MethodGet, "/bgm/9999", "", false)
	rJumpMulti.Body.Close()
	if rJumpMulti.StatusCode != http.StatusFound || !strings.HasSuffix(rJumpMulti.Header.Get("Location"), "?versions=1") {
		t.Fatalf("multi-match /bgm/9999: status=%d loc=%q", rJumpMulti.StatusCode, rJumpMulti.Header.Get("Location"))
	}

	// 0-match: /bgm/123456?title=测试漫画 -> 404 HTML page with search link
	rJumpMiss := doReq(http.MethodGet, "/bgm/123456?title=%E6%B5%8B%E8%AF%95%E6%BC%AB%E7%94%BB", "", false)
	missBody, _ := io.ReadAll(rJumpMiss.Body)
	rJumpMiss.Body.Close()
	if rJumpMiss.StatusCode != http.StatusNotFound || !strings.Contains(string(missBody), "/libraries?search=") {
		t.Fatalf("0-match /bgm/123456: status=%d body=%s", rJumpMiss.StatusCode, missBody)
	}

	// 8. Userscript endpoint returns valid Tampermonkey script with server origin & empty default API key
	rUS := doReq(http.MethodGet, "/admin/api/bangumi/userscript.user.js", "", false)
	usBody, _ := io.ReadAll(rUS.Body)
	rUS.Body.Close()
	if rUS.StatusCode != 200 || !strings.Contains(string(usBody), "==UserScript==") ||
		!strings.Contains(string(usBody), ts.URL) || !strings.Contains(string(usBody), "const DEFAULT_API_KEY = '';") {
		t.Fatalf("unexpected userscript response: status=%d len=%d", rUS.StatusCode, len(usBody))
	}
}
