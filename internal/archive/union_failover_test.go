package archive_test

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

func makePNGBytes(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTestCBZ(t *testing.T, filePath string, pages map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range pages {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnionArchiveFailoverAndReadOnlyCoverReuse(t *testing.T) {
	tmp := t.TempDir()
	upperDir := filepath.Join(tmp, "UpperRoot")
	lowerDir := filepath.Join(tmp, "LowerRoot")

	upperImg := makePNGBytes(t, 220, 20, 20)
	lowerImg := makePNGBytes(t, 20, 40, 220)

	// Upper layer has 2-page CBZ; Lower layer has 3-page CBZ (different size & page count!)
	upperCBZ := filepath.Join(upperDir, "SeriesA", "Vol 01.cbz")
	lowerCBZ := filepath.Join(lowerDir, "SeriesA", "Vol 01.cbz")
	writeTestCBZ(t, upperCBZ, map[string][]byte{
		"01.png": upperImg,
		"02.png": upperImg,
	})
	writeTestCBZ(t, lowerCBZ, map[string][]byte{
		"01.png": lowerImg,
		"02.png": lowerImg,
		"03.png": lowerImg,
	})

	store, err := database.Open(filepath.Join(tmp, "failover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_upper", Name: "本地上层", ProviderType: database.ProviderLocal,
		RootCID: "local:" + upperDir, RootPath: upperDir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib_lower", Name: "备份下层", ProviderType: database.ProviderLocal,
		RootCID: "local:" + lowerDir, RootPath: lowerDir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	client := oneonefive.New(store, logger)
	wdMgr := webdav.NewManager()
	scanMgr := scanner.NewWithWebDAV(store, client, wdMgr, logger)
	defer scanMgr.Close()
	cacheMgr, err := cache.New(store, filepath.Join(tmp, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	archSvc := archive.NewServiceWithWebDAV(store, client, wdMgr, cacheMgr, logger)
	defer archSvc.Close()
	thumbSvc, err := thumbnail.New(store, filepath.Join(tmp, "thumbs", "series"), logger)
	if err != nil {
		t.Fatal(err)
	}

	// Scan both physical libraries synchronously
	for _, libID := range []string{"lib_upper", "lib_lower"} {
		run, err := scanMgr.StartLibrary(ctx, libID, "manual")
		if err != nil {
			t.Fatal(err)
		}
		for {
			cur, err := scanMgr.Run(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cur.Status == "success" {
				break
			}
			if cur.Status == "failed" || cur.Status == "canceled" {
				t.Fatalf("scan %s ended with %s: %s", libID, cur.Status, cur.Error)
			}
		}
	}

	// Generate a local first-volume cover on physical upper series BEFORE union enrollment
	upperSeriesList, _, err := store.SeriesPage(ctx, database.SeriesQuery{LibraryIDs: []string{"lib_upper"}, Size: 10})
	if err != nil || len(upperSeriesList) != 1 {
		t.Fatalf("upper series: err=%v len=%d", err, len(upperSeriesList))
	}
	upperFirstBook, err := store.FirstBookInSeries(ctx, upperSeriesList[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := thumbSvc.Generate(ctx, upperFirstBook, upperImg); err != nil {
		t.Fatal(err)
	}
	beforeThumbStats, _ := thumbSvc.Stats(ctx)

	// Create Union Library [lib_upper, lib_lower]
	unionLib, _, err := store.CreateUnionLibrary(ctx, "联合容灾库", true, []string{"lib_upper", "lib_lower"})
	if err != nil {
		t.Fatal(err)
	}
	uSeriesList, _, err := store.SeriesPage(ctx, database.SeriesQuery{LibraryIDs: []string{unionLib.ID}, Size: 10})
	if err != nil || len(uSeriesList) != 1 {
		t.Fatalf("union series: err=%v len=%d", err, len(uSeriesList))
	}
	uBooks, _, err := store.BooksPage(ctx, database.BookQuery{SeriesID: uSeriesList[0].ID, Unpaged: true})
	if err != nil || len(uBooks) != 1 {
		t.Fatalf("union books: err=%v len=%d", err, len(uBooks))
	}
	uBook := uBooks[0]

	// 1. Read-only cover fallback: union series serves upper physical series's cover without duplicating disk files
	coverData, ok, err := thumbSvc.Get(ctx, uSeriesList[0].ID)
	if err != nil || !ok || len(coverData.Bytes) == 0 {
		t.Fatalf("expected union read-only cover fallback to succeed, ok=%v err=%v", ok, err)
	}
	afterThumbStats, _ := thumbSvc.Stats(ctx)
	if afterThumbStats.Files != beforeThumbStats.Files {
		t.Fatalf("expected zero duplicate series cover files, before=%d after=%d", beforeThumbStats.Files, afterThumbStats.Files)
	}

	// 2. Normal read hits Upper layer (2 pages, upperImg bytes)
	pages, err := archSvc.ListPages(ctx, uBook)
	if err != nil || len(pages) != 2 {
		t.Fatalf("expected 2 pages from upper layer, got len=%d err=%v", len(pages), err)
	}
	page1, err := archSvc.ReadPage(ctx, uBook, 1)
	if err != nil || !bytes.Equal(page1.Data, upperImg) {
		t.Fatalf("expected page 1 from upper layer, err=%v match=%v", err, bytes.Equal(page1.Data, upperImg))
	}

	// 3. Remove Upper file on disk (simulate SMB disconnect/missing file):
	// Reading uncached page 2 hits ENOENT on upper layer -> transparently falls back to Lower (3 pages, lowerImg!)
	if err := os.Remove(upperCBZ); err != nil {
		t.Fatal(err)
	}
	failPage2, err := archSvc.ReadPage(ctx, uBook, 2)
	if err != nil || failPage2.TotalPages != 3 || !bytes.Equal(failPage2.Data, lowerImg) {
		t.Fatalf("expected transparent failover to lower layer on uncached page 2 (3 pages, lowerImg), got total=%d match=%v err=%v",
			failPage2.TotalPages, bytes.Equal(failPage2.Data, lowerImg), err)
	}
	// And after clearing page cache, page 1 ALSO transparently falls back to Lower!
	if err := cacheMgr.Clear(ctx, cache.TypePage); err != nil {
		t.Fatal(err)
	}
	failPage1, err := archSvc.ReadPage(ctx, uBook, 1)
	if err != nil || failPage1.TotalPages != 3 || !bytes.Equal(failPage1.Data, lowerImg) {
		t.Fatalf("expected transparent failover to lower layer on page 1 after cache clear (3 pages, lowerImg), got total=%d match=%v err=%v",
			failPage1.TotalPages, bytes.Equal(failPage1.Data, lowerImg), err)
	}
}
