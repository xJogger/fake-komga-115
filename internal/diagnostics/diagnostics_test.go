package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeTextRedactsSensitiveValues(t *testing.T) {
	input := `GET http://user:pass@192.168.1.50:19798/dav/secret/book.cbz failed: Bearer abcdef1234567890 cookie=fk115_session=xyz123 path=/home/alice/private/manga.zip win=C:\Users\alice\comic.cbz`
	got := SanitizeText(input)
	for _, leaked := range []string{"user:pass", "192.168.1.50", "/dav/secret", "abcdef1234567890", "xyz123", "/home/alice", `C:\Users\alice`} {
		if strings.Contains(got, leaked) {
			t.Fatalf("sanitized text leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "[redacted-url]") || !strings.Contains(got, "[redacted-path]") {
		t.Fatalf("expected redaction markers in %q", got)
	}
	// Loopback stays readable for local diagnostics.
	if out := SanitizeText("listening on 127.0.0.1:25600"); !strings.Contains(out, "127.0.0.1:25600") {
		t.Fatalf("expected loopback address preserved, got %q", out)
	}
}

func TestRecorderConciseVerboseRotationAndExport(t *testing.T) {
	dir := t.TempDir()
	rec, err := New(dir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec.maxFileBytes = 512

	// Concise mode drops verbose/debug and non-lifecycle info events.
	rec.Record(Event{Level: LevelInfo, Component: "archive", Stage: "page_read", Detail: "dropped in concise"})
	rec.RecordVerbose(Event{Level: LevelDebug, Component: "webdav", Stage: "range", Detail: "dropped verbose"})
	rec.Record(Event{
		Level:     LevelError,
		Component: "webdav",
		Stage:     "range",
		Code:      "TIMEOUT",
		Detail:    "request to https://user:secret@nas.local/dav/file.cbz timed out",
		Meta: map[string]any{
			"provider":   "webdav",
			"secretKey":  "MUST_NOT_APPEAR",
			"httpStatus": 504,
		},
	})

	events := rec.Recent(50)
	if len(events) != 1 {
		t.Fatalf("expected 1 concise event, got %d: %+v", len(events), events)
	}
	if _, leaked := events[0].Meta["secretKey"]; leaked {
		t.Fatalf("unlisted meta key should be stripped: %+v", events[0].Meta)
	}
	if strings.Contains(events[0].Detail, "secret") || strings.Contains(events[0].Detail, "nas.local") {
		t.Fatalf("detail leaked url/secret: %s", events[0].Detail)
	}

	// Enable verbose mode and force rotation.
	rec.SetVerbose(true)
	for i := 0; i < 25; i++ {
		rec.RecordVerbose(Event{
			Level:      LevelDebug,
			Component:  "archive",
			Stage:      "page_read",
			DurationMs: int64(i + 1),
			Meta:       map[string]any{"pageNumber": i + 1, "format": "zip"},
		})
	}
	if _, err := os.Stat(rec.backupPath); err != nil {
		t.Fatalf("expected backup rotation file to exist: %v", err)
	}

	// Write a simulated Android native log file and verify ExportText merges & sanitizes it.
	androidLog := filepath.Join(rec.Dir(), androidEventsFileName)
	if err := os.WriteFile(androidLog, []byte(`{"stage":"android_saf","detail":"failed content://com.android.externalstorage/tree/123"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write android log: %v", err)
	}
	exported := rec.ExportText(100)
	if !strings.Contains(exported, "## Core Events") || !strings.Contains(exported, "## Android Native Events") {
		t.Fatalf("export missing sections:\n%s", exported)
	}
	if strings.Contains(exported, "com.android.externalstorage") {
		t.Fatalf("android URI leaked in export:\n%s", exported)
	}

	if err := rec.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got := rec.Recent(50); len(got) != 0 {
		t.Fatalf("expected 0 events after Clear, got %d", len(got))
	}
}

func TestWriteStartupFailure(t *testing.T) {
	dir := t.TempDir()
	WriteStartupFailure(dir, "server", "startup", errors.New("listen tcp 0.0.0.0:25600: bind: address already in use"))
	rec, err := New(dir, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	events := rec.Recent(10)
	if len(events) != 1 || events[0].Stage != "startup" || events[0].Level != LevelError {
		t.Fatalf("unexpected startup failure event: %+v", events)
	}
}
