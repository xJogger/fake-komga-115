package webdav

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestWebDAVPropfindAndRange(t *testing.T) {
	payload := []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "reader" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			if r.URL.Path == "/dav/" || r.URL.Path == "/dav" {
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/dav/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/%E6%BC%AB%E7%94%BB/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
</D:multistatus>`))
				return
			}
			if r.URL.Path == "/dav/漫画/" {
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/dav/%E6%BC%AB%E7%94%BB/</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/%E6%BC%AB%E7%94%BB/%5BAuthor%5D%20Vol.01%20%231.cbz</D:href>
    <D:propstat>
      <D:status>HTTP/1.1 200 OK</D:status>
      <D:prop>
        <D:resourcetype/>
        <D:getcontentlength>62</D:getcontentlength>
        <D:getetag>"etag-v1"</D:getetag>
        <D:getlastmodified>Wed, 18 Nov 2026 02:00:00 GMT</D:getlastmodified>
      </D:prop>
    </D:propstat>
  </D:response>
</D:multistatus>`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case http.MethodGet:
			if r.URL.Path != "/dav/漫画/[Author] Vol.01 #1.cbz" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			rng := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
			parts := strings.SplitN(rng, "-", 2)
			start, _ := strconv.Atoi(parts[0])
			end, _ := strconv.Atoi(parts[1])
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[start : end+1])
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	mgr := NewManager()
	account := database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{
			ID:            "wd_test",
			BaseURL:       server.URL + "/dav",
			Username:      "reader",
			RatePerSecond: 100,
		},
		Password: "secret",
	}
	ctx := t.Context()
	if err := mgr.TestConnection(ctx, account); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	entries, err := mgr.ListDirectory(ctx, account, "/漫画", "/漫画")
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "[Author] Vol.01 #1.cbz" || entries[0].ETag != "etag-v1" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	chunk, err := mgr.FetchRange(ctx, account, "/漫画", "[Author] Vol.01 #1.cbz", 10, 19)
	if err != nil {
		t.Fatalf("FetchRange: %v", err)
	}
	if string(chunk) != "abcdefghij" {
		t.Fatalf("chunk=%q, want abcdefghij", string(chunk))
	}
}

func TestWebDAVRetryStatAndRedirect(t *testing.T) {
	payload := []byte("0123456789abcdefghij")
	// Separate target server to verify cross-host redirect strips Authorization while keeping Range.
	cdnServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization header leaked on cross-host redirect")
		}
		rng := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
		parts := strings.SplitN(rng, "-", 2)
		start, _ := strconv.Atoi(parts[0])
		end, _ := strconv.Atoi(parts[1])
		if end >= len(payload) {
			end = len(payload) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer cdnServer.Close()

	var propfindCalls, rangeCalls int
	davServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			propfindCalls++
			if propfindCalls == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/dav/vol.cbz</D:href>
    <D:propstat><D:status>HTTP/1.1 200 OK</D:status><D:prop><D:resourcetype/><D:getcontentlength>20</D:getcontentlength></D:prop></D:propstat>
  </D:response>
</D:multistatus>`))
		case http.MethodHead:
			// Simulate server omitting Content-Length on HEAD to force PROPFIND Depth: 0 fallback.
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			rangeCalls++
			if rangeCalls == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			http.Redirect(w, r, cdnServer.URL+"/blob", http.StatusFound)
		}
	}))
	defer davServer.Close()

	mgr := NewManager()
	account := database.WebDAVAccountSecret{
		WebDAVAccount: database.WebDAVAccount{
			ID:            "wd_retry",
			BaseURL:       davServer.URL + "/dav",
			Username:      "reader",
			RatePerSecond: 100,
		},
		Password: "secret",
	}
	ctx := t.Context()

	size, err := mgr.StatFileSize(ctx, account, "/", "vol.cbz")
	if err != nil {
		t.Fatalf("StatFileSize: %v", err)
	}
	if size != 20 || propfindCalls != 2 {
		t.Fatalf("size=%d propfindCalls=%d, want 20 and 2", size, propfindCalls)
	}

	// Request slightly past EOF to also test Content-Range tail short-read tolerance.
	got, err := mgr.FetchRange(ctx, account, "/", "vol.cbz", 15, 25)
	if err != nil {
		t.Fatalf("FetchRange with retry+redirect: %v", err)
	}
	if string(got) != "fghij" || rangeCalls != 2 {
		t.Fatalf("got=%q rangeCalls=%d, want fghij and 2", string(got), rangeCalls)
	}
}
