package server

// OAuth 2.1 authorization server for MCP clients such as ChatGPT, following
// the MCP authorization spec: protected resource metadata (RFC 9728),
// authorization server metadata (RFC 8414), dynamic client registration
// (RFC 7591), authorization code flow with mandatory PKCE (S256), rotating
// refresh tokens and revocation (RFC 7009).
//
// The user approves access on /oauth/authorize while signed in to the web
// UI. Access tokens are ordinary API tokens (scopes read or read+write,
// one hour) that belong to the approving user.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Lamisator/fundus/internal/store"
	"github.com/Lamisator/fundus/web"
)

var consentTmpl = template.Must(template.ParseFS(web.FS, "templates/consent.html"))

// isOAuthPublic lists the endpoints that OAuth clients call server to
// server or cross-origin, without a browser session.
func isOAuthPublic(p string) bool {
	switch p {
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server", "/oauth/token", "/oauth/register", "/oauth/revoke":
		return true
	}
	return false
}

// bearerChallenge is the WWW-Authenticate value that points MCP clients to
// the OAuth discovery document.
func (s *Server) bearerChallenge(r *http.Request, errCode string) string {
	v := `Bearer realm="fundus", resource_metadata="` + s.baseURL(r) + `/.well-known/oauth-protected-resource"`
	if errCode != "" {
		v += `, error="` + errCode + `"`
	}
	return v
}

// cors lets browser-based MCP clients (e.g. MCP Inspector) use the public
// OAuth endpoints.
func cors(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fn(w, r)
	}
}

func (s *Server) handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 base + "/mcp",
		"authorization_servers":    []string{base},
		"scopes_supported":         []string{"read", "write"},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            s.cfg.SiteName,
		"resource_documentation":   base + "/api-docs",
	})
}

func (s *Server) handleOAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         base,
		"authorization_endpoint":                         base + "/oauth/authorize",
		"token_endpoint":                                 base + "/oauth/token",
		"registration_endpoint":                          base + "/oauth/register",
		"revocation_endpoint":                            base + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_post", "client_secret_basic"},
		"revocation_endpoint_auth_methods_supported":     []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                               []string{"read", "write"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// validRedirectURI accepts https URLs, and http only on loopback addresses.
func validRedirectURI(raw string) bool {
	if len(raw) > 500 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// handleOAuthRegister implements dynamic client registration (RFC 7591).
func (s *Server) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientName    string   `json:"client_name"`
		RedirectURIs  []string `json:"redirect_uris"`
		GrantTypes    []string `json:"grant_types"`
		ResponseTypes []string `json:"response_types"`
		AuthMethod    string   `json:"token_endpoint_auth_method"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON body")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "1 to 10 redirect_uris are required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must use https (or http on localhost): "+u)
			return
		}
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type: "+g)
			return
		}
	}
	for _, rt := range in.ResponseTypes {
		if rt != "code" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported response_type: "+rt)
			return
		}
	}
	method := in.AuthMethod
	switch method {
	case "":
		method = "client_secret_basic" // RFC 7591 default
	case "none", "client_secret_post", "client_secret_basic":
	default:
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method: "+method)
		return
	}
	c, secret, err := s.st.RegisterOAuthClient(r.Context(), in.ClientName, in.RedirectURIs, method != "none")
	if errors.Is(err, store.ErrOAuthTooMany) {
		oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many registered clients, try again later")
		return
	}
	if err != nil {
		slog.Error("oauth register", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "internal server error")
		return
	}
	slog.Info("oauth client registered", "client_id", c.ID, "name", c.Name, "ip", clientIP(r))
	out := map[string]any{
		"client_id": c.ID, "client_id_issued_at": c.CreatedAt.Unix(), "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": method, "scope": "read write",
	}
	if secret != "" {
		out["client_secret"] = secret
		out["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, out)
}

// authzRequest holds validated authorization request parameters.
type authzRequest struct {
	Client        store.OAuthClient
	RedirectURI   string
	State         string
	CodeChallenge string
	WantsWrite    bool
	Resource      string
}

type authzError struct {
	redirect bool // false: show an error page, never redirect (unknown client or redirect_uri)
	code     string
	desc     string
}

func (s *Server) checkAuthorize(r *http.Request, q url.Values) (authzRequest, *authzError) {
	var a authzRequest
	c, err := s.st.GetOAuthClient(r.Context(), q.Get("client_id"))
	if err != nil {
		return a, &authzError{false, "invalid_client", "This app is not registered here (or its registration expired). Set up the connection again."}
	}
	a.Client = c
	a.RedirectURI = q.Get("redirect_uri")
	if a.RedirectURI == "" && len(c.RedirectURIs) == 1 {
		a.RedirectURI = c.RedirectURIs[0]
	}
	if !slices.Contains(c.RedirectURIs, a.RedirectURI) {
		return a, &authzError{false, "invalid_request", "The redirect address is not registered for this app."}
	}
	a.State = q.Get("state")
	if q.Get("response_type") != "code" {
		return a, &authzError{true, "unsupported_response_type", "only response_type=code is supported"}
	}
	a.CodeChallenge = q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || len(a.CodeChallenge) < 43 || len(a.CodeChallenge) > 128 {
		return a, &authzError{true, "invalid_request", "PKCE with code_challenge_method=S256 is required"}
	}
	for _, sc := range strings.Fields(q.Get("scope")) {
		if sc == "write" {
			a.WantsWrite = true
		}
	}
	a.Resource = q.Get("resource")
	if a.Resource != "" {
		base := s.baseURL(r)
		if a.Resource != base && !strings.HasPrefix(a.Resource, base+"/") {
			return a, &authzError{true, "invalid_target", "unknown resource"}
		}
	}
	return a, nil
}

func redirectWith(redirectURI string, params map[string]string) string {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) errorRedirect(r *http.Request, a authzRequest, e *authzError) string {
	return redirectWith(a.RedirectURI, map[string]string{"error": e.code, "error_description": e.desc, "state": a.State, "iss": s.baseURL(r)})
}

func (s *Server) renderConsent(w http.ResponseWriter, status int, data map[string]any) {
	data["Site"] = s.cfg.SiteName
	var buf bytes.Buffer
	if err := consentTmpl.Execute(&buf, data); err != nil {
		slog.Error("consent template", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// handleOAuthAuthorizePage shows the consent page. Without a session it
// sends the browser to the login page, which comes back here afterwards.
func (s *Server) handleOAuthAuthorizePage(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	if p == nil || p.Via != "session" {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	q := r.URL.Query()
	a, e := s.checkAuthorize(r, q)
	if e != nil {
		if e.redirect {
			http.Redirect(w, r, s.errorRedirect(r, a, e), http.StatusFound)
			return
		}
		s.renderConsent(w, http.StatusBadRequest, map[string]any{"Error": e.desc})
		return
	}
	params, _ := json.Marshal(map[string]string{
		"client_id": a.Client.ID, "redirect_uri": a.RedirectURI, "state": a.State, "response_type": "code",
		"code_challenge": a.CodeChallenge, "code_challenge_method": "S256", "scope": q.Get("scope"), "resource": a.Resource,
	})
	host := a.RedirectURI
	if u, err := url.Parse(a.RedirectURI); err == nil {
		host = u.Host
	}
	name := p.DisplayName
	if name == "" {
		name = p.Username
	}
	s.renderConsent(w, http.StatusOK, map[string]any{
		"ClientName": a.Client.Name, "RedirectHost": host, "User": name,
		"WantsWrite": a.WantsWrite && p.CanEdit(), "Params": string(params),
	})
}

// handleOAuthAuthorizeDecision takes the user's decision from the consent
// page (session + CSRF checked like any UI request) and returns where the
// browser goes next.
func (s *Server) handleOAuthAuthorizeDecision(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	if p.Via != "session" {
		apiError(w, http.StatusForbidden, "forbidden", "only available to browser sessions")
		return
	}
	var in struct {
		Params     map[string]string `json:"params"`
		Approve    bool              `json:"approve"`
		AllowWrite bool              `json:"allow_write"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	q := url.Values{}
	for k, v := range in.Params {
		q.Set(k, v)
	}
	a, e := s.checkAuthorize(r, q)
	if e != nil {
		if !e.redirect {
			badRequest(w, e.desc)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"redirect": s.errorRedirect(r, a, e)})
		return
	}
	if !in.Approve {
		s.audit(r, "oauth.denied", a.Client.ID, a.Client.Name)
		writeJSON(w, http.StatusOK, map[string]string{"redirect": s.errorRedirect(r, a, &authzError{true, "access_denied", "the user denied access"})})
		return
	}
	scopes := []string{"read"}
	if a.WantsWrite && in.AllowWrite && p.CanEdit() {
		scopes = append(scopes, "write")
	}
	code, err := s.st.CreateOAuthCode(r.Context(), store.OAuthCode{ClientID: a.Client.ID, UserID: p.ID, RedirectURI: a.RedirectURI,
		CodeChallenge: a.CodeChallenge, Scopes: scopes, Resource: a.Resource})
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "oauth.granted", a.Client.ID, a.Client.Name+" ("+strings.Join(scopes, ",")+")")
	writeJSON(w, http.StatusOK, map[string]string{"redirect": redirectWith(a.RedirectURI, map[string]string{"code": code, "state": a.State, "iss": s.baseURL(r)})})
}

// clientAuth reads the client credentials (HTTP Basic or form fields) and
// checks the secret of confidential clients.
func (s *Server) clientAuth(r *http.Request) (string, error) {
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id == "" {
		return "", store.ErrOAuthInvalidClient
	}
	if err := s.st.CheckOAuthClientSecret(r.Context(), id, secret); err != nil {
		return "", err
	}
	return id, nil
}

func pkceMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "expected an application/x-www-form-urlencoded body")
		return
	}
	clientID, err := s.clientAuth(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="fundus"`)
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	var tok store.OAuthTokens
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		c, err := s.st.ConsumeOAuthCode(r.Context(), r.PostForm.Get("code"))
		if err != nil || c.ClientID != clientID {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the code is invalid, expired or already used")
			return
		}
		if ru := r.PostForm.Get("redirect_uri"); ru != "" && ru != c.RedirectURI {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
			return
		}
		if !pkceMatches(r.PostForm.Get("code_verifier"), c.CodeChallenge) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
		tok, err = s.st.IssueOAuthTokens(r.Context(), clientID, c.UserID, c.Scopes)
		if err != nil {
			slog.Error("oauth issue", "err", err)
			oauthError(w, http.StatusInternalServerError, "server_error", "internal server error")
			return
		}
	case "refresh_token":
		readOnly := r.PostForm.Get("scope") != "" && !slices.Contains(strings.Fields(r.PostForm.Get("scope")), "write")
		tok, err = s.st.RefreshOAuthTokens(r.Context(), clientID, r.PostForm.Get("refresh_token"), func(u store.User, granted []string) []string {
			if readOnly || !u.CanEdit() {
				return []string{"read"}
			}
			return granted
		})
		if errors.Is(err, store.ErrOAuthInvalidGrant) || errors.Is(err, store.ErrOAuthInvalidClient) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is invalid or expired")
			return
		}
		if err != nil {
			slog.Error("oauth refresh", "err", err)
			oauthError(w, http.StatusInternalServerError, "server_error", "internal server error")
			return
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "supported: authorization_code, refresh_token")
		return
	}
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, tok)
}

func (s *Server) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "expected an application/x-www-form-urlencoded body")
		return
	}
	clientID, err := s.clientAuth(r)
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	if err := s.st.RevokeOAuthToken(r.Context(), clientID, r.PostForm.Get("token")); err != nil {
		slog.Error("oauth revoke", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "internal server error")
		return
	}
	w.WriteHeader(http.StatusOK) // RFC 7009: also for unknown tokens
}

// ---------------------------------------------------------------- connected apps (settings)

func (s *Server) handleListOAuthApps(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListOAuthGrants(r.Context(), who(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleDeleteOAuthApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.st.DeleteOAuthGrant(r.Context(), who(r).ID, id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "oauth.disconnected", id, "")
	w.WriteHeader(http.StatusNoContent)
}
