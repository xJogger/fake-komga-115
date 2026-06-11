package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwaples/rardecode/v2"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/natsort"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
)

const remoteRARName = "archive.rar"

type RARService struct {
	store  *database.Store
	client *oneonefive.Client
	cache  *cache.Manager
	logger *slog.Logger
}

func NewRARService(
	store *database.Store,
	client *oneonefive.Client,
	cacheManager *cache.Manager,
	logger *slog.Logger,
) *RARService {
	return &RARService{store: store, client: client, cache: cacheManager, logger: logger}
}

func (r *RARService) ListPages(ctx context.Context, book database.Book) ([]PageEntry, error) {
	version := bookVersion(book)
	var raw string
	err := r.store.DB().QueryRowContext(ctx,
		`SELECT index_json FROM zip_indexes WHERE book_id=? AND version=?`, book.ID, version).Scan(&raw)
	if err == nil {
		var pages []PageEntry
		if json.Unmarshal([]byte(raw), &pages) == nil {
			return pages, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	files, _, err := r.listFiles(ctx, book)
	if err != nil {
		return nil, err
	}

	pages, err := rarPageEntries(files)
	if err != nil {
		return nil, err
	}
	if err := r.saveIndex(ctx, book, pages); err != nil {
		return nil, err
	}
	r.logger.Info("rar index parsed", "book", book.ID, "pages", len(pages))
	return pages, nil
}

func rarPageEntries(files []*rardecode.File) ([]PageEntry, error) {
	pages := make([]PageEntry, 0, len(files))
	for index, file := range files {
		if err := validateRARFile(file); err != nil {
			return nil, err
		}
		if file.IsDir || ignoredEntry(file.Name) {
			continue
		}
		mimeType := MimeType(file.Name)
		if mimeType == "" {
			continue
		}
		pages = append(pages, PageEntry{
			Name:             file.Name,
			Format:           formatRAR,
			ArchiveIndex:     index,
			CompressedSize:   uint64(file.PackedSize),
			UncompressedSize: uint64(file.UnPackedSize),
			MimeType:         mimeType,
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
	return pages, nil
}

func (r *RARService) ReadPage(
	ctx context.Context,
	book database.Book,
	pageNumber int,
) (PageData, error) {
	pages, err := r.ListPages(ctx, book)
	if err != nil {
		return PageData{}, err
	}
	if pageNumber < 1 || pageNumber > len(pages) {
		return PageData{}, sql.ErrNoRows
	}
	entry := pages[pageNumber-1]
	maxPageSize := r.store.Int64Setting(ctx, "max_page_size", 100<<20)
	if maxPageSize <= 0 {
		maxPageSize = 100 << 20
	}
	if entry.UncompressedSize > uint64(maxPageSize) ||
		entry.CompressedSize > uint64(maxPageSize) {
		return PageData{}, ErrPageTooLarge
	}
	pageKey := fmt.Sprintf("%s:%s:%d", book.ID, bookVersion(book), pageNumber)
	data, _, err := r.cache.GetOrLoad(
		ctx, cache.TypePage, pageKey,
		r.store.Int64Setting(ctx, "page_cache_max_bytes", 5<<30),
		func(loadCtx context.Context) ([]byte, error) {
			return r.readEntry(loadCtx, book, entry, maxPageSize)
		},
	)
	return PageData{Entry: entry, Data: data}, err
}

func (r *RARService) Prefetch(book database.Book, afterPage, count int) {
	if count <= 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for page := afterPage + 1; page <= afterPage+count; page++ {
			if _, err := r.ReadPage(ctx, book, page); err != nil {
				return
			}
		}
	}()
}

func (r *RARService) listFiles(
	ctx context.Context,
	book database.Book,
) ([]*rardecode.File, *remoteArchiveFS, error) {
	indexBlockSize := r.store.Int64Setting(ctx, "rar_index_block_size", 64<<10)
	dataBlockSize := r.store.Int64Setting(ctx, "range_block_size", 1<<20)
	remoteFS := newRemoteArchiveFS(
		ctx, book, r.store, r.client, r.cache, r.logger, indexBlockSize, dataBlockSize,
	)
	maxDictionarySize := r.store.Int64Setting(ctx, "rar_max_dictionary_size", 100<<20)
	if maxDictionarySize < 256<<10 {
		maxDictionarySize = 256 << 10
	}
	files, err := rardecode.List(
		remoteRARName,
		rardecode.FileSystem(remoteFS),
		rardecode.BufferSize(32<<10),
		rardecode.MaxDictionarySize(maxDictionarySize),
	)
	if err != nil {
		return nil, remoteFS, mapRARError(err)
	}
	return files, remoteFS, nil
}

func (r *RARService) readEntry(
	ctx context.Context,
	book database.Book,
	entry PageEntry,
	maxPageSize int64,
) ([]byte, error) {
	files, remoteFS, err := r.listFiles(ctx, book)
	if err != nil {
		return nil, err
	}
	remoteFS.UseDataBlocks()
	if entry.ArchiveIndex < 0 || entry.ArchiveIndex >= len(files) {
		return nil, fmt.Errorf("%w: page entry no longer exists", ErrInvalidRAR)
	}
	file := files[entry.ArchiveIndex]
	if err := validateRARFile(file); err != nil {
		return nil, err
	}
	if file.Name != entry.Name || uint64(file.UnPackedSize) != entry.UncompressedSize {
		return nil, fmt.Errorf("%w: page entry changed", ErrInvalidRAR)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, mapRARError(err)
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, maxPageSize+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, mapRARError(readErr)
	}
	if closeErr != nil {
		return nil, mapRARError(closeErr)
	}
	if int64(len(data)) > maxPageSize {
		return nil, ErrPageTooLarge
	}
	if uint64(len(data)) != entry.UncompressedSize {
		return nil, fmt.Errorf("%w: page size mismatch", ErrInvalidRAR)
	}
	return data, nil
}

func (r *RARService) saveIndex(
	ctx context.Context,
	book database.Book,
	pages []PageEntry,
) error {
	encoded, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = r.store.DB().ExecContext(ctx, `
INSERT INTO zip_indexes(book_id,version,page_count,index_json,created_at,updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(book_id) DO UPDATE SET
 version=excluded.version,page_count=excluded.page_count,index_json=excluded.index_json,updated_at=excluded.updated_at`,
		book.ID, bookVersion(book), len(pages), string(encoded), now, now)
	return err
}

func validateRARFile(file *rardecode.File) error {
	switch {
	case file.Encrypted || file.HeaderEncrypted:
		return ErrEncryptedRAR
	case !file.IsDir && file.Solid:
		return ErrSolidRAR
	case !file.IsDir && file.UnKnownSize:
		return fmt.Errorf("%w: entry %q has unknown unpacked size", ErrUnsupportedRAR, file.Name)
	case file.PackedSize < 0 || file.UnPackedSize < 0:
		return fmt.Errorf("%w: entry %q has an invalid size", ErrInvalidRAR, file.Name)
	default:
		return nil
	}
}

func mapRARError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrSolidRAR),
		errors.Is(err, ErrEncryptedRAR),
		errors.Is(err, ErrMultiVolumeRAR),
		errors.Is(err, ErrUnsupportedRAR),
		errors.Is(err, ErrInvalidRAR):
		return err
	case errors.Is(err, rardecode.ErrSolidOpen):
		return ErrSolidRAR
	case errors.Is(err, rardecode.ErrArchiveEncrypted),
		errors.Is(err, rardecode.ErrArchivedFileEncrypted):
		return ErrEncryptedRAR
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: a required volume is unavailable", ErrMultiVolumeRAR)
	case errors.Is(err, rardecode.ErrDictionaryTooLarge):
		return fmt.Errorf("%w: decompression dictionary exceeds the configured limit", ErrUnsupportedRAR)
	default:
		return fmt.Errorf("%w: %v", ErrInvalidRAR, err)
	}
}

type remoteArchiveFS struct {
	ctx       context.Context
	book      database.Book
	store     *database.Store
	client    *oneonefive.Client
	cache     *cache.Manager
	logger    *slog.Logger
	indexSize int64
	dataSize  int64
	dataMode  atomic.Bool
}

func newRemoteArchiveFS(
	ctx context.Context,
	book database.Book,
	store *database.Store,
	client *oneonefive.Client,
	cacheManager *cache.Manager,
	logger *slog.Logger,
	indexSize, dataSize int64,
) *remoteArchiveFS {
	if indexSize <= 0 {
		indexSize = 64 << 10
	}
	if dataSize <= 0 {
		dataSize = 1 << 20
	}
	return &remoteArchiveFS{
		ctx: ctx, book: book, store: store, client: client, cache: cacheManager,
		logger: logger, indexSize: indexSize, dataSize: dataSize,
	}
}

func (r *remoteArchiveFS) UseDataBlocks() {
	r.dataMode.Store(true)
}

func (r *remoteArchiveFS) Open(name string) (fs.File, error) {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(name, "\\", "/")), "./")
	if clean != remoteRARName {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	blockSize := r.indexSize
	if r.dataMode.Load() {
		blockSize = r.dataSize
	}
	reader := NewRemoteReaderAtWithBlockSize(
		r.ctx, r.book, r.store, r.client, r.cache, r.logger, blockSize,
	)
	return &remoteArchiveFile{reader: reader, size: r.book.Size, name: remoteRARName}, nil
}

type remoteArchiveFile struct {
	reader *RemoteReaderAt
	size   int64
	offset int64
	name   string
}

func (r *remoteArchiveFile) Read(p []byte) (int, error) {
	n, err := r.reader.ReadAt(p, r.offset)
	r.offset += int64(n)
	return n, err
}

func (r *remoteArchiveFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.offset + offset
	case io.SeekEnd:
		next = r.size + offset
	default:
		return 0, fs.ErrInvalid
	}
	if next < 0 {
		return 0, fs.ErrInvalid
	}
	r.offset = next
	return next, nil
}

func (r *remoteArchiveFile) Stat() (fs.FileInfo, error) {
	return remoteArchiveFileInfo{name: r.name, size: r.size}, nil
}

func (r *remoteArchiveFile) Close() error {
	return nil
}

type remoteArchiveFileInfo struct {
	name string
	size int64
}

func (r remoteArchiveFileInfo) Name() string       { return r.name }
func (r remoteArchiveFileInfo) Size() int64        { return r.size }
func (r remoteArchiveFileInfo) Mode() fs.FileMode  { return 0o444 }
func (r remoteArchiveFileInfo) ModTime() time.Time { return time.Time{} }
func (r remoteArchiveFileInfo) IsDir() bool        { return false }
func (r remoteArchiveFileInfo) Sys() any           { return nil }
