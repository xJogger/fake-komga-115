package httpserver

import (
	"database/sql"
	"encoding/json"
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
	"github.com/xJogger/fake-komga-115/internal/buildinfo"
	"github.com/xJogger/fake-komga-115/internal/database"
)

func (s *Server) komgaRoutes(r chi.Router) {
	r.Get("/claim", s.komgaClaimStatus)
	r.Post("/claim", s.komgaClaimCreate)
	r.Get("/login/set-cookie", s.komgaSetCookie)
	r.Post("/login/set-cookie", s.komgaSetCookie)

	r.Get("/server/capabilities", s.komgaCapabilities)
	r.Get("/libraries", s.komgaLibraries)
	r.Get("/users/me", s.komgaMe)
	r.Get("/client-settings/user/list", s.komgaClientSettings)
	r.Patch("/client-settings/user", s.patchKomgaClientSettings)

	r.Get("/series", s.komgaSeries)
	r.Post("/series/list", s.komgaSeriesList)
	r.Get("/series/{seriesID}", s.komgaSeriesByID)
	r.Patch("/series/{seriesID}/metadata", s.patchKomgaSeriesMetadata)
	r.Post("/series/{seriesID}/read-progress", s.markKomgaSeriesRead)
	r.Delete("/series/{seriesID}/read-progress", s.deleteKomgaSeriesReadProgress)
	r.Get("/series/{seriesID}/books", s.komgaSeriesBooks)
	r.Get("/series/{seriesID}/thumbnail", s.komgaSeriesThumbnail)

	r.Get("/books", s.komgaBooks)
	r.Post("/books/list", s.komgaBooksList)
	r.Get("/books/{bookID}", s.komgaBookByID)
	r.Get("/books/{bookID}/next", s.komgaBookNext)
	r.Get("/books/{bookID}/previous", s.komgaBookPrevious)
	r.Get("/books/{bookID}/read-progress", s.komgaBookReadProgress)
	r.Patch("/books/{bookID}/read-progress", s.patchKomgaBookReadProgress)
	r.Delete("/books/{bookID}/read-progress", s.deleteKomgaBookReadProgress)
	r.Get("/books/{bookID}/pages", s.komgaPages)
	r.Get("/books/{bookID}/pages/{pageNumber}", s.komgaPageImage)
	r.Get("/books/{bookID}/pages/{pageNumber}/raw", s.komgaPageImage)
	r.Get("/books/{bookID}/pages/{pageNumber}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })
	r.Get("/books/{bookID}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })

	r.Get("/collections", s.emptyPage)
	r.Get("/collections/{id}/series", s.emptyPage)
	r.Get("/collections/{id}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })
	r.Get("/readlists", s.emptyPage)
	r.Get("/readlists/{id}/thumbnail", func(w http.ResponseWriter, _ *http.Request) { s.writePlaceholder(w) })

	r.Get("/genres", s.komgaGenres)
	r.Get("/tags", s.komgaTags)
	r.Get("/tags/series", s.komgaTags)
	r.Get("/tags/book", s.emptyList)
	r.Get("/publishers", s.komgaPublishers)
	r.Get("/authors", s.komgaAuthors)
	r.Get("/authors/names", s.komgaAuthorNames)
	r.Get("/languages", s.emptyList)
	r.Get("/age-ratings", s.emptyList)
}

func (s *Server) komgaCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":          "fake-komga-115",
		"version":       buildinfo.Version,
		"apiBasePath":   "/api/v1",
		"compatibility": "komga-partial",
		"features": map[string]bool{
			"authentication":         true,
			"builtInBookshelf":       true,
			"builtInReader":          true,
			"bangumiMetadata":        true,
			"localLibraries":         true,
			"webdavLibraries":        true,
			"libraries":              true,
			"seriesList":             true,
			"booksList":              true,
			"bookPages":              true,
			"pageImages":             true,
			"seriesThumbnails":       true,
			"pageReadProgress":       true,
			"deleteBookReadProgress": true,
			"bookSiblingNavigation":  true,
			"clientSettings":         true,
		},
	})
}

func (s *Server) komgaLibraries(w http.ResponseWriter, r *http.Request) {
	libraries, err := s.store.LibrariesBasic(r.Context(), true)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(libraries))
	for _, library := range libraries {
		out = append(out, map[string]any{
			"id": library.ID, "name": library.Name, "root": "",
			"providerType":              library.ProviderType,
			"importComicInfoBook":       false,
			"importComicInfoSeries":     false,
			"importComicInfoCollection": false,
			"importComicInfoReadList":   false,
			"importEpubBook":            false,
			"importEpubSeries":          false,
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
	filters := legacyKomgaFilters(r, "series")
	items, total, err := s.store.SeriesPage(r.Context(), database.SeriesQuery{
		Search:       filters.Search,
		LibraryIDs:   filters.LibraryIDs,
		ReadStatus:   filters.ReadStatuses,
		Statuses:     filters.Statuses,
		Genres:       filters.Genres,
		Tags:         filters.Tags,
		Publishers:   filters.Publishers,
		Authors:      filters.Authors,
		Languages:    filters.Languages,
		ScrapeStatus: strings.TrimSpace(r.URL.Query().Get("scrape_status")),
		OneShot:      filters.OneShot,
		Empty:        filters.Empty,
		Page:         page,
		Size:         size,
		Sort:         r.URL.Query().Get("sort"),
	})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	out, err := s.seriesDTOs(r, items)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
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
	progress, err := s.store.SeriesReadProgress(r.Context(), item.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	meta, err := s.store.SeriesMetadataByID(r.Context(), item)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, seriesDTOFull(item, progress, meta))
}

func (s *Server) patchKomgaSeriesMetadata(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.SeriesByID(r.Context(), chi.URLParam(r, "seriesID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	var patch database.SeriesMetadataPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if _, err := s.store.PatchSeriesMetadata(r.Context(), item, patch); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) markKomgaSeriesRead(w http.ResponseWriter, r *http.Request) {
	seriesID := chi.URLParam(r, "seriesID")
	if _, err := s.store.SeriesByID(r.Context(), seriesID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
		return
	}
	if err := s.store.MarkSeriesCompleted(r.Context(), seriesID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteKomgaSeriesReadProgress(w http.ResponseWriter, r *http.Request) {
	seriesID := chi.URLParam(r, "seriesID")
	if _, err := s.store.SeriesByID(r.Context(), seriesID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
		return
	}
	if err := s.store.DeleteSeriesReadProgress(r.Context(), seriesID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) komgaGenres(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Genres(r.Context(), listQuery(r, "library_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) komgaTags(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Tags(r.Context(), listQuery(r, "library_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) komgaPublishers(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Publishers(r.Context(), listQuery(r, "library_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) komgaAuthors(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Authors(
		r.Context(),
		listQuery(r, "library_id"),
		r.URL.Query().Get("search"),
		r.URL.Query().Get("role"),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	if r.URL.Query().Get("unpaged") == "false" || r.URL.Query().Get("page") != "" {
		page, size := intQuery(r, "page", 0), intQuery(r, "size", 50)
		if size <= 0 {
			size = 50
		}
		start := min(page*size, len(items))
		end := min(start+size, len(items))
		writeJSON(w, http.StatusOK, makePage(items[start:end], page, size, int64(len(items)), false))
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) komgaAuthorNames(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Authors(r.Context(), listQuery(r, "library_id"), r.URL.Query().Get("search"), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(items))
	for _, a := range items {
		if !seen[a.Name] {
			seen[a.Name] = true
			names = append(names, a.Name)
		}
	}
	writeJSON(w, http.StatusOK, names)
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
	filters := legacyKomgaFilters(r, "books")
	items, total, err := s.store.BooksPage(r.Context(), database.BookQuery{
		Search:     filters.Search,
		LibraryIDs: filters.LibraryIDs,
		ReadStatus: filters.ReadStatuses,
		Tags:       filters.Tags,
		Authors:    filters.Authors,
		SeriesID:   seriesID,
		OneShot:    filters.OneShot,
		Empty:      filters.Empty,
		Page:       page,
		Size:       size,
		Unpaged:    unpaged,
		Sort:       r.URL.Query().Get("sort"),
	})
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	out, err := s.bookDTOs(r, items)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
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
	seriesMeta, _ := s.store.SeriesMetadataByID(r.Context(), series)
	progress, ok, err := s.store.BookReadProgress(r.Context(), book.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, bookDTOWithProgressAndMeta(book, series, seriesMeta, progress, ok))
}

func (s *Server) seriesDTOs(r *http.Request, items []database.Series) ([]any, error) {
	out := make([]any, 0, len(items))
	if len(items) == 0 {
		return out, nil
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	progressMap, err := s.store.SeriesReadProgresses(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	metaMap, err := s.store.SeriesMetadatas(r.Context(), items)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		progress := progressMap[item.ID]
		meta := metaMap[item.ID]
		out = append(out, seriesDTOFull(item, progress, meta))
	}
	return out, nil
}

func (s *Server) bookDTOs(r *http.Request, items []database.Book) ([]any, error) {
	out := make([]any, 0, len(items))
	if len(items) == 0 {
		return out, nil
	}
	bookIDs := make([]string, 0, len(items))
	seriesIDs := make([]string, 0, len(items))
	for _, item := range items {
		bookIDs = append(bookIDs, item.ID)
		seriesIDs = append(seriesIDs, item.SeriesID)
	}
	progresses, err := s.store.BookReadProgresses(r.Context(), bookIDs)
	if err != nil {
		return nil, err
	}
	seriesMap, err := s.store.SeriesByIDs(r.Context(), seriesIDs)
	if err != nil {
		return nil, err
	}
	seriesSlice := make([]database.Series, 0, len(seriesMap))
	for _, sr := range seriesMap {
		seriesSlice = append(seriesSlice, sr)
	}
	metaMap, err := s.store.SeriesMetadatas(r.Context(), seriesSlice)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		series, ok := seriesMap[item.SeriesID]
		if !ok {
			series, err = s.store.SeriesByID(r.Context(), item.SeriesID)
			if err != nil {
				return nil, err
			}
		}
		meta := metaMap[item.SeriesID]
		progress, hasProg := progresses[item.ID]
		out = append(out, bookDTOWithProgressAndMeta(item, series, meta, progress, hasProg))
	}
	return out, nil
}

func (s *Server) komgaBookNext(w http.ResponseWriter, r *http.Request) {
	s.komgaBookSibling(w, r, true)
}

func (s *Server) komgaBookPrevious(w http.ResponseWriter, r *http.Request) {
	s.komgaBookSibling(w, r, false)
}

func (s *Server) komgaBookSibling(w http.ResponseWriter, r *http.Request, next bool) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Book not found.")
		return
	}
	var sibling database.Book
	if next {
		sibling, err = s.store.NextBookInSeries(r.Context(), book)
	} else {
		sibling, err = s.store.PreviousBookInSeries(r.Context(), book)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Sibling book not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	series, err := s.store.SeriesByID(r.Context(), sibling.SeriesID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	seriesMeta, _ := s.store.SeriesMetadataByID(r.Context(), series)
	progress, ok, err := s.store.BookReadProgress(r.Context(), sibling.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bookDTOWithProgressAndMeta(sibling, series, seriesMeta, progress, ok))
}

func (s *Server) komgaBookReadProgress(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		writeError(w, 404, "NOT_FOUND", "Book not found.")
		return
	}
	progress, ok, err := s.store.BookReadProgress(r.Context(), book.ID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	if !ok {
		writeJSON(w, 200, nil)
		return
	}
	writeJSON(w, 200, bookReadProgressDTO(progress))
}

func (s *Server) deleteKomgaBookReadProgress(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Book not found.")
		return
	}
	if err := s.store.DeleteBookReadProgress(r.Context(), book.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) patchKomgaBookReadProgress(w http.ResponseWriter, r *http.Request) {
	book, err := s.store.BookByID(r.Context(), chi.URLParam(r, "bookID"))
	if err != nil {
		writeError(w, 404, "NOT_FOUND", "Book not found.")
		return
	}
	var request struct {
		Completed *bool `json:"completed"`
		Page      *int  `json:"page"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if request.Completed == nil && request.Page == nil {
		writeError(w, http.StatusBadRequest, "INVALID_PROGRESS", "completed or page is required.")
		return
	}
	if request.Page != nil && *request.Page < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_PROGRESS", "page must be greater than zero.")
		return
	}
	if err := s.store.UpdateBookReadProgress(
		r.Context(), book, request.Completed, request.Page,
	); err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if _, err := w.Write(page.Data); err != nil {
		return
	}
	if err := s.store.RecordBookPageProgress(r.Context(), book, pageNumber, page.TotalPages); err != nil {
		s.logger.Warn("record inferred page progress", "error", err, "book_id", book.ID)
	}
	s.thumbs.MaybeGenerate(book, pageNumber, page.Data)
	s.archive.Prefetch(book, pageNumber, int(s.store.Int64Setting(r.Context(), "page_prefetch_count", 2)))
	s.archive.PrefetchNextVolumeIndex(book, pageNumber, page.TotalPages)
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
	seriesID := chi.URLParam(r, "seriesID")
	if _, err := s.store.SeriesByID(r.Context(), seriesID); err != nil {
		writeError(w, 404, "NOT_FOUND", "Series not found.")
		return
	}
	progress, err := s.store.SeriesReadProgress(r.Context(), seriesID)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, progress)
}

func (s *Server) putSeriesProgress(w http.ResponseWriter, r *http.Request) {
	seriesID := chi.URLParam(r, "seriesID")
	if _, err := s.store.SeriesByID(r.Context(), seriesID); err != nil {
		writeError(w, 404, "NOT_FOUND", "Series not found.")
		return
	}
	var request struct {
		LastBookNumberSortRead float64 `json:"lastBookNumberSortRead"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if request.LastBookNumberSortRead < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PROGRESS", "lastBookNumberSortRead must be positive or zero.")
		return
	}
	if err := s.store.UpdateSeriesReadProgress(r.Context(), seriesID, request.LastBookNumberSortRead); err != nil {
		writeError(w, 500, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func seriesDTO(item database.Series) map[string]any {
	progress := database.SeriesReadProgress{
		BooksCount:       item.BooksCount,
		BooksUnreadCount: item.BooksCount,
		MaxNumberSort:    float64(item.BooksCount),
	}
	return seriesDTOWithProgress(item, progress)
}

func seriesDTOWithProgress(
	item database.Series,
	progress database.SeriesReadProgress,
) map[string]any {
	return seriesDTOFull(item, progress, database.SeriesMetadata{
		SeriesID:         item.ID,
		Status:           "ONGOING",
		Title:            item.Name,
		TitleSort:        item.Name,
		ReadingDirection: "LEFT_TO_RIGHT",
		Genres:           []string{},
		Tags:             []string{},
		Authors:          []database.Author{},
		AlternateTitles:  []database.AlternateTitle{},
		Links:            []database.WebLink{},
		SharingLabels:    []string{},
	})
}

func seriesDTOFull(
	item database.Series,
	progress database.SeriesReadProgress,
	meta database.SeriesMetadata,
) map[string]any {
	created, updated := item.CreatedAt.UTC().Format(time.RFC3339), item.UpdatedAt.UTC().Format(time.RFC3339)
	fileModified := updated
	if item.FileModifiedAt != nil {
		fileModified = item.FileModifiedAt.UTC().Format(time.RFC3339)
	}
	displayTitle := strings.TrimSpace(meta.Title)
	if displayTitle == "" {
		displayTitle = item.Name
	}
	titleSort := strings.TrimSpace(meta.TitleSort)
	if titleSort == "" {
		titleSort = displayTitle
	}
	status := meta.Status
	if status == "" {
		status = "ONGOING"
	}
	readDir := meta.ReadingDirection
	if readDir == "" {
		readDir = "LEFT_TO_RIGHT"
	}
	genres := meta.Genres
	if genres == nil {
		genres = []string{}
	}
	tags := meta.Tags
	if tags == nil {
		tags = []string{}
	}
	authors := meta.Authors
	if authors == nil {
		authors = []database.Author{}
	}
	links := meta.Links
	if links == nil {
		links = []database.WebLink{}
	}
	altTitles := meta.AlternateTitles
	if altTitles == nil {
		altTitles = []database.AlternateTitle{}
	}
	sharing := meta.SharingLabels
	if sharing == nil {
		sharing = []string{}
	}
	var releaseDate any
	if meta.ReleaseDate != "" {
		releaseDate = meta.ReleaseDate
	}
	var ageRating any
	if meta.AgeRating != nil {
		ageRating = *meta.AgeRating
	}
	var totalBookCount any
	if meta.TotalBookCount != nil {
		totalBookCount = *meta.TotalBookCount
	}

	return map[string]any{
		"id": item.ID, "libraryId": item.LibraryID, "name": item.Name, "url": "",
		"created": created, "lastModified": updated, "fileLastModified": fileModified,
		"booksCount":           item.BooksCount,
		"booksReadCount":       progress.BooksReadCount,
		"booksUnreadCount":     progress.BooksUnreadCount,
		"booksInProgressCount": progress.BooksInProgressCount,
		"bgmSubjectId":         meta.BgmSubjectID,
		"bgmScore":             meta.BgmScore,
		"scrapeStatus":         meta.ScrapeStatus,
		"coverSource":          meta.CoverSource,
		"metadata": map[string]any{
			"status": status, "statusLock": meta.StatusLock,
			"created": created, "lastModified": updated,
			"title": displayTitle, "titleLock": meta.TitleLock,
			"titleSort": titleSort, "titleSortLock": meta.TitleSortLock,
			"summary": meta.Summary, "summaryLock": meta.SummaryLock,
			"readingDirection": readDir, "readingDirectionLock": meta.ReadingDirLock,
			"publisher": meta.Publisher, "publisherLock": meta.PublisherLock,
			"ageRating": ageRating, "ageRatingLock": meta.AgeRatingLock,
			"language": meta.Language, "languageLock": meta.LanguageLock,
			"genres": genres, "genresLock": meta.GenresLock,
			"tags": tags, "tagsLock": meta.TagsLock,
			"totalBookCount": totalBookCount, "totalBookCountLock": meta.TotalBookCountLock,
			"sharingLabels": sharing, "sharingLabelsLock": meta.SharingLabelsLock,
			"links": links, "linksLock": meta.LinksLock,
			"alternateTitles": altTitles, "alternateTitlesLock": meta.AlternateTitlesLock,
		},
		"booksMetadata": map[string]any{
			"authors": authors, "tags": tags, "releaseDate": releaseDate,
			"summary": meta.Summary, "summaryNumber": "", "created": created, "lastModified": updated,
		},
		"deleted": false, "oneshot": item.OneShot,
	}
}

func bookDTO(item database.Book, series database.Series) map[string]any {
	return bookDTOWithProgress(item, series, database.BookReadProgress{}, false)
}

func bookDTOWithProgress(
	item database.Book,
	series database.Series,
	progress database.BookReadProgress,
	hasProgress bool,
) map[string]any {
	return bookDTOWithProgressAndMeta(item, series, database.SeriesMetadata{}, progress, hasProgress)
}

func bookDTOWithProgressAndMeta(
	item database.Book,
	series database.Series,
	seriesMeta database.SeriesMetadata,
	progress database.BookReadProgress,
	hasProgress bool,
) map[string]any {
	createdAt := item.CreatedAt
	if item.FileCreatedAt != nil {
		createdAt = *item.FileCreatedAt
	}
	created, updated := createdAt.UTC().Format(time.RFC3339), item.UpdatedAt.UTC().Format(time.RFC3339)
	fileModified := updated
	if item.FileModifiedAt != nil {
		fileModified = item.FileModifiedAt.UTC().Format(time.RFC3339)
	}
	title := strings.TrimSuffix(item.Name, filepath.Ext(item.Name))
	seriesTitle := strings.TrimSpace(seriesMeta.Title)
	if seriesTitle == "" {
		seriesTitle = series.Name
	}
	authors := seriesMeta.Authors
	if authors == nil {
		authors = []database.Author{}
	}
	number := strconv.FormatFloat(item.NumberSort, 'f', -1, 64)
	return map[string]any{
		"id": item.ID, "seriesId": item.SeriesID, "seriesTitle": seriesTitle,
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
			"releaseDate": nil, "releaseDateLock": false, "authors": authors, "authorsLock": false,
			"tags": []string{}, "tagsLock": false, "isbn": "", "isbnLock": false,
			"links": []any{}, "linksLock": false, "created": created, "lastModified": updated,
		},
		"readProgress": func() any {
			if !hasProgress {
				return nil
			}
			return bookReadProgressDTO(progress)
		}(),
		"deleted": false, "oneshot": series.OneShot,
	}
}

func bookReadProgressDTO(progress database.BookReadProgress) map[string]any {
	var page any
	if progress.Page != nil {
		page = *progress.Page
	}
	var readDate any
	if progress.ReadDate != nil {
		readDate = progress.ReadDate.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"completed": progress.Completed,
		"page":      page,
		"readDate":  readDate,
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
