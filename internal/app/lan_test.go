package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func pickFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestDualListenerLANServerIsolation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	token := "test-mobile-loopback-token-0123456789"
	a, err := New(context.Background(), Options{
		Host:          "127.0.0.1",
		Port:          0,
		DataDir:       t.TempDir(),
		Logger:        logger,
		LoopbackToken: token,
	})
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	if _, err := a.Start(); err != nil {
		t.Fatalf("app.Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = a.Shutdown(ctx)
		cancel()
	}()

	lanPort := pickFreePort(t)
	// 1. Before admin setup, LAN listener must refuse to start.
	if _, err := a.StartLANServer(lanPort); err == nil {
		t.Fatal("expected StartLANServer to fail before admin password is configured")
	}

	// 2. Verify loopback listener blocks requests without fk115_mobile_guard cookie.
	loopURL := "http://" + a.ListenAddr()
	resp, err := http.Get(loopURL + "/health")
	if err != nil {
		t.Fatalf("loopback unguarded GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 without loopback guard cookie, got %d", resp.StatusCode)
	}

	// 3. Initialize admin via guarded loopback endpoint.
	setupBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "password123"})
	req, _ := http.NewRequest(http.MethodPost, loopURL+"/admin/api/auth/setup", bytes.NewReader(setupBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "fk115_mobile_guard", Value: token})
	setupResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("setup request: %v", err)
	}
	setupResp.Body.Close()
	if setupResp.StatusCode != http.StatusOK && setupResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup status=%d", setupResp.StatusCode)
	}

	// 4. Now LAN listener starts cleanly and enforces normal auth without loopback cookie.
	bound, err := a.StartLANServer(lanPort)
	if err != nil {
		t.Fatalf("StartLANServer after setup: %v", err)
	}
	active, gotPort, lastErr := a.LANStatus()
	if !active || gotPort != lanPort || lastErr != "" || bound == "" {
		t.Fatalf("unexpected LAN status: active=%v port=%d err=%q bound=%q", active, gotPort, lastErr, bound)
	}

	_, portStr, err := net.SplitHostPort(bound)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", bound, err)
	}
	lanBase := "http://" + net.JoinHostPort("127.0.0.1", portStr)

	// /health is accessible on LAN listener without loopback cookie.
	hResp, err := http.Get(lanBase + "/health")
	if err != nil {
		t.Fatalf("LAN /health: %v", err)
	}
	hResp.Body.Close()
	if hResp.StatusCode != http.StatusOK {
		t.Fatalf("LAN /health status=%d, want 200", hResp.StatusCode)
	}

	// Komga API on LAN listener requires real credentials (401 without, 200 with Basic Auth).
	unauthResp, err := http.Get(lanBase + "/api/v1/libraries")
	if err != nil {
		t.Fatalf("LAN unauth /api/v1/libraries: %v", err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("LAN unauth status=%d, want 401", unauthResp.StatusCode)
	}

	authReq, _ := http.NewRequest(http.MethodGet, lanBase+"/api/v1/libraries", nil)
	authReq.SetBasicAuth("admin", "password123")
	authResp, err := http.DefaultClient.Do(authReq)
	if err != nil {
		t.Fatalf("LAN BasicAuth /api/v1/libraries: %v", err)
	}
	authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("LAN BasicAuth status=%d, want 200", authResp.StatusCode)
	}

	// 5. Stopping LAN listener leaves loopback listener alive.
	if err := a.StopLANServer(); err != nil {
		t.Fatalf("StopLANServer: %v", err)
	}
	if active, _, _ := a.LANStatus(); active {
		t.Fatal("expected LAN listener inactive after StopLANServer")
	}
	loopHealthReq, _ := http.NewRequest(http.MethodGet, loopURL+"/health", nil)
	loopHealthReq.AddCookie(&http.Cookie{Name: "fk115_mobile_guard", Value: token})
	lhResp, err := http.DefaultClient.Do(loopHealthReq)
	if err != nil {
		t.Fatalf("loopback /health after LAN stop: %v", err)
	}
	lhResp.Body.Close()
	if lhResp.StatusCode != http.StatusOK {
		t.Fatalf("loopback /health status=%d, want 200", lhResp.StatusCode)
	}
}
