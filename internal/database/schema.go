package database

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 10000;

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS provider_accounts (
  provider TEXT PRIMARY KEY,
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS libraries (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  root_cid TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  one_shot INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_scan_started_at TEXT,
  last_scan_completed_at TEXT,
  last_scan_status TEXT NOT NULL DEFAULT 'never',
  last_scan_error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS scan_runs (
  id TEXT PRIMARY KEY,
  library_id TEXT,
  status TEXT NOT NULL,
  trigger_type TEXT NOT NULL,
  started_at TEXT,
  completed_at TEXT,
  directories_seen INTEGER NOT NULL DEFAULT 0,
  files_seen INTEGER NOT NULL DEFAULT 0,
  series_seen INTEGER NOT NULL DEFAULT 0,
  books_seen INTEGER NOT NULL DEFAULT 0,
  current_path TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_scan_runs_created ON scan_runs(created_at DESC);

CREATE TABLE IF NOT EXISTS series (
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
  UNIQUE(library_id, cid),
  FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_series_library_name ON series(library_id, name);
CREATE INDEX IF NOT EXISTS idx_series_seen_scan ON series(library_id, seen_scan_id);

CREATE TABLE IF NOT EXISTS books (
  id TEXT PRIMARY KEY,
  series_id TEXT NOT NULL,
  library_id TEXT NOT NULL,
  file_id TEXT NOT NULL,
  parent_cid TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL,
  pick_code TEXT NOT NULL,
  sha1 TEXT NOT NULL DEFAULT '',
  file_created_at TEXT,
  file_modified_at TEXT,
  number_sort REAL NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  seen_scan_id TEXT NOT NULL,
  UNIQUE(library_id, file_id),
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE,
  FOREIGN KEY(library_id) REFERENCES libraries(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_books_series_number ON books(series_id, number_sort);
CREATE INDEX IF NOT EXISTS idx_books_library_name ON books(library_id, name);
CREATE INDEX IF NOT EXISTS idx_books_seen_scan ON books(library_id, seen_scan_id);

CREATE TABLE IF NOT EXISTS scan_series_staging (
  run_id TEXT NOT NULL,
  id TEXT NOT NULL,
  library_id TEXT NOT NULL,
  cid TEXT NOT NULL,
  name TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  one_shot INTEGER NOT NULL DEFAULT 0,
  file_modified_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(run_id, id),
  UNIQUE(run_id, library_id, cid),
  FOREIGN KEY(run_id) REFERENCES scan_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS scan_books_staging (
  run_id TEXT NOT NULL,
  id TEXT NOT NULL,
  series_id TEXT NOT NULL,
  library_id TEXT NOT NULL,
  file_id TEXT NOT NULL,
  parent_cid TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL,
  pick_code TEXT NOT NULL,
  sha1 TEXT NOT NULL DEFAULT '',
  file_created_at TEXT,
  file_modified_at TEXT,
  number_sort REAL NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(run_id, id),
  UNIQUE(run_id, library_id, file_id),
  FOREIGN KEY(run_id) REFERENCES scan_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS zip_indexes (
  book_id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  page_count INTEGER NOT NULL,
  index_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS series_thumbnails (
  series_id TEXT PRIMARY KEY,
  source_book_id TEXT NOT NULL,
  source_version TEXT NOT NULL,
  path TEXT NOT NULL,
  media_type TEXT NOT NULL,
  width INTEGER NOT NULL,
  height INTEGER NOT NULL,
  size INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE,
  FOREIGN KEY(source_book_id) REFERENCES books(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS downurl_cache (
  cache_key TEXT PRIMARY KEY,
  pick_code TEXT NOT NULL,
  ua_hash TEXT NOT NULL,
  url TEXT NOT NULL,
  user_agent TEXT NOT NULL,
  expire_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_downurl_expire ON downurl_cache(expire_at);

CREATE TABLE IF NOT EXISTS cache_entries (
  cache_key TEXT PRIMARY KEY,
  cache_type TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  last_access_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cache_entries_type_access ON cache_entries(cache_type, last_access_at);
`
