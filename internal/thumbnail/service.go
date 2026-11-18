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
	"golang.org/x/sync/singleflight"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/database"
)

type CoverFetcher func(ctx context.Context, coverURL string, subjectID int64) ([]byte, error)

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

type SeriesStats struct {
	SourceBookID       string
	SourceVersion      string
	MediaType          string
	Width              int
	Height             int
	Size               int
	GenerationDuration time.Duration
	UpdatedAt          time.Time
}

type Service struct {
	store  *database.Store
	root   string
	bgmDir string
	logger *slog.Logger

	bgmFetcher CoverFetcher
	bgmFlight  singleflight.Group
	bgmSem     chan struct{}

	operationMu sync.RWMutex
	inflightMu  sync.Mutex
	inflight    map[string]struct{}
}

func New(store *database.Store, root string, logger *slog.Logger) (*Service, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	bgmDir := filepath.Join(filepath.Dir(root), "bangumi")
	if err := os.MkdirAll(bgmDir, 0o700); err != nil {
		return nil, err
	}
	service := &Service{
		store: store, root: root, bgmDir: bgmDir, logger: logger,
		bgmSem:   make(chan struct{}, 6),
		inflight: make(map[string]struct{}),
	}
	if err := service.cleanupOrphans(context.Background()); err != nil {
		logger.Warn("clean orphan series thumbnails", "error", err)
	}
	return service, nil
}

func (s *Service) SetBangumiCoverFetcher(fetcher CoverFetcher) {
	s.bgmFetcher = fetcher
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
	return s.GenerateWithOptions(ctx, book, page, false)
}

func (s *Service) GenerateWithOptions(
	ctx context.Context,
	book database.Book,
	page []byte,
	force bool,
) error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	started := time.Now()

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
	} else if !force && ok && current.SourceBookID == first.ID && current.SourceVersion == version {
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
 series_id,source_book_id,source_version,path,media_type,width,height,size,
 generation_duration_ns,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(series_id) DO UPDATE SET
 source_book_id=excluded.source_book_id,source_version=excluded.source_version,
 path=excluded.path,media_type=excluded.media_type,width=excluded.width,height=excluded.height,
 size=excluded.size,generation_duration_ns=excluded.generation_duration_ns,
 updated_at=excluded.updated_at`,
		book.SeriesID, first.ID, version, name, "image/jpeg", width, height,
		len(thumbnail), time.Since(started).Nanoseconds(), now, now)
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

func (s *Service) SetGenerationDuration(
	ctx context.Context,
	seriesID string,
	duration time.Duration,
) error {
	if duration <= 0 {
		return nil
	}
	_, err := s.store.DB().ExecContext(ctx, `
UPDATE series_thumbnails SET generation_duration_ns=? WHERE series_id=?`,
		duration.Nanoseconds(), seriesID)
	return err
}

func (s *Service) Get(ctx context.Context, seriesID string) (Data, bool, error) {
	if data, ok, preferOnlyBgm := s.getBangumiCoverIfPreferred(ctx, seriesID); ok {
		return data, true, nil
	} else if preferOnlyBgm {
		return Data{}, false, nil
	}

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

func (s *Service) GetOrFetchBangumiCover(ctx context.Context, subjectID int64, coverURL string) (Data, error) {
	if subjectID <= 0 {
		return Data{}, errors.New("invalid Bangumi subject ID")
	}
	expectedFile := fmt.Sprintf("bgm_%d.jpg", subjectID)
	diskFull := filepath.Join(s.bgmDir, expectedFile)
	if raw, err := os.ReadFile(diskFull); err == nil && len(raw) > 0 {
		return Data{Bytes: raw, MediaType: "image/jpeg"}, nil
	}
	if s.bgmFetcher == nil {
		return Data{}, errors.New("Bangumi cover fetcher is not configured")
	}
	flightKey := fmt.Sprintf("%d", subjectID)
	val, fetchErr, _ := s.bgmFlight.Do(flightKey, func() (any, error) {
		select {
		case s.bgmSem <- struct{}{}:
			defer func() { <-s.bgmSem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if raw, err := os.ReadFile(diskFull); err == nil && len(raw) > 0 {
			return raw, nil
		}
		dlCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		fetched, err := s.bgmFetcher(dlCtx, coverURL, subjectID)
		if err != nil {
			return nil, err
		}
		compressed, _, _, err := compressWithMaxEdge(fetched, 420)
		if err != nil {
			return nil, err
		}
		if err := writeAtomic(diskFull, compressed); err != nil {
			return nil, err
		}
		return compressed, nil
	})
	if fetchErr != nil || val == nil {
		if fetchErr == nil {
			fetchErr = errors.New("empty Bangumi cover result")
		}
		return Data{}, fetchErr
	}
	return Data{Bytes: val.([]byte), MediaType: "image/jpeg"}, nil
}

func (s *Service) getBangumiCoverIfPreferred(ctx context.Context, seriesID string) (Data, bool, bool) {
	var (
		bgmID       sql.NullInt64
		coverSource string
		coverURL    string
		coverPath   string
	)
	err := s.store.DB().QueryRowContext(ctx, `
SELECT bgm_subject_id,cover_source,bgm_cover_url,bgm_cover_path
FROM series_metadata WHERE series_id=?`, seriesID).Scan(&bgmID, &coverSource, &coverURL, &coverPath)
	if err != nil || !bgmID.Valid || bgmID.Int64 <= 0 {
		return Data{}, false, false
	}
	if coverSource == database.CoverSourceBook {
		return Data{}, false, false
	}
	forceOnlyBgm := coverSource == database.CoverSourceBangumi
	subjectID := bgmID.Int64
	expectedFile := fmt.Sprintf("bgm_%d.jpg", subjectID)
	if coverPath != "" {
		if raw, err := os.ReadFile(filepath.Join(s.bgmDir, filepath.Base(coverPath))); err == nil && len(raw) > 0 {
			return Data{Bytes: raw, MediaType: "image/jpeg"}, true, forceOnlyBgm
		}
	}
	data, fetchErr := s.GetOrFetchBangumiCover(ctx, subjectID, coverURL)
	if fetchErr != nil {
		if s.logger != nil {
			s.logger.Debug("lazy fetch Bangumi cover failed", "series", seriesID, "subject", subjectID, "error", fetchErr)
		}
		return Data{}, false, forceOnlyBgm
	}
	_ = s.store.SetSeriesBgmCoverPath(ctx, seriesID, expectedFile)
	return data, true, forceOnlyBgm
}

func (s *Service) BangumiStats(ctx context.Context) (Stats, error) {
	var st Stats
	entries, err := os.ReadDir(s.bgmDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		return st, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if info, err := entry.Info(); err == nil {
			st.Files++
			st.Bytes += info.Size()
		}
	}
	return st, nil
}

func (s *Service) ClearBangumiCovers(ctx context.Context) error {
	if err := os.RemoveAll(s.bgmDir); err != nil {
		return err
	}
	if err := os.MkdirAll(s.bgmDir, 0o700); err != nil {
		return err
	}
	_, err := s.store.DB().ExecContext(ctx, `UPDATE series_metadata SET bgm_cover_path='' WHERE bgm_cover_path<>''`)
	return err
}

func (s *Service) HasValid(ctx context.Context, seriesID string) (bool, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	first, err := s.store.FirstBookInSeries(ctx, seriesID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	item, ok, err := s.metadata(ctx, seriesID)
	if err != nil || !ok {
		return false, err
	}
	if item.SourceBookID != first.ID || item.SourceVersion != archive.BookVersion(first) {
		s.delete(ctx, seriesID, item)
		return false, nil
	}
	if _, err := os.Stat(s.path(item.Path)); errors.Is(err, fs.ErrNotExist) {
		s.delete(ctx, seriesID, item)
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	err := s.store.DB().QueryRowContext(ctx,
		`SELECT count(*),coalesce(sum(size),0) FROM series_thumbnails`).
		Scan(&stats.Files, &stats.Bytes)
	return stats, err
}

func (s *Service) SeriesStats(
	ctx context.Context,
	seriesID string,
) (SeriesStats, bool, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	first, err := s.store.FirstBookInSeries(ctx, seriesID)
	if errors.Is(err, sql.ErrNoRows) {
		return SeriesStats{}, false, nil
	}
	if err != nil {
		return SeriesStats{}, false, err
	}
	item, ok, err := s.metadata(ctx, seriesID)
	if err != nil || !ok {
		return SeriesStats{}, false, err
	}
	if item.SourceBookID != first.ID || item.SourceVersion != archive.BookVersion(first) {
		s.delete(ctx, seriesID, item)
		return SeriesStats{}, false, nil
	}
	if _, err := os.Stat(s.path(item.Path)); errors.Is(err, fs.ErrNotExist) {
		s.delete(ctx, seriesID, item)
		return SeriesStats{}, false, nil
	} else if err != nil {
		return SeriesStats{}, false, err
	}
	return SeriesStats{
		SourceBookID:       item.SourceBookID,
		SourceVersion:      item.SourceVersion,
		MediaType:          item.MediaType,
		Width:              item.Width,
		Height:             item.Height,
		Size:               item.Size,
		GenerationDuration: item.GenerationDuration,
		UpdatedAt:          item.UpdatedAt,
	}, true, nil
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
	SourceBookID       string
	SourceVersion      string
	Path               string
	MediaType          string
	Width              int
	Height             int
	Size               int
	GenerationDuration time.Duration
	UpdatedAt          time.Time
}

func (s *Service) metadata(ctx context.Context, seriesID string) (metadata, bool, error) {
	var item metadata
	var durationNS int64
	var updated string
	err := s.store.DB().QueryRowContext(ctx, `
SELECT source_book_id,source_version,path,media_type,width,height,size,generation_duration_ns,updated_at
FROM series_thumbnails WHERE series_id=?`, seriesID).Scan(
		&item.SourceBookID, &item.SourceVersion, &item.Path,
		&item.MediaType, &item.Width, &item.Height, &item.Size, &durationNS, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return metadata{}, false, nil
	}
	item.GenerationDuration = time.Duration(durationNS)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
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
	return compressWithMaxEdge(data, defaultMaxEdge)
}

func compressWithMaxEdge(data []byte, maxEdge int) ([]byte, int, int, error) {
	if maxEdge <= 0 {
		maxEdge = defaultMaxEdge
	}
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
	width, height := scaledSize(config.Width, config.Height, maxEdge)
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
