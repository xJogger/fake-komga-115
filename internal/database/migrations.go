package database

import (
	"database/sql"
	"fmt"
)

func applyMigrations(db *sql.DB) error {
	if err := ensureProgressTables(db); err != nil {
		return err
	}
	if err := ensureOperationStats(db); err != nil {
		return err
	}
	if err := ensureMaintenanceTables(db); err != nil {
		return err
	}
	migrations := []struct {
		table      string
		column     string
		definition string
	}{
		{"libraries", "one_shot", "INTEGER NOT NULL DEFAULT 0"},
		{"series", "one_shot", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, migration := range migrations {
		exists, err := columnExists(db, migration.table, migration.column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		query := fmt.Sprintf(
			"ALTER TABLE %s ADD COLUMN %s %s",
			migration.table, migration.column, migration.definition,
		)
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func ensureOperationStats(db *sql.DB) error {
	if err := addColumnIfMissing(db, "zip_indexes", "index_duration_ns",
		"INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "series_thumbnails", "generation_duration_ns",
		"INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS book_download_stats (
  book_id TEXT PRIMARY KEY,
  series_id TEXT NOT NULL,
  bytes INTEGER NOT NULL DEFAULT 0,
  duration_ns INTEGER NOT NULL DEFAULT 0,
  samples INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_book_download_stats_series ON book_download_stats(series_id);
`)
	return err
}

func ensureMaintenanceTables(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS maintenance_runs (
  id TEXT PRIMARY KEY,
  operation TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_name TEXT NOT NULL DEFAULT '',
  series_id TEXT,
  book_id TEXT,
  force INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  total_items INTEGER NOT NULL DEFAULT 0,
  processed_items INTEGER NOT NULL DEFAULT 0,
  generated_count INTEGER NOT NULL DEFAULT 0,
  skipped_count INTEGER NOT NULL DEFAULT 0,
  failed_count INTEGER NOT NULL DEFAULT 0,
  current_item TEXT NOT NULL DEFAULT '',
  errors_json TEXT NOT NULL DEFAULT '[]',
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  started_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE,
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_maintenance_runs_created ON maintenance_runs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_maintenance_runs_target ON maintenance_runs(target_type,target_id,created_at DESC);
`)
	return err
}

func ensureProgressTables(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS book_read_progress (
  book_id TEXT PRIMARY KEY,
  series_id TEXT NOT NULL,
  completed INTEGER NOT NULL DEFAULT 1,
  completed_at TEXT,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_book_read_progress_series ON book_read_progress(series_id, completed);

CREATE TABLE IF NOT EXISTS book_page_progress (
  book_id TEXT PRIMARY KEY,
  series_id TEXT NOT NULL,
  last_loaded_page INTEGER NOT NULL,
  max_loaded_page INTEGER NOT NULL,
  page_count INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_book_page_progress_series_updated ON book_page_progress(series_id, updated_at DESC);
`)
	return err
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func addColumnIfMissing(db *sql.DB, table, column, definition string) error {
	exists, err := columnExists(db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)
	_, err = db.Exec(query)
	return err
}
