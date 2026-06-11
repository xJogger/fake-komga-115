package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenMigratesOneShotColumns(t *testing.T) {
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
		{"series", "one_shot"},
	} {
		exists, err := columnExists(store.DB(), item.table, item.column)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s.%s was not migrated", item.table, item.column)
		}
	}
}
