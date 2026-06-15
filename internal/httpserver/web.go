package httpserver

import (
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/database"
)

type webSeriesPage struct {
	Name         string
	LibraryName  string
	RelativePath string
	CoverURL     string
	BooksCount   int
	TotalSize    string
	CreatedAt    string
	LastModified string
	OneShot      bool
	Books        []webBookRow
}

type webBookRow struct {
	Name         string
	URL          string
	Size         string
	Pages        string
	LastModified string
}

type webBookPage struct {
	Name         string
	SeriesName   string
	SeriesURL    string
	LibraryName  string
	RelativePath string
	Size         string
	Pages        string
	MediaType    string
	CreatedAt    string
	LastModified string
	ShowCover    bool
	CoverURL     string
	OneShot      bool
}

func (s *Server) webStyles(w http.ResponseWriter, _ *http.Request) {
	data, err := staticFiles.ReadFile("static/web.css")
	if err != nil {
		http.Error(w, "stylesheet unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(data)
}

func (s *Server) webSeries(w http.ResponseWriter, r *http.Request) {
	series, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if errors.Is(err, sql.ErrNoRows) {
		s.renderNotFound(w)
		return
	}
	if err != nil {
		s.renderWebError(w, err)
		return
	}
	library, err := s.store.Library(r.Context(), series.LibraryID)
	if err != nil {
		s.renderWebError(w, err)
		return
	}
	books, _, err := s.store.BooksPage(r.Context(), database.BookQuery{
		SeriesID: series.ID, Unpaged: true,
	})
	if err != nil {
		s.renderWebError(w, err)
		return
	}

	rows := make([]webBookRow, 0, len(books))
	var totalSize int64
	for _, book := range books {
		totalSize += book.Size
		rows = append(rows, webBookRow{
			Name: book.Name, URL: "/book/" + book.ID, Size: formatBytes(book.Size),
			Pages: pageCountText(book.PageCount), LastModified: bookModifiedTime(book),
		})
	}
	modified := series.UpdatedAt
	if series.FileModifiedAt != nil {
		modified = *series.FileModifiedAt
	}
	s.renderWeb(w, http.StatusOK, "web_series.html", webSeriesPage{
		Name: series.Name, LibraryName: library.Name, RelativePath: series.RelativePath,
		CoverURL:   "/api/v1/series/" + series.ID + "/thumbnail",
		BooksCount: len(books), TotalSize: formatBytes(totalSize),
		CreatedAt: webTime(series.CreatedAt), LastModified: webTime(modified),
		OneShot: series.OneShot, Books: rows,
	})
}

func (s *Server) webBook(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if errors.Is(err, sql.ErrNoRows) {
		s.renderNotFound(w)
		return
	}
	if err != nil {
		s.renderWebError(w, err)
		return
	}
	series, err := s.store.SeriesByID(r.Context(), book.SeriesID)
	if err != nil {
		s.renderWebError(w, err)
		return
	}
	library, err := s.store.Library(r.Context(), book.LibraryID)
	if err != nil {
		s.renderWebError(w, err)
		return
	}
	created := book.CreatedAt
	if book.FileCreatedAt != nil {
		created = *book.FileCreatedAt
	}
	modified := book.UpdatedAt
	if book.FileModifiedAt != nil {
		modified = *book.FileModifiedAt
	}
	s.renderWeb(w, http.StatusOK, "web_book.html", webBookPage{
		Name: book.Name, SeriesName: series.Name, SeriesURL: "/series/" + series.ID,
		LibraryName: library.Name, RelativePath: webBookPath(series, book),
		Size: formatBytes(book.Size), Pages: pageCountText(book.PageCount),
		MediaType: webArchiveType(book.Name), CreatedAt: webTime(created),
		LastModified: webTime(modified), ShowCover: series.OneShot,
		CoverURL: "/api/v1/series/" + series.ID + "/thumbnail", OneShot: series.OneShot,
	})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") ||
		strings.HasPrefix(r.URL.Path, "/admin/api/") {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Endpoint not found.")
		return
	}
	s.renderNotFound(w)
}

func (s *Server) renderNotFound(w http.ResponseWriter) {
	s.renderWeb(w, http.StatusNotFound, "web_404.html", nil)
}

func (s *Server) renderWebError(w http.ResponseWriter, err error) {
	s.logger.Error("render web page", "error", err)
	s.renderWeb(w, http.StatusInternalServerError, "web_error.html", nil)
}

func (s *Server) renderWeb(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self' 'unsafe-inline'")
	w.WriteHeader(status)
	if err := s.web.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("execute web template", "template", name, "error", err)
	}
}

func webBookPath(series database.Series, book database.Book) string {
	if series.OneShot {
		return series.RelativePath
	}
	return path.Join(series.RelativePath, book.Name)
}

func pageCountText(count int) string {
	if count <= 0 {
		return "尚未建立页面索引"
	}
	return strconv.Itoa(count) + " 页"
}

func bookModifiedTime(book database.Book) string {
	value := book.UpdatedAt
	if book.FileModifiedAt != nil {
		value = *book.FileModifiedAt
	}
	return webTime(value)
}

func webTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func webArchiveType(name string) string {
	extension := strings.TrimPrefix(path.Ext(name), ".")
	if extension == "" {
		return "漫画归档"
	}
	return strings.ToUpper(extension) + " 漫画归档"
}
