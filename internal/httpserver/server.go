package httpserver

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/cache"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/oneonefive"
	"github.com/xJogger/fake-komga-115/internal/scanner"
	"github.com/xJogger/fake-komga-115/internal/thumbnail"
)

//go:embed static/admin.html
var staticFiles embed.FS

type Server struct {
	store   *database.Store
	client  *oneonefive.Client
	scanner *scanner.Manager
	cache   *cache.Manager
	archive *archive.Service
	thumbs  *thumbnail.Service
	logger  *slog.Logger
	router  chi.Router

	placeholder []byte
}

func New(
	store *database.Store,
	client *oneonefive.Client,
	scanManager *scanner.Manager,
	cacheManager *cache.Manager,
	archiveService *archive.Service,
	thumbnailService *thumbnail.Service,
	logger *slog.Logger,
) *Server {
	s := &Server{
		store: store, client: client, scanner: scanManager, cache: cacheManager,
		archive: archiveService, thumbs: thumbnailService,
		logger: logger, placeholder: makePlaceholder(),
	}
	s.router = s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) routes() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(s.requestLogger)
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusTemporaryRedirect)
	})
	r.Get("/setup", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusTemporaryRedirect)
	})
	r.Get("/admin", s.adminPage)
	r.Route("/admin/api", s.adminRoutes)
	r.Route("/api/v1", s.komgaRoutes)
	r.Get("/api/v2/series/{seriesID}/read-progress/tachiyomi", s.getSeriesProgress)
	r.Put("/api/v2/series/{seriesID}/read-progress/tachiyomi", s.putSeriesProgress)
	return r
}

func (s *Server) adminPage(w http.ResponseWriter, _ *http.Request) {
	data, err := staticFiles.ReadFile("static/admin.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapped, r)
		s.logger.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", wrapped.Status(), "duration", time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func intQuery(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}

func listQuery(r *http.Request, key string) []string {
	var out []string
	for _, group := range r.URL.Query()[key] {
		for _, item := range strings.Split(group, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

func boolQuery(r *http.Request, key string) *bool {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func (s *Server) archiveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, archive.ErrRangeNotSupported):
		writeError(w, http.StatusNotImplemented, "RANGE_NOT_SUPPORTED", err.Error())
	case errors.Is(err, archive.ErrPageTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "PAGE_TOO_LARGE", err.Error())
	case errors.Is(err, archive.ErrInvalidZIP), errors.Is(err, archive.ErrUnsupportedZIP):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_ZIP", err.Error())
	case errors.Is(err, archive.ErrSolidRAR):
		writeError(w, http.StatusUnprocessableEntity, "SOLID_RAR_NOT_SUPPORTED", err.Error())
	case errors.Is(err, archive.ErrEncryptedRAR):
		writeError(w, http.StatusUnprocessableEntity, "ENCRYPTED_RAR_NOT_SUPPORTED", err.Error())
	case errors.Is(err, archive.ErrMultiVolumeRAR):
		writeError(w, http.StatusUnprocessableEntity, "MULTI_VOLUME_RAR_NOT_SUPPORTED", err.Error())
	case errors.Is(err, archive.ErrInvalidRAR), errors.Is(err, archive.ErrUnsupportedRAR):
		writeError(w, http.StatusUnprocessableEntity, "INVALID_RAR", err.Error())
	case errors.Is(err, archive.ErrUnsupportedArchive):
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_ARCHIVE", err.Error())
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "REQUEST_CANCELED", err.Error())
	default:
		s.logger.Error("archive request", "error", err)
		writeError(w, http.StatusBadGateway, "REMOTE_READ_FAILED", "Failed to read the remote comic archive.")
	}
}

func makePlaceholder() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 320; x++ {
			shade := uint8(40)
			if (x/32+y/32)%2 == 0 {
				shade = 48
			}
			img.Set(x, y, color.RGBA{shade, shade, shade, 255})
		}
	}
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, img)
	return buffer.Bytes()
}

func (s *Server) writePlaceholder(w http.ResponseWriter) {
	s.writePlaceholderWithCache(w, "public, max-age=86400")
}

func (s *Server) writeUncachedPlaceholder(w http.ResponseWriter) {
	s.writePlaceholderWithCache(w, "no-store")
}

func (s *Server) writePlaceholderWithCache(w http.ResponseWriter, cacheControl string) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Content-Length", fmt.Sprint(len(s.placeholder)))
	_, _ = w.Write(s.placeholder)
}
