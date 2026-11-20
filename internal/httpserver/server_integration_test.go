package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/id"
	"github.com/xJogger/fake-komga-115/internal/maintenance"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func TestV2AuthAndMihonKomgaContract(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	libraryID := id.Library("root")
	seriesID := id.Series(libraryID, "series-cid")
	bookID := id.Book(libraryID, "file-id")
	now := time.Now().UTC().Format(time.RFC3339Nano)

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
	thumbnailService, err := thumbnail.New(store, filepath.Join(tempDir, "thumbnails"), logger)
	if err != nil {
		t.Fatal(err)
	}
	thumbnailService.SetBangumiCoverFetcher(bangumiSvc.FetchCoverBytes)
	archiveService := archive.NewServiceWithWebDAV(store, client, webdavMgr, cacheManager, logger)
	defer archiveService.Close()
	coverManager := thumbnail.NewBatchManager(
		store, archiveService, thumbnailService, logger,
	)
	defer coverManager.Close()
	maintenanceManager := maintenance.New(
		store, archiveService, thumbnailService, logger,
	)
	defer maintenanceManager.Close()

	handler := New(
		store, client, scanManager, cacheManager,
		archiveService, thumbnailService, coverManager,
		maintenanceManager,
		RuntimeInfo{
			DataDir: "/tmp/fake-komga-115-test-data",
			Host:    "127.0.0.1",
			Port:    25600,
		}, logger,
	).WithServices(webdavMgr, bangumiSvc).Handler()

	server := httptest.NewServer(handler)
	defer server.Close()

	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// 1. Unclaimed state verification
	resp, err := noRedirect.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health check must remain public, got %d", resp.StatusCode)
	}

	resp, err = noRedirect.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != "/setup" {
		t.Fatalf("unclaimed / expected 307 -> /setup, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, err = noRedirect.Get(server.URL + "/api/v1/libraries")
	if err != nil {
		t.Fatal(err)
	}
	var errBody map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || errBody["error"] != "SETUP_REQUIRED" {
		t.Fatalf("unclaimed API expected 401 SETUP_REQUIRED, got %d %#v", resp.StatusCode, errBody)
	}

	var claimStatus map[string]bool
	resp, err = noRedirect.Get(server.URL + "/api/v1/claim")
	if err != nil {
		t.Fatal(err)
	}
	decodeResponseJSON(t, resp, &claimStatus)
	if claimStatus["isClaimed"] {
		t.Fatalf("fresh DB must report isClaimed=false")
	}

	// 2. Claim admin via /admin/api/auth/setup with custom username
	jar, _ := cookiejar.New(nil)
	sessionClient := &http.Client{Jar: jar}

	const adminUser = "manga_owner"
	const adminPass = "CorrectHorseBattery99"

	setupResp := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/auth/setup", map[string]any{
		"username": adminUser,
		"password": adminPass,
	})
	var foundPermCookie bool
	for _, ck := range setupResp.Cookies() {
		if ck.Name == sessionCookieName && ck.MaxAge == cookieMaxAgeSeconds {
			foundPermCookie = true
		}
	}
	setupResp.Body.Close()
	if setupResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup expected 201, got %d", setupResp.StatusCode)
	}
	if !foundPermCookie {
		t.Fatalf("expected permanent session cookie with MaxAge=%d", cookieMaxAgeSeconds)
	}

	var sessions []database.AuthSession
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/auth/sessions", &sessions)
	if len(sessions) != 1 || sessions[0].ExpiresAt != database.PermanentSessionExpiresAt {
		t.Fatalf("expected permanent session in DB, got %+v", sessions)
	}

	// Verify Bangumi proxy config save + redaction + subject cover preview endpoint
	bgmCfgResp := requestJSONWithClient(t, sessionClient, http.MethodPut, server.URL+"/admin/api/bangumi/config", map[string]string{
		"proxyUrl": "socks5://proxyuser:SuperSecret@127.0.0.1:1080",
	})
	bgmCfgResp.Body.Close()
	if bgmCfgResp.StatusCode != http.StatusOK {
		t.Fatalf("put bangumi config status=%d", bgmCfgResp.StatusCode)
	}
	var bgmSt map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/bangumi/status", &bgmSt)
	if bgmSt["proxyUrl"] != "socks5://proxyuser:xxxxx@127.0.0.1:1080" {
		t.Fatalf("expected redacted proxyUrl, got %#v", bgmSt["proxyUrl"])
	}
	clearProxyResp := requestJSONWithClient(t, sessionClient, http.MethodPut, server.URL+"/admin/api/bangumi/config", map[string]string{
		"proxyUrl": "",
	})
	clearProxyResp.Body.Close()

	mockCoverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testCoverPNG(t))
	}))
	defer mockCoverSrv.Close()
	coverPreviewResp, err := sessionClient.Get(server.URL + "/admin/api/bangumi/subjects/24596/cover?u=" + mockCoverSrv.URL + "/cover.png")
	if err != nil {
		t.Fatal(err)
	}
	coverPreviewResp.Body.Close()
	if coverPreviewResp.StatusCode != http.StatusOK || coverPreviewResp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("bangumi candidate cover preview status=%d type=%q",
			coverPreviewResp.StatusCode, coverPreviewResp.Header.Get("Content-Type"))
	}

	// Second setup attempt must be permanently locked (409)
	dupSetup := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/auth/setup", map[string]any{
		"username": "intruder",
		"password": "AnotherPassword123",
	})
	dupSetup.Body.Close()
	if dupSetup.StatusCode != http.StatusConflict {
		t.Fatalf("second setup must return 409, got %d", dupSetup.StatusCode)
	}

	// 3. Unauthenticated behavior & WWW-Authenticate header check
	unauthAPI, err := noRedirect.Get(server.URL + "/api/v1/libraries")
	if err != nil {
		t.Fatal(err)
	}
	unauthAPI.Body.Close()
	if unauthAPI.StatusCode != http.StatusUnauthorized ||
		!strings.HasPrefix(unauthAPI.Header.Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("Mihon unauthenticated API must return 401 + WWW-Authenticate Basic, got %d %q",
			unauthAPI.StatusCode, unauthAPI.Header.Get("WWW-Authenticate"))
	}

	xhrReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/libraries", nil)
	xhrReq.Header.Set("X-FK115-UI", "1")
	xhrResp, err := noRedirect.Do(xhrReq)
	if err != nil {
		t.Fatal(err)
	}
	xhrResp.Body.Close()
	if xhrResp.StatusCode != http.StatusUnauthorized || xhrResp.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("UI XHR 401 must omit WWW-Authenticate, got %d %q",
			xhrResp.StatusCode, xhrResp.Header.Get("WWW-Authenticate"))
	}

	unauthWeb, err := noRedirect.Get(server.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	unauthWeb.Body.Close()
	if unauthWeb.StatusCode != http.StatusTemporaryRedirect ||
		!strings.HasPrefix(unauthWeb.Header.Get("Location"), "/login?redirect=") {
		t.Fatalf("unauthenticated /admin expected redirect to /login, got %d %q",
			unauthWeb.StatusCode, unauthWeb.Header.Get("Location"))
	}

	// 4. HTTP Basic Auth + API Key checks
	basicReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/users/me", nil)
	basicReq.SetBasicAuth(adminUser, adminPass)
	basicResp, err := noRedirect.Do(basicReq)
	if err != nil {
		t.Fatal(err)
	}
	var me map[string]any
	decodeResponseJSON(t, basicResp, &me)
	if me["email"] != adminUser {
		t.Fatalf("Basic Auth /api/v1/users/me email=%v, want %q", me["email"], adminUser)
	}

	var createdKey struct {
		Key    string `json:"key"`
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	akResp := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/auth/api-keys", map[string]string{
		"name": "Mihon Tablet",
	})
	if akResp.StatusCode != http.StatusCreated {
		akResp.Body.Close()
		t.Fatalf("create API key status=%d", akResp.StatusCode)
	}
	if err := json.NewDecoder(akResp.Body).Decode(&createdKey); err != nil {
		akResp.Body.Close()
		t.Fatal(err)
	}
	akResp.Body.Close()

	keyReq, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/libraries", nil)
	keyReq.Header.Set("X-API-Key", createdKey.Key)
	keyResp, err := noRedirect.Do(keyReq)
	if err != nil {
		t.Fatal(err)
	}
	keyResp.Body.Close()
	if keyResp.StatusCode != http.StatusOK {
		t.Fatalf("X-API-Key auth failed: status=%d", keyResp.StatusCode)
	}

	delKeyReq, _ := http.NewRequest(http.MethodDelete, server.URL+"/admin/api/auth/api-keys/"+createdKey.Record.ID, nil)
	delKeyResp, err := sessionClient.Do(delKeyReq)
	if err != nil {
		t.Fatal(err)
	}
	delKeyResp.Body.Close()
	if delKeyResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete API key status=%d", delKeyResp.StatusCode)
	}
	revokedResp, err := noRedirect.Do(keyReq)
	if err != nil {
		t.Fatal(err)
	}
	revokedResp.Body.Close()
	if revokedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked API key must return 401, got %d", revokedResp.StatusCode)
	}

	// 5. Fresh authenticated endpoints & settings
	for _, path := range []string{
		"/admin/api/libraries",
		"/admin/api/scans",
		"/admin/api/cover-jobs",
	} {
		var empty []any
		getJSONWithClient(t, sessionClient, server.URL+path, &empty)
		if empty == nil || len(empty) != 0 {
			t.Fatalf("fresh installation endpoint %s must return [], got %#v", path, empty)
		}
	}
	var settings map[string]string
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/settings", &settings)
	if settings["volume_index_prefetch_enabled"] != "false" ||
		settings["volume_index_prefetch_remaining_pages"] != "10" {
		t.Fatalf("unexpected volume prefetch defaults: %#v", settings)
	}
	response := requestJSONWithClient(t, sessionClient, http.MethodPut, server.URL+"/admin/api/settings", map[string]string{
		"volume_index_prefetch_enabled":         "true",
		"volume_index_prefetch_remaining_pages": "12",
	})
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("valid volume prefetch settings status=%d", response.StatusCode)
	}
	response.Body.Close()

	// 6. Seed 115 One-Shot library + test Local library creation
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: libraryID, Name: "Comics", ProviderType: database.Provider115, RootCID: "root", Enabled: true, OneShot: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,1,?,?,'scan')`,
		seriesID, libraryID, "series-cid", "One Shot", "Root/One Shot.cbz", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,'001.cbz',1234,'pick','sha',?,?,1,?,?,'scan')`,
		bookID, seriesID, libraryID, "file-id", "series-cid", now, now, now, now); err != nil {
		t.Fatal(err)
	}

	localComicsDir := filepath.Join(tempDir, "local-comics")
	if err := os.MkdirAll(localComicsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	localLibResp := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/libraries", map[string]any{
		"name":         "Local Manga",
		"providerType": "local",
		"rootPath":     localComicsDir,
		"enabled":      false,
	})
	if localLibResp.StatusCode != http.StatusCreated {
		localLibResp.Body.Close()
		t.Fatalf("create local library status=%d", localLibResp.StatusCode)
	}
	localLibResp.Body.Close()

	var status map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/status", &status)
	if status["comicBytes"].(float64) != 1234 {
		t.Fatalf("unexpected total comic bytes: %#v", status)
	}
	if status["dataDir"] != "/tmp/fake-komga-115-test-data" {
		t.Fatalf("unexpected data directory: %#v", status)
	}

	var capabilities map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/server/capabilities", &capabilities)
	features := capabilities["features"].(map[string]any)
	if capabilities["name"] != "MangaAlpha" ||
		features["authentication"] != true ||
		features["builtInReader"] != true ||
		features["bangumiMetadata"] != true ||
		features["bookSiblingNavigation"] != true ||
		features["collections"] != true ||
		features["readLists"] != true ||
		features["sse"] != true ||
		features["gzip"] != true {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}

	var komgaLibraries []map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/libraries", &komgaLibraries)
	if len(komgaLibraries) != 1 || komgaLibraries[0]["oneshotsDirectory"] != "." {
		t.Fatalf("unexpected enabled Komga library: %#v", komgaLibraries)
	}

	// 7. Series Metadata PATCH + Taxonomy & Search verification
	metaPatch := requestJSONWithClient(t, sessionClient, http.MethodPatch, server.URL+"/api/v1/series/"+seriesID+"/metadata", map[string]any{
		"title":     "短篇杰作集",
		"titleLock": true,
		"publisher": "集英社",
		"genres":    []string{"奇幻", "冒险"},
		"tags":      []string{"短篇"},
	})
	metaPatch.Body.Close()
	if metaPatch.StatusCode != http.StatusNoContent {
		t.Fatalf("series metadata PATCH status=%d", metaPatch.StatusCode)
	}

	var genres []string
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/genres", &genres)
	if len(genres) != 2 {
		t.Fatalf("expected 2 genres, got %#v", genres)
	}

	// Both Chinese title ("短篇杰作集") and original folder name ("One Shot") must match search!
	var seriesPage map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/series?search=短篇杰作", &seriesPage)
	if seriesPage["totalElements"].(float64) != 1 {
		t.Fatalf("Chinese title search failed: %#v", seriesPage)
	}
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/series?search=One+Shot&genre=奇幻", &seriesPage)
	if seriesPage["totalElements"].(float64) != 1 {
		t.Fatalf("original folder name + genre search failed: %#v", seriesPage)
	}

	// 8. Client settings, Books, Structured search & Read progress
	response = requestJSONWithClient(t, sessionClient, http.MethodPatch, server.URL+"/api/v1/client-settings/user", map[string]any{
		"koharia.manga." + seriesID + ".viewerFlags": map[string]any{"value": "42"},
	})
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("client settings PATCH status=%d", response.StatusCode)
	}

	var structuredSeriesPage map[string]any
	response = requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/api/v1/series/list?page=0&size=10&sort=metadata.titleSort,asc", map[string]any{
		"condition": map[string]any{
			"allOf": []any{
				map[string]any{"deleted": map[string]any{"operator": "isFalse"}},
				map[string]any{"anyOf": []any{
					map[string]any{"libraryId": map[string]any{"operator": "is", "value": libraryID}},
				}},
				map[string]any{"oneShot": map[string]any{"operator": "isTrue"}},
			},
		},
		"fullTextSearch": "One",
	})
	decodeResponseJSON(t, response, &structuredSeriesPage)
	if structuredSeriesPage["totalElements"].(float64) != 1 {
		t.Fatalf("structured series search failed: %#v", structuredSeriesPage)
	}

	response = requestJSONWithClient(t, sessionClient, http.MethodPatch, server.URL+"/api/v1/books/"+bookID+"/read-progress", map[string]any{
		"page": 3,
	})
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("book page progress PATCH status=%d", response.StatusCode)
	}
	var bookDetail map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/books/"+bookID, &bookDetail)
	readProgress := bookDetail["readProgress"].(map[string]any)
	if readProgress["completed"] != false || readProgress["page"].(float64) != 3 {
		t.Fatalf("book readProgress after page sync: %#v", readProgress)
	}

	// Thumbnail + Tachiyomi v2 progress + Sibling navigation
	book, err := store.BookByID(ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	if err := thumbnailService.Generate(ctx, book, testCoverPNG(t)); err != nil {
		t.Fatal(err)
	}
	thumbResp, err := sessionClient.Get(server.URL + "/api/v1/series/" + seriesID + "/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	thumbResp.Body.Close()
	if thumbResp.StatusCode != http.StatusOK || thumbResp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("generated thumbnail status=%d type=%q", thumbResp.StatusCode, thumbResp.Header.Get("Content-Type"))
	}

	response = requestJSONWithClient(t, sessionClient, http.MethodPut, server.URL+"/api/v2/series/"+seriesID+"/read-progress/tachiyomi", map[string]any{
		"lastBookNumberSortRead": 1,
	})
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("progress PUT status=%d", response.StatusCode)
	}

	normalSeriesID := id.Series(libraryID, "normal-series")
	normalBookID := id.Book(libraryID, "normal-file")
	normalBook2ID := id.Book(libraryID, "normal-file-2")
	_, _ = store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,0,?,?,'scan')`,
		normalSeriesID, libraryID, "normal-series", "Normal Series", "Root/Normal Series", now, now)
	_, _ = store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,'Vol 01.cbz',2048,'normal-pick','normal-sha',?,?,1,?,?,'scan')`,
		normalBookID, normalSeriesID, libraryID, "normal-file", "normal-series", now, now, now, now)
	_, _ = store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,'Vol 02.cbz',4096,'normal-pick-2','normal-sha-2',?,?,2,?,?,'scan')`,
		normalBook2ID, normalSeriesID, libraryID, "normal-file-2", "normal-series", now, now, now, now)

	var sibling map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/books/"+normalBookID+"/next", &sibling)
	if sibling["id"] != normalBook2ID {
		t.Fatalf("unexpected next book: %#v", sibling)
	}
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/books/"+normalBook2ID+"/previous", &sibling)
	if sibling["id"] != normalBookID {
		t.Fatalf("unexpected previous book: %#v", sibling)
	}

	// 9. Rich detail API + SPA routes + 404 handling
	var richSeries map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/series/"+seriesID+"/detail", &richSeries)
	if richSeries["originalName"] != "One Shot" {
		t.Fatalf("rich series detail missing originalName: %#v", richSeries)
	}
	var richBook map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/books/"+normalBookID+"/detail", &richBook)
	if richBook["nextBookId"] != normalBook2ID {
		t.Fatalf("rich book detail missing nextBookId: %#v", richBook)
	}

	for _, spaRoute := range []string{"/", "/libraries", "/series/" + seriesID, "/book/" + bookID, "/books/" + bookID, "/admin"} {
		html := getTextWithClient(t, sessionClient, server.URL+spaRoute, http.StatusOK)
		if !strings.Contains(html, `/ui/app.js`) {
			t.Fatalf("SPA route %s did not render app shell", spaRoute)
		}
	}

	// Convenience /reader/book/{bookID} redirect + /reader/series/{seriesID}/book/{bookID} serving
	noRedirSession := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	readerShortResp, err := noRedirSession.Get(server.URL + "/reader/book/" + bookID + "?page=2")
	if err != nil {
		t.Fatal(err)
	}
	readerShortResp.Body.Close()
	wantReaderLoc := "/reader/series/" + seriesID + "/book/" + bookID + "?page=2"
	if readerShortResp.StatusCode != http.StatusTemporaryRedirect || readerShortResp.Header.Get("Location") != wantReaderLoc {
		t.Fatalf("/reader/book redirect = %d %q, want 307 %q",
			readerShortResp.StatusCode, readerShortResp.Header.Get("Location"), wantReaderLoc)
	}
	readerEntryResp, err := sessionClient.Get(server.URL + wantReaderLoc)
	if err != nil {
		t.Fatal(err)
	}
	readerBody, _ := io.ReadAll(readerEntryResp.Body)
	readerEntryResp.Body.Close()
	if readerEntryResp.StatusCode != http.StatusOK && readerEntryResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/reader entry unexpected status=%d", readerEntryResp.StatusCode)
	}
	if readerEntryResp.StatusCode == http.StatusOK && !strings.Contains(string(readerBody), "/reader/assets/") {
		t.Fatalf("compiled /reader HTML missing /reader/assets/ bundle reference")
	}

	// 9. Hidden library lifecycle: retains data, isolates all browse/direct/reader/API/job/stat operations, restores cleanly
	var initialAdminLibs []database.Library
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/libraries", &initialAdminLibs)
	for _, lib := range initialAdminLibs {
		if !lib.Enabled {
			r := requestJSONWithClient(t, sessionClient, http.MethodPut, server.URL+"/admin/api/libraries/"+lib.ID, map[string]any{"enabled": true})
			r.Body.Close()
		}
	}

	var statusBeforeHide map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/status", &statusBeforeHide)
	activeLibsBefore := int(statusBeforeHide["libraries"].(float64))
	activeSeriesBefore := int(statusBeforeHide["series"].(float64))

	hideResp := requestJSONWithClient(t, sessionClient, http.MethodPut, server.URL+"/admin/api/libraries/"+libraryID, map[string]any{
		"enabled": false,
	})
	var hiddenLibDTO database.Library
	decodeResponseJSON(t, hideResp, &hiddenLibDTO)
	if hiddenLibDTO.Enabled || hiddenLibDTO.Name != "Comics" || hiddenLibDTO.SeriesCount != 2 || hiddenLibDTO.BookCount != 3 {
		t.Fatalf("hidden library DTO unexpected: %+v", hiddenLibDTO)
	}

	// Admin library list still retains hidden library (sorted to bottom) with intact counts
	var adminLibs []database.Library
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/libraries", &adminLibs)
	if len(adminLibs) < 2 || adminLibs[len(adminLibs)-1].ID != libraryID || adminLibs[len(adminLibs)-1].Enabled {
		t.Fatalf("expected hidden library sorted last in admin list, got %+v", adminLibs)
	}

	// Admin status excludes hidden library from active totals and reports hiddenLibraries=1
	var statusDuringHide map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/admin/api/status", &statusDuringHide)
	if int(statusDuringHide["libraries"].(float64)) != activeLibsBefore-1 ||
		int(statusDuringHide["hiddenLibraries"].(float64)) != 1 ||
		int(statusDuringHide["series"].(float64)) != activeSeriesBefore-2 {
		t.Fatalf("status during hide unexpected: %+v", statusDuringHide)
	}

	// Direct browser routes, reader entry routes, Komga single-item routes, and rich detail routes must 404 while hidden
	for _, hiddenGetURL := range []string{
		server.URL + "/libraries/" + libraryID,
		server.URL + "/series/" + seriesID,
		server.URL + "/book/" + bookID,
		server.URL + "/books/" + bookID,
		server.URL + "/reader/book/" + bookID,
		server.URL + "/reader/series/" + seriesID + "/book/" + bookID,
		server.URL + "/api/v1/series/" + seriesID,
		server.URL + "/api/v1/series/" + seriesID + "/books",
		server.URL + "/api/v1/series/" + seriesID + "/thumbnail",
		server.URL + "/api/v1/books/" + bookID,
		server.URL + "/api/v1/books/" + bookID + "/pages",
		server.URL + "/api/v1/books/" + bookID + "/pages/1",
		server.URL + "/admin/api/series/" + seriesID + "/detail",
		server.URL + "/admin/api/books/" + bookID + "/detail",
	} {
		resp, hErr := noRedirSession.Get(hiddenGetURL)
		if hErr != nil {
			t.Fatal(hErr)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("hidden library route %s returned %d, want 404", hiddenGetURL, resp.StatusCode)
		}
	}

	hiddenProgResp := requestJSONWithClient(t, sessionClient, http.MethodPatch, server.URL+"/api/v1/books/"+bookID+"/read-progress", map[string]any{
		"page": 1,
	})
	hiddenProgResp.Body.Close()
	if hiddenProgResp.StatusCode != http.StatusNotFound {
		t.Fatalf("PATCH read-progress on hidden book status=%d, want 404", hiddenProgResp.StatusCode)
	}

	// Background scan, cover job, and maintenance job must reject hidden library
	scanHiddenResp := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/scans", map[string]any{
		"libraryId": libraryID,
	})
	scanHiddenResp.Body.Close()
	if scanHiddenResp.StatusCode != http.StatusConflict {
		t.Fatalf("scan on hidden library status=%d, want 409", scanHiddenResp.StatusCode)
	}
	coverHiddenResp := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/cover-jobs", map[string]any{
		"libraryId": libraryID,
		"all":       false,
	})
	coverHiddenResp.Body.Close()
	if coverHiddenResp.StatusCode != http.StatusConflict {
		t.Fatalf("cover job on hidden library status=%d, want 409", coverHiddenResp.StatusCode)
	}
	maintHiddenResp := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/maintenance-jobs", map[string]any{
		"operation": "series_thumbnail",
		"seriesId":  seriesID,
	})
	maintHiddenResp.Body.Close()
	if maintHiddenResp.StatusCode != http.StatusNotFound {
		t.Fatalf("maintenance job on hidden series status=%d, want 404", maintHiddenResp.StatusCode)
	}

	// Un-hide library and confirm data, thumbnail, and read progress are 100% preserved
	unhideResp := requestJSONWithClient(t, sessionClient, http.MethodPut, server.URL+"/admin/api/libraries/"+libraryID, map[string]any{
		"enabled": true,
	})
	decodeResponseJSON(t, unhideResp, &hiddenLibDTO)
	if !hiddenLibDTO.Enabled {
		t.Fatal("library should be visible again after unhide")
	}
	_ = getTextWithClient(t, sessionClient, server.URL+"/libraries/"+libraryID, http.StatusOK)
	_ = getTextWithClient(t, sessionClient, server.URL+"/series/"+seriesID, http.StatusOK)
	var restoredBook map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/books/"+bookID, &restoredBook)
	restoredProg, _ := restoredBook["readProgress"].(map[string]any)
	if restoredProg == nil || restoredProg["completed"] != true {
		t.Fatalf("expected preserved readProgress after unhide, got %#v", restoredBook["readProgress"])
	}
	restoredThumb, err := sessionClient.Get(server.URL + "/api/v1/series/" + seriesID + "/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	restoredThumb.Body.Close()
	if restoredThumb.StatusCode != http.StatusOK || restoredThumb.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("restored thumbnail status=%d type=%q", restoredThumb.StatusCode, restoredThumb.Header.Get("Content-Type"))
	}

	// 10. Agent API (/api/agent/v1) with Authorization: Bearer + NSFW default-hide & toggle lifecycle
	var agentKey struct {
		Key    string `json:"key"`
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	agentKeyResp := requestJSONWithClient(t, sessionClient, http.MethodPost, server.URL+"/admin/api/auth/api-keys", map[string]string{
		"name": "Library Agent",
	})
	if agentKeyResp.StatusCode != http.StatusCreated {
		agentKeyResp.Body.Close()
		t.Fatalf("create agent API key status=%d", agentKeyResp.StatusCode)
	}
	if err := json.NewDecoder(agentKeyResp.Body).Decode(&agentKey); err != nil {
		agentKeyResp.Body.Close()
		t.Fatal(err)
	}
	agentKeyResp.Body.Close()

	doAgentJSON := func(method, path string, body any, out any) int {
		var bodyReader io.Reader
		if body != nil {
			b, mErr := json.Marshal(body)
			if mErr != nil {
				t.Fatal(mErr)
			}
			bodyReader = bytes.NewReader(b)
		}
		req, rErr := http.NewRequest(method, server.URL+path, bodyReader)
		if rErr != nil {
			t.Fatal(rErr)
		}
		req.Header.Set("Authorization", "Bearer "+agentKey.Key)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, dErr := noRedirect.Do(req)
		if dErr != nil {
			t.Fatal(dErr)
		}
		defer resp.Body.Close()
		if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}

	var manifest map[string]any
	if st := doAgentJSON(http.MethodGet, "/api/agent/v1/manifest", nil, &manifest); st != http.StatusOK || manifest["basePath"] != "/api/agent/v1" {
		t.Fatalf("agent manifest failed: status=%d body=%#v", st, manifest)
	}

	// Mark seriesID as NSFW via Agent batch API
	var batchOut map[string]any
	if st := doAgentJSON(http.MethodPost, "/api/agent/v1/series/batch", map[string]any{
		"operations": []map[string]any{
			{
				"seriesId": seriesID,
				"action":   "patch_metadata",
				"patch": map[string]any{
					"isNsfw":   true,
					"nsfwLock": true,
				},
			},
		},
	}, &batchOut); st != http.StatusOK || batchOut["succeeded"].(float64) != 1 {
		t.Fatalf("agent batch patch_metadata failed: status=%d body=%#v", st, batchOut)
	}

	// Default show_nsfw=false: seriesID must be hidden (404) on UI, reader, and Komga API,
	// while remaining visible to Agent API (?nsfw=only)!
	for _, nsfwURL := range []string{
		server.URL + "/series/" + seriesID,
		server.URL + "/reader/series/" + seriesID + "/book/" + bookID,
		server.URL + "/api/v1/series/" + seriesID,
		server.URL + "/api/v1/books/" + bookID,
	} {
		resp, nErr := noRedirSession.Get(nsfwURL)
		if nErr != nil {
			t.Fatal(nErr)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("NSFW route %s expected 404 when show_nsfw=false, got %d", nsfwURL, resp.StatusCode)
		}
	}

	var agentNSFWPage map[string]any
	if st := doAgentJSON(http.MethodGet, "/api/agent/v1/series?nsfw=only", nil, &agentNSFWPage); st != http.StatusOK || agentNSFWPage["totalElements"].(float64) != 1 {
		t.Fatalf("agent series?nsfw=only expected 1 item, got status=%d body=%#v", st, agentNSFWPage)
	}

	// Toggle showNsfw=true via Agent settings API -> seriesID becomes visible on UI and Komga API!
	if st := doAgentJSON(http.MethodPatch, "/api/agent/v1/settings", map[string]any{"showNsfw": true}, nil); st != http.StatusOK {
		t.Fatalf("agent PATCH settings showNsfw=true status=%d", st)
	}
	var visibleNSFWSeries map[string]any
	getJSONWithClient(t, sessionClient, server.URL+"/api/v1/series/"+seriesID, &visibleNSFWSeries)
	if visibleNSFWSeries["isNsfw"] != true {
		t.Fatalf("expected isNsfw=true on visible series DTO, got %#v", visibleNSFWSeries)
	}
	_ = getTextWithClient(t, sessionClient, server.URL+"/series/"+seriesID, http.StatusOK)

	// Restore showNsfw=false
	_ = doAgentJSON(http.MethodPatch, "/api/agent/v1/settings", map[string]any{"showNsfw": false}, nil)

	_ = getTextWithClient(t, sessionClient, server.URL+"/series/non-existent", http.StatusNotFound)
	notFoundHTML := getTextWithClient(t, sessionClient, server.URL+"/missing-page", http.StatusNotFound)
	if !strings.Contains(notFoundHTML, "没有找到这个页面") {
		t.Fatal("browser 404 page missing friendly message")
	}
	apiNotFound, err := sessionClient.Get(server.URL + "/api/v1/missing-page")
	if err != nil {
		t.Fatal(err)
	}
	defer apiNotFound.Body.Close()
	if apiNotFound.StatusCode != http.StatusNotFound ||
		!strings.HasPrefix(apiNotFound.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("API 404 must remain JSON: status=%d type=%q",
			apiNotFound.StatusCode, apiNotFound.Header.Get("Content-Type"))
	}
}

func testCoverPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{R: 50, G: 100, B: 150, A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func getJSONWithClient(t *testing.T, client *http.Client, url string, target any) {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d", url, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func decodeResponseJSON(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s %s status=%d", response.Request.Method, response.Request.URL, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func getTextWithClient(t *testing.T, client *http.Client, url string, status int) string {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("GET %s status=%d, want %d", url, response.StatusCode, status)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func requestJSONWithClient(t *testing.T, client *http.Client, method, url string, value any) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
