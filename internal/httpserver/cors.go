package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

const corsAllowedOriginsSetting = "cors_allowed_origins"

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" || !isExternalAPIPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		normalizedOrigin, ok := normalizeCORSOrigin(origin)
		if !ok {
			if isCORSPreflight(r) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := s.allowedCORSOrigins(r.Context())
		if err != nil || !allowed[normalizedOrigin] {
			if isCORSPreflight(r) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		writeCORSHeaders(w, normalizedOrigin, r.Header.Get("Access-Control-Request-Private-Network") == "true")
		if isCORSPreflight(r) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isExternalAPIPath(path string) bool {
	return path == "/api/v1" || path == "/api/v2" ||
		strings.HasPrefix(path, "/api/v1/") || strings.HasPrefix(path, "/api/v2/")
}

func isCORSPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func writeCORSHeaders(w http.ResponseWriter, origin string, allowPrivateNetwork bool) {
	header := w.Header()
	header.Set("Access-Control-Allow-Origin", origin)
	header.Set("Access-Control-Allow-Credentials", "true")
	header.Set("Access-Control-Allow-Methods", "GET,HEAD,POST,PUT,PATCH,DELETE,OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Content-Type,Accept,Authorization")
	header.Set("Access-Control-Max-Age", "600")
	if allowPrivateNetwork {
		header.Set("Access-Control-Allow-Private-Network", "true")
	}
	addVary(header, "Origin")
	addVary(header, "Access-Control-Request-Method")
	addVary(header, "Access-Control-Request-Headers")
	addVary(header, "Access-Control-Request-Private-Network")
}

func addVary(header http.Header, value string) {
	for _, part := range strings.Split(header.Get("Vary"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), value) {
			return
		}
	}
	header.Add("Vary", value)
}

func (s *Server) allowedCORSOrigins(ctx context.Context) (map[string]bool, error) {
	value, err := s.store.GetSetting(ctx, corsAllowedOriginsSetting)
	if err != nil {
		return map[string]bool{}, nil
	}
	origins := map[string]bool{}
	for _, line := range strings.Split(value, "\n") {
		origin, ok := normalizeCORSOrigin(line)
		if ok {
			origins[origin] = true
		}
	}
	return origins, nil
}

func normalizeCORSOrigins(value string) (string, error) {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		origin, ok := normalizeCORSOrigin(line)
		if !ok {
			return "", errInvalidCORSOrigin
		}
		if !seen[origin] {
			out = append(out, origin)
			seen[origin] = true
		}
	}
	return strings.Join(out, "\n"), nil
}

var errInvalidCORSOrigin = errString("CORS origin must be an http(s) origin without path, query, fragment or wildcard")

type errString string

func (e errString) Error() string { return string(e) }

func normalizeCORSOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "*") || strings.EqualFold(raw, "null") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(parsed.Host), true
}
