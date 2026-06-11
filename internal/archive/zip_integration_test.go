package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
)

func TestRemoteZIPIndexAndPages(t *testing.T) {
	archiveBytes := makeTestZIP(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		start, end, ok := parseRangeHeader(r.Header.Get("Range"), int64(len(archiveBytes)))
		if !ok {
			http.Error(w, "range required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(archiveBytes)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(archiveBytes[start : end+1])
	}))
	defer server.Close()

	store, err := database.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SetSetting(ctx, "range_block_size", "64"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib", Name: "Library", RootCID: "root", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES('series','lib','cid','Series','Series',?,?, 'scan')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES('book','series','lib','file','cid','book.cbz',?,'pick','sha',?,?,1,?,?,'scan')`,
		len(archiveBytes), now, now, now, now); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(oneonefive.UserAgent))
	uaHash := hex.EncodeToString(sum[:])
	if _, err := store.DB().Exec(`
INSERT INTO downurl_cache(cache_key,pick_code,ua_hash,url,user_agent,expire_at,created_at)
VALUES(?,?,?,?,?,?,?)`,
		"pick:"+uaHash, "pick", uaHash, server.URL, oneonefive.UserAgent,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), now); err != nil {
		t.Fatal(err)
	}
	cacheManager, err := cache.New(store, t.TempDir()+"/cache")
	if err != nil {
		t.Fatal(err)
	}
	book, err := store.BookByID(ctx, "book")
	if err != nil {
		t.Fatal(err)
	}
	service := NewZIPService(store, nil, cacheManager, testLogger())
	pages, err := service.ListPages(ctx, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Name != "1.jpg" || pages[1].Name != "10.png" {
		t.Fatalf("unexpected pages: %+v", pages)
	}
	page1, err := service.ReadPage(ctx, book, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(page1.Data) != strings.Repeat("jpeg-data-", 30) {
		t.Fatalf("unexpected first page")
	}
	page2, err := service.ReadPage(ctx, book, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(page2.Data) != strings.Repeat("png-data-", 40) {
		t.Fatalf("unexpected second page")
	}
	before := requests.Load()
	if _, err := service.ReadPage(ctx, book, 2); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != before {
		t.Fatalf("page cache miss: requests before=%d after=%d", before, requests.Load())
	}
}

func makeTestZIP(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	storeHeader := &zip.FileHeader{Name: "1.jpg", Method: zip.Store}
	storeHeader.SetModTime(time.Unix(0, 0))
	first, err := writer.CreateHeader(storeHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = first.Write([]byte(strings.Repeat("jpeg-data-", 30)))
	deflateHeader := &zip.FileHeader{Name: "10.png", Method: zip.Deflate}
	deflateHeader.SetModTime(time.Unix(0, 0))
	second, err := writer.CreateHeader(deflateHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = second.Write([]byte(strings.Repeat("png-data-", 40)))
	other, _ := writer.Create("__MACOSX/ignored.jpg")
	_, _ = other.Write([]byte("ignored"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func parseRangeHeader(value string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err1 := strconv.ParseInt(parts[0], 10, 64)
	end, err2 := strconv.ParseInt(parts[1], 10, 64)
	return start, end, err1 == nil && err2 == nil && start >= 0 && end >= start && end < size
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
