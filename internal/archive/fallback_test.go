package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func buildTestZipBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("001.jpg")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	// Minimal JPEG header bytes.
	if _, err := w.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xff, 0xd9}); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestLocalZeroSizeCalibrationAndMisnamedCBRFallback(t *testing.T) {
	dir := t.TempDir()
	store, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	cacheMgr, err := cache.New(store, filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatalf("cache: %v", err)
	}

	rootDir := filepath.Join(dir, "comics")
	if err := os.MkdirAll(filepath.Join(rootDir, "SeriesA"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	zipData := buildTestZipBytes(t)
	// Intentionally save a valid ZIP archive with a .cbr extension (common misnamed comic archive).
	archiveRel := "SeriesA/vol01.cbr"
	if err := os.WriteFile(filepath.Join(rootDir, filepath.FromSlash(archiveRel)), zipData, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.DB().Exec(`
INSERT INTO libraries(id,name,provider_type,root_cid,root_path,enabled,one_shot,created_at,updated_at)
VALUES('lib_local','Local','local','',?,1,0,?,?)`, rootDir, now, now)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	_, err = store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,books_count,created_at,updated_at,seen_scan_id)
VALUES('ser_1','lib_local','SeriesA','SeriesA','SeriesA',0,1,?,?,'scan_1')`, now, now)
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}
	// Intentionally store book.size = 0 (simulating SAF provider returning missing COLUMN_SIZE).
	_, err = store.DB().Exec(`
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,number_sort,created_at,updated_at,seen_scan_id)
VALUES('book_1','ser_1','lib_local',?,'SeriesA','vol01.cbr',0,'','',1,?,?,'scan_1')`, archiveRel, now, now)
	if err != nil {
		t.Fatalf("insert book: %v", err)
	}

	svc := NewService(store, nil, cacheMgr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer svc.Close()

	book, err := store.BookByID(context.Background(), "book_1")
	if err != nil {
		t.Fatalf("BookByID: %v", err)
	}

	pages, err := svc.ListPages(context.Background(), book)
	if err != nil {
		t.Fatalf("ListPages with zero-size + misnamed .cbr failed: %v", err)
	}
	if len(pages) != 1 || pages[0].Format != formatZIP || pages[0].Name != "001.jpg" {
		t.Fatalf("unexpected pages: %+v", pages)
	}

	// Verify DB size was calibrated from FD.
	calibrated, err := store.BookByID(context.Background(), "book_1")
	if err != nil {
		t.Fatalf("BookByID after calibrate: %v", err)
	}
	if calibrated.Size != int64(len(zipData)) {
		t.Fatalf("calibrated size=%d want %d", calibrated.Size, len(zipData))
	}

	// Read page 1 (should dispatch cleanly via cached format="zip").
	page, err := svc.ReadPage(context.Background(), calibrated, 1)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if len(page.Data) == 0 || page.Entry.MimeType != "image/jpeg" {
		t.Fatalf("unexpected page data: len=%d mime=%s", len(page.Data), page.Entry.MimeType)
	}
}

func TestWebDAVZeroSizeCalibration(t *testing.T) {
	zipData := buildTestZipBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", strconv.Itoa(len(zipData)))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			rng := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
			parts := strings.SplitN(rng, "-", 2)
			start, _ := strconv.Atoi(parts[0])
			end, _ := strconv.Atoi(parts[1])
			if end >= len(zipData) {
				end = len(zipData) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(zipData)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(zipData[start : end+1])
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	store, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	cacheMgr, err := cache.New(store, filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatalf("cache: %v", err)
	}

	acct, err := store.UpsertWebDAVAccount(context.Background(), database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{
			Name:          "CD2",
			BaseURL:       srv.URL + "/dav",
			RatePerSecond: 100,
		},
	}, false)
	if err != nil {
		t.Fatalf("UpsertWebDAVAccount: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = store.DB().Exec(`
INSERT INTO libraries(id,name,provider_type,webdav_account_id,root_cid,root_path,enabled,one_shot,created_at,updated_at)
VALUES('lib_wd','WD','webdav',?,'','/',1,0,?,?)`, acct.ID, now, now)
	if err != nil {
		t.Fatalf("insert lib: %v", err)
	}
	_, err = store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,one_shot,books_count,created_at,updated_at,seen_scan_id)
VALUES('ser_wd','lib_wd','S','S','S',0,1,?,?,'scan_wd')`, now, now)
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}
	_, err = store.DB().Exec(`
INSERT INTO books(id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,number_sort,created_at,updated_at,seen_scan_id)
VALUES('book_wd','ser_wd','lib_wd','S/01.cbz','S','01.cbz',0,'','',1,?,?,'scan_wd')`, now, now)
	if err != nil {
		t.Fatalf("insert book: %v", err)
	}

	svc := NewServiceWithWebDAV(store, nil, webdav.NewManager(), cacheMgr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer svc.Close()

	book, err := store.BookByID(context.Background(), "book_wd")
	if err != nil {
		t.Fatalf("BookByID: %v", err)
	}
	pages, err := svc.ListPages(context.Background(), book)
	if err != nil {
		t.Fatalf("ListPages webdav zero-size: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(pages))
	}
}
