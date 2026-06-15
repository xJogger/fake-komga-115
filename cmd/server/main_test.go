package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultDataDir(t *testing.T) {
	failedConfig := func() (string, error) { return "", errors.New("unavailable") }
	if got := defaultDataDir("linux", func(string) string { return "" }, failedConfig); got != "./data" {
		t.Fatalf("linux data directory = %q", got)
	}
	got := defaultDataDir(
		"windows",
		func(key string) string {
			if key == "LOCALAPPDATA" {
				return `C:\Users\reader\AppData\Local`
			}
			return ""
		},
		failedConfig,
	)
	want := filepath.Join(`C:\Users\reader\AppData\Local`, "fake-komga-115", "data")
	if got != want {
		t.Fatalf("windows data directory = %q, want %q", got, want)
	}
}

func TestLocalAdminURL(t *testing.T) {
	if got := localAdminURL("0.0.0.0", 25600); got != "http://127.0.0.1:25600/admin" {
		t.Fatalf("wildcard URL = %q", got)
	}
	if got := localAdminURL("::", 25600); got != "http://[::1]:25600/admin" {
		t.Fatalf("IPv6 URL = %q", got)
	}
}

func TestBrowserCommand(t *testing.T) {
	tests := []struct {
		goos string
		name string
		args []string
	}{
		{"windows", "rundll32.exe", []string{"url.dll,FileProtocolHandler", "http://localhost"}},
		{"darwin", "open", []string{"http://localhost"}},
		{"linux", "xdg-open", []string{"http://localhost"}},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			name, args, err := browserCommand(test.goos, "http://localhost")
			if err != nil {
				t.Fatal(err)
			}
			if name != test.name || !reflect.DeepEqual(args, test.args) {
				t.Fatalf("command = %q %#v", name, args)
			}
		})
	}
	if _, _, err := browserCommand("plan9", "http://localhost"); err == nil {
		t.Fatal("unsupported platform must return an error")
	}
}
