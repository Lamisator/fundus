package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// OAuth lifetimes and limits.
const (
	OAuthCodeTTL    = 10 * time.Minute
	OAuthAccessTTL  = time.Hour
	OAuthRefreshTTL = 90 * 24 * time.Hour
	// Registration is open to anyone (RFC 7591), so cap it; clients that
	// never completed a grant are dropped after an hour.
	maxOAuthClients = 100
)

var (
	ErrOAuthInvalidGrant  = errors.New("invalid_grant")
	ErrOAuthInvalidClient = errors.New("invalid_client")
	ErrOAuthTooMany       = errors.New("too many registered OAuth clients")
)

// OAuthClient is a dynamically registered OAuth client.
type OAuthClient struct {
	ID           string     `json:"client_id"`
	Name         string     `json:"client_name"`
	RedirectURIs []string   `json:"redirect_uris"`
	Confidential bool       `json:"confidential"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
}

// OAuthCode is a pending authorization code.
type OAuthCode struct {
	ClientID      string
	UserID        int64
	RedirectURI   string
	CodeChallenge string
	Scopes        []string
	Resource      string
}

// OAuthTokens is the token endpoint response.
type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// OAuthGrant is an app a user has connected.
type OAuthGrant struct {
	ClientID    string     `json:"client_id"`
	ClientName  string     `json:"client_name"`
	RedirectURI string     `json:"redirect_uri"`
	Scopes      []string   `json:"scopes"`
	GrantedAt   time.Time  `json:"granted_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
}

// RegisterOAuthClient stores a new client. Confidential clients get a
// secret, which is returned once.
func (s *Store) RegisterOAuthClient(ctx context.Context, name string, redirects []string, confidential bool) (OAuthClient, string, error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM oauth_clients WHERE last_used_at IS NULL AND created_at < now() - interval '1 hour'`); err != nil {
		return OAuthClient{}, "", err
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM oauth_clients`).Scan(&n); err != nil {
		return OAuthClient{}, "", err
	}
	if n >= maxOAuthClients {
		return OAuthClient{}, "", ErrOAuthTooMany
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unnamed app"
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	c := OAuthClient{ID: "fdc_" + RandomToken(16), Name: name, RedirectURIs: redirects, Confidential: confidential}
	var secret string
	var secretHash *string
	if confidential {
		secret = "fds_" + RandomToken(32)
		h := HashToken(secret)
		secretHash = &h
	}
	err := s.db.QueryRow(ctx, `INSERT INTO oauth_clients (id, name, redirect_uris, secret_hash) VALUES ($1, $2, $3, $4)
		RETURNING created_at`, c.ID, c.Name, redirects, secretHash).Scan(&c.CreatedAt)
	return c, secret, err
}

func (s *Store) GetOAuthClient(ctx context.Context, id string) (OAuthClient, error) {
	var c OAuthClient
	err := s.db.QueryRow(ctx, `SELECT id, name, redirect_uris, secret_hash IS NOT NULL, created_at, last_used_at
		FROM oauth_clients WHERE id = $1`, id).Scan(&c.ID, &c.Name, &c.RedirectURIs, &c.Confidential, &c.CreatedAt, &c.LastUsedAt)
	return c, notFound(err)
}

// CheckOAuthClientSecret verifies the secret of a confidential client;
// public clients pass without one.
func (s *Store) CheckOAuthClientSecret(ctx context.Context, id, secret string) error {
	var h *string
	if err := s.db.QueryRow(ctx, `SELECT secret_hash FROM oauth_clients WHERE id = $1`, id).Scan(&h); err != nil {
		return ErrOAuthInvalidClient
	}
	if h == nil {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(*h), []byte(HashToken(secret))) != 1 {
		return ErrOAuthInvalidClient
	}
	return nil
}

// CreateOAuthCode stores a single-use authorization code and returns it.
func (s *Store) CreateOAuthCode(ctx context.Context, c OAuthCode) (string, error) {
	code := "fda_" + RandomToken(32)
	_, err := s.db.Exec(ctx, `INSERT INTO oauth_codes (code_hash, client_id, user_id, redirect_uri, code_challenge, scopes, resource, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		HashToken(code), c.ClientID, c.UserID, c.RedirectURI, c.CodeChallenge, c.Scopes, c.Resource, time.Now().Add(OAuthCodeTTL))
	return code, err
}

// ConsumeOAuthCode deletes a code and returns it if it was still valid. A
// failed exchange uses the code up as well.
func (s *Store) ConsumeOAuthCode(ctx context.Context, code string) (OAuthCode, error) {
	var c OAuthCode
	var exp time.Time
	err := s.db.QueryRow(ctx, `DELETE FROM oauth_codes WHERE code_hash = $1
		RETURNING client_id, user_id, redirect_uri, code_challenge, scopes, resource, expires_at`, HashToken(code)).
		Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge, &c.Scopes, &c.Resource, &exp)
	if err != nil || time.Now().After(exp) {
		return c, ErrOAuthInvalidGrant
	}
	return c, nil
}

func issueOAuth(ctx context.Context, tx pgx.Tx, c OAuthClient, userID int64, scopes []string) (OAuthTokens, error) {
	access := "fdk_" + RandomToken(30)
	refresh := "fdr_" + RandomToken(32)
	if _, err := tx.Exec(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, prefix, scopes, expires_at, oauth_client_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		userID, c.Name+" (OAuth)", HashToken(access), access[:12], scopes, time.Now().Add(OAuthAccessTTL), c.ID); err != nil {
		return OAuthTokens{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO oauth_refresh_tokens (token_hash, client_id, user_id, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, HashToken(refresh), c.ID, userID, scopes, time.Now().Add(OAuthRefreshTTL)); err != nil {
		return OAuthTokens{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE oauth_clients SET last_used_at = now() WHERE id = $1`, c.ID); err != nil {
		return OAuthTokens{}, err
	}
	return OAuthTokens{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(OAuthAccessTTL.Seconds()),
		RefreshToken: refresh, Scope: strings.Join(scopes, " ")}, nil
}

// IssueOAuthTokens creates an access and a refresh token after a code exchange.
func (s *Store) IssueOAuthTokens(ctx context.Context, clientID string, userID int64, scopes []string) (OAuthTokens, error) {
	c, err := s.GetOAuthClient(ctx, clientID)
	if err != nil {
		return OAuthTokens{}, ErrOAuthInvalidClient
	}
	var out OAuthTokens
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		out, err = issueOAuth(ctx, tx, c, userID, scopes)
		return err
	})
	return out, err
}

// RefreshOAuthTokens exchanges a refresh token for new tokens; the old one
// stops working (rotation). allowed trims the granted scopes, e.g. when the
// client asks for less or the user lost the editor role meanwhile. It
// returns ErrOAuthInvalidGrant for unknown, expired or foreign tokens and
// for disabled users.
func (s *Store) RefreshOAuthTokens(ctx context.Context, clientID, refresh string, allowed func(u User, granted []string) []string) (OAuthTokens, error) {
	c, err := s.GetOAuthClient(ctx, clientID)
	if err != nil {
		return OAuthTokens{}, ErrOAuthInvalidClient
	}
	var out OAuthTokens
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var userID int64
		var granted []string
		var exp time.Time
		err := tx.QueryRow(ctx, `DELETE FROM oauth_refresh_tokens WHERE token_hash = $1 AND client_id = $2
			RETURNING user_id, scopes, expires_at`, HashToken(refresh), c.ID).Scan(&userID, &granted, &exp)
		if err != nil || time.Now().After(exp) {
			return ErrOAuthInvalidGrant
		}
		u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, userID))
		if err != nil || u.Disabled {
			return ErrOAuthInvalidGrant
		}
		out, err = issueOAuth(ctx, tx, c, userID, allowed(u, granted))
		return err
	})
	if errors.Is(err, ErrOAuthInvalidGrant) {
		// the rotated token must stay deleted even though the tx rolled back
		_, _ = s.db.Exec(ctx, `DELETE FROM oauth_refresh_tokens WHERE token_hash = $1 AND client_id = $2`, HashToken(refresh), c.ID)
	}
	return out, err
}

// RevokeOAuthToken deletes an access or refresh token of a client (RFC 7009).
func (s *Store) RevokeOAuthToken(ctx context.Context, clientID, token string) error {
	h := HashToken(token)
	if _, err := s.db.Exec(ctx, `DELETE FROM oauth_refresh_tokens WHERE token_hash = $1 AND client_id = $2`, h, clientID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM api_tokens WHERE token_hash = $1 AND oauth_client_id = $2`, h, clientID)
	return err
}

// ListOAuthGrants lists the apps a user has connected (valid refresh tokens).
func (s *Store) ListOAuthGrants(ctx context.Context, userID int64) ([]OAuthGrant, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id, c.name, c.redirect_uris[1],
			(SELECT array_agg(DISTINCT x ORDER BY x) FROM oauth_refresh_tokens r2, unnest(r2.scopes) x
			  WHERE r2.client_id = c.id AND r2.user_id = $1 AND r2.expires_at > now()),
			min(r.created_at), c.last_used_at
		FROM oauth_clients c JOIN oauth_refresh_tokens r ON r.client_id = c.id
		WHERE r.user_id = $1 AND r.expires_at > now()
		GROUP BY c.id ORDER BY c.name, c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthGrant{}
	for rows.Next() {
		var g OAuthGrant
		if err := rows.Scan(&g.ClientID, &g.ClientName, &g.RedirectURI, &g.Scopes, &g.GrantedAt, &g.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteOAuthGrant disconnects an app from a user: all of the user's tokens
// for that client go. The client itself is removed once nobody uses it.
func (s *Store) DeleteOAuthGrant(ctx context.Context, userID int64, clientID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM oauth_refresh_tokens WHERE user_id = $1 AND client_id = $2`, userID, clientID)
		if err != nil {
			return err
		}
		tag2, err := tx.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = $1 AND oauth_client_id = $2`, userID, clientID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM oauth_codes WHERE user_id = $1 AND client_id = $2`, userID, clientID); err != nil {
			return err
		}
		if tag.RowsAffected() == 0 && tag2.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `DELETE FROM oauth_clients c WHERE id = $1
			AND NOT EXISTS (SELECT 1 FROM oauth_refresh_tokens WHERE client_id = c.id)
			AND NOT EXISTS (SELECT 1 FROM api_tokens WHERE oauth_client_id = c.id)`, clientID)
		return err
	})
}

func (s *Store) cleanupOAuth(ctx context.Context) error {
	for _, q := range []string{
		`DELETE FROM oauth_codes WHERE expires_at < now()`,
		`DELETE FROM oauth_refresh_tokens WHERE expires_at < now()`,
		`DELETE FROM api_tokens WHERE oauth_client_id IS NOT NULL AND expires_at < now()`,
		`DELETE FROM oauth_clients WHERE last_used_at IS NULL AND created_at < now() - interval '1 hour'`,
	} {
		if _, err := s.db.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
