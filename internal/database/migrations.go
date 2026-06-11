package database

import (
	"database/sql"
	"fmt"
)

func applyMigrations(db *sql.DB) error {
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
