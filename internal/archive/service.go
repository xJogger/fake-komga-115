package archive

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
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
		store:  store,
		logger: logger,
		zip:    NewZIPServiceWithWebDAV(store, client, webdavManager, cacheManager, logger),
		rar:    NewRARServiceWithWebDAV(store, client, webdavManager, cacheManager, logger),
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

func (s *Service) listPagesDirect(ctx context.Context, book database.Book) ([]PageEntry, error) {
	switch archiveFormat(book.Name) {
	case formatZIP:
		return s.zip.ListPages(ctx, book)
	case formatRAR:
		return s.rar.ListPages(ctx, book)
	default:
		return nil, ErrUnsupportedArchive
	}
}

func (s *Service) readPageDirect(ctx context.Context, book database.Book, pageNumber int) (PageData, error) {
	switch archiveFormat(book.Name) {
	case formatZIP:
		return s.zip.ReadPage(ctx, book, pageNumber)
	case formatRAR:
		return s.rar.ReadPage(ctx, book, pageNumber)
	default:
		return PageData{}, ErrUnsupportedArchive
	}
}

func (s *Service) syncUnionIndexSummary(ctx context.Context, unionBook database.Book, pageCount int, duration time.Duration) {
	if s.store == nil || pageCount <= 0 {
		return
	}
	_ = s.store.RecordArchiveIndexBuild(ctx, unionBook.ID, BookVersion(unionBook), "[]", pageCount, duration)
}

func (s *Service) ListPages(ctx context.Context, book database.Book) ([]PageEntry, error) {
	candidates, isUnion := s.resolveCandidates(ctx, book)
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
	switch archiveFormat(book.Name) {
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
