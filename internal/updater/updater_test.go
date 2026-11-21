package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v2.5.0", "v2.4.0", 1},
		{"2.5.0", "v2.5.0-preview.8", 1},
		{"v2.5.0", "v2.5.0-dev", 1},
		{"v2.5.0", "v2.5.0+abc123", 0},
		{"v2.4.0", "v2.5.0", -1},
		{"v2.4.0", "v2.5.0-preview+7899704", -1},
		{"v2.5.0", "v2.5.0", 0},
	}
	for _, tc := range cases {
		got := CompareVersions(tc.a, tc.b)
		if (got > 0 && tc.want <= 0) || (got < 0 && tc.want >= 0) || (got == 0 && tc.want != 0) {
			t.Fatalf("CompareVersions(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCheckNowAndVerifiedAssetDownload(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(filepath.Join(t.TempDir(), "updater.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	apkPayload := []byte("fake-signed-apk-binary-payload-v2.5.0")
	sum := sha256.Sum256(apkPayload)
	sumHex := hex.EncodeToString(sum[:])

	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     "v2.5.0",
				"html_url":     "https://github.com/xJogger/MangaAlpha/releases/tag/v2.5.0",
				"body":         "## MangaAlpha 2.5.0\n- New features",
				"published_at": "2026-11-22T00:00:00Z",
				"assets": []map[string]any{
					{
						"name":                 "mangaalpha-arm64.apk",
						"browser_download_url": serverURL + "/dl/mangaalpha-arm64.apk",
						"size":                 len(apkPayload),
					},
					{
						"name":                 "SHA256SUMS",
						"browser_download_url": serverURL + "/dl/SHA256SUMS",
						"size":                 90,
					},
				},
			})
		case "/dl/SHA256SUMS":
			fmt.Fprintf(w, "%s  mangaalpha-arm64.apk\n", sumHex)
		case "/dl/mangaalpha-arm64.apk":
			_, _ = w.Write(apkPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL

	svc := NewService(store, nil)
	defer svc.Close()
	svc.releaseAPIURL = ts.URL + "/releases/latest"
	svc.currentVersion = "v2.4.0"
	svc.goos = "android"
	svc.goarch = "arm64"
	svc.inDocker = false

	st, err := svc.CheckNow(ctx)
	if err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	if !st.HasUpdate || st.LatestVersion != "v2.5.0" || st.AssetName != "mangaalpha-arm64.apk" {
		t.Fatalf("unexpected status: %+v", st)
	}

	var maxSeen int64
	apkPath, err := svc.DownloadVerifiedAsset(ctx, t.TempDir(), func(done, total int64) {
		if done > maxSeen {
			maxSeen = done
		}
	})
	if err != nil {
		t.Fatalf("DownloadVerifiedAsset: %v", err)
	}
	gotBytes, err := os.ReadFile(apkPath)
	if err != nil || string(gotBytes) != string(apkPayload) {
		t.Fatalf("downloaded file mismatch: err=%v len=%d", err, len(gotBytes))
	}
	if maxSeen != int64(len(apkPayload)) {
		t.Fatalf("progress callback maxSeen=%d want %d", maxSeen, len(apkPayload))
	}

	// Dismiss version hides update banner flag
	dismiss := "v2.5.0"
	st2, err := svc.UpdateConfig(ctx, nil, &dismiss)
	if err != nil || !st2.Dismissed {
		t.Fatalf("expected dismissed=true, got %+v err=%v", st2, err)
	}
}
