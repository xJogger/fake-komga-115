package archive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/natsort"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
)

type ZIPService struct {
	store  *database.Store
	client *oneonefive.Client
	cache  *cache.Manager
	logger *slog.Logger
}

func NewZIPService(
	store *database.Store,
	client *oneonefive.Client,
	cacheManager *cache.Manager,
	logger *slog.Logger,
) *ZIPService {
	return &ZIPService{store: store, client: client, cache: cacheManager, logger: logger}
}

func (z *ZIPService) ListPages(ctx context.Context, book database.Book) ([]PageEntry, error) {
	version := bookVersion(book)
	var raw string
	err := z.store.DB().QueryRowContext(ctx,
		`SELECT index_json FROM zip_indexes WHERE book_id=? AND version=?`, book.ID, version).Scan(&raw)
	if err == nil {
		var pages []PageEntry
		if json.Unmarshal([]byte(raw), &pages) == nil {
			return pages, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	readerAt := NewRemoteReaderAt(ctx, book, z.store, z.client, z.cache, z.logger)
	reader, err := zip.NewReader(readerAt, book.Size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidZIP, err)
	}
	var pages []PageEntry
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || ignoredEntry(file.Name) {
			continue
		}
		mimeType := MimeType(file.Name)
		if mimeType == "" {
			continue
		}
		if file.Flags&1 != 0 || (file.Method != zip.Store && file.Method != zip.Deflate) {
			return nil, ErrUnsupportedZIP
		}
		offset, err := file.DataOffset()
		if err != nil {
			return nil, fmt.Errorf("%w: data offset for %s: %v", ErrInvalidZIP, file.Name, err)
		}
		pages = append(pages, PageEntry{
			Name:             file.Name,
			Format:           formatZIP,
			CompressedSize:   file.CompressedSize64,
			UncompressedSize: file.UncompressedSize64,
			Method:           file.Method,
			MimeType:         mimeType,
			DataOffset:       offset,
			CRC32:            file.CRC32,
		})
	}
	slices.SortFunc(pages, func(a, b PageEntry) int {
		if natsort.Less(a.Name, b.Name) {
			return -1
		}
		if natsort.Less(b.Name, a.Name) {
			return 1
		}
		return 0
	})
	for index := range pages {
		pages[index].Number = index + 1
	}
	encoded, err := json.Marshal(pages)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = z.store.DB().ExecContext(ctx, `
INSERT INTO zip_indexes(book_id,version,page_count,index_json,created_at,updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(book_id) DO UPDATE SET
 version=excluded.version,page_count=excluded.page_count,index_json=excluded.index_json,updated_at=excluded.updated_at`,
		book.ID, version, len(pages), string(encoded), now, now)
	if err != nil {
		return nil, err
	}
	z.logger.Info("zip index parsed", "book", book.ID, "pages", len(pages))
	return pages, nil
}

func (z *ZIPService) ReadPage(ctx context.Context, book database.Book, pageNumber int) (PageData, error) {
	pages, err := z.ListPages(ctx, book)
	if err != nil {
		return PageData{}, err
	}
	if pageNumber < 1 || pageNumber > len(pages) {
		return PageData{}, sql.ErrNoRows
	}
	entry := pages[pageNumber-1]
	maxPageSize := z.store.Int64Setting(ctx, "max_page_size", 100<<20)
	if maxPageSize <= 0 {
		maxPageSize = 100 << 20
	}
	if entry.UncompressedSize > uint64(maxPageSize) ||
		entry.CompressedSize > uint64(maxPageSize) {
		return PageData{}, ErrPageTooLarge
	}
	pageKey := fmt.Sprintf("%s:%s:%d", book.ID, bookVersion(book), pageNumber)
	if entry.Method == zip.Deflate {
		data, _, err := z.cache.GetOrLoad(
			ctx, cache.TypePage, pageKey,
			z.store.Int64Setting(ctx, "page_cache_max_bytes", 5<<30),
			func(loadCtx context.Context) ([]byte, error) {
				return z.readEntry(loadCtx, book, entry, maxPageSize)
			},
		)
		return PageData{Entry: entry, Data: data}, err
	}
	data, err := z.readEntry(ctx, book, entry, maxPageSize)
	return PageData{Entry: entry, Data: data}, err
}

func (z *ZIPService) Prefetch(book database.Book, afterPage, count int) {
	if count <= 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for page := afterPage + 1; page <= afterPage+count; page++ {
			if _, err := z.ReadPage(ctx, book, page); err != nil {
				return
			}
		}
	}()
}

func (z *ZIPService) readEntry(
	ctx context.Context,
	book database.Book,
	entry PageEntry,
	maxPageSize int64,
) ([]byte, error) {
	compressed := make([]byte, int(entry.CompressedSize))
	readerAt := NewRemoteReaderAt(ctx, book, z.store, z.client, z.cache, z.logger)
	if _, err := readerAt.ReadAt(compressed, entry.DataOffset); err != nil {
		return nil, err
	}
	var data []byte
	switch entry.Method {
	case zip.Store:
		data = compressed
	case zip.Deflate:
		reader := flate.NewReader(bytes.NewReader(compressed))
		defer reader.Close()
		var err error
		data, err = io.ReadAll(io.LimitReader(reader, maxPageSize+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxPageSize {
			return nil, ErrPageTooLarge
		}
	default:
		return nil, ErrUnsupportedZIP
	}
	if uint64(len(data)) != entry.UncompressedSize || crc32.ChecksumIEEE(data) != entry.CRC32 {
		return nil, fmt.Errorf("%w: page checksum or size mismatch", ErrInvalidZIP)
	}
	return data, nil
}

func MimeType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".avif":
		return "image/avif"
	default:
		return ""
	}
}

func ignoredEntry(name string) bool {
	clean := strings.ReplaceAll(name, "\\", "/")
	base := strings.ToLower(filepath.Base(clean))
	return strings.HasPrefix(clean, "__MACOSX/") ||
		base == ".ds_store" ||
		base == "thumbs.db"
}

func bookVersion(book database.Book) string {
	modified := ""
	if book.FileModifiedAt != nil {
		modified = book.FileModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf(
		"%s:%s:%d:%s:%s",
		book.FileID, archiveFormat(book.Name), book.Size, book.SHA1, modified,
	)
}

func BookVersion(book database.Book) string {
	return bookVersion(book)
}
