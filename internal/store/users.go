package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	DisplayName  string     `json:"display_name"`
	Email        string     `json:"email"`
	Role         string     `json:"role"`
	Disabled     bool       `json:"disabled"`
	HasPassword  bool       `json:"has_password"`
	SSO          bool       `json:"sso"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	PasswordHash string     `json:"-"`
	OIDCIssuer   string     `json:"-"`
	OIDCSubject  string     `json:"-"`
}

// Name returns the display name, falling back to the username.
func (u User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

func (u User) CanEdit() bool { return u.Role == "admin" || u.Role == "editor" }
func (u User) IsAdmin() bool { return u.Role == "admin" }
func (u User) Ref() UserRef {
	return UserRef{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}

const userCols = `id, username, display_name, email, role, disabled, coalesce(password_hash, ''),
	coalesce(oidc_issuer, ''), coalesce(oidc_subject, ''), created_at, last_login_at`

// userColsU is userCols for a query that aliases users as "u".
const userColsU = `u.id, u.username, u.display_name, u.email, u.role, u.disabled, coalesce(u.password_hash, ''),
	coalesce(u.oidc_issuer, ''), coalesce(u.oidc_subject, ''), u.created_at, u.last_login_at`

type scanner interface{ Scan(...any) error }

func scanUser(r scanner) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Disabled, &u.PasswordHash,
		&u.OIDCIssuer, &u.OIDCSubject, &u.CreatedAt, &u.LastLoginAt)
	u.HasPassword = u.PasswordHash != ""
	u.SSO = u.OIDCSubject != ""
	return u, notFound(err)
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY lower(username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) GetUserByUsername(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(username) = lower($1)`, name))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users
		WHERE email <> '' AND lower(email) = lower($1) ORDER BY id LIMIT 1`, email))
}

func (s *Store) GetUserByOIDC(ctx context.Context, issuer, subject string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE oidc_issuer = $1 AND oidc_subject = $2`, issuer, subject))
}

type NewUser struct {
	Username     string
	DisplayName  string
	Email        string
	Role         string
	PasswordHash string // empty = no password
	OIDCIssuer   string
	OIDCSubject  string
}

func (s *Store) CreateUser(ctx context.Context, n NewUser) (User, error) {
	var pw, iss, sub *string
	if n.PasswordHash != "" {
		pw = &n.PasswordHash
	}
	if n.OIDCSubject != "" {
		iss, sub = &n.OIDCIssuer, &n.OIDCSubject
	}
	u, err := scanUser(s.db.QueryRow(ctx, `INSERT INTO users (username, display_name, email, role, password_hash, oidc_issuer, oidc_subject)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+userCols,
		strings.TrimSpace(n.Username), strings.TrimSpace(n.DisplayName), strings.TrimSpace(n.Email), n.Role, pw, iss, sub))
	if isUnique(err) {
		return u, ErrConflict
	}
	return u, err
}

type UserPatch struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Role        *string `json:"role"`
	Disabled    *bool   `json:"disabled"`
}

func (s *Store) UpdateUser(ctx context.Context, id int64, p UserPatch) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `UPDATE users SET
		display_name = coalesce($2, display_name),
		email        = coalesce($3, email),
		role         = coalesce($4, role),
		disabled     = coalesce($5, disabled)
		WHERE id = $1 RETURNING `+userCols, id, p.DisplayName, p.Email, p.Role, p.Disabled))
}

func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	var h *string
	if hash != "" {
		h = &hash
	}
	_, err := s.db.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, h)
	return err
}

func (s *Store) LinkOIDC(ctx context.Context, id int64, issuer, subject string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET oidc_issuer = $2, oidc_subject = $3 WHERE id = $1`, id, issuer, subject)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UnlinkOIDC(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET oidc_issuer = NULL, oidc_subject = NULL WHERE id = $1`, id)
	return err
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND NOT disabled`).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------- sessions

type Session struct {
	ID         string    `json:"id"` // hash prefix, safe to show
	UserID     int64     `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Method     string    `json:"method"`
	UserAgent  string    `json:"user_agent"`
	IP         string    `json:"ip"`
	Current    bool      `json:"current"`
}

// CreateSession stores a new session and returns the raw cookie token.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration, method, ua, ip string) (string, error) {
	tok := RandomToken(32)
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err := s.db.Exec(ctx, `INSERT INTO sessions (id, user_id, expires_at, method, user_agent, ip) VALUES ($1, $2, $3, $4, $5, $6)`,
		HashToken(tok), userID, time.Now().Add(ttl), method, ua, ip)
	if err != nil {
		return "", err
	}
	_, _ = s.db.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return tok, nil
}

// UserForSession resolves a session cookie, extending it (sliding expiry) at
// most once a minute.
func (s *Store) UserForSession(ctx context.Context, tok string, ttl time.Duration) (User, string, error) {
	id := HashToken(tok)
	u, err := scanUser(s.db.QueryRow(ctx, `SELECT `+userColsU+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > now()`, id))
	if err != nil {
		return u, "", err
	}
	_, _ = s.db.Exec(ctx, `UPDATE sessions SET last_seen_at = now(), expires_at = $2
		WHERE id = $1 AND last_seen_at < now() - interval '1 minute'`, id, time.Now().Add(ttl))
	return u, id, nil
}

func (s *Store) DeleteSession(ctx context.Context, tok string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, HashToken(tok))
	return err
}

func (s *Store) ListSessions(ctx context.Context, userID int64, currentID string) ([]Session, error) {
	rows, err := s.db.Query(ctx, `SELECT id, user_id, created_at, last_seen_at, expires_at, method, user_agent, ip
		FROM sessions WHERE user_id = $1 AND expires_at > now() ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.UserID, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt, &x.Method, &x.UserAgent, &x.IP); err != nil {
			return nil, err
		}
		x.Current = x.ID == currentID
		x.ID = x.ID[:16]
		out = append(out, x)
	}
	return out, rows.Err()
}

// RevokeSession deletes one of the user's sessions by its displayed id prefix.
func (s *Store) RevokeSession(ctx context.Context, userID int64, prefix string) error {
	if len(prefix) != 16 {
		return ErrNotFound
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND left(id, 16) = $2`, userID, prefix)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// Cleanup removes expired sessions and WebAuthn ceremonies.
func (s *Store) Cleanup(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM webauthn_sessions WHERE expires_at < now()`)
	return err
}

// ---------------------------------------------------------------- API tokens

type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	Token      string     `json:"token,omitempty"` // only on creation
}

func (s *Store) CreateToken(ctx context.Context, userID int64, name string, scopes []string, expires *time.Time) (APIToken, error) {
	raw := "fdk_" + RandomToken(30)
	t := APIToken{Name: name, Prefix: raw[:12], Scopes: scopes, ExpiresAt: expires, Token: raw}
	err := s.db.QueryRow(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, prefix, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		userID, name, HashToken(raw), t.Prefix, scopes, expires).Scan(&t.ID, &t.CreatedAt)
	return t, err
}

func (s *Store) ListTokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, prefix, scopes, created_at, last_used_at, expires_at
		FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scopes, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteToken(ctx context.Context, userID, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1 AND user_id = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// UserForToken resolves a bearer token to its user and scopes.
func (s *Store) UserForToken(ctx context.Context, raw string) (User, []string, error) {
	var scopes []string
	var tid int64
	row := s.db.QueryRow(ctx, `SELECT `+userColsU+`, t.scopes, t.id FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND (t.expires_at IS NULL OR t.expires_at > now())`, HashToken(raw))
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.Disabled, &u.PasswordHash,
		&u.OIDCIssuer, &u.OIDCSubject, &u.CreatedAt, &u.LastLoginAt, &scopes, &tid)
	if err != nil {
		return u, nil, notFound(err)
	}
	u.HasPassword, u.SSO = u.PasswordHash != "", u.OIDCSubject != ""
	_, _ = s.db.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE id = $1
		AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, tid)
	return u, scopes, nil
}

// ---------------------------------------------------------------- passkeys

type Passkey struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	CreatedAt  time.Time       `json:"created_at"`
	LastUsedAt *time.Time      `json:"last_used_at"`
	Data       json.RawMessage `json:"-"`
}

func (s *Store) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, created_at, last_used_at, data FROM webauthn_credentials
		WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt, &p.Data); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) AddPasskey(ctx context.Context, userID int64, id, name string, data []byte) error {
	_, err := s.db.Exec(ctx, `INSERT INTO webauthn_credentials (id, user_id, name, data) VALUES ($1, $2, $3, $4)`, id, userID, name, data)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdatePasskeyData(ctx context.Context, id string, data []byte) error {
	_, err := s.db.Exec(ctx, `UPDATE webauthn_credentials SET data = $2, last_used_at = now() WHERE id = $1`, id, data)
	return err
}

func (s *Store) RenamePasskey(ctx context.Context, userID int64, id, name string) error {
	tag, err := s.db.Exec(ctx, `UPDATE webauthn_credentials SET name = $3 WHERE id = $1 AND user_id = $2`, id, userID, name)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeletePasskey(ctx context.Context, userID int64, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM webauthn_credentials WHERE id = $1 AND user_id = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SaveWebAuthnSession(ctx context.Context, id string, data []byte, ttl time.Duration) error {
	_, err := s.db.Exec(ctx, `INSERT INTO webauthn_sessions (id, data, expires_at) VALUES ($1, $2, $3)`, id, data, time.Now().Add(ttl))
	return err
}

// TakeWebAuthnSession returns and deletes a pending ceremony (single use).
func (s *Store) TakeWebAuthnSession(ctx context.Context, id string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRow(ctx, `DELETE FROM webauthn_sessions WHERE id = $1 AND expires_at > now() RETURNING data`, id).Scan(&data)
	return data, notFound(err)
}
