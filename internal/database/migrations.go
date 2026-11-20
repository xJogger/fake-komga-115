package database

import (
	"database/sql"
	"fmt"
)

func applyMigrations(db *sql.DB) error {
	if err := ensureClientSettingsTable(db); err != nil {
		return err
	}
	if err := ensureAuthTables(db); err != nil {
		return err
	}
	if err := ensureStorageProviderTables(db); err != nil {
		return err
	}
	if err := ensureProgressTables(db); err != nil {
		return err
	}
	if err := ensureOperationStats(db); err != nil {
		return err
	}
	if err := ensureMaintenanceTables(db); err != nil {
		return err
	}
	if err := ensureMetadataAndScrapeTables(db); err != nil {
		return err
	}
	if err := ensureOrganizationTables(db); err != nil {
		return err
	}
	migrations := []struct {
		table      string
		column     string
		definition string
	}{
		{"libraries", "one_shot", "INTEGER NOT NULL DEFAULT 0"},
		{"libraries", "provider_type", "TEXT NOT NULL DEFAULT '115'"},
		{"libraries", "webdav_account_id", "TEXT NOT NULL DEFAULT ''"},
		{"libraries", "root_path", "TEXT NOT NULL DEFAULT ''"},
		{"series", "one_shot", "INTEGER NOT NULL DEFAULT 0"},
		{"series", "books_count", "INTEGER NOT NULL DEFAULT 0"},
		{"series", "latest_book_modified_at", "TEXT"},
		{"series", "latest_book_created_at", "TEXT"},
		{"series", "sort_modified_at", "TEXT NOT NULL DEFAULT ''"},
		{"series", "sort_created_at", "TEXT NOT NULL DEFAULT ''"},
		{"series_metadata", "is_nsfw", "INTEGER NOT NULL DEFAULT 0"},
		{"series_metadata", "bgm_rank", "INTEGER"},
		{"series_metadata", "nsfw_lock", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, migration := range migrations {
		if err := addColumnIfMissing(db, migration.table, migration.column, migration.definition); err != nil {
			return err
		}
	}
	if err := ensurePerformanceIndexes(db); err != nil {
		return err
	}
	if err := ensureSeriesRollups(db); err != nil {
		return err
	}
	return createShelfRevisions(db)
}

func ensureAuthTables(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS auth_admin (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  remember INTEGER NOT NULL DEFAULT 0,
  user_agent TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at);

CREATE TABLE IF NOT EXISTS auth_api_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  key_prefix TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);
`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
UPDATE auth_sessions SET remember=1,expires_at=? WHERE expires_at>? AND expires_at<>?`,
		PermanentSessionExpiresAt, nowText(), PermanentSessionExpiresAt)
	return err
}

func ensureStorageProviderTables(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS webdav_accounts (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  base_url TEXT NOT NULL,
  username TEXT NOT NULL DEFAULT '',
  password TEXT NOT NULL DEFAULT '',
  insecure_tls INTEGER NOT NULL DEFAULT 0,
  rate_per_second REAL NOT NULL DEFAULT 8,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`)
	return err
}

func ensurePerformanceIndexes(db *sql.DB) error {
	if exists, err := tableExists(db, "series"); err != nil {
		return err
	} else if exists {
		for _, stmt := range []string{
			`CREATE INDEX IF NOT EXISTS idx_series_lib_sort_mod ON series(library_id, sort_modified_at DESC, name COLLATE NOCASE, id)`,
			`CREATE INDEX IF NOT EXISTS idx_series_lib_sort_cre ON series(library_id, sort_created_at DESC, name COLLATE NOCASE, id)`,
			`CREATE INDEX IF NOT EXISTS idx_series_sort_mod ON series(sort_modified_at DESC, name COLLATE NOCASE, id, library_id)`,
			`CREATE INDEX IF NOT EXISTS idx_series_sort_cre ON series(sort_created_at DESC, name COLLATE NOCASE, id, library_id)`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	if exists, err := tableExists(db, "books"); err != nil {
		return err
	} else if exists {
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_books_library_size ON books(library_id, size)`); err != nil {
			return err
		}
	}
	if exists, err := tableExists(db, "book_read_progress"); err != nil {
		return err
	} else if exists {
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_book_read_progress_updated ON book_read_progress(completed, updated_at DESC)`); err != nil {
			return err
		}
		if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_book_read_progress_updated_all ON book_read_progress(updated_at DESC, book_id)`); err != nil {
			return err
		}
	}
	if exists, err := tableExists(db, "series_metadata"); err != nil {
		return err
	} else if exists {
		for _, stmt := range []string{
			`CREATE INDEX IF NOT EXISTS idx_series_metadata_nsfw ON series_metadata(is_nsfw)`,
			`CREATE INDEX IF NOT EXISTS idx_series_metadata_nsfw_series ON series_metadata(is_nsfw, series_id)`,
			`CREATE INDEX IF NOT EXISTS idx_series_metadata_fast ON series_metadata(series_id, is_nsfw, scrape_status, bgm_subject_id, bgm_rank, bgm_score)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_series_metadata_search ON series_metadata(series_id, title, title_sort, alternate_titles_json)`,
			`CREATE INDEX IF NOT EXISTS idx_series_metadata_bgm_rank ON series_metadata(bgm_rank ASC, series_id, is_nsfw) WHERE bgm_rank > 0`,
			`CREATE INDEX IF NOT EXISTS idx_series_metadata_bgm_sub ON series_metadata(bgm_subject_id, series_id, bgm_rank, bgm_score) WHERE bgm_subject_id > 0`,
			`CREATE INDEX IF NOT EXISTS idx_series_metadata_pub_series ON series_metadata(publisher COLLATE NOCASE, series_id)`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureOrganizationTables(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS book_read_progress_tombstones (
  book_id TEXT PRIMARY KEY,
  series_id TEXT NOT NULL,
  deleted_at TEXT NOT NULL,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_progress_tombstones_series ON book_read_progress_tombstones(series_id);
CREATE INDEX IF NOT EXISTS idx_progress_tombstones_deleted ON book_read_progress_tombstones(deleted_at);

CREATE TABLE IF NOT EXISTS collections (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  ordered INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS collection_series (
  collection_id TEXT NOT NULL,
  series_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY(collection_id, series_id),
  FOREIGN KEY(collection_id) REFERENCES collections(id) ON DELETE CASCADE,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_collection_series_pos ON collection_series(collection_id, position);
CREATE INDEX IF NOT EXISTS idx_collection_series_series ON collection_series(series_id, collection_id);

CREATE TABLE IF NOT EXISTS readlists (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  summary TEXT NOT NULL DEFAULT '',
  ordered INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS readlist_books (
  readlist_id TEXT NOT NULL,
  book_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  PRIMARY KEY(readlist_id, book_id),
  FOREIGN KEY(readlist_id) REFERENCES readlists(id) ON DELETE CASCADE,
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_readlist_books_pos ON readlist_books(readlist_id, position);
CREATE INDEX IF NOT EXISTS idx_readlist_books_book ON readlist_books(book_id, readlist_id);
`)
	return err
}

func ensureSeriesRollups(db *sql.DB) error {
	triggers := []string{
		`CREATE TRIGGER IF NOT EXISTS fk115_series_rollup_ins
AFTER INSERT ON series
WHEN NEW.sort_modified_at='' OR NEW.sort_created_at=''
BEGIN
  UPDATE series
  SET sort_modified_at=COALESCE(NULLIF(latest_book_modified_at,''), NULLIF(file_modified_at,''), updated_at),
      sort_created_at=COALESCE(NULLIF(latest_book_created_at,''), created_at)
  WHERE id=NEW.id;
END`,
		`CREATE TRIGGER IF NOT EXISTS fk115_series_rollup_upd
AFTER UPDATE OF file_modified_at, updated_at, created_at ON series
BEGIN
  UPDATE series
  SET sort_modified_at=COALESCE(NULLIF(latest_book_modified_at,''), NULLIF(NEW.file_modified_at,''), NEW.updated_at),
      sort_created_at=COALESCE(NULLIF(latest_book_created_at,''), NEW.created_at)
  WHERE id=NEW.id;
END`,
		`CREATE TRIGGER IF NOT EXISTS fk115_books_rollup_ins
AFTER INSERT ON books
BEGIN
  UPDATE series
  SET books_count=books_count+1,
      latest_book_modified_at=CASE
        WHEN NEW.file_modified_at IS NULL OR NEW.file_modified_at='' THEN latest_book_modified_at
        WHEN latest_book_modified_at IS NULL OR latest_book_modified_at='' OR NEW.file_modified_at>latest_book_modified_at THEN NEW.file_modified_at
        ELSE latest_book_modified_at
      END,
      latest_book_created_at=CASE
        WHEN NEW.file_created_at IS NULL OR NEW.file_created_at='' THEN latest_book_created_at
        WHEN latest_book_created_at IS NULL OR latest_book_created_at='' OR NEW.file_created_at>latest_book_created_at THEN NEW.file_created_at
        ELSE latest_book_created_at
      END,
      sort_modified_at=COALESCE(
        NULLIF(CASE
          WHEN NEW.file_modified_at IS NULL OR NEW.file_modified_at='' THEN latest_book_modified_at
          WHEN latest_book_modified_at IS NULL OR latest_book_modified_at='' OR NEW.file_modified_at>latest_book_modified_at THEN NEW.file_modified_at
          ELSE latest_book_modified_at
        END,''),
        NULLIF(file_modified_at,''),
        updated_at
      ),
      sort_created_at=COALESCE(
        NULLIF(CASE
          WHEN NEW.file_created_at IS NULL OR NEW.file_created_at='' THEN latest_book_created_at
          WHEN latest_book_created_at IS NULL OR latest_book_created_at='' OR NEW.file_created_at>latest_book_created_at THEN NEW.file_created_at
          ELSE latest_book_created_at
        END,''),
        created_at
      )
  WHERE id=NEW.series_id;
END`,
		`CREATE TRIGGER IF NOT EXISTS fk115_books_rollup_del
AFTER DELETE ON books
BEGIN
  UPDATE series
  SET books_count=(SELECT count(*) FROM books b WHERE b.series_id=OLD.series_id),
      latest_book_modified_at=(SELECT MAX(NULLIF(b.file_modified_at,'')) FROM books b WHERE b.series_id=OLD.series_id),
      latest_book_created_at=(SELECT MAX(NULLIF(b.file_created_at,'')) FROM books b WHERE b.series_id=OLD.series_id),
      sort_modified_at=COALESCE((SELECT MAX(NULLIF(b.file_modified_at,'')) FROM books b WHERE b.series_id=OLD.series_id), NULLIF(file_modified_at,''), updated_at),
      sort_created_at=COALESCE((SELECT MAX(NULLIF(b.file_created_at,'')) FROM books b WHERE b.series_id=OLD.series_id), created_at)
  WHERE id=OLD.series_id;
END`,
		`CREATE TRIGGER IF NOT EXISTS fk115_books_rollup_upd
AFTER UPDATE OF series_id, file_modified_at, file_created_at ON books
BEGIN
  UPDATE series
  SET books_count=(SELECT count(*) FROM books b WHERE b.series_id=NEW.series_id),
      latest_book_modified_at=(SELECT MAX(NULLIF(b.file_modified_at,'')) FROM books b WHERE b.series_id=NEW.series_id),
      latest_book_created_at=(SELECT MAX(NULLIF(b.file_created_at,'')) FROM books b WHERE b.series_id=NEW.series_id),
      sort_modified_at=COALESCE((SELECT MAX(NULLIF(b.file_modified_at,'')) FROM books b WHERE b.series_id=NEW.series_id), NULLIF(file_modified_at,''), updated_at),
      sort_created_at=COALESCE((SELECT MAX(NULLIF(b.file_created_at,'')) FROM books b WHERE b.series_id=NEW.series_id), created_at)
  WHERE id=NEW.series_id;
  UPDATE series
  SET books_count=(SELECT count(*) FROM books b WHERE b.series_id=OLD.series_id),
      latest_book_modified_at=(SELECT MAX(NULLIF(b.file_modified_at,'')) FROM books b WHERE b.series_id=OLD.series_id),
      latest_book_created_at=(SELECT MAX(NULLIF(b.file_created_at,'')) FROM books b WHERE b.series_id=OLD.series_id),
      sort_modified_at=COALESCE((SELECT MAX(NULLIF(b.file_modified_at,'')) FROM books b WHERE b.series_id=OLD.series_id), NULLIF(file_modified_at,''), updated_at),
      sort_created_at=COALESCE((SELECT MAX(NULLIF(b.file_created_at,'')) FROM books b WHERE b.series_id=OLD.series_id), created_at)
  WHERE OLD.series_id<>NEW.series_id AND id=OLD.series_id;
END`,
	}
	for _, stmt := range triggers {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	var done string
	err := db.QueryRow(`SELECT value FROM settings WHERE key='series_rollups_v1'`).Scan(&done)
	if err == nil && done == "1" {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS temp._fk115_series_agg`,
		`CREATE TEMP TABLE _fk115_series_agg (
  series_id TEXT PRIMARY KEY,
  cnt INTEGER NOT NULL,
  max_mod TEXT,
  max_cre TEXT
)`,
		`INSERT INTO _fk115_series_agg(series_id, cnt, max_mod, max_cre)
SELECT series_id, count(*), MAX(NULLIF(file_modified_at,'')), MAX(NULLIF(file_created_at,''))
FROM books
GROUP BY series_id`,
		`UPDATE series
SET books_count=0,
    latest_book_modified_at=NULL,
    latest_book_created_at=NULL,
    sort_modified_at=COALESCE(NULLIF(file_modified_at,''), updated_at),
    sort_created_at=created_at`,
		`UPDATE series
SET books_count=a.cnt,
    latest_book_modified_at=a.max_mod,
    latest_book_created_at=a.max_cre,
    sort_modified_at=COALESCE(a.max_mod, NULLIF(series.file_modified_at,''), series.updated_at),
    sort_created_at=COALESCE(a.max_cre, series.created_at)
FROM _fk115_series_agg a
WHERE a.series_id=series.id`,
		`DROP TABLE IF EXISTS temp._fk115_series_agg`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
INSERT INTO settings(key,value,updated_at) VALUES('series_rollups_v1','1',?)
ON CONFLICT(key) DO UPDATE SET value='1',updated_at=excluded.updated_at`, nowText()); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureMetadataAndScrapeTables(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS series_metadata (
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
  is_nsfw INTEGER NOT NULL DEFAULT 0,
  nsfw_lock INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_series_metadata_title ON series_metadata(title COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_series_metadata_publisher ON series_metadata(publisher COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS idx_series_metadata_scrape_status ON series_metadata(scrape_status);
CREATE INDEX IF NOT EXISTS idx_series_metadata_bgm_id ON series_metadata(bgm_subject_id);

CREATE TABLE IF NOT EXISTS series_genres (
  series_id TEXT NOT NULL,
  genre TEXT NOT NULL,
  PRIMARY KEY(series_id, genre),
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_series_genres_genre ON series_genres(genre COLLATE NOCASE, series_id);

CREATE TABLE IF NOT EXISTS series_tags (
  series_id TEXT NOT NULL,
  tag TEXT NOT NULL,
  PRIMARY KEY(series_id, tag),
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_series_tags_tag ON series_tags(tag COLLATE NOCASE, series_id);

CREATE TABLE IF NOT EXISTS series_authors (
  series_id TEXT NOT NULL,
  name TEXT NOT NULL,
  role TEXT NOT NULL,
  PRIMARY KEY(series_id, name, role),
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_series_authors_name ON series_authors(name COLLATE NOCASE, role, series_id);

CREATE TABLE IF NOT EXISTS bangumi_scrape_runs (
  id TEXT PRIMARY KEY,
  library_id TEXT,
  mode TEXT NOT NULL,
  force INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  total_series INTEGER NOT NULL DEFAULT 0,
  processed_series INTEGER NOT NULL DEFAULT 0,
  matched_count INTEGER NOT NULL DEFAULT 0,
  unmatched_count INTEGER NOT NULL DEFAULT 0,
  skipped_count INTEGER NOT NULL DEFAULT 0,
  failed_count INTEGER NOT NULL DEFAULT 0,
  current_series TEXT NOT NULL DEFAULT '',
  errors_json TEXT NOT NULL DEFAULT '[]',
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  started_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_bangumi_scrape_runs_created ON bangumi_scrape_runs(created_at DESC);
`)
	return err
}

func ensureClientSettingsTable(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS client_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  allow_unauthenticated INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);
`)
	return err
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
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS book_read_progress (
  book_id TEXT PRIMARY KEY,
  series_id TEXT NOT NULL,
  completed INTEGER NOT NULL DEFAULT 1,
  page INTEGER,
  read_date TEXT,
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
`); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "book_read_progress", "page", "INTEGER"); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "book_read_progress", "read_date", "TEXT"); err != nil {
		return err
	}
	return nil
}

func tableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
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
