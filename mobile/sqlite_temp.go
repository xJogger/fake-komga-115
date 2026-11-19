//go:build android || linux

package mobile

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

var sqliteTemp struct {
	sync.Mutex
	dir string
}

// prepareSQLiteTemp runs before the embedded app opens any databases. The
// bundled SQLite Unix VFS snapshots its environment, so changing only TMPDIR
// at gomobile startup does not reliably affect it. Set the SQLite directory
// once in this dedicated process; don't change the process-global PRAGMA
// while any database work may be in flight.
func prepareSQLiteTemp(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("SQLite temporary directory must be an absolute path")
	}
	dir = filepath.Clean(dir)

	sqliteTemp.Lock()
	defer sqliteTemp.Unlock()
	if sqliteTemp.dir != "" && sqliteTemp.dir != dir {
		return errors.New("SQLite temporary directory is already configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create SQLite temporary directory: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".sqlite-probe-*")
	if err != nil {
		return fmt.Errorf("open a file in the SQLite temporary directory: %w", err)
	}
	closeErr := probe.Close()
	if err := os.Remove(probe.Name()); err != nil || closeErr != nil {
		return fmt.Errorf("clean up SQLite temporary directory probe: %w", errors.Join(closeErr, err))
	}
	if sqliteTemp.dir == dir {
		return nil
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer db.Close()
	quoted := "'" + strings.ReplaceAll(dir, "'", "''") + "'"
	if _, err := db.Exec("PRAGMA temp_store_directory = " + quoted); err != nil {
		return fmt.Errorf("configure SQLite temporary directory: %w", err)
	}
	var active string
	if err := db.QueryRow("PRAGMA temp_store_directory").Scan(&active); err != nil || active != dir {
		return errors.New("SQLite did not accept its temporary directory")
	}
	sqliteTemp.dir = dir
	return nil
}
