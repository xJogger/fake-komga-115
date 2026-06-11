package thumbnail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	defaultMaxEdge = 300
	jpegQuality    = 75
	maxImagePixels = 100_000_000
)

type Data struct {
	Bytes     []byte
	MediaType string
	Width     int
	Height    int
}

type Stats struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

type Service struct {
	store  *database.Store
	root   string
	logger *slog.Logger

	operationMu sync.RWMutex
	inflightMu  sync.Mutex
	inflight    map[string]struct{}
}

func New(store *database.Store, root string, logger *slog.Logger) (*Service, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	service := &Service{
		store: store, root: root, logger: logger, inflight: make(map[string]struct{}),
	}
	if err := service.cleanupOrphans(context.Background()); err != nil {
		logger.Warn("clean orphan series thumbnails", "error", err)
	}
	return service, nil
}

func (s *Service) MaybeGenerate(book database.Book, pageNumber int, page []byte) {
	if pageNumber != 1 || len(page) == 0 || !s.claim(book.SeriesID) {
		return
	}
	go func() {
		defer s.release(book.SeriesID)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.Generate(ctx, book, page); err != nil {
			s.logger.Warn("generate series thumbnail", "series", book.SeriesID, "error", err)
		}
	}()
}

func (s *Service) Generate(ctx context.Context, book database.Book, page []byte) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	first, err := s.store.FirstBookInSeries(ctx, book.SeriesID)
	if err != nil {
		return err
	}
	if first.ID != book.ID {
		return nil
	}
	version := archive.BookVersion(first)
	if current, ok, err := s.metadata(ctx, book.SeriesID); err != nil {
		return err
	} else if ok && current.SourceBookID == first.ID && current.SourceVersion == version {
		if _, err := os.Stat(s.path(current.Path)); err == nil {
			return nil
		}
	}

	thumbnail, width, height, err := compress(page)
	if err != nil {
		return err
	}
	name := fileName(book.SeriesID)
	if err := writeAtomic(s.path(name), thumbnail); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.store.DB().ExecContext(ctx, `
INSERT INTO series_thumbnails(
 series_id,source_book_id,source_version,path,media_type,width,height,size,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(series_id) DO UPDATE SET
 source_book_id=excluded.source_book_id,source_version=excluded.source_version,
 path=excluded.path,media_type=excluded.media_type,width=excluded.width,height=excluded.height,
 size=excluded.size,updated_at=excluded.updated_at`,
		book.SeriesID, first.ID, version, name, "image/jpeg", width, height,
		len(thumbnail), now, now)
	if err != nil {
		_ = os.Remove(s.path(name))
		return err
	}
	s.logger.Info(
		"series thumbnail generated",
		"series", book.SeriesID, "book", book.ID, "width", width, "height", height,
	)
	return nil
}

func (s *Service) Get(ctx context.Context, seriesID string) (Data, bool, error) {
	s.operationMu.RLock()
	defer s.operationMu.RUnlock()

	first, err := s.store.FirstBookInSeries(ctx, seriesID)
	if errors.Is(err, sql.ErrNoRows) {
		return Data{}, false, nil
	}
	if err != nil {
		return Data{}, false, err
	}
	item, ok, err := s.metadata(ctx, seriesID)
	if err != nil || !ok {
		return Data{}, false, err
	}
	if item.SourceBookID != first.ID || item.SourceVersion != archive.BookVersion(first) {
		s.delete(ctx, seriesID, item)
		return Data{}, false, nil
	}
	data, err := os.ReadFile(s.path(item.Path))
	if errors.Is(err, fs.ErrNotExist) {
		s.delete(ctx, seriesID, item)
		return Data{}, false, nil
	}
	if err != nil {
		return Data{}, false, err
	}
	return Data{
		Bytes: data, MediaType: item.MediaType, Width: item.Width, Height: item.Height,
	}, true, nil
}

func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	err := s.store.DB().QueryRowContext(ctx,
		`SELECT count(*),coalesce(sum(size),0) FROM series_thumbnails`).
		Scan(&stats.Files, &stats.Bytes)
	return stats, err
}

func (s *Service) Clear(ctx context.Context) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if err := os.RemoveAll(s.root); err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	_, err := s.store.DB().ExecContext(ctx, `DELETE FROM series_thumbnails`)
	return err
}

type metadata struct {
	SourceBookID  string
	SourceVersion string
	Path          string
	MediaType     string
	Width         int
	Height        int
}

func (s *Service) metadata(ctx context.Context, seriesID string) (metadata, bool, error) {
	var item metadata
	err := s.store.DB().QueryRowContext(ctx, `
SELECT source_book_id,source_version,path,media_type,width,height
FROM series_thumbnails WHERE series_id=?`, seriesID).Scan(
		&item.SourceBookID, &item.SourceVersion, &item.Path,
		&item.MediaType, &item.Width, &item.Height,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return metadata{}, false, nil
	}
	return item, err == nil, err
}

func (s *Service) delete(ctx context.Context, seriesID string, item metadata) {
	_ = os.Remove(s.path(item.Path))
	_, _ = s.store.DB().ExecContext(ctx,
		`DELETE FROM series_thumbnails WHERE series_id=?`, seriesID)
}

func (s *Service) path(name string) string {
	return filepath.Join(s.root, filepath.Base(name))
}

func (s *Service) claim(seriesID string) bool {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if _, exists := s.inflight[seriesID]; exists {
		return false
	}
	s.inflight[seriesID] = struct{}{}
	return true
}

func (s *Service) release(seriesID string) {
	s.inflightMu.Lock()
	delete(s.inflight, seriesID)
	s.inflightMu.Unlock()
}

func (s *Service) cleanupOrphans(ctx context.Context) error {
	rows, err := s.store.DB().QueryContext(ctx, `SELECT path FROM series_thumbnails`)
	if err != nil {
		return err
	}
	known := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		known[filepath.Base(name)] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := known[entry.Name()]; !ok {
			_ = os.Remove(filepath.Join(s.root, entry.Name()))
		}
	}
	return nil
}

func fileName(seriesID string) string {
	sum := sha256.Sum256([]byte(seriesID))
	return hex.EncodeToString(sum[:]) + ".jpg"
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".thumbnail-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func compress(data []byte) ([]byte, int, int, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode cover image configuration: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width)*int64(config.Height) > maxImagePixels {
		return nil, 0, 0, errors.New("cover image dimensions are invalid or too large")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode cover image: %w", err)
	}
	width, height := scaledSize(config.Width, config.Height, defaultMaxEdge)
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	stddraw.Draw(target, target.Bounds(), &image.Uniform{C: color.White}, image.Point{}, stddraw.Src)
	xdraw.CatmullRom.Scale(target, target.Bounds(), source, source.Bounds(), stddraw.Over, nil)

	var output bytes.Buffer
	if err := jpeg.Encode(&output, target, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, 0, 0, err
	}
	return output.Bytes(), width, height, nil
}

func scaledSize(width, height, maxEdge int) (int, int) {
	if width <= maxEdge && height <= maxEdge {
		return width, height
	}
	if width >= height {
		return maxEdge, max(1, height*maxEdge/width)
	}
	return max(1, width*maxEdge/height), maxEdge
}
