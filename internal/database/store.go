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
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrLibraryInUnion = errors.New("library is enrolled in a union library; remove it from the union library first")

type Store struct {
	db                   *sql.DB
	shelf                shelfCache
	unpagedBookThreshold int64

	unionMu        sync.Mutex
	basicAuthMu    sync.RWMutex
	basicAuthCache map[string]time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-16384)&_pragma=temp_store(MEMORY)"
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
	s := &Store{
		db:             db,
		basicAuthCache: make(map[string]time.Time),
	}
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
		"api_rate_per_second":                   "1",
		"range_block_size":                      "1048576",
		"rar_index_block_size":                  "65536",
		"rar_max_dictionary_size":               "104857600",
		"range_cache_max_bytes":                 "10737418240",
		"page_cache_max_bytes":                  "5368709120",
		"page_prefetch_count":                   "2",
		"volume_index_prefetch_enabled":         "false",
		"volume_index_prefetch_remaining_pages": "10",
		"max_page_size":                         "104857600",
		"downurl_ttl_seconds":                   "1200",
		"show_nsfw":                             "false",
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
	rows, err := s.db.QueryContext(ctx, `
SELECT key,value FROM settings
WHERE key NOT IN ('cors_allowed_origins','bangumi_proxy_url')
ORDER BY key`)
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

// LibrariesBasic lists libraries without running per-library Series/Book/Bytes aggregate subqueries.
// When enabledOnly is true, returns only libraries visible to Bookshelf/Komga clients
// (enabled=1 AND not enrolled as a physical sub-library inside a union library).
func (s *Store) LibrariesBasic(ctx context.Context, enabledOnly bool) ([]Library, error) {
	query := `
SELECT l.id,l.name,l.provider_type,l.root_cid,l.webdav_account_id,coalesce(w.name,''),l.root_path,
 l.enabled,l.one_shot,coalesce(ul.union_library_id,''),coalesce(up.name,''),coalesce(ul.priority,0),
 l.created_at,l.updated_at,
 l.last_scan_started_at,l.last_scan_completed_at,l.last_scan_status,l.last_scan_error
FROM libraries l
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
LEFT JOIN libraries up ON up.id=ul.union_library_id`
	if enabledOnly {
		query += ` WHERE l.enabled=1 AND ul.union_library_id IS NULL`
	}
	query += ` ORDER BY l.enabled DESC, CASE WHEN ul.union_library_id IS NULL THEN 0 ELSE 1 END, l.name COLLATE NOCASE, l.id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Library, 0)
	for rows.Next() {
		var item Library
		if err := rows.Scan(
			&item.ID, &item.Name, &item.ProviderType, &item.RootCID,
			&item.WebDAVAccountID, &item.WebDAVAccountName, &item.RootPath,
			&item.Enabled, &item.OneShot, &item.UnionParentID, &item.UnionParentName, &item.UnionPriority,
			&item.CreatedAt, &item.UpdatedAt,
			&item.LastScanStartedAt, &item.LastScanCompletedAt, &item.LastScanStatus, &item.LastScanError,
		); err != nil {
			return nil, err
		}
		item.ProviderType = item.EffectiveProvider()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.populateUnionLayers(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Libraries(ctx context.Context, enabledOnly bool) ([]Library, error) {
	query := `
SELECT l.id,l.name,l.provider_type,l.root_cid,l.webdav_account_id,coalesce(w.name,''),l.root_path,
 l.enabled,l.one_shot,coalesce(ul.union_library_id,''),coalesce(up.name,''),coalesce(ul.priority,0),
 l.created_at,l.updated_at,
 l.last_scan_started_at,l.last_scan_completed_at,l.last_scan_status,l.last_scan_error,
 coalesce(sc.cnt,0),
 coalesce(bc.cnt,0),
 coalesce(bc.bytes,0)
FROM libraries l
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
LEFT JOIN libraries up ON up.id=ul.union_library_id
LEFT JOIN (SELECT library_id, count(*) AS cnt FROM series GROUP BY library_id) sc ON sc.library_id=l.id
LEFT JOIN (SELECT library_id, count(*) AS cnt, coalesce(sum(size),0) AS bytes FROM books GROUP BY library_id) bc ON bc.library_id=l.id`
	if enabledOnly {
		query += ` WHERE l.enabled=1 AND ul.union_library_id IS NULL`
	}
	query += ` ORDER BY l.enabled DESC, CASE WHEN ul.union_library_id IS NULL THEN 0 ELSE 1 END, l.name COLLATE NOCASE, l.id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Library, 0)
	for rows.Next() {
		var item Library
		if err := rows.Scan(
			&item.ID, &item.Name, &item.ProviderType, &item.RootCID,
			&item.WebDAVAccountID, &item.WebDAVAccountName, &item.RootPath,
			&item.Enabled, &item.OneShot, &item.UnionParentID, &item.UnionParentName, &item.UnionPriority,
			&item.CreatedAt, &item.UpdatedAt,
			&item.LastScanStartedAt, &item.LastScanCompletedAt, &item.LastScanStatus, &item.LastScanError,
			&item.SeriesCount, &item.BookCount, &item.ComicBytes,
		); err != nil {
			return nil, err
		}
		item.ProviderType = item.EffectiveProvider()
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.populateUnionLayers(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) populateUnionLayers(ctx context.Context, libs []Library) error {
	unionIdx := make(map[string]int)
	var unionIDs []string
	for i := range libs {
		if libs[i].EffectiveProvider() == ProviderUnion {
			unionIdx[libs[i].ID] = i
			unionIDs = append(unionIDs, libs[i].ID)
			libs[i].UnionLayers = []UnionLayer{}
		}
	}
	if len(unionIDs) == 0 {
		return nil
	}
	args := make([]any, len(unionIDs))
	for i, id := range unionIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT ul.union_library_id,ul.member_library_id,ml.name,ml.provider_type,coalesce(w.name,''),ul.priority
FROM union_layers ul
JOIN libraries ml ON ml.id=ul.member_library_id
LEFT JOIN webdav_accounts w ON w.id=ml.webdav_account_id
WHERE ul.union_library_id IN `+Placeholders(len(args))+`
ORDER BY ul.union_library_id,ul.priority ASC,ul.member_library_id ASC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var layer UnionLayer
		if err := rows.Scan(
			&layer.UnionLibraryID, &layer.MemberLibraryID, &layer.MemberLibraryName,
			&layer.ProviderType, &layer.WebDAVAccountName, &layer.Priority,
		); err != nil {
			return err
		}
		if idx, ok := unionIdx[layer.UnionLibraryID]; ok {
			libs[idx].UnionLayers = append(libs[idx].UnionLayers, layer)
		}
	}
	return rows.Err()
}

// enabledLibraryIDs returns all library IDs visible to Bookshelf/Komga clients
// (enabled=1 AND not enrolled as a union sub-library).
func (s *Store) enabledLibraryIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT l.id FROM libraries l
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
WHERE l.enabled=1 AND ul.union_library_id IS NULL
ORDER BY l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 8)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) Library(ctx context.Context, id string) (Library, error) {
	var item Library
	err := s.db.QueryRowContext(ctx, `
SELECT l.id,l.name,l.provider_type,l.root_cid,l.webdav_account_id,coalesce(w.name,''),l.root_path,
 l.enabled,l.one_shot,coalesce(ul.union_library_id,''),coalesce(up.name,''),coalesce(ul.priority,0),
 l.created_at,l.updated_at,
 l.last_scan_started_at,l.last_scan_completed_at,l.last_scan_status,l.last_scan_error,
 (SELECT count(*) FROM series s WHERE s.library_id=l.id),
 (SELECT count(*) FROM books b WHERE b.library_id=l.id),
 (SELECT coalesce(sum(b.size),0) FROM books b WHERE b.library_id=l.id)
FROM libraries l
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
LEFT JOIN libraries up ON up.id=ul.union_library_id
WHERE l.id=?`, id).Scan(
		&item.ID, &item.Name, &item.ProviderType, &item.RootCID,
		&item.WebDAVAccountID, &item.WebDAVAccountName, &item.RootPath,
		&item.Enabled, &item.OneShot, &item.UnionParentID, &item.UnionParentName, &item.UnionPriority,
		&item.CreatedAt, &item.UpdatedAt,
		&item.LastScanStartedAt, &item.LastScanCompletedAt, &item.LastScanStatus, &item.LastScanError,
		&item.SeriesCount, &item.BookCount, &item.ComicBytes,
	)
	if err != nil {
		return Library{}, err
	}
	item.ProviderType = item.EffectiveProvider()
	if item.ProviderType == ProviderUnion {
		list := []Library{item}
		if err := s.populateUnionLayers(ctx, list); err != nil {
			return Library{}, err
		}
		item = list[0]
	}
	return item, nil
}

// LibraryBasic fetches a single library without running Series/Book aggregate subqueries.
func (s *Store) LibraryBasic(ctx context.Context, id string) (Library, error) {
	var item Library
	err := s.db.QueryRowContext(ctx, `
SELECT l.id,l.name,l.provider_type,l.root_cid,l.webdav_account_id,coalesce(w.name,''),l.root_path,
 l.enabled,l.one_shot,coalesce(ul.union_library_id,''),coalesce(up.name,''),coalesce(ul.priority,0),
 l.created_at,l.updated_at,
 l.last_scan_started_at,l.last_scan_completed_at,l.last_scan_status,l.last_scan_error
FROM libraries l
LEFT JOIN webdav_accounts w ON w.id=l.webdav_account_id
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
LEFT JOIN libraries up ON up.id=ul.union_library_id
WHERE l.id=?`, id).Scan(
		&item.ID, &item.Name, &item.ProviderType, &item.RootCID,
		&item.WebDAVAccountID, &item.WebDAVAccountName, &item.RootPath,
		&item.Enabled, &item.OneShot, &item.UnionParentID, &item.UnionParentName, &item.UnionPriority,
		&item.CreatedAt, &item.UpdatedAt,
		&item.LastScanStartedAt, &item.LastScanCompletedAt, &item.LastScanStatus, &item.LastScanError,
	)
	if err != nil {
		return Library{}, err
	}
	item.ProviderType = item.EffectiveProvider()
	if item.ProviderType == ProviderUnion {
		list := []Library{item}
		if err := s.populateUnionLayers(ctx, list); err != nil {
			return Library{}, err
		}
		item = list[0]
	}
	return item, nil
}

func (s *Store) UpsertLibrary(ctx context.Context, item Library) error {
	now := nowText()
	provider := item.EffectiveProvider()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO libraries(
 id,name,provider_type,root_cid,webdav_account_id,root_path,enabled,one_shot,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,
 provider_type=excluded.provider_type,
 root_cid=excluded.root_cid,
 webdav_account_id=excluded.webdav_account_id,
 root_path=excluded.root_path,
 enabled=excluded.enabled,
 one_shot=excluded.one_shot,
 updated_at=excluded.updated_at`,
		item.ID, strings.TrimSpace(item.Name), provider, strings.TrimSpace(item.RootCID),
		strings.TrimSpace(item.WebDAVAccountID), strings.TrimSpace(item.RootPath),
		item.Enabled, item.OneShot, now, now)
	return err
}

func (s *Store) DeleteLibrary(ctx context.Context, id string) error {
	var inUnion int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM union_layers WHERE member_library_id=?`, id,
	).Scan(&inUnion); err != nil {
		return err
	}
	if inUnion > 0 {
		return ErrLibraryInUnion
	}

	var bookCount int64
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM books WHERE library_id=?`, id).Scan(&bookCount)

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if bookCount > 5000 {
		_, _ = conn.ExecContext(ctx, `PRAGMA cache_size = -131072`)
		defer conn.ExecContext(context.Background(), `PRAGMA cache_size = -16384`)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=1 WHERE id=1`); err != nil {
		return err
	}
	// Delete bottom-up so large libraries bypass per-row rollup/shelf trigger storms.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM union_book_sources
WHERE union_book_id IN (SELECT id FROM books WHERE library_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM union_series_sources
WHERE union_series_id IN (SELECT id FROM series WHERE library_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM books WHERE library_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE library_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM libraries WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shelf_revision SET bulk_mode=0, catalog=catalog+1, progress=progress+1 WHERE id=1`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) HiddenLibraryCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM libraries l
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
WHERE l.enabled=0 AND ul.union_library_id IS NULL`).Scan(&count)
	return count, err
}

// IsLibraryEnabled reports whether the library is enabled for scanning/management
// (true for enabled physical sub-libraries inside a union library as well).
func (s *Store) IsLibraryEnabled(ctx context.Context, id string) bool {
	var enabled int
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM libraries WHERE id=?`, id).Scan(&enabled); err != nil {
		return false
	}
	return enabled != 0
}

// IsLibraryVisible reports whether the library is exposed on the Bookshelf and Komga APIs
// (enabled=1 AND not enrolled as a union sub-library).
func (s *Store) IsLibraryVisible(ctx context.Context, id string) bool {
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*) FROM libraries l
LEFT JOIN union_layers ul ON ul.member_library_id=l.id
WHERE l.id=? AND l.enabled=1 AND ul.union_library_id IS NULL`, id).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func (s *Store) ShowNSFW(ctx context.Context) bool {
	return s.BoolSetting(ctx, "show_nsfw", false)
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }
