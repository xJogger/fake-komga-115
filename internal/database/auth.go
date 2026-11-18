package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

var (
	ErrAlreadyClaimed      = errors.New("administrator account is already configured")
	ErrInvalidCredentials  = errors.New("invalid username or password")
	ErrInvalidUsername     = errors.New("username must be 1 to 64 characters without leading/trailing spaces")
	ErrWeakPassword        = errors.New("password must be between 6 and 256 characters")
	basicAuthCacheDuration = 5 * time.Minute
)

const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 32 * 1024
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32

	defaultSessionTTL  = 24 * time.Hour
	rememberSessionTTL = 90 * 24 * time.Hour
)

func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM auth_admin WHERE id=1`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) Admin(ctx context.Context) (AdminUser, bool, error) {
	var user AdminUser
	err := s.db.QueryRowContext(ctx,
		`SELECT username,created_at,updated_at FROM auth_admin WHERE id=1`,
	).Scan(&user.Username, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, false, nil
	}
	if err != nil {
		return AdminUser{}, false, err
	}
	return user, true, nil
}

func (s *Store) ClaimAdmin(ctx context.Context, username, password string) (AdminUser, error) {
	username = strings.TrimSpace(username)
	if err := validateAdminUsername(username); err != nil {
		return AdminUser{}, err
	}
	if err := validateAdminPassword(password); err != nil {
		return AdminUser{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return AdminUser{}, err
	}
	now := nowText()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO auth_admin(id,username,password_hash,created_at,updated_at)
VALUES(1,?,?,?,?)`, username, hash, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") ||
			strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return AdminUser{}, ErrAlreadyClaimed
		}
		return AdminUser{}, err
	}
	s.clearBasicAuthCache()
	return AdminUser{Username: username, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) VerifyAdminPassword(ctx context.Context, username, password string) (AdminUser, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return AdminUser{}, false, nil
	}
	var (
		user       AdminUser
		storedHash string
	)
	err := s.db.QueryRowContext(ctx, `
SELECT username,password_hash,created_at,updated_at FROM auth_admin WHERE id=1`,
	).Scan(&user.Username, &storedHash, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, false, nil
	}
	if err != nil {
		return AdminUser{}, false, err
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(user.Username)), []byte(strings.ToLower(username))) != 1 {
		return AdminUser{}, false, nil
	}
	cacheKey := basicCacheKey(user.Username, password, storedHash)
	if s.checkBasicAuthCache(cacheKey) {
		return user, true, nil
	}
	if !verifyPassword(password, storedHash) {
		return AdminUser{}, false, nil
	}
	s.putBasicAuthCache(cacheKey)
	return user, true, nil
}

func (s *Store) UpdateAdminCredentials(
	ctx context.Context,
	currentPassword, newUsername, newPassword, keepSessionToken string,
) (AdminUser, error) {
	var (
		existingUser AdminUser
		storedHash   string
	)
	err := s.db.QueryRowContext(ctx, `
SELECT username,password_hash,created_at,updated_at FROM auth_admin WHERE id=1`,
	).Scan(&existingUser.Username, &storedHash, &existingUser.CreatedAt, &existingUser.UpdatedAt)
	if err != nil {
		return AdminUser{}, err
	}
	if !verifyPassword(currentPassword, storedHash) {
		return AdminUser{}, ErrInvalidCredentials
	}
	targetUsername := strings.TrimSpace(newUsername)
	if targetUsername == "" {
		targetUsername = existingUser.Username
	}
	if err := validateAdminUsername(targetUsername); err != nil {
		return AdminUser{}, err
	}
	targetHash := storedHash
	if newPassword != "" {
		if err := validateAdminPassword(newPassword); err != nil {
			return AdminUser{}, err
		}
		targetHash, err = hashPassword(newPassword)
		if err != nil {
			return AdminUser{}, err
		}
	}
	now := nowText()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminUser{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE auth_admin SET username=?,password_hash=?,updated_at=? WHERE id=1`,
		targetUsername, targetHash, now); err != nil {
		return AdminUser{}, err
	}
	if keepSessionToken != "" {
		keepHash := sha256Hex(keepSessionToken)
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM auth_sessions WHERE token_hash<>?`, keepHash); err != nil {
			return AdminUser{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE auth_sessions SET username=? WHERE token_hash=?`, targetUsername, keepHash); err != nil {
			return AdminUser{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions`); err != nil {
			return AdminUser{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AdminUser{}, err
	}
	s.clearBasicAuthCache()
	existingUser.Username = targetUsername
	existingUser.UpdatedAt = now
	return existingUser, nil
}

func (s *Store) CreateSession(
	ctx context.Context,
	username string,
	remember bool,
	userAgent string,
) (string, AuthSession, error) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at<=?`, nowText())
	rawToken, err := randomURLToken(32)
	if err != nil {
		return "", AuthSession{}, err
	}
	tokenHash := sha256Hex(rawToken)
	now := time.Now().UTC()
	ttl := defaultSessionTTL
	rememberInt := 0
	if remember {
		ttl = rememberSessionTTL
		rememberInt = 1
	}
	createdText := now.Format(time.RFC3339Nano)
	expiresText := now.Add(ttl).Format(time.RFC3339Nano)
	ua := strings.TrimSpace(userAgent)
	if len(ua) > 256 {
		ua = ua[:256]
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO auth_sessions(token_hash,username,remember,user_agent,created_at,last_seen_at,expires_at)
VALUES(?,?,?,?,?,?,?)`,
		tokenHash, username, rememberInt, ua, createdText, createdText, expiresText,
	); err != nil {
		return "", AuthSession{}, err
	}
	return rawToken, AuthSession{
		ID:         tokenHash[:16],
		Username:   username,
		Remember:   remember,
		UserAgent:  ua,
		CreatedAt:  createdText,
		LastSeenAt: createdText,
		ExpiresAt:  expiresText,
	}, nil
}

func (s *Store) ValidateSession(ctx context.Context, rawToken string) (AuthSession, bool, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return AuthSession{}, false, nil
	}
	tokenHash := sha256Hex(rawToken)
	var (
		session     AuthSession
		rememberInt int
	)
	err := s.db.QueryRowContext(ctx, `
SELECT username,remember,user_agent,created_at,last_seen_at,expires_at
FROM auth_sessions WHERE token_hash=?`, tokenHash).Scan(
		&session.Username, &rememberInt, &session.UserAgent,
		&session.CreatedAt, &session.LastSeenAt, &session.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthSession{}, false, nil
	}
	if err != nil {
		return AuthSession{}, false, err
	}
	now := time.Now().UTC()
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil || !expiresAt.After(now) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash=?`, tokenHash)
		return AuthSession{}, false, nil
	}
	session.ID = tokenHash[:16]
	session.Remember = rememberInt != 0
	lastSeen, _ := time.Parse(time.RFC3339Nano, session.LastSeenAt)
	if now.Sub(lastSeen) > 5*time.Minute {
		ttl := defaultSessionTTL
		if session.Remember {
			ttl = rememberSessionTTL
		}
		newSeen := now.Format(time.RFC3339Nano)
		newExpires := now.Add(ttl).Format(time.RFC3339Nano)
		_, _ = s.db.ExecContext(ctx, `
UPDATE auth_sessions SET last_seen_at=?,expires_at=? WHERE token_hash=?`,
			newSeen, newExpires, tokenHash)
		session.LastSeenAt = newSeen
		session.ExpiresAt = newExpires
	}
	return session, true, nil
}

func (s *Store) DeleteSession(ctx context.Context, rawToken string) error {
	if strings.TrimSpace(rawToken) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash=?`, sha256Hex(rawToken))
	return err
}

func (s *Store) DeleteSessionByPrefix(ctx context.Context, prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if len(prefix) != 16 {
		return sql.ErrNoRows
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE substr(token_hash,1,16)=?`, prefix)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListSessions(ctx context.Context, currentRawToken string) ([]AuthSession, error) {
	now := nowText()
	_, _ = s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at<=?`, now)
	currentHash := ""
	if strings.TrimSpace(currentRawToken) != "" {
		currentHash = sha256Hex(strings.TrimSpace(currentRawToken))
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT token_hash,username,remember,user_agent,created_at,last_seen_at,expires_at
FROM auth_sessions ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuthSession, 0)
	for rows.Next() {
		var (
			tokenHash   string
			item        AuthSession
			rememberInt int
		)
		if err := rows.Scan(
			&tokenHash, &item.Username, &rememberInt, &item.UserAgent,
			&item.CreatedAt, &item.LastSeenAt, &item.ExpiresAt,
		); err != nil {
			return nil, err
		}
		item.ID = tokenHash[:16]
		item.Remember = rememberInt != 0
		item.Current = currentHash != "" && tokenHash == currentHash
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreateAPIKey(ctx context.Context, name string) (string, APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", APIKey{}, errors.New("API key name must be between 1 and 80 characters")
	}
	rawRandom, err := randomURLToken(28)
	if err != nil {
		return "", APIKey{}, err
	}
	rawKey := "fk115_" + rawRandom
	id, err := randomHexID(16)
	if err != nil {
		return "", APIKey{}, err
	}
	prefix := rawKey[:12]
	keyHash := sha256Hex(rawKey)
	now := nowText()
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO auth_api_keys(id,name,key_prefix,key_hash,created_at)
VALUES(?,?,?,?,?)`, id, name, prefix, keyHash, now); err != nil {
		return "", APIKey{}, err
	}
	return rawKey, APIKey{
		ID:        id,
		Name:      name,
		KeyPrefix: prefix,
		CreatedAt: now,
	}, nil
}

func (s *Store) ValidateAPIKey(ctx context.Context, rawKey string) (APIKey, bool, error) {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return APIKey{}, false, nil
	}
	keyHash := sha256Hex(rawKey)
	var item APIKey
	err := s.db.QueryRowContext(ctx, `
SELECT id,name,key_prefix,created_at,last_used_at FROM auth_api_keys WHERE key_hash=?`,
		keyHash).Scan(&item.ID, &item.Name, &item.KeyPrefix, &item.CreatedAt, &item.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, false, nil
	}
	if err != nil {
		return APIKey{}, false, err
	}
	now := nowText()
	if item.LastUsedAt == nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE auth_api_keys SET last_used_at=? WHERE id=?`, now, item.ID)
	} else if parsed, err := time.Parse(time.RFC3339Nano, *item.LastUsedAt); err == nil && time.Since(parsed) > 5*time.Minute {
		_, _ = s.db.ExecContext(ctx, `UPDATE auth_api_keys SET last_used_at=? WHERE id=?`, now, item.ID)
	}
	return item, true, nil
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id,name,key_prefix,created_at,last_used_at FROM auth_api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]APIKey, 0)
	for rows.Next() {
		var item APIKey
		if err := rows.Scan(
			&item.ID, &item.Name, &item.KeyPrefix, &item.CreatedAt, &item.LastUsedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_api_keys WHERE id=?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func validateAdminUsername(username string) error {
	if username == "" || len(username) > 64 || strings.ContainsAny(username, ":\r\n\t\x00") {
		return ErrInvalidUsername
	}
	return nil
}

func validateAdminPassword(password string) error {
	if len(password) < 6 || len(password) > 256 {
		return ErrWeakPassword
	}
	return nil
}

func hashPassword(password string) (string, error) {
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	derived := argon2.IDKey([]byte(password), salt[:], argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonTime,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt[:]),
		base64.RawStdEncoding.EncodeToString(derived),
	), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var (
		version int
		memory  uint32
		timeVal uint32
		threads uint8
	)
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeVal, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) == 0 {
		return false
	}
	derived := argon2.IDKey([]byte(password), salt, timeVal, memory, threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(derived, expected) == 1
}

func basicCacheKey(username, password, storedHash string) string {
	return sha256Hex(strings.ToLower(username) + "\x00" + password + "\x00" + storedHash)
}

func (s *Store) checkBasicAuthCache(key string) bool {
	s.basicAuthMu.RLock()
	exp, ok := s.basicAuthCache[key]
	s.basicAuthMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		s.basicAuthMu.Lock()
		delete(s.basicAuthCache, key)
		s.basicAuthMu.Unlock()
		return false
	}
	return true
}

func (s *Store) putBasicAuthCache(key string) {
	s.basicAuthMu.Lock()
	if len(s.basicAuthCache) > 256 {
		s.basicAuthCache = make(map[string]time.Time)
	}
	s.basicAuthCache[key] = time.Now().Add(basicAuthCacheDuration)
	s.basicAuthMu.Unlock()
}

func (s *Store) clearBasicAuthCache() {
	s.basicAuthMu.Lock()
	s.basicAuthCache = make(map[string]time.Time)
	s.basicAuthMu.Unlock()
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomURLToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomHexID(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
