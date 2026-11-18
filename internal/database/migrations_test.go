package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenMigratesLegacyToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE libraries (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 root_cid TEXT NOT NULL UNIQUE,
 enabled INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 last_scan_started_at TEXT,
 last_scan_completed_at TEXT,
 last_scan_status TEXT NOT NULL DEFAULT 'never',
 last_scan_error TEXT NOT NULL DEFAULT ''
);
INSERT INTO libraries(id,name,root_cid,enabled,created_at,updated_at)
VALUES('l_MTIzNDU','Legacy115','12345',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
CREATE TABLE series (
 id TEXT PRIMARY KEY,
 library_id TEXT NOT NULL,
 cid TEXT NOT NULL,
 name TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 file_modified_at TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 seen_scan_id TEXT NOT NULL,
 UNIQUE(library_id, cid)
);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, item := range []struct {
		table  string
		column string
	}{
		{"libraries", "one_shot"},
		{"libraries", "provider_type"},
		{"libraries", "webdav_account_id"},
		{"libraries", "root_path"},
		{"series", "one_shot"},
		{"auth_admin", "password_hash"},
		{"auth_sessions", "token_hash"},
		{"auth_api_keys", "key_hash"},
		{"webdav_accounts", "base_url"},
		{"series_metadata", "bgm_subject_id"},
		{"series_genres", "genre"},
		{"series_tags", "tag"},
		{"series_authors", "name"},
		{"bangumi_scrape_runs", "matched_count"},
		{"book_read_progress", "book_id"},
		{"book_read_progress", "page"},
		{"book_read_progress", "read_date"},
		{"book_page_progress", "last_loaded_page"},
		{"client_settings", "allow_unauthenticated"},
		{"zip_indexes", "index_duration_ns"},
		{"series_thumbnails", "generation_duration_ns"},
		{"book_download_stats", "duration_ns"},
		{"maintenance_runs", "operation"},
	} {
		exists, err := columnExists(store.DB(), item.table, item.column)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s.%s was not migrated", item.table, item.column)
		}
	}

	lib, err := store.Library(t.Context(), "l_MTIzNDU")
	if err != nil {
		t.Fatalf("read migrated legacy library: %v", err)
	}
	if lib.ProviderType != Provider115 || lib.RootCID != "12345" {
		t.Fatalf("unexpected migrated library: %+v", lib)
	}
}
