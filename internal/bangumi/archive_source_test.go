package bangumi

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func TestResolveArchiveSource(t *testing.T) {
	cfg, err := ResolveArchiveSource("", "")
	if err != nil || cfg.Source != ArchiveSourceZH || cfg.MetaURL != ChineseArchiveMetaURL {
		t.Fatalf("default source mismatch: cfg=%+v err=%v", cfg, err)
	}

	cfg, err = ResolveArchiveSource("official", "")
	if err != nil || cfg.Source != ArchiveSourceOfficial || cfg.MetaURL != OfficialArchiveMetaURL {
		t.Fatalf("official source mismatch: cfg=%+v err=%v", cfg, err)
	}

	cfg, err = ResolveArchiveSource("custom", "https://example.com/aux/latest.json")
	if err != nil || cfg.Source != ArchiveSourceCustom || cfg.MetaURL != "https://example.com/aux/latest.json" {
		t.Fatalf("custom source mismatch: cfg=%+v err=%v", cfg, err)
	}

	if _, err := ResolveArchiveSource("custom", "ftp://bad.example/latest.json"); err == nil {
		t.Fatal("expected error for non-http custom url")
	}
	if _, err := ResolveArchiveSource("unknown_src", ""); err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestServiceArchiveSourcePersistence(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := database.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	svc, err := NewService(store, filepath.Join(dir, "bgm"), logger)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()

	if got := svc.ArchiveSource(ctx); got.Source != ArchiveSourceZH || got.MetaURL != ChineseArchiveMetaURL {
		t.Fatalf("expected default zh_cn source, got %+v", got)
	}

	saved, err := svc.SetArchiveSource(ctx, ArchiveSourceOfficial, "")
	if err != nil {
		t.Fatalf("SetArchiveSource official: %v", err)
	}
	if saved.Source != ArchiveSourceOfficial || svc.archive.MetaURL() != OfficialArchiveMetaURL {
		t.Fatalf("unexpected official saved=%+v metaURL=%q", saved, svc.archive.MetaURL())
	}

	customURL := "https://mirror.example.org/bgm/aux/latest.json"
	saved, err = svc.SetArchiveSource(ctx, ArchiveSourceCustom, customURL)
	if err != nil {
		t.Fatalf("SetArchiveSource custom: %v", err)
	}
	if saved.Source != ArchiveSourceCustom || svc.archive.MetaURL() != customURL {
		t.Fatalf("unexpected custom saved=%+v metaURL=%q", saved, svc.archive.MetaURL())
	}
	svc.Close()

	// Re-open service and confirm custom source persisted across restart.
	svc2, err := NewService(store, filepath.Join(dir, "bgm"), logger)
	if err != nil {
		t.Fatalf("NewService reopen: %v", err)
	}
	defer svc2.Close()
	if got := svc2.ArchiveSource(ctx); got.Source != ArchiveSourceCustom || got.MetaURL != customURL || svc2.archive.MetaURL() != customURL {
		t.Fatalf("custom source did not persist across restart: got=%+v metaURL=%q", got, svc2.archive.MetaURL())
	}
}
