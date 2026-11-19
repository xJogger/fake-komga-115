package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMobileLoopbackGuard(t *testing.T) {
	guard := loopbackGuard("fixture-key", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tt := range []struct {
		host, cookie string
		want         int
	}{
		{"127.0.0.1:32100", "", http.StatusNotFound},
		{"127.0.0.1:32100", "wrong-key", http.StatusNotFound},
		{"other.test:32100", "fixture-key", http.StatusNotFound},
		{"127.0.0.1:32100", "fixture-key", http.StatusNoContent},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/health", nil)
		if tt.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "fk115_mobile_guard", Value: tt.cookie})
		}
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Fatalf("host %q got %d, want %d", tt.host, rec.Code, tt.want)
		}
	}
}
