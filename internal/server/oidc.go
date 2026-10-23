package server

// Single sign-on via OpenID Connect, written against Zitadel but standard
// enough for any conformant IdP. Notes from earlier projects:
//   - Zitadel v4 only puts the mandatory claims into the ID token unless the
//     project has "User Info inside ID Token" enabled, so profile claims are
//     merged in from the userinfo endpoint (filling gaps only);
//   - restricting logins to one organisation is done by requesting the
//     urn:zitadel:iam:org:domain:primary:<domain> scope and cross-checking the
//     returned claim, so a misconfigured scope cannot let outsiders in;
//   - auth-code flow with PKCE, so Zitadel "PKCE" apps without a client
//     secret work as well as "Code" apps with one.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Lamisator/fundus/internal/store"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	oidcCookie    = "fundus_oidc"
	oidcCookieTTL = 10 * time.Minute

	claimOrgDomain           = "urn:zitadel:iam:org:domain:primary"
	claimResourceOwnerDomain = "urn:zitadel:iam:user:resourceowner:primary_domain"
	scopeOrgDomainFmt        = "urn:zitadel:iam:org:domain:primary:%s"
)

type oidcState struct {
	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	at       time.Time
}

// provider discovers the IdP lazily and caches the result for an hour, so a
// Zitadel that is down at startup does not keep Fundus from booting.
func (s *Server) provider(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	st := s.oidc
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.provider != nil && time.Since(st.at) < time.Hour {
		return st.provider, st.verifier, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, s.cfg.OIDC.Issuer)
	if err != nil {
		if st.provider != nil {
			return st.provider, st.verifier, nil // keep using the stale one
		}
		return nil, nil, fmt.Errorf("discover OIDC issuer: %w", err)
	}
	st.provider, st.verifier, st.at = p, p.Verifier(&oidc.Config{ClientID: s.cfg.OIDC.ClientID}), time.Now()
	return st.provider, st.verifier, nil
}

func (s *Server) oauthConfig(r *http.Request, p *oidc.Provider) *oauth2.Config {
	redirect := s.cfg.OIDC.RedirectURL
	if redirect == "" {
		redirect = s.baseURL(r) + "/auth/oidc/callback"
	}
	scopes := append([]string{}, s.cfg.OIDC.Scopes...)
	has := false
	for _, sc := range scopes {
		has = has || sc == oidc.ScopeOpenID
	}
	if !has {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	if d := s.cfg.OIDC.AllowedDomain; d != "" {
		scopes = append(scopes, fmt.Sprintf(scopeOrgDomainFmt, d))
	}
	if s.cfg.OIDC.RoleClaim == "urn:zitadel:iam:org:project:roles" {
		scopes = append(scopes, "urn:zitadel:iam:org:project:roles")
	}
	return &oauth2.Config{
		ClientID:     s.cfg.OIDC.ClientID,
		ClientSecret: s.cfg.OIDC.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirect,
		Scopes:       scopes,
	}
}

type oidcFlow struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	LinkUser int64  `json:"l,omitempty"` // link to this signed-in user instead of logging in
	Return   string `json:"r,omitempty"`
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled() {
		http.Error(w, "SSO is not configured", http.StatusNotFound)
		return
	}
	p, _, err := s.provider(r.Context())
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	flow := oidcFlow{State: store.RandomToken(24), Verifier: store.RandomToken(48), Nonce: store.RandomToken(18)}
	if r.URL.Query().Get("link") == "1" {
		if me := who(r); me != nil && me.Via == "session" {
			flow.LinkUser = me.ID
		}
	}
	if ret := r.URL.Query().Get("return"); strings.HasPrefix(ret, "/") && !strings.HasPrefix(ret, "//") {
		flow.Return = ret
	}
	raw, _ := json.Marshal(flow)
	http.SetCookie(w, &http.Cookie{
		Name: oidcCookie, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/auth/oidc/",
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: int(oidcCookieTTL.Seconds()),
	})
	sum := sha256.Sum256([]byte(flow.Verifier))
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oidc.Nonce(flow.Nonce),
	}
	if pr := r.URL.Query().Get("prompt"); pr == "login" || pr == "select_account" {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", pr))
	}
	http.Redirect(w, r, s.oauthConfig(r, p).AuthCodeURL(flow.State, opts...), http.StatusFound)
}

func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	s.st.Audit(context.WithoutCancel(r.Context()), 0, "login.failed", "", "oidc: "+msg, clientIP(r))
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/auth/oidc/", HttpOnly: true, MaxAge: -1})
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OIDC.Enabled() {
		http.Error(w, "SSO is not configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		msg := e
		if d := q.Get("error_description"); d != "" {
			msg += ": " + d
		}
		s.oidcFail(w, r, msg)
		return
	}
	c, err := r.Cookie(oidcCookie)
	if err != nil {
		s.oidcFail(w, r, "the sign-in took too long or cookies are blocked, please try again")
		return
	}
	var flow oidcFlow
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || json.Unmarshal(raw, &flow) != nil || flow.State == "" {
		s.oidcFail(w, r, "malformed sign-in state")
		return
	}
	if q.Get("state") != flow.State || q.Get("code") == "" {
		s.oidcFail(w, r, "state mismatch, please try again")
		return
	}
	p, verifier, err := s.provider(r.Context())
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := s.oauthConfig(r, p).Exchange(ctx, q.Get("code"), oauth2.SetAuthURLParam("code_verifier", flow.Verifier))
	if err != nil {
		s.oidcFail(w, r, "token exchange failed: "+err.Error())
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		s.oidcFail(w, r, "the identity provider returned no id_token")
		return
	}
	idt, err := verifier.Verify(ctx, rawID)
	if err != nil {
		s.oidcFail(w, r, "id_token verification failed: "+err.Error())
		return
	}
	if idt.Nonce != flow.Nonce {
		s.oidcFail(w, r, "nonce mismatch")
		return
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		s.oidcFail(w, r, "unreadable claims")
		return
	}
	if ui, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
		extra := map[string]any{}
		if ui.Claims(&extra) == nil {
			for k, v := range extra {
				if _, ok := claims[k]; !ok {
					claims[k] = v
				}
			}
		}
	}
	str := func(k string) string { v, _ := claims[k].(string); return strings.TrimSpace(v) }
	sub, issuer := idt.Subject, idt.Issuer

	if d := s.cfg.OIDC.AllowedDomain; d != "" {
		got := str(claimOrgDomain)
		if got == "" {
			got = str(claimResourceOwnerDomain)
		}
		if !strings.EqualFold(got, d) {
			s.oidcFail(w, r, "your account does not belong to the allowed organisation")
			return
		}
	}
	role, roleOK := s.roleFromClaims(claims)
	if s.cfg.OIDC.RoleClaim != "" && !roleOK {
		s.oidcFail(w, r, "no Fundus role (admin, editor or viewer) is granted to your account")
		return
	}

	// Linking an existing, signed-in account.
	if flow.LinkUser != 0 {
		me := who(r)
		if me == nil || me.ID != flow.LinkUser {
			s.oidcFail(w, r, "sign in again before linking SSO")
			return
		}
		if err := s.st.LinkOIDC(r.Context(), me.ID, issuer, sub); err != nil {
			msg := "linking failed"
			if errors.Is(err, store.ErrConflict) {
				msg = "this SSO identity is already linked to another account"
			}
			s.oidcFail(w, r, msg)
			return
		}
		s.audit(r, "sso.linked", me.Username, issuer)
		s.oidcDone(w, r, "/settings?linked=1")
		return
	}

	u, err := s.st.GetUserByOIDC(r.Context(), issuer, sub)
	if errors.Is(err, store.ErrNotFound) {
		u, err = s.provisionOIDC(r, issuer, sub, claims, role, roleOK)
	}
	if err != nil {
		s.oidcFail(w, r, err.Error())
		return
	}
	if u.Disabled {
		s.oidcFail(w, r, "your account is disabled")
		return
	}
	// Keep profile and (optionally) role in sync with the IdP.
	patch := store.UserPatch{}
	if n := str("name"); n != "" && n != u.DisplayName {
		patch.DisplayName = &n
	}
	if e := str("email"); e != "" && e != u.Email {
		patch.Email = &e
	}
	if roleOK && role != u.Role {
		patch.Role = &role
	}
	if patch != (store.UserPatch{}) {
		if nu, err := s.st.UpdateUser(r.Context(), u.ID, patch); err == nil {
			u = nu
		}
	}
	if err := s.startSession(w, r, u, "oidc"); err != nil {
		s.oidcFail(w, r, "could not start a session")
		return
	}
	ret := flow.Return
	if ret == "" {
		ret = "/"
	}
	s.oidcDone(w, r, ret)
}

func (s *Server) oidcDone(w http.ResponseWriter, r *http.Request, to string) {
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/auth/oidc/", HttpOnly: true, MaxAge: -1})
	http.Redirect(w, r, to, http.StatusFound)
}

// provisionOIDC links the identity to an existing local account with the same
// username or e-mail, or creates a new account when auto-create is on.
func (s *Server) provisionOIDC(r *http.Request, issuer, sub string, claims map[string]any, role string, roleOK bool) (store.User, error) {
	str := func(k string) string { v, _ := claims[k].(string); return strings.TrimSpace(v) }
	email := str("email")
	uname := str("preferred_username")
	if uname == "" && email != "" {
		uname = strings.SplitN(email, "@", 2)[0]
	}
	// Zitadel's preferred_username is often the login name "user@org.domain".
	candidates := []string{uname}
	if i := strings.IndexByte(uname, '@'); i > 0 {
		candidates = append(candidates, uname[:i])
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if u, err := s.st.GetUserByUsername(r.Context(), c); err == nil && u.OIDCSubject == "" {
			if err := s.st.LinkOIDC(r.Context(), u.ID, issuer, sub); err != nil {
				return u, err
			}
			s.st.Audit(r.Context(), u.ID, "sso.linked", u.Username, "matched username", clientIP(r))
			return s.st.GetUser(r.Context(), u.ID)
		}
	}
	if email != "" && claims["email_verified"] == true {
		if u, err := s.st.GetUserByEmail(r.Context(), email); err == nil && u.OIDCSubject == "" {
			if err := s.st.LinkOIDC(r.Context(), u.ID, issuer, sub); err != nil {
				return u, err
			}
			s.st.Audit(r.Context(), u.ID, "sso.linked", u.Username, "matched verified e-mail", clientIP(r))
			return s.st.GetUser(r.Context(), u.ID)
		}
	}
	if !s.cfg.OIDC.AutoCreate {
		return store.User{}, errors.New("no Fundus account exists for you yet; ask an admin to create one")
	}
	if !roleOK {
		role = s.cfg.OIDC.DefaultRole
	}
	base := ""
	for _, c := range append([]string{candidates[len(candidates)-1]}, candidates...) {
		if usernameRe.MatchString(c) {
			base = c
			break
		}
	}
	if base == "" {
		sum := sha256.Sum256([]byte(sub))
		base = fmt.Sprintf("sso-%x", sum[:4])
	}
	name := base
	for i := 2; i < 50; i++ {
		u, err := s.st.CreateUser(r.Context(), store.NewUser{Username: name, DisplayName: str("name"), Email: email,
			Role: role, OIDCIssuer: issuer, OIDCSubject: sub})
		if err == nil {
			s.st.Audit(r.Context(), u.ID, "user.created", u.Username, "SSO auto-provisioned, role "+role, clientIP(r))
			return u, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return u, err
		}
		name = fmt.Sprintf("%s%d", base, i)
	}
	return store.User{}, errors.New("could not find a free username")
}

// roleFromClaims maps the configured role claim to a Fundus role. Accepted
// shapes: Zitadel's {"role": {...}} map, a list of strings, or one string.
// Role keys may be plain (admin) or prefixed (fundus-admin, fundus:admin).
func (s *Server) roleFromClaims(claims map[string]any) (string, bool) {
	if s.cfg.OIDC.RoleClaim == "" {
		return "", false
	}
	var keys []string
	switch v := claims[s.cfg.OIDC.RoleClaim].(type) {
	case map[string]any:
		for k := range v {
			keys = append(keys, k)
		}
	case []any:
		for _, x := range v {
			if str, ok := x.(string); ok {
				keys = append(keys, str)
			}
		}
	case string:
		keys = strings.Fields(strings.ReplaceAll(v, ",", " "))
	}
	have := map[string]bool{}
	for _, k := range keys {
		k = strings.ToLower(k)
		k = strings.TrimPrefix(strings.TrimPrefix(k, "fundus-"), "fundus:")
		have[k] = true
	}
	for _, r := range []string{"admin", "editor", "viewer"} {
		if have[r] {
			return r, true
		}
	}
	return "", false
}
