package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.ensureDefaults(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

func (s *Store) ensureDefaults(ctx context.Context) error {
	defaults := map[string]string{
		"auto_scan_enabled":          "false",
		"auto_scan_interval_minutes": "360",
		"scan_on_startup":            "false",
		"api_rate_per_second":        "1",
		"range_block_size":           "1048576",
		"rar_index_block_size":       "65536",
		"rar_max_dictionary_size":    "104857600",
		"range_cache_max_bytes":      "10737418240",
		"page_cache_max_bytes":       "5368709120",
		"page_prefetch_count":        "2",
		"max_page_size":              "104857600",
		"downurl_ttl_seconds":        "1200",
	}
	now := nowText()
	for key, value := range defaults {
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO settings(key,value,updated_at) VALUES(?,?,?)`,
			key, value, now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&value)
	return value, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings(key,value,updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`,
		key, value, nowText())
	return err
}

func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func (s *Store) BoolSetting(ctx context.Context, key string, fallback bool) bool {
	value, err := s.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (s *Store) Int64Setting(ctx context.Context, key string, fallback int64) int64 {
	value, err := s.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func (s *Store) Float64Setting(ctx context.Context, key string, fallback float64) float64 {
	value, err := s.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func (s *Store) Account(ctx context.Context) (Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx, `
SELECT access_token,refresh_token,updated_at FROM provider_accounts WHERE provider='115'`,
	).Scan(&a.AccessToken, &a.RefreshToken, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, nil
	}
	return a, err
}

func (s *Store) SaveAccount(ctx context.Context, accessToken, refreshToken string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO provider_accounts(provider,access_token,refresh_token,updated_at)
VALUES('115',?,?,?)
ON CONFLICT(provider) DO UPDATE SET
 access_token=excluded.access_token,
 refresh_token=excluded.refresh_token,
 updated_at=excluded.updated_at`,
		accessToken, refreshToken, nowText())
	return err
}

func (s *Store) Libraries(ctx context.Context, enabledOnly bool) ([]Library, error) {
	query := `
SELECT l.id,l.name,l.root_cid,l.enabled,l.one_shot,l.created_at,l.updated_at,
 l.last_scan_started_at,l.last_scan_completed_at,l.last_scan_status,l.last_scan_error,
 (SELECT count(*) FROM series s WHERE s.library_id=l.id),
 (SELECT count(*) FROM books b WHERE b.library_id=l.id),
 (SELECT coalesce(sum(b.size),0) FROM books b WHERE b.library_id=l.id)
FROM libraries l`
	if enabledOnly {
		query += ` WHERE l.enabled=1`
	}
	query += ` ORDER BY l.name COLLATE NOCASE,l.id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var item Library
		if err := rows.Scan(
			&item.ID, &item.Name, &item.RootCID, &item.Enabled, &item.OneShot, &item.CreatedAt, &item.UpdatedAt,
			&item.LastScanStartedAt, &item.LastScanCompletedAt, &item.LastScanStatus, &item.LastScanError,
			&item.SeriesCount, &item.BookCount, &item.ComicBytes,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Library(ctx context.Context, id string) (Library, error) {
	var item Library
	err := s.db.QueryRowContext(ctx, `
SELECT l.id,l.name,l.root_cid,l.enabled,l.one_shot,l.created_at,l.updated_at,
 l.last_scan_started_at,l.last_scan_completed_at,l.last_scan_status,l.last_scan_error,
 (SELECT count(*) FROM series s WHERE s.library_id=l.id),
 (SELECT count(*) FROM books b WHERE b.library_id=l.id),
 (SELECT coalesce(sum(b.size),0) FROM books b WHERE b.library_id=l.id)
FROM libraries l WHERE l.id=?`, id).Scan(
		&item.ID, &item.Name, &item.RootCID, &item.Enabled, &item.OneShot, &item.CreatedAt, &item.UpdatedAt,
		&item.LastScanStartedAt, &item.LastScanCompletedAt, &item.LastScanStatus, &item.LastScanError,
		&item.SeriesCount, &item.BookCount, &item.ComicBytes,
	)
	return item, err
}

func (s *Store) UpsertLibrary(ctx context.Context, item Library) error {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO libraries(id,name,root_cid,enabled,one_shot,created_at,updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,root_cid=excluded.root_cid,enabled=excluded.enabled,
 one_shot=excluded.one_shot,updated_at=excluded.updated_at`,
		item.ID, strings.TrimSpace(item.Name), strings.TrimSpace(item.RootCID), item.Enabled, item.OneShot, now, now)
	return err
}

func (s *Store) DeleteLibrary(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM libraries WHERE id=?`, id)
	return err
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }
