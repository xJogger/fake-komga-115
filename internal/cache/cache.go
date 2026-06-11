package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	TypeRange = "range"
	TypePage  = "page"
)

type Manager struct {
	store *database.Store
	root  string

	mu       sync.Mutex
	inflight map[string]*flight
}

type flight struct {
	done chan struct{}
	data []byte
	err  error
}

func New(store *database.Store, root string) (*Manager, error) {
	for _, cacheType := range []string{TypeRange, TypePage} {
		if err := os.MkdirAll(filepath.Join(root, cacheType), 0o700); err != nil {
			return nil, err
		}
	}
	return &Manager{store: store, root: root, inflight: make(map[string]*flight)}, nil
}

func (m *Manager) GetOrLoad(
	ctx context.Context,
	cacheType, key string,
	maxBytes int64,
	loader func(context.Context) ([]byte, error),
) ([]byte, bool, error) {
	if data, ok, err := m.Get(ctx, cacheType, key); err != nil || ok {
		return data, ok, err
	}
	flightKey := cacheType + ":" + key
	m.mu.Lock()
	if existing := m.inflight[flightKey]; existing != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-existing.done:
			return existing.data, false, existing.err
		}
	}
	current := &flight{done: make(chan struct{})}
	m.inflight[flightKey] = current
	m.mu.Unlock()

	data, err := loader(ctx)
	if err == nil {
		err = m.Put(ctx, cacheType, key, data, maxBytes)
	}
	current.data, current.err = data, err
	close(current.done)
	m.mu.Lock()
	delete(m.inflight, flightKey)
	m.mu.Unlock()
	return data, false, err
}

func (m *Manager) Get(ctx context.Context, cacheType, key string) ([]byte, bool, error) {
	path := m.path(cacheType, key)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.store.DB().ExecContext(ctx, `
UPDATE cache_entries SET last_access_at=? WHERE cache_key=? AND cache_type=?`,
		now, key, cacheType)
	return data, true, nil
}

func (m *Manager) Put(ctx context.Context, cacheType, key string, data []byte, maxBytes int64) error {
	path := m.path(cacheType, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".cache-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = m.store.DB().ExecContext(ctx, `
INSERT INTO cache_entries(cache_key,cache_type,path,size,last_access_at,created_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(cache_key) DO UPDATE SET
 cache_type=excluded.cache_type,path=excluded.path,size=excluded.size,last_access_at=excluded.last_access_at`,
		key, cacheType, path, len(data), now, now)
	if err != nil {
		return err
	}
	return m.enforceLimit(ctx, cacheType, maxBytes)
}

func (m *Manager) Clear(ctx context.Context, cacheType string) error {
	types := []string{cacheType}
	if cacheType == "" || cacheType == "all" {
		types = []string{TypeRange, TypePage}
	}
	for _, item := range types {
		if item != TypeRange && item != TypePage {
			return fmt.Errorf("unknown cache type %q", item)
		}
		if err := os.RemoveAll(filepath.Join(m.root, item)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(m.root, item), 0o700); err != nil {
			return err
		}
		if _, err := m.store.DB().ExecContext(ctx,
			`DELETE FROM cache_entries WHERE cache_type=?`, item); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Stats(ctx context.Context) ([]database.CacheStats, error) {
	rows, err := m.store.DB().QueryContext(ctx, `
SELECT cache_type,count(*),coalesce(sum(size),0) FROM cache_entries GROUP BY cache_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := map[string]database.CacheStats{
		TypeRange: {Type: TypeRange},
		TypePage:  {Type: TypePage},
	}
	for rows.Next() {
		var item database.CacheStats
		if err := rows.Scan(&item.Type, &item.Files, &item.Bytes); err != nil {
			return nil, err
		}
		stats[item.Type] = item
	}
	return []database.CacheStats{stats[TypeRange], stats[TypePage]}, rows.Err()
}

func (m *Manager) EnforceLimit(ctx context.Context, cacheType string, maxBytes int64) error {
	if cacheType != TypeRange && cacheType != TypePage {
		return fmt.Errorf("unknown cache type %q", cacheType)
	}
	return m.enforceLimit(ctx, cacheType, maxBytes)
}

func (m *Manager) path(cacheType, key string) string {
	sum := sha256.Sum256([]byte(cacheType + "\x00" + key))
	name := hex.EncodeToString(sum[:])
	return filepath.Join(m.root, cacheType, name[:2], name[2:]+".cache")
}

func (m *Manager) enforceLimit(ctx context.Context, cacheType string, maxBytes int64) error {
	if maxBytes <= 0 {
		return nil
	}
	var total int64
	if err := m.store.DB().QueryRowContext(ctx,
		`SELECT coalesce(sum(size),0) FROM cache_entries WHERE cache_type=?`, cacheType).Scan(&total); err != nil {
		return err
	}
	if total <= maxBytes {
		return nil
	}
	rows, err := m.store.DB().QueryContext(ctx, `
SELECT cache_key,path,size FROM cache_entries
WHERE cache_type=? ORDER BY last_access_at ASC`, cacheType)
	if err != nil {
		return err
	}
	defer rows.Close()
	type victim struct {
		key, path string
		size      int64
	}
	var victims []victim
	for rows.Next() && total > maxBytes {
		var item victim
		if err := rows.Scan(&item.key, &item.path, &item.size); err != nil {
			return err
		}
		victims = append(victims, item)
		total -= item.size
	}
	for _, item := range victims {
		_ = os.Remove(item.path)
		_, _ = m.store.DB().ExecContext(ctx,
			`DELETE FROM cache_entries WHERE cache_key=? AND cache_type=?`, item.key, cacheType)
	}
	return nil
}
