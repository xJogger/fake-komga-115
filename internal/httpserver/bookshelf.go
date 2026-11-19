package httpserver

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (s *Server) bookshelfVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current, err := s.store.SeriesByID(ctx, chi.URLParam(r, "seriesID"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "NOT_FOUND", "Series not found.")
		} else {
			writeError(w, 500, "DATABASE_ERROR", "Could not load series.")
		}
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("library_id"))
	meta, err := s.store.SeriesMetadataByID(ctx, current)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "Could not load series metadata.")
		return
	}
	if current.OneShot || (meta.BgmSubjectID == nil || *meta.BgmSubjectID <= 0) || (scope != "" && current.LibraryID != scope) {
		writeJSON(w, 200, map[string]any{"versions": []any{}})
		return
	}
	where := " WHERE gm.bgm_subject_id=? AND s.one_shot=0 AND l.enabled=1"
	args := []any{*meta.BgmSubjectID}
	if !s.store.ShowNSFW(ctx) {
		where += " AND gm.is_nsfw=0"
	}
	if scope != "" {
		where += " AND s.library_id=?"
		args = append(args, scope)
	}
	joins := " FROM series s JOIN series_metadata gm ON gm.series_id=s.id JOIN libraries l ON l.id=s.library_id"
	rows, err := s.store.DB().QueryContext(ctx, "SELECT s.id,s.name,l.name,l.provider_type,"+
		"(SELECT count(*) FROM books b WHERE b.series_id=s.id)"+joins+where+" ORDER BY l.name,s.name,s.id", args...)
	if err != nil {
		writeError(w, 500, "DATABASE_ERROR", "Could not load versions.")
		return
	}
	defer rows.Close()
	type version struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Library    string `json:"library"`
		Provider   string `json:"provider"`
		BooksCount int    `json:"booksCount"`
	}
	versions := make([]version, 0)
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.ID, &v.Name, &v.Library, &v.Provider, &v.BooksCount); err != nil {
			writeError(w, 500, "DATABASE_ERROR", "Could not read versions.")
			return
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "DATABASE_ERROR", "Could not read versions.")
		return
	}
	rows.Close()
	result := map[string]any{"versions": versions}
	var seriesID, bookID string
	var page int
	var done bool
	if len(versions) > 1 {
		err := s.store.DB().QueryRowContext(ctx, "SELECT s.id,b.id,coalesce(p.page,1),p.completed"+joins+
			" JOIN books b ON b.series_id=s.id JOIN book_read_progress p ON p.book_id=b.id"+
			where+" ORDER BY p.updated_at DESC,b.id LIMIT 1", args...).Scan(&seriesID, &bookID, &page, &done)
		if err == nil {
			result["lastRead"] = map[string]any{"seriesId": seriesID, "bookId": bookID, "page": page, "completed": done}
		}
	}
	writeJSON(w, 200, result)
}
