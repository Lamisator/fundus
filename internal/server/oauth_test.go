package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Lamisator/fundus/internal/store"
)

const testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk-and-some-more"

func testChallenge() string {
	sum := sha256.Sum256([]byte(testVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorize runs the consent step as the signed-in browser c and returns
// the redirect URL the consent page would follow.
func (e *env) authorize(c *client, clientID, scope string, approve, allowWrite bool) *url.URL {
	e.t.Helper()
	params := map[string]string{"client_id": clientID, "redirect_uri": "https://app.example/cb", "response_type": "code",
		"state": "xyz", "code_challenge": testChallenge(), "code_challenge_method": "S256", "scope": scope}
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	e.must(c.do("GET", "/oauth/authorize?"+q.Encode(), "", nil), 200)
	var out struct{ Redirect string }
	e.decode(c.do("POST", "/oauth/authorize", "", map[string]any{"params": params, "approve": approve, "allow_write": allowWrite}), 200, &out)
	u, err := url.Parse(out.Redirect)
	if err != nil || u.Host != "app.example" || u.Query().Get("state") != "xyz" || u.Query().Get("iss") != e.srv.URL {
		e.t.Fatalf("bad redirect %q", out.Redirect)
	}
	return u
}

func (e *env) token(c *client, form url.Values) (int, map[string]any) {
	e.t.Helper()
	resp := c.do("POST", "/oauth/token", "", form.Encode())
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestOAuthFlow(t *testing.T) {
	e := setup(t, nil)
	anon := e.client()

	// discovery: 401 on /mcp points to the resource metadata
	resp := anon.do("POST", "/mcp", "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="`+e.srv.URL+`/.well-known/oauth-protected-resource"`) {
		t.Fatalf("mcp 401: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	var prm, asm map[string]any
	e.decode(anon.do("GET", "/.well-known/oauth-protected-resource", "", nil), 200, &prm)
	if prm["resource"] != e.srv.URL+"/mcp" {
		t.Fatalf("resource metadata: %v", prm)
	}
	e.decode(anon.do("GET", "/.well-known/oauth-authorization-server", "", nil), 200, &asm)
	if asm["token_endpoint"] != e.srv.URL+"/oauth/token" || asm["registration_endpoint"] != e.srv.URL+"/oauth/register" {
		t.Fatalf("server metadata: %v", asm)
	}
	pre := anon.do("OPTIONS", "/oauth/token", "", nil)
	pre.Body.Close()
	if pre.StatusCode != 204 || pre.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("CORS preflight: %d", pre.StatusCode)
	}

	// registration
	e.must(anon.do("POST", "/oauth/register", "", map[string]any{"redirect_uris": []string{"http://evil.example/cb"}}), 400)
	var reg map[string]any
	e.decode(anon.do("POST", "/oauth/register", "", map[string]any{"client_name": "ChatGPT",
		"redirect_uris": []string{"https://app.example/cb"}, "token_endpoint_auth_method": "none"}), 201, &reg)
	cid, _ := reg["client_id"].(string)
	if cid == "" || reg["client_secret"] != nil {
		t.Fatalf("public client: %v", reg)
	}

	// authorize without a session goes to the login page
	resp = anon.do("GET", "/oauth/authorize?client_id="+cid, "", nil)
	resp.Body.Close()
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=%2Foauth%2Fauthorize") {
		t.Fatalf("authorize without session: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	browser := e.client()
	e.must(browser.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "admin-password-1"}), 200)
	// unknown redirect_uri: error page, no redirect
	e.must(browser.do("GET", "/oauth/authorize?response_type=code&client_id="+cid+"&redirect_uri=https://evil.example/cb", "", nil), 400)
	// missing PKCE: error redirect back to the app
	resp = browser.do("GET", "/oauth/authorize?response_type=code&client_id="+cid+"&redirect_uri=https://app.example/cb&state=s", "", nil)
	resp.Body.Close()
	if loc, _ := url.Parse(resp.Header.Get("Location")); resp.StatusCode != 302 || loc.Query().Get("error") != "invalid_request" || loc.Query().Get("state") != "s" {
		t.Fatalf("missing PKCE: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// cross-site decision is refused
	req, _ := http.NewRequest("POST", e.srv.URL+"/oauth/authorize", strings.NewReader("approve=true"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if r, _ := browser.h.Do(req); r.StatusCode != 403 {
		t.Fatalf("CSRF: %d", r.StatusCode)
	}

	// deny
	if u := e.authorize(browser, cid, "read write", false, false); u.Query().Get("error") != "access_denied" {
		t.Fatalf("deny: %v", u)
	}

	// approve read+write, exchange the code
	code := e.authorize(browser, cid, "read write", true, true).Query().Get("code")
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {cid}, "code": {code},
		"redirect_uri": {"https://app.example/cb"}, "code_verifier": {"wrong-verifier-wrong-verifier-wrong-verifier-x"}}
	if st, m := e.token(anon, form); st != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("bad verifier: %d %v", st, m)
	}
	form.Set("code_verifier", testVerifier)
	if st, m := e.token(anon, form); st != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("code must be single use even after a failed exchange: %d %v", st, m)
	}
	code = e.authorize(browser, cid, "read write", true, true).Query().Get("code")
	form.Set("code", code)
	st, tok := e.token(anon, form)
	if st != 200 || tok["scope"] != "read write" || tok["token_type"] != "Bearer" {
		t.Fatalf("token: %d %v", st, tok)
	}
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)

	// the access token works on MCP with write tools
	var rpc struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	e.decode(anon.do("POST", "/mcp", access, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}), 200, &rpc)
	if len(rpc.Result.Tools) != 8 {
		t.Fatalf("tools = %d", len(rpc.Result.Tools))
	}
	// OAuth tokens are not listed as API tokens but as a connected app
	var toks []store.APIToken
	e.decode(browser.do("GET", "/api/v1/me/tokens", "", nil), 200, &toks)
	for _, k := range toks {
		if strings.Contains(k.Name, "OAuth") {
			t.Fatalf("OAuth token listed: %+v", k)
		}
	}
	var apps []store.OAuthGrant
	e.decode(browser.do("GET", "/api/v1/me/oauth-apps", "", nil), 200, &apps)
	if len(apps) != 1 || apps[0].ClientName != "ChatGPT" || strings.Join(apps[0].Scopes, ",") != "read,write" {
		t.Fatalf("apps: %+v", apps)
	}

	// refresh rotates; narrowing to read drops write
	st, tok2 := e.token(anon, url.Values{"grant_type": {"refresh_token"}, "client_id": {cid}, "refresh_token": {refresh}, "scope": {"read"}})
	if st != 200 || tok2["scope"] != "read" {
		t.Fatalf("refresh: %d %v", st, tok2)
	}
	if st, _ := e.token(anon, url.Values{"grant_type": {"refresh_token"}, "client_id": {cid}, "refresh_token": {refresh}}); st != 400 {
		t.Fatalf("old refresh token still works: %d", st)
	}
	e.decode(anon.do("POST", "/mcp", tok2["access_token"].(string), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}), 200, &rpc)
	if len(rpc.Result.Tools) != 4 {
		t.Fatalf("read-only tools = %d", len(rpc.Result.Tools))
	}

	// revoke the first access token
	e.must(anon.do("POST", "/oauth/revoke", "", url.Values{"client_id": {cid}, "token": {access}}.Encode()), 200)
	resp = anon.do("POST", "/mcp", access, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("revoked token: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	// disconnecting removes every token of the app
	del, _ := http.NewRequest("DELETE", e.srv.URL+"/api/v1/me/oauth-apps/"+cid, nil)
	del.Header.Set("X-Requested-With", "fundus")
	if r, err := browser.h.Do(del); err != nil || r.StatusCode != 204 {
		t.Fatalf("disconnect: %v %v", err, r)
	}
	e.must(anon.do("POST", "/mcp", tok2["access_token"].(string), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}), 401)
	if st, _ := e.token(anon, url.Values{"grant_type": {"refresh_token"}, "client_id": {cid}, "refresh_token": {tok2["refresh_token"].(string)}}); st == 200 {
		t.Fatal("refresh after disconnect")
	}
	e.decode(browser.do("GET", "/api/v1/me/oauth-apps", "", nil), 200, &apps)
	if len(apps) != 0 {
		t.Fatalf("apps after disconnect: %+v", apps)
	}
}

func TestOAuthConfidentialClientAndViewer(t *testing.T) {
	e := setup(t, nil)
	anon := e.client()
	e.must(anon.do("POST", "/api/v1/users", e.admin, map[string]any{"username": "vera", "password": "viewer-password-1", "role": "viewer"}), 201)

	// no token_endpoint_auth_method = client_secret_basic (RFC 7591 default)
	var reg map[string]any
	e.decode(anon.do("POST", "/oauth/register", "", map[string]any{"client_name": "Secret app", "redirect_uris": []string{"https://app.example/cb"}}), 201, &reg)
	cid, secret := reg["client_id"].(string), reg["client_secret"].(string)
	if secret == "" || reg["token_endpoint_auth_method"] != "client_secret_basic" {
		t.Fatalf("confidential client: %v", reg)
	}

	viewer := e.client()
	e.must(viewer.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "vera", "password": "viewer-password-1"}), 200)
	code := e.authorize(viewer, cid, "read write", true, true).Query().Get("code")

	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {testVerifier}}
	send := func(user, pass string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", e.srv.URL+"/oauth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(user, pass)
		resp, err := anon.h.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	if st, m := send(cid, "wrong"); st != 401 || m["error"] != "invalid_client" {
		t.Fatalf("wrong secret: %d %v", st, m)
	}
	st, tok := send(cid, secret)
	if st != 200 || tok["scope"] != "read" { // a viewer never gets write
		t.Fatalf("viewer token: %d %v", st, tok)
	}
	var me struct{ User store.User }
	e.decode(anon.do("GET", "/api/v1/me", tok["access_token"].(string), nil), 200, &me)
	if me.User.Username != "vera" {
		t.Fatalf("token belongs to %q", me.User.Username)
	}
	e.must(anon.do("POST", "/api/v1/entries", tok["access_token"].(string), map[string]any{"title": "x"}), 403)
}
