package scanner

import (
	"archive/zip"
	"bytes"
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

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/id"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func TestScanAndReadLocalAndWebDAVLibraries(t *testing.T) {
	ctx := t.Context()
	tmp := t.TempDir()
	store, err := database.Open(filepath.Join(tmp, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cacheMgr, err := cache.New(store, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	webdavMgr := webdav.NewManager()

	// 1. Prepare Local Library with a valid CBZ
	localRoot := filepath.Join(tmp, "local-comics")
	seriesDir := filepath.Join(localRoot, "作品A")
	if err := os.MkdirAll(seriesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cbzBytes := buildSampleCBZ(t, "page1-local-bytes")
	if err := os.WriteFile(filepath.Join(seriesDir, "Vol.01.cbz"), cbzBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	localLocator := "local:" + localRoot
	localLib := database.Library{
		ID:           id.Library(localLocator),
		Name:         "本地漫画库",
		ProviderType: database.ProviderLocal,
		RootCID:      localLocator,
		RootPath:     localRoot,
		Enabled:      true,
	}
	if err := store.UpsertLibrary(ctx, localLib); err != nil {
		t.Fatal(err)
	}

	// 2. Prepare Mock WebDAV Server & WebDAV Library
	davCBZ := buildSampleCBZ(t, "page1-webdav-bytes")
	davServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			switch r.URL.Path {
			case "/comics/":
				_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">
<d:response><d:href>/comics/</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>/comics/SeriesB/</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
</d:multistatus>`))
			case "/comics/SeriesB/":
				_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">
<d:response><d:href>/comics/SeriesB/</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>/comics/SeriesB/Ch01.cbz</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><d:resourcetype/><d:getcontentlength>%d</d:getcontentlength><d:getetag>"v1"</d:getetag></d:prop></d:propstat></d:response>
</d:multistatus>`, len(davCBZ))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodGet:
			if r.URL.Path != "/comics/SeriesB/Ch01.cbz" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			rng := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
			parts := strings.SplitN(rng, "-", 2)
			start, _ := strconv.Atoi(parts[0])
			end, _ := strconv.Atoi(parts[1])
			if end >= len(davCBZ) {
				end = len(davCBZ) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(davCBZ)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(davCBZ[start : end+1])
		}
	}))
	defer davServer.Close()

	wdAcc, err := store.UpsertWebDAVAccount(ctx, database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{
			Name:          "TestNAS",
			BaseURL:       davServer.URL,
			RatePerSecond: 100,
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	davLocator := "webdav:" + wdAcc.ID + ":/comics"
	davLib := database.Library{
		ID:              id.Library(davLocator),
		Name:            "WebDAV漫画库",
		ProviderType:    database.ProviderWebDAV,
		RootCID:         davLocator,
		WebDAVAccountID: wdAcc.ID,
		RootPath:        "/comics",
		Enabled:         true,
	}
	if err := store.UpsertLibrary(ctx, davLib); err != nil {
		t.Fatal(err)
	}

	scanMgr := NewWithWebDAV(store, nil, webdavMgr, logger)
	defer scanMgr.Close()
	if _, err := scanMgr.StartAll(ctx, "manual"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l1, _ := store.Library(ctx, localLib.ID)
		l2, _ := store.Library(ctx, davLib.ID)
		if l1.LastScanStatus == "success" && l2.LastScanStatus == "success" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	books, total, err := store.BooksPage(ctx, database.BookQuery{Unpaged: true})
	if err != nil || total != 2 || len(books) != 2 {
		t.Fatalf("expected 2 scanned books across local+webdav, got total=%d err=%v", total, err)
	}

	archiveSvc := archive.NewServiceWithWebDAV(store, nil, webdavMgr, cacheMgr, logger)
	defer archiveSvc.Close()

	for _, b := range books {
		page, err := archiveSvc.ReadPage(ctx, b, 1)
		if err != nil {
			t.Fatalf("ReadPage(%s): %v", b.Name, err)
		}
		if b.LibraryID == localLib.ID && string(page.Data) != "page1-local-bytes" {
			t.Fatalf("unexpected local page data: %q", string(page.Data))
		}
		if b.LibraryID == davLib.ID && string(page.Data) != "page1-webdav-bytes" {
			t.Fatalf("unexpected webdav page data: %q", string(page.Data))
		}
	}

	// Verify local read did NOT pollute Range cache, only WebDAV used Range cache!
	stats, err := cacheMgr.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rangeFiles int64
	for _, s := range stats {
		if s.Type == cache.TypeRange {
			rangeFiles = s.Files
		}
	}
	if rangeFiles != 1 {
		t.Fatalf("expected exactly 1 range cache entry (from WebDAV only), got %d", rangeFiles)
	}
}

func buildSampleCBZ(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "001.jpg", Method: zip.Store}
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
