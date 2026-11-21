package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
)

const settingDiagnosticsVerbose = "diagnostics_verbose"

func (s *Server) diagnosticsRecorder() *diagnostics.Recorder {
	if s.diag != nil {
		return s.diag
	}
	return diagnostics.Default()
}

func (s *Server) getDiagnostics(w http.ResponseWriter, r *http.Request) {
	rec := s.diagnosticsRecorder()
	if rec == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": diagnostics.Status{},
			"events": []diagnostics.Event{},
		})
		return
	}
	limit := intQuery(r, "limit", 120)
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": rec.Status(),
		"events": rec.Recent(limit),
	})
}

func (s *Server) putDiagnosticsConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Verbose *bool `json:"verbose"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Verbose == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "verbose boolean is required.")
		return
	}
	if err := s.store.SetSetting(r.Context(), settingDiagnosticsVerbose, strconv.FormatBool(*req.Verbose)); err != nil {
		s.writeDBError(w, r, err)
		return
	}
	rec := s.diagnosticsRecorder()
	if rec != nil {
		rec.SetVerbose(*req.Verbose)
		writeJSON(w, http.StatusOK, rec.Status())
		return
	}
	writeJSON(w, http.StatusOK, diagnostics.Status{Verbose: *req.Verbose})
}

func (s *Server) exportDiagnostics(w http.ResponseWriter, r *http.Request) {
	rec := s.diagnosticsRecorder()
	text := rec.ExportText(intQuery(r, "limit", 500))
	filename := fmt.Sprintf("mangaalpha-diagnostics-%s.txt", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(text))
}

func (s *Server) clearDiagnostics(w http.ResponseWriter, r *http.Request) {
	rec := s.diagnosticsRecorder()
	if rec != nil {
		if err := rec.Clear(); err != nil {
			writeError(w, http.StatusInternalServerError, "DIAGNOSTICS_CLEAR_FAILED", err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getBangumiArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	writeJSON(w, http.StatusOK, s.bangumi.ArchiveSchedule(r.Context()))
}

func (s *Server) putBangumiArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	var req bangumi.ArchiveScheduleUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	saved, err := s.bangumi.UpdateArchiveSchedule(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARCHIVE_SCHEDULE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}
