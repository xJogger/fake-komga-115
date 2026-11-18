package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	sessionCookieName      = "fk115_session"
	komgaSessionCookieName = "KOMGA-SESSION"
)

type authContextKey struct{}

type authPrincipal struct {
	Username   string
	Method     string // "session", "apikey", "basic"
	RawSession string
}

func (s *Server) currentPrincipal(r *http.Request) (authPrincipal, bool) {
	val, ok := r.Context().Value(authContextKey{}).(authPrincipal)
	return val, ok
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path

		// 1. Always public health check & static UI/reader bundles (no user data inside static code bundles).
		if p == "/health" || p == "/favicon.ico" ||
			strings.HasPrefix(p, "/ui/") ||
			strings.HasPrefix(p, "/assets/") ||
			isStaticReaderAssetPath(p) {
			next.ServeHTTP(w, r)
			return
		}

		claimed, err := s.store.HasAdmin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Failed to verify authentication state.")
			return
		}

		// 2. Unclaimed state (first install or first boot after upgrading from pre-v2):
		if !claimed {
			switch {
			case p == "/setup" && (r.Method == http.MethodGet || r.Method == http.MethodHead),
				p == "/api/v1/claim" && (r.Method == http.MethodGet || r.Method == http.MethodPost),
				p == "/admin/api/auth/status" && r.Method == http.MethodGet,
				p == "/admin/api/auth/setup" && r.Method == http.MethodPost:
				next.ServeHTTP(w, r)
				return
			case isAPIRequestPath(p):
				writeError(w, http.StatusUnauthorized, "SETUP_REQUIRED", "Initial administrator password setup is required.")
				return
			default:
				http.Redirect(w, r, "/setup", http.StatusTemporaryRedirect)
				return
			}
		}

		// 3. Claimed state: try authenticating incoming credentials.
		principal, authed := s.authenticateRequest(r)

		// Public auth/claim status endpoints when already claimed:
		if p == "/api/v1/claim" && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if p == "/admin/api/auth/status" && r.Method == http.MethodGet {
			if authed {
				r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, principal))
			}
			next.ServeHTTP(w, r)
			return
		}
		if (p == "/admin/api/auth/login" || p == "/admin/api/auth/logout") && r.Method == http.MethodPost {
			if authed {
				r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, principal))
			}
			next.ServeHTTP(w, r)
			return
		}
		if p == "/setup" {
			if authed {
				http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			} else {
				http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			}
			return
		}
		if p == "/login" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			if authed {
				http.Redirect(w, r, sanitizeRedirectTarget(r.URL.Query().Get("redirect")), http.StatusTemporaryRedirect)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		if !authed {
			if isAPIRequestPath(p) {
				if !isBrowserUIXHR(r) {
					w.Header().Set("WWW-Authenticate", `Basic realm="fake-komga-115"`)
				}
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required.")
				return
			}
			target := r.URL.Path
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, "/login?redirect="+url.QueryEscape(target), http.StatusTemporaryRedirect)
			return
		}

		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, principal))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticateRequest(r *http.Request) (authPrincipal, bool) {
	// 1. Session Cookie (fk115_session or KOMGA-SESSION) or X-Auth-Token header
	for _, token := range candidateSessionTokens(r) {
		if sess, ok, err := s.store.ValidateSession(r.Context(), token); err == nil && ok {
			return authPrincipal{
				Username:   sess.Username,
				Method:     "session",
				RawSession: token,
			}, true
		}
	}
	// 2. X-API-Key header
	if rawKey := strings.TrimSpace(r.Header.Get("X-API-Key")); rawKey != "" {
		if _, ok, err := s.store.ValidateAPIKey(r.Context(), rawKey); err == nil && ok {
			admin, has, _ := s.store.Admin(r.Context())
			username := "admin"
			if has && admin.Username != "" {
				username = admin.Username
			}
			return authPrincipal{
				Username: username,
				Method:   "apikey",
			}, true
		}
	}
	// 3. HTTP Basic Auth
	if username, password, ok := r.BasicAuth(); ok {
		if user, valid, err := s.store.VerifyAdminPassword(r.Context(), username, password); err == nil && valid {
			return authPrincipal{
				Username: user.Username,
				Method:   "basic",
			}, true
		}
	}
	return authPrincipal{}, false
}

func candidateSessionTokens(r *http.Request) []string {
	var tokens []string
	if c, err := r.Cookie(sessionCookieName); err == nil && strings.TrimSpace(c.Value) != "" {
		tokens = append(tokens, strings.TrimSpace(c.Value))
	}
	if c, err := r.Cookie(komgaSessionCookieName); err == nil && strings.TrimSpace(c.Value) != "" {
		tokens = append(tokens, strings.TrimSpace(c.Value))
	}
	if hdr := strings.TrimSpace(r.Header.Get("X-Auth-Token")); hdr != "" {
		tokens = append(tokens, hdr)
	}
	return tokens
}

func isAPIRequestPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/admin/api" || strings.HasPrefix(p, "/admin/api/")
}

func isStaticReaderAssetPath(p string) bool {
	if !strings.HasPrefix(p, "/reader/") {
		return false
	}
	// Entry routes like /reader/series/... or /reader/manga/... require authentication;
	// hashed static bundles/icons/licenses under /reader/assets/ etc. are safe static files.
	if strings.HasPrefix(p, "/reader/assets/") ||
		strings.HasPrefix(p, "/reader/locales/") ||
		p == "/reader/LICENSE-Suwayomi.txt" ||
		p == "/reader/BUILD-MANIFEST.json" ||
		p == "/reader/site.webmanifest" ||
		strings.HasSuffix(p, ".png") ||
		strings.HasSuffix(p, ".svg") ||
		strings.HasSuffix(p, ".ico") {
		return true
	}
	return false
}

func isBrowserUIXHR(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") {
		return true
	}
	if r.Header.Get("X-FK115-UI") == "1" {
		return true
	}
	return false
}

func sanitizeRedirectTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.HasPrefix(raw, "/login") || strings.HasPrefix(raw, "/setup") {
		return "/"
	}
	return raw
}

func setSessionCookies(w http.ResponseWriter, r *http.Request, token string, remember bool) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	maxAge := 0
	var expires time.Time
	if remember {
		maxAge = 90 * 24 * 3600
		expires = time.Now().UTC().Add(90 * 24 * time.Hour)
	}
	for _, name := range []string{sessionCookieName, komgaSessionCookieName} {
		cookie := &http.Cookie{
			Name:     name,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		}
		if remember {
			cookie.MaxAge = maxAge
			cookie.Expires = expires
		}
		http.SetCookie(w, cookie)
	}
}

func clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	for _, name := range []string{sessionCookieName, komgaSessionCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// --- Auth & Claim Handlers ---

func (s *Server) authStatusHandler(w http.ResponseWriter, r *http.Request) {
	claimed, err := s.store.HasAdmin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	principal, authed := s.currentPrincipal(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"claimed":       claimed,
		"authenticated": authed,
		"username":      principal.Username,
		"method":        principal.Method,
	})
}

func (s *Server) authSetupHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember *bool  `json:"remember"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	admin, err := s.store.ClaimAdmin(r.Context(), req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrAlreadyClaimed):
			writeError(w, http.StatusConflict, "ALREADY_CLAIMED", err.Error())
		case errors.Is(err, database.ErrInvalidUsername), errors.Is(err, database.ErrWeakPassword):
			writeError(w, http.StatusBadRequest, "INVALID_CREDENTIALS", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		}
		return
	}
	remember := true
	if req.Remember != nil {
		remember = *req.Remember
	}
	if rawToken, _, sErr := s.store.CreateSession(r.Context(), admin.Username, remember, r.UserAgent()); sErr == nil {
		setSessionCookies(w, r, rawToken, remember)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"username": admin.Username,
	})
}

func (s *Server) authLoginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user, ok, err := s.store.VerifyAdminPassword(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码不正确")
		return
	}
	rawToken, _, err := s.store.CreateSession(r.Context(), user.Username, req.Remember, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_ERROR", err.Error())
		return
	}
	setSessionCookies(w, r, rawToken, req.Remember)
	writeJSON(w, http.StatusOK, map[string]any{
		"username": user.Username,
	})
}

func (s *Server) authLogoutHandler(w http.ResponseWriter, r *http.Request) {
	for _, tok := range candidateSessionTokens(r) {
		_ = s.store.DeleteSession(r.Context(), tok)
	}
	clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewUsername     string `json:"newUsername"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	principal, _ := s.currentPrincipal(r)
	updated, err := s.store.UpdateAdminCredentials(
		r.Context(), req.CurrentPassword, req.NewUsername, req.NewPassword, principal.RawSession,
	)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "INVALID_PASSWORD", "当前密码不正确")
		case errors.Is(err, database.ErrInvalidUsername), errors.Is(err, database.ErrWeakPassword):
			writeError(w, http.StatusBadRequest, "INVALID_CREDENTIALS", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) listSessionsHandler(w http.ResponseWriter, r *http.Request) {
	principal, _ := s.currentPrincipal(r)
	items, err := s.store.ListSessions(r.Context(), principal.RawSession)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) deleteSessionHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSessionByPrefix(r.Context(), chi.URLParam(r, "sessionID")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Session not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAPIKeysHandler(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rawKey, info, err := s.store.CreateAPIKey(r.Context(), req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_API_KEY", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    rawKey,
		"record": info,
	})
}

func (s *Server) deleteAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIKey(r.Context(), chi.URLParam(r, "keyID")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "API key not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Komga-standard claim / login / user endpoints ---

func (s *Server) komgaClaimStatus(w http.ResponseWriter, r *http.Request) {
	claimed, err := s.store.HasAdmin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"isClaimed": claimed})
}

func (s *Server) komgaClaimCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.Header.Get("X-Komga-Email"))
	password := r.Header.Get("X-Komga-Password")
	if username == "" && password == "" {
		var body struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
		username = strings.TrimSpace(body.Username)
		if username == "" {
			username = strings.TrimSpace(body.Email)
		}
		password = body.Password
	}
	admin, err := s.store.ClaimAdmin(r.Context(), username, password)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrAlreadyClaimed):
			writeError(w, http.StatusBadRequest, "ALREADY_CLAIMED", err.Error())
		case errors.Is(err, database.ErrInvalidUsername), errors.Is(err, database.ErrWeakPassword):
			writeError(w, http.StatusBadRequest, "INVALID_CREDENTIALS", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":    "admin",
		"email": admin.Username,
		"roles": []string{"USER", "ADMIN", "FILE_DOWNLOAD", "PAGE_STREAMING"},
	})
}

func (s *Server) komgaSetCookie(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.currentPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required.")
		return
	}
	remember := r.URL.Query().Get("remember-me") == "true"
	if principal.Method == "session" && principal.RawSession != "" {
		setSessionCookies(w, r, principal.RawSession, remember)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rawToken, _, err := s.store.CreateSession(r.Context(), principal.Username, remember, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_ERROR", err.Error())
		return
	}
	setSessionCookies(w, r, rawToken, remember)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) komgaPatchMyPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	principal, _ := s.currentPrincipal(r)
	if req.CurrentPassword == "" {
		if _, pass, ok := r.BasicAuth(); ok {
			req.CurrentPassword = pass
		}
	}
	if _, err := s.store.UpdateAdminCredentials(r.Context(), req.CurrentPassword, "", req.Password, principal.RawSession); err != nil {
		if errors.Is(err, database.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "INVALID_PASSWORD", err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID_PASSWORD", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
