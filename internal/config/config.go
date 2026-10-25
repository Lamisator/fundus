// Package config reads Fundus' settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr        string // listen address, e.g. ":8080"
	DatabaseURL string
	DataDir     string // attachments live in DataDir/attachments
	BaseURL     string // public URL, e.g. https://kb.example.com (optional; derived per request if empty)
	SiteName    string

	// Bootstrap admin, created on first start if no users exist.
	AdminUser     string
	AdminPassword string

	PasswordLogin bool          // allow username + password login
	SessionTTL    time.Duration // sliding session lifetime
	MaxUploadMB   int64

	// WebAuthn. Empty = derived from the Host header of each request.
	RPID     string
	RPOrigin string

	OIDC OIDC
}

type OIDC struct {
	Issuer        string
	ClientID      string
	ClientSecret  string // optional: Zitadel "PKCE" apps have no secret
	RedirectURL   string // optional: derived from the request
	Scopes        []string
	ButtonLabel   string
	AutoCreate    bool   // provision unknown users on first login
	DefaultRole   string // role for auto-provisioned users
	AllowedDomain string // Zitadel org primary domain; others are refused
	// RoleClaim names a claim holding project roles. Zitadel uses
	// "urn:zitadel:iam:org:project:roles" (a map of role -> orgs). When set,
	// the user's Fundus role is synced from it on each login: a role key
	// "admin", "editor" or "viewer" (or fundus-admin, ...) wins, highest first.
	RoleClaim string
	// Prompt is sent as the OIDC "prompt" parameter. The default
	// "select_account" makes Zitadel show its account picker instead of
	// silently reusing whichever account the browser is signed in with.
	Prompt string
}

func (o OIDC) Enabled() bool { return o.Issuer != "" && o.ClientID != "" }

func Load() (Config, error) {
	c := Config{
		Addr:          env("FUNDUS_ADDR", ":8080"),
		DatabaseURL:   env("DATABASE_URL", ""),
		DataDir:       env("FUNDUS_DATA_DIR", "./data"),
		BaseURL:       strings.TrimRight(env("FUNDUS_BASE_URL", ""), "/"),
		SiteName:      env("FUNDUS_SITE_NAME", "Fundus"),
		AdminUser:     env("FUNDUS_ADMIN_USER", ""),
		AdminPassword: env("FUNDUS_ADMIN_PASSWORD", ""),
		PasswordLogin: envBool("FUNDUS_PASSWORD_LOGIN", true),
		RPID:          env("FUNDUS_RP_ID", ""),
		RPOrigin:      env("FUNDUS_RP_ORIGIN", ""),
		OIDC: OIDC{
			Issuer:        strings.TrimRight(env("FUNDUS_OIDC_ISSUER", ""), "/"),
			ClientID:      env("FUNDUS_OIDC_CLIENT_ID", ""),
			ClientSecret:  env("FUNDUS_OIDC_CLIENT_SECRET", ""),
			RedirectURL:   env("FUNDUS_OIDC_REDIRECT_URL", ""),
			Scopes:        strings.Fields(env("FUNDUS_OIDC_SCOPES", "openid profile email")),
			ButtonLabel:   env("FUNDUS_OIDC_BUTTON_LABEL", "Sign in with Zitadel"),
			AutoCreate:    envBool("FUNDUS_OIDC_AUTO_CREATE", true),
			DefaultRole:   env("FUNDUS_OIDC_DEFAULT_ROLE", "editor"),
			AllowedDomain: env("FUNDUS_OIDC_ALLOWED_DOMAIN", ""),
			RoleClaim:     env("FUNDUS_OIDC_ROLE_CLAIM", ""),
			Prompt:        strings.TrimSpace(env("FUNDUS_OIDC_PROMPT", "select_account")),
		},
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	days, err := strconv.Atoi(env("FUNDUS_SESSION_DAYS", "30"))
	if err != nil || days < 1 {
		return c, fmt.Errorf("FUNDUS_SESSION_DAYS must be a positive integer")
	}
	c.SessionTTL = time.Duration(days) * 24 * time.Hour
	c.MaxUploadMB, err = strconv.ParseInt(env("FUNDUS_MAX_UPLOAD_MB", "50"), 10, 64)
	if err != nil || c.MaxUploadMB < 1 {
		return c, fmt.Errorf("FUNDUS_MAX_UPLOAD_MB must be a positive integer")
	}
	switch c.OIDC.DefaultRole {
	case "admin", "editor", "viewer":
	default:
		return c, fmt.Errorf("FUNDUS_OIDC_DEFAULT_ROLE must be admin, editor or viewer")
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(env(key, ""))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
