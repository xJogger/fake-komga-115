package httpserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func collectionDTO(c database.Collection) map[string]any {
	seriesIDs := c.SeriesIDs
	if seriesIDs == nil {
		seriesIDs = []string{}
	}
	return map[string]any{
		"id":               c.ID,
		"name":             c.Name,
		"ordered":          c.Ordered,
		"seriesIds":        seriesIDs,
		"createdDate":      c.CreatedAt.UTC().Format(time.RFC3339),
		"lastModifiedDate": c.UpdatedAt.UTC().Format(time.RFC3339),
		"filtered":         c.Filtered,
	}
}

func readListDTO(rl database.ReadList) map[string]any {
	bookIDs := rl.BookIDs
	if bookIDs == nil {
		bookIDs = []string{}
	}
	return map[string]any{
		"id":               rl.ID,
		"name":             rl.Name,
		"summary":          rl.Summary,
		"ordered":          rl.Ordered,
		"bookIds":          bookIDs,
		"createdDate":      rl.CreatedAt.UTC().Format(time.RFC3339),
		"lastModifiedDate": rl.UpdatedAt.UTC().Format(time.RFC3339),
		"filtered":         rl.Filtered,
	}
}

// --- Collections ---

func (s *Server) komgaCollections(w http.ResponseWriter, r *http.Request) {
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	unpaged := r.URL.Query().Get("unpaged") == "true"
	items, total, err := s.store.ListCollections(
		r.Context(),
		r.URL.Query().Get("search"),
		listQuery(r, "library_id"),
		page, size, unpaged,
		r.URL.Query().Get("sort"),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, collectionDTO(item))
	}
	writeJSON(w, http.StatusOK, makePage(out, page, size, total, unpaged))
}

func (s *Server) komgaCollectionByID(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.CollectionByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Collection not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, collectionDTO(item))
}

func (s *Server) komgaCreateCollection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string   `json:"name"`
		Ordered   bool     `json:"ordered"`
		SeriesIDs []string `json:"seriesIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	item, err := s.store.CreateCollection(r.Context(), req.Name, req.Ordered, req.SeriesIDs)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrEmptyName):
			writeError(w, http.StatusBadRequest, "INVALID_NAME", "Collection name cannot be empty.")
		case errors.Is(err, database.ErrDuplicateName):
			writeError(w, http.StatusBadRequest, "DUPLICATE_NAME", "A collection with this name already exists.")
		default:
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		}
		return
	}
	s.sse.Publish("CollectionAdded", map[string]any{
		"collectionId": item.ID,
		"seriesIds":    item.SeriesIDs,
	})
	writeJSON(w, http.StatusOK, collectionDTO(item))
}

func (s *Server) komgaUpdateCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name      *string   `json:"name"`
		Ordered   *bool     `json:"ordered"`
		SeriesIDs *[]string `json:"seriesIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	item, err := s.store.UpdateCollection(r.Context(), id, req.Name, req.Ordered, req.SeriesIDs)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Collection not found.")
		case errors.Is(err, database.ErrEmptyName):
			writeError(w, http.StatusBadRequest, "INVALID_NAME", "Collection name cannot be empty.")
		case errors.Is(err, database.ErrDuplicateName):
			writeError(w, http.StatusBadRequest, "DUPLICATE_NAME", "A collection with this name already exists.")
		default:
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		}
		return
	}
	s.sse.Publish("CollectionChanged", map[string]any{
		"collectionId": item.ID,
		"seriesIds":    item.SeriesIDs,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) komgaDeleteCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteCollection(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Collection not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	s.sse.Publish("CollectionDeleted", map[string]any{
		"collectionId": id,
		"seriesIds":    []string{},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) komgaCollectionSeries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	unpaged := r.URL.Query().Get("unpaged") == "true"
	filters := legacyKomgaFilters(r, "series")
	items, total, err := s.store.CollectionSeriesPage(r.Context(), id, database.SeriesQuery{
		Search:     filters.Search,
		LibraryIDs: filters.LibraryIDs,
		ReadStatus: filters.ReadStatuses,
		Statuses:   filters.Statuses,
		Genres:     filters.Genres,
		Tags:       filters.Tags,
		Publishers: filters.Publishers,
		Authors:    filters.Authors,
		Languages:  filters.Languages,
		OneShot:    filters.OneShot,
		Empty:      filters.Empty,
		Page:       page,
		Size:       size,
		Sort:       r.URL.Query().Get("sort"),
	}, unpaged)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Collection not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out, err := s.seriesDTOs(r, items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makePage(out, page, size, total, unpaged))
}

func (s *Server) komgaCollectionThumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.CollectionByID(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Collection not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	seriesID, err := s.store.CollectionFirstVisibleSeriesID(r.Context(), id)
	if err != nil || seriesID == "" {
		s.writeUncachedPlaceholder(w)
		return
	}
	s.serveSeriesThumbOrPlaceholder(w, r, seriesID)
}

func (s *Server) komgaSeriesCollections(w http.ResponseWriter, r *http.Request) {
	seriesID := chi.URLParam(r, "seriesID")
	if _, err := s.store.SeriesByID(r.Context(), seriesID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Series not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	items, err := s.store.CollectionsForSeries(r.Context(), seriesID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, collectionDTO(item))
	}
	writeJSON(w, http.StatusOK, out)
}

// --- ReadLists ---

func (s *Server) komgaReadLists(w http.ResponseWriter, r *http.Request) {
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	unpaged := r.URL.Query().Get("unpaged") == "true"
	items, total, err := s.store.ListReadLists(
		r.Context(),
		r.URL.Query().Get("search"),
		listQuery(r, "library_id"),
		page, size, unpaged,
		r.URL.Query().Get("sort"),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, readListDTO(item))
	}
	writeJSON(w, http.StatusOK, makePage(out, page, size, total, unpaged))
}

func (s *Server) komgaReadListByID(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.ReadListByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ReadList not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, readListDTO(item))
}

func (s *Server) komgaCreateReadList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string   `json:"name"`
		Summary string   `json:"summary"`
		Ordered *bool    `json:"ordered"`
		BookIDs []string `json:"bookIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	ordered := true
	if req.Ordered != nil {
		ordered = *req.Ordered
	}
	item, err := s.store.CreateReadList(r.Context(), req.Name, req.Summary, ordered, req.BookIDs)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrEmptyName):
			writeError(w, http.StatusBadRequest, "INVALID_NAME", "ReadList name cannot be empty.")
		case errors.Is(err, database.ErrDuplicateName):
			writeError(w, http.StatusBadRequest, "DUPLICATE_NAME", "A readlist with this name already exists.")
		default:
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		}
		return
	}
	s.sse.Publish("ReadListAdded", map[string]any{
		"readListId": item.ID,
		"bookIds":    item.BookIDs,
	})
	writeJSON(w, http.StatusOK, readListDTO(item))
}

func (s *Server) komgaUpdateReadList(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name    *string   `json:"name"`
		Summary *string   `json:"summary"`
		Ordered *bool     `json:"ordered"`
		BookIDs *[]string `json:"bookIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	item, err := s.store.UpdateReadList(r.Context(), id, req.Name, req.Summary, req.Ordered, req.BookIDs)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ReadList not found.")
		case errors.Is(err, database.ErrEmptyName):
			writeError(w, http.StatusBadRequest, "INVALID_NAME", "ReadList name cannot be empty.")
		case errors.Is(err, database.ErrDuplicateName):
			writeError(w, http.StatusBadRequest, "DUPLICATE_NAME", "A readlist with this name already exists.")
		default:
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		}
		return
	}
	s.sse.Publish("ReadListChanged", map[string]any{
		"readListId": item.ID,
		"bookIds":    item.BookIDs,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) komgaDeleteReadList(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteReadList(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ReadList not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	s.sse.Publish("ReadListDeleted", map[string]any{
		"readListId": id,
		"bookIds":    []string{},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) komgaReadListBooks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	unpaged := r.URL.Query().Get("unpaged") == "true"
	filters := legacyKomgaFilters(r, "books")
	items, total, err := s.store.ReadListBooksPage(r.Context(), id, database.BookQuery{
		Search:     filters.Search,
		LibraryIDs: filters.LibraryIDs,
		ReadStatus: filters.ReadStatuses,
		Tags:       filters.Tags,
		Authors:    filters.Authors,
		OneShot:    filters.OneShot,
		Empty:      filters.Empty,
		Page:       page,
		Size:       size,
		Unpaged:    unpaged,
		Sort:       r.URL.Query().Get("sort"),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ReadList not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out, err := s.bookDTOs(r, items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makePage(out, page, size, total, unpaged))
}

func (s *Server) komgaReadListBookNext(w http.ResponseWriter, r *http.Request) {
	s.komgaReadListBookSibling(w, r, true)
}

func (s *Server) komgaReadListBookPrevious(w http.ResponseWriter, r *http.Request) {
	s.komgaReadListBookSibling(w, r, false)
}

func (s *Server) komgaReadListBookSibling(w http.ResponseWriter, r *http.Request, next bool) {
	readListID := chi.URLParam(r, "id")
	bookID := chi.URLParam(r, "bookID")
	sibling, err := s.store.ReadListSiblingBook(r.Context(), readListID, bookID, next)
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

func (s *Server) komgaReadListThumbnail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.ReadListByID(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ReadList not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	seriesID, err := s.store.ReadListFirstVisibleSeriesID(r.Context(), id)
	if err != nil || seriesID == "" {
		s.writeUncachedPlaceholder(w)
		return
	}
	s.serveSeriesThumbOrPlaceholder(w, r, seriesID)
}

func (s *Server) komgaBookReadLists(w http.ResponseWriter, r *http.Request) {
	bookID := chi.URLParam(r, "bookID")
	if _, err := s.store.BookByID(r.Context(), bookID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Book not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	items, err := s.store.ReadListsForBook(r.Context(), bookID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, readListDTO(item))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) serveSeriesThumbOrPlaceholder(w http.ResponseWriter, r *http.Request, seriesID string) {
	data, ok, err := s.thumbs.Get(r.Context(), seriesID)
	if err != nil || !ok {
		s.writeUncachedPlaceholder(w)
		return
	}
	w.Header().Set("Content-Type", data.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(data.Bytes)))
	_, _ = w.Write(data.Bytes)
}

func (s *Server) komgaAuthorsV2(w http.ResponseWriter, r *http.Request) {
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	unpaged := r.URL.Query().Get("unpaged") == "true"
	items, total, err := s.store.AuthorsPage(r.Context(), database.AuthorQuery{
		LibraryIDs:    listQuery(r, "library_id"),
		CollectionIDs: listQuery(r, "collection_id"),
		ReadListIDs:   listQuery(r, "readlist_id"),
		Search:        strings.TrimSpace(r.URL.Query().Get("search")),
		Role:          strings.TrimSpace(r.URL.Query().Get("role")),
		Page:          page,
		Size:          size,
		Unpaged:       unpaged,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makePage(items, page, size, total, unpaged))
}
