package diagnostics

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xJogger/fake-komga-115/internal/buildinfo"
)

const (
	// MaxFileBytes bounds each rotating diagnostics file (~1 MiB x 2 files = ~2 MiB total).
	MaxFileBytes = 1 << 20

	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"

	eventsFileName        = "events.jsonl"
	backupEventsFileName  = "events.jsonl.1"
	androidEventsFileName = "android-events.jsonl"
)

// Event is a strictly allow-listed, sanitized diagnostic record.
// Never store tokens, passwords, cookies, API keys, full URLs, or private file paths.
type Event struct {
	Timestamp     string         `json:"ts"`
	Level         string         `json:"level"`
	Component     string         `json:"component"`
	Stage         string         `json:"stage"`
	Action        string         `json:"action,omitempty"`
	Status        int            `json:"status,omitempty"`
	Code          string         `json:"code,omitempty"`
	DurationMs    int64          `json:"durationMs,omitempty"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Detail        string         `json:"detail,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
}

// Recorder persists sanitized diagnostic events to bounded rotating JSONL files.
type Recorder struct {
	dir          string
	eventsPath   string
	backupPath   string
	androidPath  string
	maxFileBytes int64

	verbose atomic.Bool
	mu      sync.Mutex
}

var (
	defaultMu       sync.RWMutex
	defaultRecorder *Recorder
)

// SetDefault registers the process-wide diagnostics recorder (nil clears it).
func SetDefault(r *Recorder) {
	defaultMu.Lock()
	defaultRecorder = r
	defaultMu.Unlock()
}

// Default returns the active process-wide recorder, or nil if unset.
func Default() *Recorder {
	defaultMu.RLock()
	r := defaultRecorder
	defaultMu.RUnlock()
	return r
}

// New creates a diagnostics recorder rooted at <dataDir>/diagnostics.
func New(dataDir string, verbose bool) (*Recorder, error) {
	dir := filepath.Join(filepath.Clean(dataDir), "diagnostics")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &Recorder{
		dir:          dir,
		eventsPath:   filepath.Join(dir, eventsFileName),
		backupPath:   filepath.Join(dir, backupEventsFileName),
		androidPath:  filepath.Join(dir, androidEventsFileName),
		maxFileBytes: MaxFileBytes,
	}
	r.verbose.Store(verbose)
	return r, nil
}

// WriteStartupFailure writes a sanitized startup failure event directly to
// <dataDir>/diagnostics/events.jsonl even when the full App failed to initialize.
func WriteStartupFailure(dataDir, component, stage string, err error) {
	if strings.TrimSpace(dataDir) == "" || err == nil {
		return
	}
	rec, rErr := New(dataDir, false)
	if rErr != nil {
		return
	}
	rec.Record(Event{
		Level:     LevelError,
		Component: component,
		Stage:     stage,
		Code:      ClassifyError(err),
		Detail:    SanitizeText(err.Error()),
	})
}

func (r *Recorder) Dir() string        { return r.dir }
func (r *Recorder) EventsPath() string { return r.eventsPath }
func (r *Recorder) Verbose() bool      { return r != nil && r.verbose.Load() }

func (r *Recorder) SetVerbose(v bool) {
	if r == nil {
		return
	}
	prev := r.verbose.Swap(v)
	if prev != v {
		r.Record(Event{
			Level:     LevelInfo,
			Component: "diagnostics",
			Stage:     "mode",
			Action:    "toggle",
			Code:      map[bool]string{true: "VERBOSE_ON", false: "VERBOSE_OFF"}[v],
			Detail:    map[bool]string{true: "detailed diagnostic logging enabled", false: "concise diagnostic logging enabled"}[v],
		})
	}
}

// Record stores an event if it is allowed by the current mode.
// Concise mode keeps warn/error and key lifecycle stages; verbose mode keeps everything.
func (r *Recorder) Record(ev Event) {
	if r == nil {
		return
	}
	ev = normalizeEvent(ev)
	if !r.shouldKeep(ev) {
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	line = append(line, '\n')

	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendLocked(line)
}

// RecordVerbose records an event only when verbose mode is active.
func (r *Recorder) RecordVerbose(ev Event) {
	if r == nil || !r.verbose.Load() {
		return
	}
	if ev.Level == "" {
		ev.Level = LevelDebug
	}
	r.Record(ev)
}

func (r *Recorder) shouldKeep(ev Event) bool {
	if r.verbose.Load() {
		return true
	}
	switch ev.Level {
	case LevelError, LevelWarn:
		return true
	}
	switch ev.Stage {
	case "startup", "shutdown", "mode", "lan_server", "scan_start", "scan_done",
		"archive_fallback", "bangumi_archive", "android_service", "android_lan":
		return true
	default:
		return false
	}
}

func (r *Recorder) appendLocked(line []byte) {
	if info, err := os.Stat(r.eventsPath); err == nil && info.Size()+int64(len(line)) > r.maxFileBytes {
		_ = os.Remove(r.backupPath)
		_ = os.Rename(r.eventsPath, r.backupPath)
	}
	f, err := os.OpenFile(r.eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(line)
	_ = f.Close()
}

// Status summarizes the diagnostic recorder state.
type Status struct {
	Verbose       bool   `json:"verbose"`
	Dir           string `json:"dir"`
	EventsCount   int    `json:"eventsCount"`
	TotalBytes    int64  `json:"totalBytes"`
	MaxTotalBytes int64  `json:"maxTotalBytes"`
	HasAndroidLog bool   `json:"hasAndroidLog"`
}

func (r *Recorder) Status() Status {
	if r == nil {
		return Status{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	hasAndroid := false
	for _, p := range []string{r.backupPath, r.eventsPath, r.androidPath} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
			if p == r.androidPath && info.Size() > 0 {
				hasAndroid = true
			}
		}
	}
	events := r.readAllLocked()
	return Status{
		Verbose:       r.verbose.Load(),
		Dir:           r.dir,
		EventsCount:   len(events),
		TotalBytes:    total,
		MaxTotalBytes: r.maxFileBytes * 2,
		HasAndroidLog: hasAndroid,
	}
}

// Recent returns up to limit newest events (in chronological order).
func (r *Recorder) Recent(limit int) []Event {
	if r == nil {
		return []Event{}
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	all := r.readAllLocked()
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
}

// Clear removes stored diagnostic event files.
func (r *Recorder) Clear() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for _, p := range []string{r.eventsPath, r.backupPath, r.androidPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ExportText builds a human-readable, strictly sanitized diagnostic bundle.
func (r *Recorder) ExportText(limit int) string {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# MangaAlpha Diagnostic Report\n")
	fmt.Fprintf(&buf, "version=%s os=%s arch=%s go=%s\n",
		buildinfo.Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(&buf, "exportedAt=%s verbose=%v\n", time.Now().UTC().Format(time.RFC3339), r.Verbose())
	fmt.Fprintf(&buf, "note=Sanitized allow-listed events only; no credentials, tokens, URLs, or private paths.\n\n")

	if r == nil {
		buf.WriteString("(diagnostics recorder unavailable)\n")
		return buf.String()
	}

	r.mu.Lock()
	events := r.readAllLocked()
	androidRaw := readAndroidTailLocked(r.androidPath, 300)
	r.mu.Unlock()

	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	fmt.Fprintf(&buf, "## Core Events (%d)\n", len(events))
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err == nil {
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}
	if len(androidRaw) > 0 {
		fmt.Fprintf(&buf, "\n## Android Native Events (%d)\n", len(androidRaw))
		for _, line := range androidRaw {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	return buf.String()
}

func (r *Recorder) readAllLocked() []Event {
	var out []Event
	for _, p := range []string{r.backupPath, r.eventsPath} {
		out = append(out, parseEventsFile(p)...)
	}
	return out
}

func parseEventsFile(path string) []Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 16*1024), 128*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

func readAndroidTailLocked(path string, maxLines int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 16*1024), 128*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			lines = append(lines, SanitizeText(line))
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

var allowedMetaKeys = map[string]bool{
	"provider":       true,
	"format":         true,
	"fallbackFormat": true,
	"attempt":        true,
	"maxAttempts":    true,
	"status":         true,
	"sizeBytes":      true,
	"actualBytes":    true,
	"pages":          true,
	"pageNumber":     true,
	"directories":    true,
	"files":          true,
	"series":         true,
	"books":          true,
	"updatedSeries":  true,
	"trigger":        true,
	"port":           true,
	"lanEnabled":     true,
	"skipped":        true,
	"reason":         true,
	"dump":           true,
	"seekable":       true,
	"retryAfterMs":   true,
	"httpStatus":     true,
}

func normalizeEvent(ev Event) Event {
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	switch strings.ToLower(strings.TrimSpace(ev.Level)) {
	case LevelDebug:
		ev.Level = LevelDebug
	case LevelWarn:
		ev.Level = LevelWarn
	case LevelError:
		ev.Level = LevelError
	default:
		ev.Level = LevelInfo
	}
	ev.Component = truncate(SanitizeToken(ev.Component), 40)
	if ev.Component == "" {
		ev.Component = "core"
	}
	ev.Stage = truncate(SanitizeToken(ev.Stage), 48)
	ev.Action = truncate(SanitizeToken(ev.Action), 48)
	ev.Code = truncate(SanitizeToken(ev.Code), 64)
	ev.CorrelationID = truncate(SanitizeToken(ev.CorrelationID), 64)
	ev.Detail = truncate(SanitizeText(ev.Detail), 300)

	if len(ev.Meta) > 0 {
		clean := make(map[string]any, len(ev.Meta))
		for k, v := range ev.Meta {
			if !allowedMetaKeys[k] {
				continue
			}
			switch val := v.(type) {
			case string:
				clean[k] = truncate(SanitizeText(val), 120)
			case int, int32, int64, uint, uint32, uint64, float32, float64, bool:
				clean[k] = val
			default:
				clean[k] = truncate(SanitizeText(fmt.Sprint(val)), 120)
			}
		}
		if len(clean) > 0 {
			ev.Meta = clean
		} else {
			ev.Meta = nil
		}
	}
	return ev
}

var (
	reURL         = regexp.MustCompile(`(?i)\b(?:https?|socks5h?|content|file)://[^\s"'<>]+`)
	reBearer      = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9+/=._~-]{6,}`)
	reSecretParam = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|apikey|api_key|cookie|authorization|session|uid|cid|seid|kid)\s*[:=]\s*[^\s,;'"&]+`)
	reUnixPath    = regexp.MustCompile(`/(?:home|Users|storage|sdcard|mnt|Volumes|data/user|data/data|var|etc|root|srv|opt|tmp)/[^\s"'<>:,;)]+`)
	reWinPath     = regexp.MustCompile(`(?i)\b[a-z]:\\[^\s"'<>:,;)]+`)
	reIPv4        = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
)

// SanitizeText scrubs URLs, credentials, tokens, private paths, and raw IPs from free-form error text.
func SanitizeText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = reURL.ReplaceAllString(s, "[redacted-url]")
	s = reBearer.ReplaceAllString(s, "$1 [redacted]")
	s = reSecretParam.ReplaceAllString(s, "$1=[redacted]")
	s = reUnixPath.ReplaceAllString(s, "[redacted-path]")
	s = reWinPath.ReplaceAllString(s, "[redacted-path]")
	s = reIPv4.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "127.0.0.1") || strings.HasPrefix(m, "0.0.0.0") {
			return m
		}
		return "[redacted-ip]"
	})
	return strings.TrimSpace(s)
}

func SanitizeToken(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.' || r == ':' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ClassifyError maps arbitrary Go errors to safe, stable error category codes.
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "TIMEOUT"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "context canceled"):
		return "CANCELED"
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "timeout"):
		return "TIMEOUT"
	case strings.Contains(msg, "connection refused"):
		return "CONN_REFUSED"
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "broken pipe"), strings.Contains(msg, "unexpected eof"), msg == "eof":
		return "CONN_RESET"
	case strings.Contains(msg, "no such host"):
		return "DNS_ERROR"
	case strings.Contains(msg, "x509"), strings.Contains(msg, "tls"):
		return "TLS_ERROR"
	case strings.Contains(msg, "401"), strings.Contains(msg, "403"), strings.Contains(msg, "auth"):
		return "AUTH_FAILED"
	case strings.Contains(msg, "404"), strings.Contains(msg, "not found"), strings.Contains(msg, "no such file"):
		return "NOT_FOUND"
	case strings.Contains(msg, "429"):
		return "RATE_LIMITED"
	case strings.Contains(msg, "range"):
		return "RANGE_ERROR"
	case strings.Contains(msg, "solid rar"):
		return "SOLID_RAR"
	case strings.Contains(msg, "encrypted"):
		return "ENCRYPTED_ARCHIVE"
	case strings.Contains(msg, "random access"):
		return "SAF_NON_SEEKABLE"
	case strings.Contains(msg, "zip"):
		return "ZIP_ERROR"
	case strings.Contains(msg, "rar"):
		return "RAR_ERROR"
	case strings.Contains(msg, "database is locked"), strings.Contains(msg, "sqlite"):
		return "DB_ERROR"
	default:
		return "ERROR"
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
