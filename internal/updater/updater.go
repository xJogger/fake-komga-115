package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/buildinfo"
	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
)

const (
	DefaultLatestReleaseURL       = "https://api.github.com/repos/xJogger/MangaAlpha/releases/latest"
	SettingUpdateCheckEnabled     = "update_check_enabled"
	SettingUpdateDismissedVersion = "update_dismissed_version"
	SettingUpdateCacheJSON        = "update_check_cache_json"
	DefaultCheckInterval          = 24 * time.Hour
	updateUserAgent               = "MangaAlpha-Updater/2.5 (https://github.com/xJogger/MangaAlpha)"
)

type Status struct {
	CurrentVersion   string `json:"currentVersion"`
	LatestVersion    string `json:"latestVersion,omitempty"`
	HasUpdate        bool   `json:"hasUpdate"`
	Dismissed        bool   `json:"dismissed"`
	DismissedVersion string `json:"dismissedVersion,omitempty"`
	AutoCheckEnabled bool   `json:"autoCheckEnabled"`
	Platform         string `json:"platform"`
	InDocker         bool   `json:"inDocker"`
	DockerCommand    string `json:"dockerCommand,omitempty"`
	ReleaseURL       string `json:"releaseUrl,omitempty"`
	ReleaseNotes     string `json:"releaseNotes,omitempty"`
	PublishedAt      string `json:"publishedAt,omitempty"`
	AssetName        string `json:"assetName,omitempty"`
	AssetURL         string `json:"assetUrl,omitempty"`
	AssetSize        int64  `json:"assetSize,omitempty"`
	ChecksumsURL     string `json:"checksumsUrl,omitempty"`
	CheckedAt        string `json:"checkedAt,omitempty"`
	Checking         bool   `json:"checking"`
	LastError        string `json:"lastError,omitempty"`
}

type cachedRelease struct {
	LatestVersion string `json:"latestVersion"`
	ReleaseURL    string `json:"releaseUrl"`
	ReleaseNotes  string `json:"releaseNotes"`
	PublishedAt   string `json:"publishedAt"`
	AssetName     string `json:"assetName"`
	AssetURL      string `json:"assetUrl"`
	AssetSize     int64  `json:"assetSize"`
	ChecksumsURL  string `json:"checksumsUrl"`
	CheckedAt     string `json:"checkedAt"`
}

type Service struct {
	store          *database.Store
	logger         *slog.Logger
	releaseAPIURL  string
	currentVersion string
	goos           string
	goarch         string
	inDocker       bool
	nowFunc        func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.RWMutex
	checking  bool
	lastError string
	cached    cachedRelease
}

func NewService(store *database.Store, logger *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		store:          store,
		logger:         logger,
		releaseAPIURL:  DefaultLatestReleaseURL,
		currentVersion: buildinfo.Version,
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		inDocker:       detectDockerEnv(),
		nowFunc:        time.Now,
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
	}
	s.loadCached(ctx)
	go s.backgroundLoop()
	return s
}

func detectDockerEnv() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if strings.TrimSpace(os.Getenv("FK115_IN_DOCKER")) == "1" {
		return true
	}
	return false
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.cancel()
	if s.done != nil {
		<-s.done
	}
}

func (s *Service) SetCurrentVersion(v string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	s.mu.Lock()
	s.currentVersion = v
	s.mu.Unlock()
}

func (s *Service) autoCheckEnabled(ctx context.Context) bool {
	if s.store == nil {
		return true
	}
	raw, err := s.store.GetSetting(ctx, SettingUpdateCheckEnabled)
	if err != nil || strings.TrimSpace(raw) == "" {
		return true
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return true
	}
	return parsed
}

func (s *Service) dismissedVersion(ctx context.Context) string {
	if s.store == nil {
		return ""
	}
	raw, err := s.store.GetSetting(ctx, SettingUpdateDismissedVersion)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(raw)
}

func (s *Service) loadCached(ctx context.Context) {
	if s.store == nil {
		return
	}
	raw, err := s.store.GetSetting(ctx, SettingUpdateCacheJSON)
	if err != nil || strings.TrimSpace(raw) == "" {
		return
	}
	var c cachedRelease
	if json.Unmarshal([]byte(raw), &c) == nil {
		s.mu.Lock()
		s.cached = c
		s.mu.Unlock()
	}
}

func (s *Service) saveCached(ctx context.Context, c cachedRelease) {
	if s.store == nil {
		return
	}
	if raw, err := json.Marshal(c); err == nil {
		_ = s.store.SetSetting(ctx, SettingUpdateCacheJSON, string(raw))
	}
}

func (s *Service) backgroundLoop() {
	defer close(s.done)
	// Delay initial check slightly so startup stays snappy.
	select {
	case <-s.ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}
	s.maybeAutoCheck()

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.maybeAutoCheck()
		}
	}
}

func (s *Service) maybeAutoCheck() {
	if !s.autoCheckEnabled(s.ctx) {
		return
	}
	s.mu.RLock()
	checkedAt := s.cached.CheckedAt
	s.mu.RUnlock()
	if checkedAt != "" {
		if t, err := time.Parse(time.RFC3339, checkedAt); err == nil {
			if s.nowFunc().Sub(t) < DefaultCheckInterval {
				return
			}
		}
	}
	checkCtx, cancel := context.WithTimeout(s.ctx, 25*time.Second)
	defer cancel()
	_, _ = s.CheckNow(checkCtx)
}

func (s *Service) Status(ctx context.Context) Status {
	s.mu.RLock()
	cur := s.currentVersion
	c := s.cached
	checking := s.checking
	lastErr := s.lastError
	goos, goarch, inDocker := s.goos, s.goarch, s.inDocker
	s.mu.RUnlock()

	autoEnabled := s.autoCheckEnabled(ctx)
	dismissedVer := s.dismissedVersion(ctx)
	hasUpdate := c.LatestVersion != "" && CompareVersions(c.LatestVersion, cur) > 0
	dismissed := hasUpdate && dismissedVer != "" && normalizeVersionTag(dismissedVer) == normalizeVersionTag(c.LatestVersion)

	dockerCmd := ""
	if inDocker && c.LatestVersion != "" {
		dockerCmd = fmt.Sprintf("docker pull blacktal/fake-komga-115:%s", ensureVPrefix(c.LatestVersion))
	}

	return Status{
		CurrentVersion:   cur,
		LatestVersion:    c.LatestVersion,
		HasUpdate:        hasUpdate,
		Dismissed:        dismissed,
		DismissedVersion: dismissedVer,
		AutoCheckEnabled: autoEnabled,
		Platform:         goos + "/" + goarch,
		InDocker:         inDocker,
		DockerCommand:    dockerCmd,
		ReleaseURL:       c.ReleaseURL,
		ReleaseNotes:     c.ReleaseNotes,
		PublishedAt:      c.PublishedAt,
		AssetName:        c.AssetName,
		AssetURL:         c.AssetURL,
		AssetSize:        c.AssetSize,
		ChecksumsURL:     c.ChecksumsURL,
		CheckedAt:        c.CheckedAt,
		Checking:         checking,
		LastError:        lastErr,
	}
}

func (s *Service) UpdateConfig(ctx context.Context, autoEnabled *bool, dismissVersion *string) (Status, error) {
	if s.store != nil {
		if autoEnabled != nil {
			if err := s.store.SetSetting(ctx, SettingUpdateCheckEnabled, strconv.FormatBool(*autoEnabled)); err != nil {
				return Status{}, err
			}
		}
		if dismissVersion != nil {
			if err := s.store.SetSetting(ctx, SettingUpdateDismissedVersion, strings.TrimSpace(*dismissVersion)); err != nil {
				return Status{}, err
			}
		}
	}
	return s.Status(ctx), nil
}

func (s *Service) httpClient(ctx context.Context, timeout time.Duration) (*http.Client, string) {
	proxyRaw := ""
	if s.store != nil {
		if raw, err := s.store.GetSetting(ctx, bangumi.SettingProxyURL); err == nil {
			proxyRaw = strings.TrimSpace(raw)
		}
	}
	tr, err := bangumi.NewBangumiTransport(proxyRaw, timeout)
	if err != nil {
		tr, _ = bangumi.NewBangumiTransport("", timeout)
		proxyRaw = ""
	}
	return &http.Client{Transport: tr, Timeout: timeout}, proxyRaw
}

type githubReleaseDTO struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

func (s *Service) CheckNow(ctx context.Context) (Status, error) {
	s.mu.Lock()
	if s.checking {
		s.mu.Unlock()
		return s.Status(ctx), nil
	}
	s.checking = true
	s.lastError = ""
	goos, goarch := s.goos, s.goarch
	apiURL := s.releaseAPIURL
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.checking = false
		s.mu.Unlock()
	}()

	client, proxyRaw := s.httpClient(ctx, 25*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		s.recordError(err, proxyRaw)
		return s.Status(ctx), err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", updateUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		s.recordError(err, proxyRaw)
		return s.Status(ctx), err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	if readErr != nil {
		s.recordError(readErr, proxyRaw)
		return s.Status(ctx), readErr
	}
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("GitHub release check returned HTTP %d", resp.StatusCode)
		s.recordError(err, proxyRaw)
		return s.Status(ctx), err
	}
	var dto githubReleaseDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		s.recordError(err, proxyRaw)
		return s.Status(ctx), err
	}
	tag := strings.TrimSpace(dto.TagName)
	if tag == "" {
		err = errors.New("latest release missing tag_name")
		s.recordError(err, proxyRaw)
		return s.Status(ctx), err
	}

	assetName, assetURL, assetSize, checksumsURL := matchPlatformAsset(dto, goos, goarch)
	notes := strings.TrimSpace(dto.Body)
	if len(notes) > 4000 {
		notes = notes[:4000] + "\n…"
	}
	c := cachedRelease{
		LatestVersion: tag,
		ReleaseURL:    strings.TrimSpace(dto.HTMLURL),
		ReleaseNotes:  notes,
		PublishedAt:   strings.TrimSpace(dto.PublishedAt),
		AssetName:     assetName,
		AssetURL:      assetURL,
		AssetSize:     assetSize,
		ChecksumsURL:  checksumsURL,
		CheckedAt:     s.nowFunc().UTC().Format(time.RFC3339),
	}
	s.mu.Lock()
	s.cached = c
	s.lastError = ""
	s.mu.Unlock()
	s.saveCached(ctx, c)
	return s.Status(ctx), nil
}

func (s *Service) recordError(err error, proxyRaw string) {
	clean := bangumi.SanitizeProxyError(err, proxyRaw)
	s.mu.Lock()
	s.lastError = clean
	s.mu.Unlock()
	diagnostics.Default().Record(diagnostics.Event{
		Level:     diagnostics.LevelWarn,
		Component: "updater",
		Stage:     "update_check",
		Action:    "check",
		Code:      diagnostics.ClassifyError(err),
		Detail:    clean,
	})
}

func matchPlatformAsset(dto githubReleaseDTO, goos, goarch string) (name, url string, size int64, checksumsURL string) {
	wantName := ""
	switch goos {
	case "android":
		wantName = "mangaalpha-arm64.apk"
	case "windows":
		wantName = fmt.Sprintf("mangaalpha_%s_windows_%s.zip", dto.TagName, goarch)
	case "linux", "darwin":
		wantName = fmt.Sprintf("mangaalpha_%s_%s_%s.tar.gz", dto.TagName, goos, goarch)
	}
	for _, a := range dto.Assets {
		if a.Name == "SHA256SUMS" {
			checksumsURL = a.BrowserDownloadURL
		}
		if wantName != "" && a.Name == wantName {
			name = a.Name
			url = a.BrowserDownloadURL
			size = a.Size
		}
	}
	return name, url, size, checksumsURL
}

// DownloadVerifiedAsset downloads the matched platform asset (e.g. mangaalpha-arm64.apk)
// reusing the configured Bangumi proxy (or direct if unset) and strictly verifies its
// SHA-256 against the Release's SHA256SUMS file before moving it into destDir.
func (s *Service) DownloadVerifiedAsset(
	ctx context.Context,
	destDir string,
	onProgress func(downloaded, total int64),
) (string, error) {
	st := s.Status(ctx)
	if st.AssetURL == "" || st.AssetName == "" {
		var err error
		st, err = s.CheckNow(ctx)
		if err != nil {
			return "", err
		}
	}
	if st.AssetURL == "" || st.AssetName == "" {
		return "", errors.New("no downloadable package found for this platform")
	}
	if st.ChecksumsURL == "" {
		return "", errors.New("SHA256SUMS not found in latest release; refusing unverified download")
	}

	client, proxyRaw := s.httpClient(ctx, 10*time.Minute)
	expectedHex, err := fetchExpectedSHA256(ctx, client, st.ChecksumsURL, st.AssetName)
	if err != nil {
		return "", fmt.Errorf("fetch SHA256SUMS: %s", bangumi.SanitizeProxyError(err, proxyRaw))
	}

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", err
	}
	safeVer := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, st.LatestVersion)
	finalPath := filepath.Join(destDir, safeVer+"-"+filepath.Base(st.AssetName))
	if existingHex, hErr := sha256File(finalPath); hErr == nil && existingHex == expectedHex {
		if onProgress != nil {
			if info, sErr := os.Stat(finalPath); sErr == nil {
				onProgress(info.Size(), info.Size())
			}
		}
		return finalPath, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, st.AssetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", updateUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download update package: %s", bangumi.SanitizeProxyError(err, proxyRaw))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download update package returned HTTP %d", resp.StatusCode)
	}

	tmpPath := finalPath + ".part"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	total := resp.ContentLength
	if total <= 0 {
		total = st.AssetSize
	}
	hasher := sha256.New()
	buf := make([]byte, 128<<10)
	var downloaded int64
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				out.Close()
				_ = os.Remove(tmpPath)
				return "", wErr
			}
			_, _ = hasher.Write(buf[:n])
			downloaded += int64(n)
			if onProgress != nil {
				onProgress(downloaded, total)
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			out.Close()
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("download interrupted: %s", bangumi.SanitizeProxyError(rErr, proxyRaw))
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	actualHex := hex.EncodeToString(hasher.Sum(nil))
	if actualHex != expectedHex {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("SHA256 verification failed for %s", st.AssetName)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	diagnostics.Default().Record(diagnostics.Event{
		Level:     diagnostics.LevelInfo,
		Component: "updater",
		Stage:     "update_download",
		Action:    "verified",
		Detail:    fmt.Sprintf("downloaded and verified %s (%s)", st.AssetName, st.LatestVersion),
	})
	return finalPath, nil
}

func fetchExpectedSHA256(ctx context.Context, client *http.Client, checksumsURL, assetName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", updateUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	resp.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS HTTP %d", resp.StatusCode)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		hash := strings.ToLower(fields[0])
		name := strings.TrimPrefix(fields[1], "*")
		if name == assetName && len(hash) == 64 {
			return hash, nil
		}
	}
	return "", fmt.Errorf("%s not listed in SHA256SUMS", assetName)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type parsedSemver struct {
	major      int
	minor      int
	patch      int
	prerelease string
	valid      bool
}

func parseSemver(raw string) parsedSemver {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if idx := strings.IndexByte(s, '+'); idx >= 0 {
		s = s[:idx]
	}
	pre := ""
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		pre = s[idx+1:]
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return parsedSemver{}
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	pat := 0
	var err3 error
	if len(parts) == 3 {
		pat, err3 = strconv.Atoi(parts[2])
	}
	if err1 != nil || err2 != nil || err3 != nil || maj < 0 || min < 0 || pat < 0 {
		return parsedSemver{}
	}
	return parsedSemver{major: maj, minor: min, patch: pat, prerelease: pre, valid: true}
}

func normalizeVersionTag(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	return s
}

func ensureVPrefix(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "v") || strings.HasPrefix(s, "V") {
		return "v" + s[1:]
	}
	return "v" + s
}

// CompareVersions compares two SemVer strings (v2.5.0 > v2.5.0-preview.1 > v2.4.0).
// Returns >0 if a > b, <0 if a < b, 0 if equal or unparseable.
func CompareVersions(a, b string) int {
	pa := parseSemver(a)
	pb := parseSemver(b)
	if !pa.valid || !pb.valid {
		return 0
	}
	if pa.major != pb.major {
		if pa.major > pb.major {
			return 1
		}
		return -1
	}
	if pa.minor != pb.minor {
		if pa.minor > pb.minor {
			return 1
		}
		return -1
	}
	if pa.patch != pb.patch {
		if pa.patch > pb.patch {
			return 1
		}
		return -1
	}
	// Stable release beats prerelease of the same version.
	if pa.prerelease == "" && pb.prerelease != "" {
		return 1
	}
	if pa.prerelease != "" && pb.prerelease == "" {
		return -1
	}
	if pa.prerelease == pb.prerelease {
		return 0
	}
	if pa.prerelease > pb.prerelease {
		return 1
	}
	return -1
}
