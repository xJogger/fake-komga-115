package database

import (
	"database/sql"
	"fmt"
)

func applyMigrations(db *sql.DB) error {
	if err := ensureProgressTables(db); err != nil {
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
