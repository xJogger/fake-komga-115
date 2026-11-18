package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) WebDAVAccounts(ctx context.Context) ([]WebDAVAccount, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id,name,base_url,username,password,insecure_tls,rate_per_second,created_at,updated_at
FROM webdav_accounts ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WebDAVAccount, 0)
	for rows.Next() {
		var (
			item        WebDAVAccount
			password    string
			insecureInt int
		)
		if err := rows.Scan(
			&item.ID, &item.Name, &item.BaseURL, &item.Username, &password,
			&insecureInt, &item.RatePerSecond, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.HasPassword = password != ""
		item.InsecureTLS = insecureInt != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) WebDAVAccount(ctx context.Context, id string) (WebDAVAccount, error) {
	sec, err := s.WebDAVAccountSecret(ctx, id)
	if err != nil {
		return WebDAVAccount{}, err
	}
	return sec.WebDAVAccount, nil
}

func (s *Store) WebDAVAccountSecret(ctx context.Context, id string) (WebDAVAccountSecret, error) {
	var (
		item        WebDAVAccountSecret
		insecureInt int
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id,name,base_url,username,password,insecure_tls,rate_per_second,created_at,updated_at
FROM webdav_accounts WHERE id=?`, strings.TrimSpace(id)).Scan(
		&item.ID, &item.Name, &item.BaseURL, &item.Username, &item.Password,
		&insecureInt, &item.RatePerSecond, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return WebDAVAccountSecret{}, err
	}
	item.HasPassword = item.Password != ""
	item.InsecureTLS = insecureInt != 0
	if item.RatePerSecond <= 0 {
		item.RatePerSecond = 8
	}
	return item, nil
}

func (s *Store) UpsertWebDAVAccount(
	ctx context.Context,
	item WebDAVAccountSecret,
	keepExistingPassword bool,
) (WebDAVAccount, error) {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		generated, err := randomHexID(12)
		if err != nil {
			return WebDAVAccount{}, err
		}
		id = "wd_" + generated
	}
	password := item.Password
	if keepExistingPassword && password == "" {
		if existing, err := s.WebDAVAccountSecret(ctx, id); err == nil {
			password = existing.Password
		} else if !errors.Is(err, sql.ErrNoRows) {
			return WebDAVAccount{}, err
		}
	}
	rate := item.RatePerSecond
	if rate <= 0 {
		rate = 8
	}
	insecureInt := 0
	if item.InsecureTLS {
		insecureInt = 1
	}
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO webdav_accounts(
 id,name,base_url,username,password,insecure_tls,rate_per_second,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,
 base_url=excluded.base_url,
 username=excluded.username,
 password=excluded.password,
 insecure_tls=excluded.insecure_tls,
 rate_per_second=excluded.rate_per_second,
 updated_at=excluded.updated_at`,
		id, strings.TrimSpace(item.Name), strings.TrimRight(strings.TrimSpace(item.BaseURL), "/"),
		strings.TrimSpace(item.Username), password, insecureInt, rate, now, now)
	if err != nil {
		return WebDAVAccount{}, err
	}
	return s.WebDAVAccount(ctx, id)
}

func (s *Store) DeleteWebDAVAccount(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	var inUse int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM libraries WHERE provider_type='webdav' AND webdav_account_id=?`,
		id,
	).Scan(&inUse); err != nil {
		return err
	}
	if inUse > 0 {
		return errors.New("cannot delete WebDAV account while one or more libraries still use it")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM webdav_accounts WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
