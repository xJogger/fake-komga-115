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
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: libraryID, Name: "Comics", RootCID: "root", Enabled: true, OneShot: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,created_at,updated_at,seen_scan_id)
VALUES(?,?,?,?,?,1,?,?,'scan')`,
		seriesID, libraryID, "series-cid", "Series", "Series", now, now); err != nil {
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
	coverManager := thumbnail.NewBatchManager(
		store, archiveService, thumbnailService, logger,
	)
	defer coverManager.Close()
	handler := New(
		store, client, scanManager, cacheManager,
		archiveService, thumbnailService, coverManager, logger,
	).Handler()
	server := httptest.NewServer(handler)
	defer server.Close()

	var status map[string]any
	getJSON(t, server.URL+"/admin/api/status", &status)
	if status["comicBytes"].(float64) != 1234 {
		t.Fatalf("unexpected total comic bytes: %#v", status)
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

	response, err := http.Get(server.URL + "/api/v1/series/" + seriesID + "/thumbnail")
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
