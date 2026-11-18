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
		{"series_metadata", "is_nsfw"},
		{"series_metadata", "nsfw_lock"},
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

func TestOpenMigratesEarlyV2SeriesMetadataWithoutNSFWColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "early-v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE libraries (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 provider_type TEXT NOT NULL DEFAULT '115',
 root_cid TEXT NOT NULL UNIQUE,
 webdav_account_id TEXT NOT NULL DEFAULT '',
 root_path TEXT NOT NULL DEFAULT '',
 enabled INTEGER NOT NULL DEFAULT 1,
 one_shot INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 last_scan_started_at TEXT,
 last_scan_completed_at TEXT,
 last_scan_status TEXT NOT NULL DEFAULT 'never',
 last_scan_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE series (
 id TEXT PRIMARY KEY,
 library_id TEXT NOT NULL,
 cid TEXT NOT NULL,
 name TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 one_shot INTEGER NOT NULL DEFAULT 0,
 file_modified_at TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 seen_scan_id TEXT NOT NULL,
 UNIQUE(library_id, cid)
);
CREATE TABLE series_metadata (
 series_id TEXT PRIMARY KEY,
 bgm_subject_id INTEGER,
 scrape_status TEXT NOT NULL DEFAULT 'none',
 scrape_confidence REAL NOT NULL DEFAULT 0,
 scrape_synced_at TEXT,
 cover_source TEXT NOT NULL DEFAULT 'auto',
 bgm_cover_url TEXT NOT NULL DEFAULT '',
 bgm_cover_path TEXT NOT NULL DEFAULT '',
 bgm_score REAL,
 status TEXT NOT NULL DEFAULT 'ONGOING',
 status_lock INTEGER NOT NULL DEFAULT 0,
 title TEXT NOT NULL DEFAULT '',
 title_lock INTEGER NOT NULL DEFAULT 0,
 title_sort TEXT NOT NULL DEFAULT '',
 title_sort_lock INTEGER NOT NULL DEFAULT 0,
 summary TEXT NOT NULL DEFAULT '',
 summary_lock INTEGER NOT NULL DEFAULT 0,
 reading_direction TEXT NOT NULL DEFAULT 'LEFT_TO_RIGHT',
 reading_direction_lock INTEGER NOT NULL DEFAULT 0,
 publisher TEXT NOT NULL DEFAULT '',
 publisher_lock INTEGER NOT NULL DEFAULT 0,
 age_rating INTEGER,
 age_rating_lock INTEGER NOT NULL DEFAULT 0,
 language TEXT NOT NULL DEFAULT '',
 language_lock INTEGER NOT NULL DEFAULT 0,
 total_book_count INTEGER,
 total_book_count_lock INTEGER NOT NULL DEFAULT 0,
 release_date TEXT NOT NULL DEFAULT '',
 release_date_lock INTEGER NOT NULL DEFAULT 0,
 genres_json TEXT NOT NULL DEFAULT '[]',
 genres_lock INTEGER NOT NULL DEFAULT 0,
 tags_json TEXT NOT NULL DEFAULT '[]',
 tags_lock INTEGER NOT NULL DEFAULT 0,
 authors_json TEXT NOT NULL DEFAULT '[]',
 authors_lock INTEGER NOT NULL DEFAULT 0,
 alternate_titles_json TEXT NOT NULL DEFAULT '[]',
 alternate_titles_lock INTEGER NOT NULL DEFAULT 0,
 links_json TEXT NOT NULL DEFAULT '[]',
 links_lock INTEGER NOT NULL DEFAULT 0,
 sharing_labels_json TEXT NOT NULL DEFAULT '[]',
 sharing_labels_lock INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL
);`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(early-v2.db) failed: %v", err)
	}
	defer store.Close()

	for _, col := range []string{"is_nsfw", "nsfw_lock"} {
		ok, err := columnExists(store.DB(), "series_metadata", col)
		if err != nil || !ok {
			t.Fatalf("expected series_metadata.%s after migration, ok=%v err=%v", col, ok, err)
		}
	}
}
