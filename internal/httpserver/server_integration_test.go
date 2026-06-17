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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/id"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
)

func TestMihonKomgaContract(t *testing.T) {
	store, err := database.Open(t.TempDir() + "/test.db")
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
	scanManager := scanner.New(store, client, logger)
	defer scanManager.Close()
	cacheManager, err := cache.New(store, t.TempDir()+"/cache")
	if err != nil {
		t.Fatal(err)
	}
	thumbnailService, err := thumbnail.New(store, t.TempDir()+"/thumbnails", logger)
	if err != nil {
		t.Fatal(err)
	}
	archiveService := archive.NewService(store, client, cacheManager, logger)
	defer archiveService.Close()
	coverManager := thumbnail.NewBatchManager(
		store, archiveService, thumbnailService, logger,
	)
	defer coverManager.Close()
	handler := New(
		store, client, scanManager, cacheManager,
		archiveService, thumbnailService, coverManager,
		RuntimeInfo{
			DataDir: "/tmp/fake-komga-115-test-data",
			Host:    "127.0.0.1",
			Port:    25600,
		}, logger,
	).Handler()
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, path := range []string{
		"/admin/api/libraries",
		"/admin/api/scans",
		"/admin/api/cover-jobs",
	} {
		var empty []any
		getJSON(t, server.URL+path, &empty)
		if empty == nil || len(empty) != 0 {
			t.Fatalf("fresh installation endpoint %s must return [], got %#v", path, empty)
		}
	}
	var settings map[string]string
	getJSON(t, server.URL+"/admin/api/settings", &settings)
	if settings["volume_index_prefetch_enabled"] != "false" ||
		settings["volume_index_prefetch_remaining_pages"] != "10" {
		t.Fatalf("unexpected volume prefetch defaults: %#v", settings)
	}
	response := putJSON(t, server.URL+"/admin/api/settings", map[string]string{
		"volume_index_prefetch_enabled":         "true",
		"volume_index_prefetch_remaining_pages": "12",
	})
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("valid volume prefetch settings status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = putJSON(t, server.URL+"/admin/api/settings", map[string]string{
		"volume_index_prefetch_remaining_pages": "0",
	})
	if response.StatusCode != http.StatusBadRequest {
		response.Body.Close()
		t.Fatalf("invalid volume prefetch threshold status=%d", response.StatusCode)
	}
	response.Body.Close()

	if err := store.UpsertLibrary(ctx, database.Library{
		ID: libraryID, Name: "Comics", RootCID: "root", Enabled: true, OneShot: true,
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

	var status map[string]any
	getJSON(t, server.URL+"/admin/api/status", &status)
	if status["comicBytes"].(float64) != 1234 {
		t.Fatalf("unexpected total comic bytes: %#v", status)
	}
	if status["dataDir"] != "/tmp/fake-komga-115-test-data" {
		t.Fatalf("unexpected data directory: %#v", status)
	}
	addresses := status["mihonAddresses"].([]any)
	if len(addresses) != 1 || addresses[0] != "http://127.0.0.1:25600" {
		t.Fatalf("unexpected Mihon addresses: %#v", status)
	}
	var libraries []map[string]any
	getJSON(t, server.URL+"/admin/api/libraries", &libraries)
	if len(libraries) != 1 || libraries[0]["comicBytes"].(float64) != 1234 {
		t.Fatalf("unexpected library comic bytes: %#v", libraries)
	}
	if libraries[0]["oneShot"] != true {
		t.Fatalf("one-shot library flag missing: %#v", libraries)
	}
	var coverJobs []map[string]any
	getJSON(t, server.URL+"/admin/api/cover-jobs", &coverJobs)
	if len(coverJobs) != 0 {
		t.Fatalf("unexpected cover jobs: %#v", coverJobs)
	}

	var komgaLibraries []map[string]any
	getJSON(t, server.URL+"/api/v1/libraries", &komgaLibraries)
	if len(komgaLibraries) != 1 || komgaLibraries[0]["oneshotsDirectory"] != "." {
		t.Fatalf("unexpected Komga library: %#v", komgaLibraries)
	}

	var seriesPage map[string]any
	getJSON(t, server.URL+"/api/v1/series?page=0&size=1", &seriesPage)
	content := seriesPage["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("series content: %#v", content)
	}
	seriesItem := content[0].(map[string]any)
	for _, field := range []string{"id", "libraryId", "booksCount", "metadata", "booksMetadata"} {
		if _, ok := seriesItem[field]; !ok {
			t.Fatalf("series field %q missing", field)
		}
	}
	if seriesItem["oneshot"] != true {
		t.Fatalf("series one-shot flag missing: %#v", seriesItem)
	}
	var nonOneShotPage map[string]any
	getJSON(t, server.URL+"/api/v1/series?oneshot=false", &nonOneShotPage)
	if nonOneShotPage["totalElements"].(float64) != 0 {
		t.Fatalf("one-shot filter ignored: %#v", nonOneShotPage)
	}

	var bookPage map[string]any
	getJSON(t, server.URL+"/api/v1/series/"+seriesID+"/books?unpaged=true&media_status=READY&deleted=false", &bookPage)
	books := bookPage["content"].([]any)
	if len(books) != 1 {
		t.Fatalf("book content: %#v", books)
	}
	bookItem := books[0].(map[string]any)
	if _, ok := bookItem["media"].(map[string]any)["mediaProfile"]; !ok {
		t.Fatal("mediaProfile missing")
	}
	if _, ok := bookItem["metadata"].(map[string]any)["numberSort"]; !ok {
		t.Fatal("numberSort missing")
	}
	if bookItem["oneshot"] != true {
		t.Fatalf("book one-shot flag missing: %#v", bookItem)
	}

	response, err = http.Get(server.URL + "/api/v1/series/" + seriesID + "/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("thumbnail status=%d type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	book, err := store.BookByID(ctx, bookID)
	if err != nil {
		t.Fatal(err)
	}
	if err := thumbnailService.Generate(ctx, book, testCoverPNG(t)); err != nil {
		t.Fatal(err)
	}
	response, err = http.Get(server.URL + "/api/v1/series/" + seriesID + "/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("generated thumbnail status=%d type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	var progress map[string]any
	getJSON(t, server.URL+"/api/v2/series/"+seriesID+"/read-progress/tachiyomi", &progress)
	if progress["booksCount"].(float64) != 1 || progress["maxNumberSort"].(float64) != 1 {
		t.Fatalf("progress: %#v", progress)
	}
	response = putJSON(t, server.URL+"/api/v2/series/"+seriesID+"/read-progress/tachiyomi", map[string]any{
		"lastBookNumberSortRead": 1,
	})
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("progress PUT status=%d", response.StatusCode)
	}
	getJSON(t, server.URL+"/api/v2/series/"+seriesID+"/read-progress/tachiyomi", &progress)
	if progress["booksReadCount"].(float64) != 1 || progress["booksUnreadCount"].(float64) != 0 ||
		progress["lastReadContinuousNumberSort"].(float64) != 1 {
		t.Fatalf("progress after PUT: %#v", progress)
	}
	response = putJSON(t, server.URL+"/api/v2/series/"+seriesID+"/read-progress/tachiyomi", map[string]any{
		"lastBookNumberSortRead": 0,
	})
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("progress rollback PUT status=%d", response.StatusCode)
	}
	if err := store.RecordBookPageProgress(ctx, book, 4, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordBookPageProgress(ctx, book, 2, 10); err != nil {
		t.Fatal(err)
	}

	seriesHTML := getText(t, server.URL+"/series/"+seriesID, http.StatusOK)
	for _, expected := range []string{
		"One Shot", "Root/One Shot.cbz", "001.cbz", "/book/" + bookID,
		"Mihon 同步进度", "推断阅读进度", "最近 第 2 / 10 页", "最大 第 4 / 10 页",
	} {
		if !strings.Contains(seriesHTML, expected) {
			t.Fatalf("series web page missing %q", expected)
		}
	}
	bookHTML := getText(t, server.URL+"/book/"+bookID, http.StatusOK)
	for _, expected := range []string{
		"Root/One Shot.cbz", "/series/" + seriesID, `class="cover-frame"`,
		"推断阅读进度", "最近加载：第 2 / 10 页", "最大加载：第 4 / 10 页",
	} {
		if !strings.Contains(bookHTML, expected) {
			t.Fatalf("one-shot book web page missing %q", expected)
		}
	}
	aliasHTML := getText(t, server.URL+"/books/"+bookID, http.StatusOK)
	if !strings.Contains(aliasHTML, "Root/One Shot.cbz") {
		t.Fatal("/books/{id} alias did not render the Book page")
	}

	normalSeriesID := id.Series(libraryID, "normal-series")
	normalBookID := id.Book(libraryID, "normal-file")
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,0,?,?,'scan')`,
		normalSeriesID, libraryID, "normal-series", "Normal Series", "Root/Normal Series",
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,?,?,?,?,'Vol 01.cbz',2048,'normal-pick','normal-sha',?,?,1,?,?,'scan')`,
		normalBookID, normalSeriesID, libraryID, "normal-file", "normal-series",
		now, now, now, now); err != nil {
		t.Fatal(err)
	}
	normalHTML := getText(t, server.URL+"/book/"+normalBookID, http.StatusOK)
	if !strings.Contains(normalHTML, "Root/Normal Series/Vol 01.cbz") {
		t.Fatal("normal Book page does not show the full relative path")
	}
	if strings.Contains(normalHTML, `class="cover-frame"`) {
		t.Fatal("normal Book page must not show a Series cover")
	}

	notFoundHTML := getText(t, server.URL+"/missing-page", http.StatusNotFound)
	if !strings.Contains(notFoundHTML, "没有找到这个页面") {
		t.Fatal("browser 404 page missing friendly message")
	}
	response, err = http.Get(server.URL + "/api/v1/missing-page")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("API 404 must remain JSON: status=%d type=%q",
			response.StatusCode, response.Header.Get("Content-Type"))
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

func getJSON(t *testing.T, url string, target any) {
	t.Helper()
	response, err := http.Get(url)
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

func getText(t *testing.T, url string, status int) string {
	t.Helper()
	response, err := http.Get(url)
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

func putJSON(t *testing.T, url string, value any) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
