package database

import (
	"context"
	"database/sql"
)

type ClientSetting struct {
	Key                  string
	Value                string
	AllowUnauthenticated bool
	UpdatedAt            string
}

func (s *Store) ClientSettings(ctx context.Context) (map[string]ClientSetting, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT key,value,allow_unauthenticated,updated_at
FROM client_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]ClientSetting{}
	for rows.Next() {
		var item ClientSetting
		var allowUnauthenticated int
		if err := rows.Scan(
			&item.Key, &item.Value, &allowUnauthenticated, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.AllowUnauthenticated = allowUnauthenticated != 0
		out[item.Key] = item
	}
	return out, rows.Err()
}

func (s *Store) UpsertClientSetting(
	ctx context.Context,
	key, value string,
	allowUnauthenticated bool,
) error {
	allowValue := 0
	if allowUnauthenticated {
		allowValue = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO client_settings(key,value,allow_unauthenticated,updated_at)
VALUES(?,?,?,?)
ON CONFLICT(key) DO UPDATE SET
 value=excluded.value,
 allow_unauthenticated=excluded.allow_unauthenticated,
 updated_at=excluded.updated_at`,
		key, value, allowValue, nowText())
	return err
}

func (s *Store) DeleteClientSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM client_settings WHERE key=?`, key)
	return err
}

func (s *Store) ClientSettingValue(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM client_settings WHERE key=?`, key).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return value, true, nil
}
