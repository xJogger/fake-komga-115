package archive

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
)

func TestRemoteRAR4Entry(t *testing.T) {
	data := readRARFixture(t, "test_read_format_rar.rar")
	service, book, requests := newRemoteRARTest(t, data)
	files, _, err := service.listFiles(context.Background(), book)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 || files[0].Name != "test.txt" {
		t.Fatalf("unexpected RAR4 files: %+v", files)
	}
	entry := PageEntry{
		Name: "test.txt", ArchiveIndex: 0,
		CompressedSize:   uint64(files[0].PackedSize),
		UncompressedSize: uint64(files[0].UnPackedSize),
	}
	page, err := service.readEntry(context.Background(), book, entry, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(page) != "test text document\r\n" {
		t.Fatalf("unexpected RAR4 data: %q", page)
	}
	if requests.Load() == 0 {
		t.Fatal("RAR4 read made no HTTP Range requests")
	}
}

func TestRemoteRAR5CompressedEntry(t *testing.T) {
	data := readRARFixture(t, "test_read_format_rar5_compressed.rar")
	service, book, requests := newRemoteRARTest(t, data)
	files, _, err := service.listFiles(context.Background(), book)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "test.bin" {
		t.Fatalf("unexpected RAR5 files: %+v", files)
	}
	entry := PageEntry{
		Number: 1, Name: "test.bin", Format: formatRAR, ArchiveIndex: 0,
		CompressedSize:   uint64(files[0].PackedSize),
		UncompressedSize: uint64(files[0].UnPackedSize),
		MimeType:         "image/jpeg",
	}
	indexJSON, err := json.Marshal([]PageEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := service.store.DB().Exec(`
INSERT INTO zip_indexes(book_id,version,page_count,index_json,created_at,updated_at)
VALUES(?,?,?,?,?,?)`,
		book.ID, bookVersion(book), 1, string(indexJSON), now, now); err != nil {
		t.Fatal(err)
	}
	page, err := service.ReadPage(context.Background(), book, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyRAR5TestData(page.Data, 0) {
		t.Fatal("RAR5 compressed data mismatch")
	}
	requestsBeforeCache := requests.Load()
	if _, err := service.ReadPage(context.Background(), book, 1); err != nil {
		t.Fatal(err)
	}
	if requestsAfterCache := requests.Load(); requestsAfterCache != requestsBeforeCache {
		t.Fatalf("RAR page cache miss: requests before=%d after=%d", requestsBeforeCache, requestsAfterCache)
	}
}

func TestRARUnsupportedVariants(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    error
	}{
		{"solid", "test_read_format_rar5_multiple_files_solid.rar", ErrSolidRAR},
		{"encrypted", "test_read_format_rar5_encrypted.rar", ErrEncryptedRAR},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := readRARFixture(t, test.fixture)
			service, book, _ := newRemoteRARTest(t, data)
			_, err := service.ListPages(context.Background(), book)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
		})
	}
}

func TestMultiVolumeRARErrorMapping(t *testing.T) {
	err := mapRARError(&fs.PathError{Op: "open", Path: "archive.r00", Err: fs.ErrNotExist})
	if !errors.Is(err, ErrMultiVolumeRAR) {
		t.Fatalf("error=%v want %v", err, ErrMultiVolumeRAR)
	}
}

func newRemoteRARTest(
	t *testing.T,
	archiveBytes []byte,
) (*RARService, database.Book, *atomic.Int64) {
	t.Helper()
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
	t.Cleanup(server.Close)

	store, err := database.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for key, value := range map[string]string{
		"range_block_size":     "128",
		"rar_index_block_size": "64",
	} {
		if err := store.SetSetting(ctx, key, value); err != nil {
			t.Fatal(err)
		}
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
) VALUES('book','series','lib','file','cid','book.cbr',?,'pick','sha',?,?,1,?,?,'scan')`,
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRARService(store, nil, cacheManager, logger), book, &requests
}

func verifyRAR5TestData(data []byte, magic int) bool {
	if len(data)%4 != 0 {
		return false
	}
	for index := 0; index < len(data)/4; index++ {
		k := index + 1
		value := k*k - 3*k + (1 + magic)
		if value < 0 {
			value = 0
		}
		offset := index * 4
		actual := int(data[offset]) |
			int(data[offset+1])<<8 |
			int(data[offset+2])<<16 |
			int(data[offset+3])<<24
		if actual != value {
			return false
		}
	}
	return true
}

func readRARFixture(t *testing.T, name string) []byte {
	t.Helper()
	fixtures := map[string]string{
		"test_read_format_rar.rar":                       rar4Fixture,
		"test_read_format_rar5_compressed.rar":           rar5CompressedFixture,
		"test_read_format_rar5_multiple_files_solid.rar": rar5SolidFixture,
		"test_read_format_rar5_encrypted.rar":            rar5EncryptedFixture,
	}
	encoded, ok := fixtures[name]
	if !ok {
		t.Fatalf("unknown fixture %q", name)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Fixtures are derived from libarchive's BSD-licensed test suite.
const rar4Fixture = "" +
	"UmFyIRoHAM+QcwAADQAAAAAAAACEUnQgkDIAFAAAABQAAAADQqLIvrd22j4UMAgApIEAAHRlc3QudHh0gAi3dto+t3baPnRlc3Qg" +
	"dGV4dCBkb2N1bWVudA0KnS90IJAyAAgAAAAIAAAAA3tEybbRTNg+FDAIAP+hAAB0ZXN0bGlua8AI0UzYPlBf2j50ZXN0LnR4dM3g" +
	"dCCQOgAUAAAAFAAAAANCosi+Y3faPhQwEACkgQAAdGVzdGRpclx0ZXN0LnR4dMDMY3faPmN32j50ZXN0IHRleHQgZG9jdW1lbnQN" +
	"CqHIdOCQMQAAAAAAAAAAAAMAAAAAY3faPhQwBwDtQQAAdGVzdGRpcsDMY3faPmR32j7m53TgkDYAAAAAAAAAAAADAAAAAJ2r1T4U" +
	"MAwA7UEAAHRlc3RlbXB0eWRpcoDMnavVPsVd2j7EPXsAQAcA"

const rar5CompressedFixture = "" +
	"UmFyIRoHAQDz4YLrCwEFBwAGAQGAgIAAicZf2iYCAwvpAgSwCaSDAs1wynyABQEIdGVzdC5iaW4KAxOLV6xb+BitHsr0ZQEnZWBU" +
	"H1V2Xb+UknHJz2WWWQxw2WWTGMWSE4ZCSSThkJCSSSQk45JJIQhJJCEJJJCSSEISEs21b3t9Mw6AfR0Drj+eup/3Qf732vfdea86" +
	"X+gAQAEAyCCfn49vX08vHv7uzr6uno5+Xk4+Lh4N/d3NrZ19bV1NPS0M/NzMrJyMfGxcTDwb+9vLu5uLe2tbSzsrGvrq2rqqmnpa" +
	"SjoqGgn52cm5mXlpSTSyUjIR6FAfjoyKiYc6cNmoaEgoB+fHt6eHZ0c3Jxb21raWdlY2FfXVtZV1VTITf1/I/QJT6JTn+ESXRYow" +
	"Z5YXajgppimUF+ZC0XadRjLDOMCoR/+glMZpCl9lUKxWVguLAZFoNy4HdeEGwA8xBIyCeZhWNAumoZbYN5uHg4Bgcg+uYROgpnYY" +
	"DwNt6Hu9h1fQjfxYQI0YMecKHk0Jg2L44OY6HCIEqeGFFjtjQ/nxUIBsIQ0IhNIxnJAvJQAdd1ZRAwUEAA=="

const rar5SolidFixture = "" +
	"UmFyIRoHAQAJ78hvCwEFBwQGAQGAgIAAGYyU/ycCAwv5AgSAIKSDAsayE36AHQEJdGVzdDEuYmluCgMTZ1+sWxpanhDJ53UBGGVU" +
	"ZSb0gFf18+fPkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkv885nDWtl1e+1F08qTnVyzt8e57r" +
	"fRrMzrw8/HdNt72b6d/xjNZ55rDOrM6c/B3AAIAIAbDDfz8e/u7ezr6uno5+bl5OPi4eDf3t3c29rZ2NfW1dTT0tHQz87NzMvKyc" +
	"jHxsXEw8LBwL++vby7urm4t7a1tLOysbCvrq2sq6qpqKempaSjoqGgn56dnJuamZiXlpWUk5KRkI+OjYyLiomIh4aFhIOCgYB/fn" +
	"18e3p5eP//v7/P372Y9gpnCmWvV8oojX8kIW1wFtxYDE0aLZQlkTKA5tgxgxNmyIiWkIW1wF+YsBiaNFsoSyJlAc/IMYMTbyRES0" +
	"hC2uAvxFgMTRotlCWRMoDn4BjBibeCIiWkIW1wF94sBiaNFsoSyJlAaYdeFfmOuV99xavpt6r5i+pz15Tl7inI2visX+ADtz4pEn" +
	"AgMLmQAEgCCkgwLLr2bxwB0BCXRlc3QyLmJpbgoDE3pfrFstVHMXQQ0WcAAgAgBqX8f3ueDF6n8w68O/j+X+AJaUzIcnAgMLqgAE" +
	"gCCkgwLZI7GfwB0BCXRlc3QzLmJpbgoDE35frFvQPQADQD0ncAAgAgBt1/H98w/vTQ8rKB9IY7gh9IY7gh9IY7gh7mHXh38fy/wA" +
	"jXhTbCcCAwuZAASAIKSDAtQ+xBDAHQEJdGVzdDQuYmluCgMTgV+sW6EmHRJCDhZwACACAG2X8f3s8GL2P5h14d/H8v8AHXdWUQMF" +
	"BAA="

const rar5EncryptedFixture = "" +
	"UmFyIRoHAQAzkrXlCgEFBgAFAQGAgAA1/zIeHwIDCxIEEiBVblrugAAABWEudHh0CgMCfCGEo4J82gFUaGlzIGlzIGZyb20gYS50" +
	"eHR6QXGzUAIDPDAEEiAYRaMCgAMABWIudHh0MAEAAw/HRF7hgPi1n9YrQzcIvFfN+jTfBqtdBdSTB3cPHY6AU+o1Zx1w8S1La5wa" +
	"nAoDAnJlmKaCfNoBY7pUstAQpUlftLrTwGmoOxu8uzgn14eTeHMvfJPi0ijEAN7+psA7fmxn1AVQANz4jXzxZh8CAwsSBBIgNT2a" +
	"lIAAAAVjLnR4dAoDAj5I6a6CfNoBVGhpcyBpcyBmcm9tIGMudHh07P3wXVICAzygAASSACDD32MSgAMABWQudHh0MAEAAw/HRF7h" +
	"gPi1n9YrQzcIvFfN883Wsxy0W6d8rDZYjrMOaYNM1EjpFevWrHe3owoDAnvFKfSPfNoB22ly9PZWjqb9GWXplbM2hTQ39Sqg0yeH" +
	"svXBAjLYql4dd1ZRAwUEAA=="
