package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
	"github.com/xJogger/fake-komga-115/internal/localfs"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

type ReaderAtCloser interface {
	io.ReaderAt
	io.Closer
}

type noopCloseReaderAt struct {
	io.ReaderAt
}

func (noopCloseReaderAt) Close() error { return nil }

func openBookReaderAt(
	ctx context.Context,
	book database.Book,
	store *database.Store,
	client115 *oneonefive.Client,
	webdavMgr *webdav.Manager,
	cacheManager *cache.Manager,
	logger *slog.Logger,
	blockSize int64,
) (ReaderAtCloser, error) {
	reader, _, err := openBookReaderAtWithSize(ctx, book, store, client115, webdavMgr, cacheManager, logger, blockSize)
	return reader, err
}

func openBookReaderAtWithSize(
	ctx context.Context,
	book database.Book,
	store *database.Store,
	client115 *oneonefive.Client,
	webdavMgr *webdav.Manager,
	cacheManager *cache.Manager,
	logger *slog.Logger,
	blockSize int64,
) (ReaderAtCloser, int64, error) {
	library, err := store.LibraryBasic(ctx, book.LibraryID)
	if err != nil {
		return nil, 0, err
	}
	actualSize := book.Size
	switch library.EffectiveProvider() {
	case database.ProviderLocal:
		file, err := localfs.OpenBook(library.RootPath, book.FileID)
		if err != nil {
			return nil, 0, err
		}
		if detected := detectFileSize(file); detected > 0 {
			if actualSize != detected {
				diagnostics.Default().Record(diagnostics.Event{
					Level:         diagnostics.LevelWarn,
					Component:     "archive",
					Stage:         "size_calibrate",
					Action:        "local_fd",
					CorrelationID: book.ID,
					Detail:        "calibrated local/SAF archive size from file descriptor",
					Meta: map[string]any{
						"provider":    database.ProviderLocal,
						"sizeBytes":   actualSize,
						"actualBytes": detected,
					},
				})
				actualSize = detected
				persistCalibratedBookSize(ctx, store, book.ID, detected)
			}
		}
		return file, actualSize, nil

	case database.ProviderWebDAV:
		if webdavMgr == nil {
			webdavMgr = webdav.NewManager()
		}
		account, err := store.WebDAVAccountSecret(ctx, library.WebDAVAccountID)
		if err != nil {
			return nil, 0, fmt.Errorf("load WebDAV account: %w", err)
		}
		if actualSize <= 0 {
			if detected, statErr := webdavMgr.StatFileSize(ctx, account, library.RootPath, book.FileID); statErr == nil && detected > 0 {
				diagnostics.Default().Record(diagnostics.Event{
					Level:         diagnostics.LevelWarn,
					Component:     "archive",
					Stage:         "size_calibrate",
					Action:        "webdav_stat",
					CorrelationID: book.ID,
					Detail:        "calibrated missing WebDAV archive size",
					Meta: map[string]any{
						"provider":    database.ProviderWebDAV,
						"sizeBytes":   actualSize,
						"actualBytes": detected,
					},
				})
				actualSize = detected
				book.Size = detected
				persistCalibratedBookSize(ctx, store, book.ID, detected)
			}
		}
		return noopCloseReaderAt{NewWebDAVReaderAt(
			ctx, book, library, account, store, webdavMgr, cacheManager, logger, blockSize,
		)}, actualSize, nil

	default:
		return noopCloseReaderAt{NewRemoteReaderAtWithBlockSize(
			ctx, book, store, client115, cacheManager, logger, blockSize,
		)}, actualSize, nil
	}
}

func detectFileSize(file *os.File) int64 {
	if file == nil {
		return 0
	}
	if info, err := file.Stat(); err == nil && info.Size() > 0 {
		return info.Size()
	}
	cur, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0
	}
	end, err := file.Seek(0, io.SeekEnd)
	_, _ = file.Seek(cur, io.SeekStart)
	if err != nil || end <= 0 {
		return 0
	}
	return end
}

func persistCalibratedBookSize(ctx context.Context, store *database.Store, bookID string, size int64) {
	if store == nil || bookID == "" || size <= 0 {
		return
	}
	_, _ = store.DB().ExecContext(ctx, `UPDATE books SET size=? WHERE id=? AND size<>?`, size, bookID, size)
}

type WebDAVReaderAt struct {
	ctx       context.Context
	book      database.Book
	library   database.Library
	account   database.WebDAVAccountSecret
	store     *database.Store
	webdav    *webdav.Manager
	cache     *cache.Manager
	logger    *slog.Logger
	blockSize int64
	maxBytes  int64
}

func NewWebDAVReaderAt(
	ctx context.Context,
	book database.Book,
	library database.Library,
	account database.WebDAVAccountSecret,
	store *database.Store,
	webdavMgr *webdav.Manager,
	cacheManager *cache.Manager,
	logger *slog.Logger,
	blockSize int64,
) *WebDAVReaderAt {
	if blockSize <= 0 {
		blockSize = store.Int64Setting(ctx, "range_block_size", 1<<20)
	}
	if blockSize <= 0 {
		blockSize = 1 << 20
	}
	return &WebDAVReaderAt{
		ctx:       ctx,
		book:      book,
		library:   library,
		account:   account,
		store:     store,
		webdav:    webdavMgr,
		cache:     cacheManager,
		logger:    logger,
		blockSize: blockSize,
		maxBytes:  store.Int64Setting(ctx, "range_cache_max_bytes", 10<<30),
	}
}

func (r *WebDAVReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= r.book.Size {
		return 0, io.EOF
	}
	requested := len(p)
	short := false
	if int64(len(p)) > r.book.Size-off {
		p = p[:r.book.Size-off]
		short = true
	}
	written := 0
	for written < len(p) {
		absolute := off + int64(written)
		blockIndex := absolute / r.blockSize
		blockOffset := absolute % r.blockSize
		block, _, err := r.cache.GetOrLoad(
			r.ctx,
			cache.TypeRange,
			r.blockKey(blockIndex),
			r.maxBytes,
			func(ctx context.Context) ([]byte, error) { return r.fetchBlock(ctx, blockIndex) },
		)
		if err != nil {
			return written, err
		}
		if blockOffset >= int64(len(block)) {
			return written, io.ErrUnexpectedEOF
		}
		n := copy(p[written:], block[blockOffset:])
		written += n
	}
	if written < len(p) || short || written < requested {
		return written, io.EOF
	}
	return written, nil
}

func (r *WebDAVReaderAt) blockKey(index int64) string {
	modText := ""
	if r.book.FileModifiedAt != nil {
		modText = r.book.FileModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf(
		"wd:%s:%s:%d:%s:%s:%d:%d",
		r.book.LibraryID, r.book.FileID, r.book.Size, r.book.SHA1, modText, r.blockSize, index,
	)
}

func (r *WebDAVReaderAt) fetchBlock(ctx context.Context, index int64) ([]byte, error) {
	start := index * r.blockSize
	end := min(start+r.blockSize-1, r.book.Size-1)
	expected := end - start + 1
	started := time.Now()
	data, err := r.webdav.FetchRange(ctx, r.account, r.library.RootPath, r.book.FileID, start, end)
	if err != nil {
		if errors.Is(err, webdav.ErrRangeNotSupported) {
			return nil, ErrRangeNotSupported
		}
		return nil, err
	}
	if err := r.store.RecordBookDownload(ctx, r.book, expected, time.Since(started)); err != nil && r.logger != nil {
		r.logger.Warn("record WebDAV range download stats", "book", r.book.ID, "error", err)
	}
	return data, nil
}
