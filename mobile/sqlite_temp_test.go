//go:build linux

package mobile

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteCanSpillToAppCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app's cache")
	if err := prepareSQLiteTemp(dir); err != nil {
		t.Fatal(err)
	}
	if err := prepareSQLiteTemp(dir); err != nil {
		t.Fatalf("restart should reuse the same directory: %v", err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `
PRAGMA temp_store=FILE;
CREATE TEMP TABLE spill(data BLOB);
PRAGMA temp.cache_size=1;
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<512)
INSERT INTO spill SELECT randomblob(8192) FROM n;`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM spill").Scan(&count); err != nil || count != 512 {
		t.Fatalf("unexpected spilled row count %d: %v", count, err)
	}

	// SQLite unlinks its temporary files right after opening them. Inspect
	// the still-open descriptor rather than assuming a directory entry exists.
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot inspect Linux file descriptors: %v", err)
	}
	for _, fd := range fds {
		path, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err == nil && strings.HasPrefix(path, filepath.Join(dir, "etilqs_")) {
			return
		}
	}
	t.Fatal("SQLite did not use the app cache for its disk-backed temporary table")
}
