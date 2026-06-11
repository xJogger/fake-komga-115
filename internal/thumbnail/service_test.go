package thumbnail

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestGenerateFirstBookSeriesThumbnail(t *testing.T) {
	store, first, second := thumbnailTestStore(t)
	service, err := New(
		store, t.TempDir()+"/thumbnails",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	page := testPNG(t, 600, 900)
	if err := service.Generate(context.Background(), second, page); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.Get(context.Background(), first.SeriesID); err != nil || ok {
		t.Fatalf("second book generated thumbnail: ok=%v err=%v", ok, err)
	}
	if err := service.Generate(context.Background(), first, page); err != nil {
		t.Fatal(err)
	}
	item, ok, err := service.Get(context.Background(), first.SeriesID)
	if err != nil || !ok {
		t.Fatalf("thumbnail missing: ok=%v err=%v", ok, err)
	}
	if item.MediaType != "image/jpeg" || item.Width != 200 || item.Height != 300 {
		t.Fatalf("unexpected thumbnail metadata: %+v", item)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(item.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 200 || config.Height != 300 {
		t.Fatalf("unexpected JPEG dimensions: %+v", config)
	}
	stats, err := service.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 || stats.Bytes != int64(len(item.Bytes)) {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	if _, err := store.DB().Exec(`UPDATE books SET size=size+1 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.Get(context.Background(), first.SeriesID); err != nil || ok {
		t.Fatalf("stale thumbnail returned: ok=%v err=%v", ok, err)
	}
	stats, err = service.Stats(context.Background())
	if err != nil || stats.Files != 0 {
		t.Fatalf("stale metadata remains: stats=%+v err=%v", stats, err)
	}
}

func TestClearSeriesThumbnails(t *testing.T) {
	store, first, _ := thumbnailTestStore(t)
	root := t.TempDir() + "/thumbnails"
	service, err := New(store, root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Generate(context.Background(), first, testPNG(t, 100, 150)); err != nil {
		t.Fatal(err)
	}
	if err := service.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("thumbnail files remain: %v", entries)
	}
	stats, err := service.Stats(context.Background())
	if err != nil || stats.Files != 0 || stats.Bytes != 0 {
		t.Fatalf("unexpected stats after clear: %+v err=%v", stats, err)
	}
}

func TestMaybeGenerateAfterReadingFirstPage(t *testing.T) {
	store, first, _ := thumbnailTestStore(t)
	service, err := New(
		store, t.TempDir()+"/thumbnails",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	page := testPNG(t, 400, 600)
	service.MaybeGenerate(first, 2, page)
	if _, ok, err := service.Get(context.Background(), first.SeriesID); err != nil || ok {
		t.Fatalf("non-first page generated thumbnail: ok=%v err=%v", ok, err)
	}
	service.MaybeGenerate(first, 1, page)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok, err := service.Get(context.Background(), first.SeriesID); err != nil {
			t.Fatal(err)
		} else if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("thumbnail was not generated asynchronously")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func thumbnailTestStore(t *testing.T) (*database.Store, database.Book, database.Book) {
	t.Helper()
	store, err := database.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.UpsertLibrary(ctx, database.Library{
		ID: "lib", Name: "Library", RootCID: "root", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`
INSERT INTO series(id,library_id,cid,name,relative_path,created_at,updated_at,seen_scan_id)
VALUES('series','lib','cid','Series','Series',?,?,'scan')`, now, now); err != nil {
		t.Fatal(err)
	}
	for _, book := range []struct {
		id, fileID, name string
		number           int
	}{
		{"first", "file-1", "001.cbz", 1},
		{"second", "file-2", "002.cbz", 2},
	} {
		if _, err := store.DB().Exec(`
INSERT INTO books(
 id,series_id,library_id,file_id,parent_cid,name,size,pick_code,sha1,
 file_created_at,file_modified_at,number_sort,created_at,updated_at,seen_scan_id
) VALUES(?,'series','lib',?,'cid',?,123,'pick','sha',?,?,?, ?,?,'scan')`,
			book.id, book.fileID, book.name, now, now, book.number, now, now); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.BookByID(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BookByID(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	return store, first, second
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestScaledSize(t *testing.T) {
	tests := []struct {
		width, height int
		wantWidth     int
		wantHeight    int
	}{
		{100, 150, 100, 150},
		{600, 900, 200, 300},
		{1200, 600, 300, 150},
	}
	for _, test := range tests {
		width, height := scaledSize(test.width, test.height, 300)
		if width != test.wantWidth || height != test.wantHeight {
			t.Fatalf(
				"scaledSize(%d,%d)=(%d,%d), want (%d,%d)",
				test.width, test.height, width, height, test.wantWidth, test.wantHeight,
			)
		}
	}
}
