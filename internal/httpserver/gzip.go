package httpserver

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r) || shouldSkipGzipPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

func acceptsGzip(r *http.Request) bool {
	ae := strings.ToLower(r.Header.Get("Accept-Encoding"))
	return strings.Contains(ae, "gzip")
}

func shouldSkipGzipPath(p string) bool {
	if strings.HasPrefix(p, "/api/v1/sse/") || strings.Contains(p, "/thumbnail") {
		return true
	}
	if strings.HasPrefix(p, "/api/v1/books/") && strings.Contains(p, "/pages/") {
		return true
	}
	return false
}

func isCompressibleContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if semi := strings.IndexByte(ct, ';'); semi >= 0 {
		ct = strings.TrimSpace(ct[:semi])
	}
	switch ct {
	case "application/json",
		"text/html",
		"text/css",
		"application/javascript",
		"text/javascript",
		"text/plain",
		"image/svg+xml",
		"application/manifest+json":
		return true
	default:
		return strings.HasSuffix(ct, "+json")
	}
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	decided     bool
	useGzip     bool
	wroteHeader bool
}

func (g *gzipResponseWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	if status == http.StatusNoContent || status == http.StatusNotModified || status == http.StatusSwitchingProtocols {
		return
	}
	if g.Header().Get("Content-Encoding") != "" {
		return
	}
	if !isCompressibleContentType(g.Header().Get("Content-Type")) {
		return
	}
	g.useGzip = true
	g.Header().Del("Content-Length")
	g.Header().Set("Content-Encoding", "gzip")
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(g.ResponseWriter)
	g.gz = gz
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(p))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.useGzip && g.gz != nil {
		return g.gz.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if flusher, ok := g.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (g *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

func (g *gzipResponseWriter) Close() {
	if g.gz != nil {
		_ = g.gz.Close()
		g.gz.Reset(io.Discard)
		gzipWriterPool.Put(g.gz)
		g.gz = nil
	}
}
