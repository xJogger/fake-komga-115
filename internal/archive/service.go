package archive

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
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
	Entry PageEntry
	Data  []byte
}

type Service struct {
	zip *ZIPService
	rar *RARService
}

func NewService(
	store *database.Store,
	client *oneonefive.Client,
	cacheManager *cache.Manager,
	logger *slog.Logger,
) *Service {
	return &Service{
		zip: NewZIPService(store, client, cacheManager, logger),
		rar: NewRARService(store, client, cacheManager, logger),
	}
}

func (s *Service) ListPages(ctx context.Context, book database.Book) ([]PageEntry, error) {
	switch archiveFormat(book.Name) {
	case formatZIP:
		return s.zip.ListPages(ctx, book)
	case formatRAR:
		return s.rar.ListPages(ctx, book)
	default:
		return nil, ErrUnsupportedArchive
	}
}

func (s *Service) ReadPage(
	ctx context.Context,
	book database.Book,
	pageNumber int,
) (PageData, error) {
	switch archiveFormat(book.Name) {
	case formatZIP:
		return s.zip.ReadPage(ctx, book, pageNumber)
	case formatRAR:
		return s.rar.ReadPage(ctx, book, pageNumber)
	default:
		return PageData{}, ErrUnsupportedArchive
	}
}

func (s *Service) Prefetch(book database.Book, afterPage, count int) {
	switch archiveFormat(book.Name) {
	case formatZIP:
		s.zip.Prefetch(book, afterPage, count)
	case formatRAR:
		s.rar.Prefetch(book, afterPage, count)
	}
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
