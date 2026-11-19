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

CREATE TABLE IF NOT EXISTS client_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  allow_unauthenticated INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

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

CREATE TABLE IF NOT EXISTS provider_accounts (
  provider TEXT PRIMARY KEY,
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

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

CREATE TABLE IF NOT EXISTS libraries (
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
CREATE INDEX IF NOT EXISTS idx_books_series_modified ON books(series_id, file_modified_at);
CREATE INDEX IF NOT EXISTS idx_books_library_name ON books(library_id, name);
CREATE INDEX IF NOT EXISTS idx_books_library_size ON books(library_id, size);
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
  bgm_rank INTEGER,
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

CREATE TABLE IF NOT EXISTS zip_indexes (
  book_id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  page_count INTEGER NOT NULL,
  index_json TEXT NOT NULL,
  index_duration_ns INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(book_id) REFERENCES books(id) ON DELETE CASCADE
);

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
CREATE INDEX IF NOT EXISTS idx_book_read_progress_updated ON book_read_progress(completed, updated_at DESC);

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

CREATE TABLE IF NOT EXISTS series_thumbnails (
  series_id TEXT PRIMARY KEY,
  source_book_id TEXT NOT NULL,
  source_version TEXT NOT NULL,
  path TEXT NOT NULL,
  media_type TEXT NOT NULL,
  width INTEGER NOT NULL,
  height INTEGER NOT NULL,
  size INTEGER NOT NULL,
  generation_duration_ns INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(series_id) REFERENCES series(id) ON DELETE CASCADE,
  FOREIGN KEY(source_book_id) REFERENCES books(id) ON DELETE CASCADE
);

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

CREATE TABLE IF NOT EXISTS thumbnail_runs (
  id TEXT PRIMARY KEY,
  library_id TEXT NOT NULL,
  mode TEXT NOT NULL,
  requested_limit INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  total_series INTEGER NOT NULL DEFAULT 0,
  processed_series INTEGER NOT NULL DEFAULT 0,
  generated_count INTEGER NOT NULL DEFAULT 0,
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
CREATE INDEX IF NOT EXISTS idx_thumbnail_runs_created ON thumbnail_runs(created_at DESC);

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
