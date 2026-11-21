package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/webdav"
)

const (
	formatZIP = "zip"
	formatRAR = "rar"
)

type PageEntry struct {
	Number           int    `json:"number"`
	Name             string `json:"name"`
	Format           string `json:"format,omitempty"`
	ArchiveIndex     int    `json:"archiveIndex,omitempty"`
	CompressedSize   uint64 `json:"compressedSize"`
	UncompressedSize uint64 `json:"uncompressedSize"`
	Method           uint16 `json:"method,omitempty"`
	MimeType         string `json:"mimeType"`
	DataOffset       int64  `json:"dataOffset,omitempty"`
	CRC32            uint32 `json:"crc32,omitempty"`
}

type PageData struct {
	Entry      PageEntry
	Data       []byte
	TotalPages int
}

type Service struct {
	store          *database.Store
	logger         *slog.Logger
	zip            *ZIPService
	rar            *RARService
	volumePrefetch *VolumeIndexPrefetcher
	loadTracker    *LoadTracker
}

func NewService(
	store *database.Store,
	client *oneonefive.Client,
	cacheManager *cache.Manager,
	logger *slog.Logger,
) *Service {
	return NewServiceWithWebDAV(store, client, webdav.NewManager(), cacheManager, logger)
}

func NewServiceWithWebDAV(
	store *database.Store,
	client *oneonefive.Client,
	webdavManager *webdav.Manager,
	cacheManager *cache.Manager,
	logger *slog.Logger,
) *Service {
	if webdavManager == nil {
		webdavManager = webdav.NewManager()
	}
	service := &Service{
		store:       store,
		logger:      logger,
		zip:         NewZIPServiceWithWebDAV(store, client, webdavManager, cacheManager, logger),
		rar:         NewRARServiceWithWebDAV(store, client, webdavManager, cacheManager, logger),
		loadTracker: newLoadTracker(),
	}
	service.volumePrefetch = newVolumeIndexPrefetcher(
		store, service.ListPages, logger,
	)
	return service
}

func (s *Service) resolveCandidates(ctx context.Context, book database.Book) ([]database.Book, bool) {
	if s.store != nil {
		if sources, _, err := s.store.UnionBookSourceBooks(ctx, book.ID); err == nil && len(sources) > 0 {
			return sources, true
		}
	}
	return []database.Book{book}, false
}

func shouldFallbackReplica(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return true
}

func (s *Service) cachedArchiveFormat(ctx context.Context, book database.Book) string {
	if s.store == nil {
		return ""
	}
	raw, ok, err := s.store.ArchiveIndexRawForVersion(ctx, book.ID, bookVersion(book))
	if err != nil || !ok {
		return ""
	}
	var pages []PageEntry
	if json.Unmarshal([]byte(raw), &pages) != nil || len(pages) == 0 {
		return ""
	}
	switch pages[0].Format {
	case formatZIP, formatRAR:
		return pages[0].Format
	default:
		return ""
	}
}

func (s *Service) detectMagicFormat(ctx context.Context, book database.Book) string {
	readerAt, actualSize, err := openBookReaderAtWithSize(
		ctx, book, s.store, s.zip.client, s.zip.webdav, s.zip.cache, s.logger, 64<<10,
	)
	if err != nil || actualSize < 4 {
		if readerAt != nil {
			_ = readerAt.Close()
		}
		return ""
	}
	defer readerAt.Close()
	var header [8]byte
	n, err := readerAt.ReadAt(header[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	sig := header[:n]
	switch {
	case len(sig) >= 7 && bytes.HasPrefix(sig, []byte{'R', 'a', 'r', '!', 0x1a, 0x07}):
		return formatRAR
	case len(sig) >= 4 && (bytes.HasPrefix(sig, []byte{'P', 'K', 0x03, 0x04}) ||
		bytes.HasPrefix(sig, []byte{'P', 'K', 0x05, 0x06}) ||
		bytes.HasPrefix(sig, []byte{'P', 'K', 0x07, 0x08})):
		return formatZIP
	default:
		return ""
	}
}

func (s *Service) effectiveFormat(ctx context.Context, book database.Book) string {
	if cached := s.cachedArchiveFormat(ctx, book); cached != "" {
		return cached
	}
	return archiveFormat(book.Name)
}

func (s *Service) listPagesDirect(ctx context.Context, book database.Book) ([]PageEntry, error) {
	primary := s.effectiveFormat(ctx, book)
	var (
		pages []PageEntry
		err   error
	)
	switch primary {
	case formatZIP:
		pages, err = s.zip.ListPages(ctx, book)
	case formatRAR:
		pages, err = s.rar.ListPages(ctx, book)
	default:
		return nil, ErrUnsupportedArchive
	}
	if err == nil {
		return pages, nil
	}
	if (primary == formatZIP && errors.Is(err, ErrInvalidZIP)) || (primary == formatRAR && errors.Is(err, ErrInvalidRAR)) {
		if detected := s.detectMagicFormat(ctx, book); detected != "" && detected != primary {
			diagnostics.Default().Record(diagnostics.Event{
				Level:         diagnostics.LevelWarn,
				Component:     "archive",
				Stage:         "archive_fallback",
				Action:        "magic_detect",
				CorrelationID: book.ID,
				Detail:        "archive file header differs from extension; falling back by magic bytes",
				Meta:          map[string]any{"format": primary, "fallbackFormat": detected},
			})
			switch detected {
			case formatZIP:
				return s.zip.ListPages(ctx, book)
			case formatRAR:
				return s.rar.ListPages(ctx, book)
			}
		}
	}
	return nil, err
}

func (s *Service) readPageDirect(ctx context.Context, book database.Book, pageNumber int) (PageData, error) {
	started := time.Now()
	primary := s.effectiveFormat(ctx, book)
	var (
		page PageData
		err  error
	)
	switch primary {
	case formatZIP:
		page, err = s.zip.ReadPage(ctx, book, pageNumber)
	case formatRAR:
		page, err = s.rar.ReadPage(ctx, book, pageNumber)
	default:
		return PageData{}, ErrUnsupportedArchive
	}
	if err != nil && ((primary == formatZIP && errors.Is(err, ErrInvalidZIP)) || (primary == formatRAR && errors.Is(err, ErrInvalidRAR))) {
		if detected := s.detectMagicFormat(ctx, book); detected != "" && detected != primary {
			diagnostics.Default().Record(diagnostics.Event{
				Level:         diagnostics.LevelWarn,
				Component:     "archive",
				Stage:         "archive_fallback",
				Action:        "magic_detect",
				CorrelationID: book.ID,
				Detail:        "archive file header differs from extension during page read; falling back by magic bytes",
				Meta:          map[string]any{"format": primary, "fallbackFormat": detected, "pageNumber": pageNumber},
			})
			switch detected {
			case formatZIP:
				page, err = s.zip.ReadPage(ctx, book, pageNumber)
			case formatRAR:
				page, err = s.rar.ReadPage(ctx, book, pageNumber)
			}
		}
	}
	if err != nil {
		diagnostics.Default().Record(diagnostics.Event{
			Level:         diagnostics.LevelError,
			Component:     "archive",
			Stage:         "page_read",
			Action:        "read",
			Code:          diagnostics.ClassifyError(err),
			DurationMs:    time.Since(started).Milliseconds(),
			CorrelationID: book.ID,
			Detail:        err.Error(),
			Meta:          map[string]any{"format": primary, "pageNumber": pageNumber},
		})
		return PageData{}, err
	}
	diagnostics.Default().RecordVerbose(diagnostics.Event{
		Level:         diagnostics.LevelDebug,
		Component:     "archive",
		Stage:         "page_read",
		Action:        "read",
		DurationMs:    time.Since(started).Milliseconds(),
		CorrelationID: book.ID,
		Meta:          map[string]any{"format": page.Entry.Format, "pageNumber": pageNumber, "sizeBytes": len(page.Data)},
	})
	return page, nil
}

func (s *Service) syncUnionIndexSummary(ctx context.Context, unionBook database.Book, pageCount int, duration time.Duration) {
	if s.store == nil || pageCount <= 0 {
		return
	}
	_ = s.store.RecordArchiveIndexBuild(ctx, unionBook.ID, BookVersion(unionBook), "[]", pageCount, duration)
}

// LoadStatus returns live server-side indexing/streaming progress for a book or specific page.
func (s *Service) LoadStatus(bookID string, pageNumber int) LoadProgress {
	if s == nil || s.loadTracker == nil {
		return LoadProgress{BookID: bookID, Stage: "idle"}
	}
	return s.loadTracker.Status(bookID, pageNumber)
}

func (s *Service) ListPages(ctx context.Context, book database.Book) ([]PageEntry, error) {
	candidates, isUnion := s.resolveCandidates(ctx, book)
	ctx, finish := s.loadTracker.begin(ctx, book.ID, 0, "indexing", "正在读取归档目录索引…", book.Size)
	defer finish()
	var lastErr error
	for idx, cand := range candidates {
		started := time.Now()
		pages, err := s.listPagesDirect(ctx, cand)
		if err == nil {
			if isUnion {
				s.syncUnionIndexSummary(ctx, book, len(pages), time.Since(started))
			}
			return pages, nil
		}
		lastErr = err
		if !shouldFallbackReplica(err) || idx+1 >= len(candidates) {
			return nil, err
		}
		reportLoadStage(ctx, "indexing", "当前存储层读取失败，正在自动切换备用层…")
		if s.logger != nil {
			s.logger.Warn("union book listPages failed on layer, falling back",
				"unionBook", book.ID, "failedLibrary", cand.LibraryID,
				"nextLibrary", candidates[idx+1].LibraryID, "error", err)
		}
	}
	return nil, lastErr
}

func (s *Service) ReadPage(
	ctx context.Context,
	book database.Book,
	pageNumber int,
) (PageData, error) {
	candidates, isUnion := s.resolveCandidates(ctx, book)
	ctx, finish := s.loadTracker.begin(ctx, book.ID, pageNumber, "downloading", "正在准备远程页面数据…", 0)
	defer finish()
	var lastErr error
	for idx, cand := range candidates {
		started := time.Now()
		page, err := s.readPageDirect(ctx, cand, pageNumber)
		if err == nil {
			if isUnion && page.TotalPages > 0 {
				if ok, _ := s.store.HasArchiveIndexVersion(ctx, book.ID, BookVersion(book)); !ok {
					s.syncUnionIndexSummary(ctx, book, page.TotalPages, time.Since(started))
				}
			}
			return page, nil
		}
		lastErr = err
		if !shouldFallbackReplica(err) || idx+1 >= len(candidates) {
			return PageData{}, err
		}
		reportLoadStage(ctx, "downloading", "当前存储层读取失败，正在切换备用层重试…")
		if s.logger != nil {
			s.logger.Warn("union book readPage failed on layer, falling back",
				"unionBook", book.ID, "page", pageNumber, "failedLibrary", cand.LibraryID,
				"nextLibrary", candidates[idx+1].LibraryID, "error", err)
		}
	}
	return PageData{}, lastErr
}

func (s *Service) Prefetch(book database.Book, afterPage, count int) {
	if count <= 0 {
		return
	}
	if s.store != nil {
		if sources, _, err := s.store.UnionBookSourceBooks(context.Background(), book.ID); err == nil && len(sources) > 0 {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				for page := afterPage + 1; page <= afterPage+count; page++ {
					if _, err := s.ReadPage(ctx, book, page); err != nil {
						return
					}
				}
			}()
			return
		}
	}
	switch s.effectiveFormat(context.Background(), book) {
	case formatZIP:
		s.zip.Prefetch(book, afterPage, count)
	case formatRAR:
		s.rar.Prefetch(book, afterPage, count)
	}
}

func (s *Service) PrefetchNextVolumeIndex(
	book database.Book,
	pageNumber, totalPages int,
) {
	s.volumePrefetch.Trigger(book, pageNumber, totalPages)
}

func (s *Service) Close() {
	s.volumePrefetch.Close()
}

func MediaType(name string) string {
	switch archiveFormat(name) {
	case formatZIP:
		return "application/zip"
	case formatRAR:
		return "application/x-rar-compressed"
	default:
		return "application/octet-stream"
	}
}

func archiveFormat(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".cbz":
		return formatZIP
	case ".rar", ".cbr":
		return formatRAR
	default:
		return ""
	}
}
