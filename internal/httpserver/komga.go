package httpserver

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/archive"
	"github.com/xJogger/fake-komga-115/internal/database"
)

func (s *Server) komgaRoutes(r chi.Router) {
	r.Get("/libraries", s.komgaLibraries)
	r.Get("/series", s.komgaSeries)
	r.Get("/series/{seriesID}", s.komgaSeriesByID)
	r.Get("/series/{seriesID}/books", s.komgaSeriesBooks)
	r.Get("/series/{seriesID}/thumbnail", s.komgaSeriesThumbnail)
	r.Get("/books", s.komgaBooks)
	r.Get("/books/{bookID}", s.komgaBookByID)
	r.Get("/books/{bookID}/pages", s.komgaPages)
	r.Get("/books/{bookID}/pages/{pageNumber}", s.komgaPageImage)
	r.Get("/books/{bookID}/pages/{pageNumber}/raw", s.komgaPageImage)
	r.Get("/books/{bookID}/pages/{pageNumber}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })
	r.Get("/books/{bookID}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })
	r.Get("/collections", s.emptyPage)
	r.Get("/collections/{id}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })
	r.Get("/readlists", s.emptyPage)
	r.Get("/readlists/{id}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })
	r.Get("/genres", s.emptyList)
	r.Get("/tags", s.emptyList)
	r.Get("/publishers", s.emptyList)
	r.Get("/authors", s.emptyList)
}

func (s *Server) komgaLibraries(w http.ResponseWriter, r *http.Request) {
	libraries, err := s.store.Libraries(r.Context(), true)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(libraries))
	for _, library := range libraries {
		out = append(out, map[string]any{
			"id": library.ID, "name": library.Name, "root": "",
			"importComicInfoBook": false, "importComicInfoSeries": false,
			"importComicInfoCollection": false, "importComicInfoReadList": false,
			"importEpubBook": false, "importEpubSeries": false,
			"oneshotsDirectory": func() any {
				if library.OneShot {
					return "."
				}
				return nil
			}(),
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) komgaSeries(w http.ResponseWriter, r *http.Request) {
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	items, total, err := s.store.SeriesPage(r.Context(), database.SeriesQuery{
		Search: r.URL.Query().Get("search"), LibraryIDs: listQuery(r, "library_id"),
		OneShot: boolQuery(r, "oneshot"), Page: page, Size: size, Sort: r.URL.Query().Get("sort"),
	})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, seriesDTO(item))
	}
	writeJSON(w, 200, makePage(out, page, size, total, false))
}

func (s *Server) komgaSeriesByID(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "NOT_FOUND", "Series not found.")
			return
		}
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, seriesDTO(item))
}

func (s *Server) komgaSeriesBooks(w http.ResponseWriter, r *http.Request) {
	s.komgaBookPage(w, r, chi.URLParam(r, "seriesID"))
}

func (s *Server) komgaBooks(w http.ResponseWriter, r *http.Request) {
	s.komgaBookPage(w, r, "")
}

func (s *Server) komgaBookPage(w http.ResponseWriter, r *http.Request, seriesID string) {
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	unpaged := r.URL.Query().Get("unpaged") == "true"
	items, total, err := s.store.BooksPage(r.Context(), database.BookQuery{
		Search: r.URL.Query().Get("search"), LibraryIDs: listQuery(r, "library_id"),
		SeriesID: seriesID, OneShot: boolQuery(r, "oneshot"),
		Page: page, Size: size, Unpaged: unpaged, Sort: r.URL.Query().Get("sort"),
	})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		series, err := s.store.SeriesByID(r.Context(), item.SeriesID)
		if err != nil {
			writeError(w, 500, "DATABASE_ERROR", err.Error())
			return
		}
		out = append(out, bookDTO(item, series))
	}
	writeJSON(w, 200, makePage(out, page, size, total, unpaged))
}

func (s *Server) komgaBookByID(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "NOT_FOUND", "Book not found.")
			return
		}
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	series, err := s.store.SeriesByID(r.Context(), book.SeriesID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, bookDTO(book, series))
}

func (s *Server) komgaPages(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		writeError(w, 404, "NOT_FOUND", "Book not found.")
		return
	}
	pages, err := s.archive.ListPages(r.Context(), book)
	if err != nil {
		s.archiveError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(pages))
	for _, page := range pages {
		out = append(out, map[string]any{
			"number": page.Number, "fileName": page.Name, "mediaType": page.MimeType,
			"width": nil, "height": nil, "sizeBytes": page.UncompressedSize,
			"size": formatBytes(int64(page.UncompressedSize)),
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) komgaPageImage(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		writeError(w, 404, "NOT_FOUND", "Book not found.")
		return
	}
	pageNumber, err := strconv.Atoi(chi.URLParam(r, "pageNumber"))
	if err != nil || pageNumber < 1 {
		writeError(w, 400, "INVALID_PAGE", "Page numbers start at 1.")
		return
	}
	page, err := s.archive.ReadPage(r.Context(), book, pageNumber)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "NOT_FOUND", "Page not found.")
			return
		}
		s.archiveError(w, err)
		return
	}
	w.Header().Set("Content-Type", page.Entry.MimeType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(page.Data)))
	_, _ = w.Write(page.Data)
	s.thumbs.MaybeGenerate(book, pageNumber, page.Data)
	s.archive.Prefetch(book, pageNumber, int(s.store.Int64Setting(r.Context(), "page_prefetch_count", 2)))
}

func (s *Server) komgaSeriesThumbnail(w http.ResponseWriter, r *http.Request) {
	data, ok, err := s.thumbs.Get(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		s.logger.Error("series thumbnail", "error", err)
		s.writeUncachedPlaceholder(w)
		return
	}
	if !ok {
		s.writeUncachedPlaceholder(w)
		return
	}
	w.Header().Set("Content-Type", data.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(data.Bytes)))
	_, _ = w.Write(data.Bytes)
}

func (s *Server) emptyPage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, makePage([]any{}, 0, 20, 0, false))
}

func (s *Server) emptyList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, []any{})
}

func (s *Server) getSeriesProgress(w http.ResponseWriter, r *http.Request) {
	series, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		writeError(w, 404, "NOT_FOUND", "Series not found.")
		return
	}
	writeJSON(w, 200, map[string]any{
		"booksCount": series.BooksCount, "booksReadCount": 0,
		"booksUnreadCount": series.BooksCount, "booksInProgressCount": 0,
		"lastReadContinuousNumberSort": 0.0, "maxNumberSort": float64(series.BooksCount),
	})
}

func (s *Server) putSeriesProgress(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func seriesDTO(item database.Series) map[string]any {
	created, updated := item.CreatedAt.UTC().Format(time.RFC3339), item.UpdatedAt.UTC().Format(time.RFC3339)
	fileModified := updated
	if item.FileModifiedAt != nil {
		fileModified = item.FileModifiedAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"id": item.ID, "libraryId": item.LibraryID, "name": item.Name, "url": "",
		"created": created, "lastModified": updated, "fileLastModified": fileModified,
		"booksCount": item.BooksCount, "booksReadCount": 0,
		"booksUnreadCount": item.BooksCount, "booksInProgressCount": 0,
		"metadata": map[string]any{
			"status": "ONGOING", "statusLock": false,
			"created": created, "lastModified": updated,
			"title": item.Name, "titleLock": false, "titleSort": item.Name, "titleSortLock": false,
			"summary": "", "summaryLock": false,
			"readingDirection": "LEFT_TO_RIGHT", "readingDirectionLock": false,
			"publisher": "", "publisherLock": false, "ageRating": nil, "ageRatingLock": false,
			"language": "", "languageLock": false, "genres": []string{}, "genresLock": false,
			"tags": []string{}, "tagsLock": false, "totalBookCount": nil, "totalBookCountLock": false,
			"sharingLabels": []string{}, "sharingLabelsLock": false,
			"links": []any{}, "linksLock": false, "alternateTitles": []any{}, "alternateTitlesLock": false,
		},
		"booksMetadata": map[string]any{
			"authors": []any{}, "tags": []string{}, "releaseDate": nil,
			"summary": "", "summaryNumber": "", "created": created, "lastModified": updated,
		},
		"deleted": false, "oneshot": item.OneShot,
	}
}

func bookDTO(item database.Book, series database.Series) map[string]any {
	created, updated := item.CreatedAt.UTC().Format(time.RFC3339), item.UpdatedAt.UTC().Format(time.RFC3339)
	fileModified := updated
	if item.FileModifiedAt != nil {
		fileModified = item.FileModifiedAt.UTC().Format(time.RFC3339)
	}
	title := strings.TrimSuffix(item.Name, filepath.Ext(item.Name))
	number := strconv.FormatFloat(item.NumberSort, 'f', -1, 64)
	return map[string]any{
		"id": item.ID, "seriesId": item.SeriesID, "seriesTitle": series.Name,
		"libraryId": item.LibraryID, "name": item.Name, "url": "", "number": int(item.NumberSort),
		"created": created, "lastModified": updated, "fileLastModified": fileModified,
		"sizeBytes": item.Size, "size": formatBytes(item.Size), "fileHash": item.SHA1,
		"media": map[string]any{
			"status": "READY", "mediaType": archive.MediaType(item.Name), "pagesCount": item.PageCount,
			"comment": "", "mediaProfile": "DIVINA", "epubDivinaCompatible": false, "epubIsKepub": false,
		},
		"metadata": map[string]any{
			"title": title, "titleLock": false, "summary": "", "summaryLock": false,
			"number": number, "numberLock": false, "numberSort": item.NumberSort, "numberSortLock": false,
			"releaseDate": nil, "releaseDateLock": false, "authors": []any{}, "authorsLock": false,
			"tags": []string{}, "tagsLock": false, "isbn": "", "isbnLock": false,
			"links": []any{}, "linksLock": false, "created": created, "lastModified": updated,
		},
		"readProgress": nil, "deleted": false, "oneshot": series.OneShot,
	}
}

func formatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(value)
	for _, unit := range units {
		size /= 1024
		if size < 1024 || unit == "TB" {
			return fmt.Sprintf("%.1f %s", math.Round(size*10)/10, unit)
		}
	}
	return fmt.Sprintf("%d B", value)
}
