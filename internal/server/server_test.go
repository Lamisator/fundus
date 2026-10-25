package server

// Integration tests against a real PostgreSQL. They run when
// FUNDUS_TEST_DATABASE_URL points at a server where the user may create
// databases, e.g.
//
//	FUNDUS_TEST_DATABASE_URL=postgres://postgres@localhost:5432/postgres?sslmode=disable go test ./...
//
// Each run creates and drops its own database.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Lamisator/fundus/internal/config"
	"github.com/Lamisator/fundus/internal/db"
	"github.com/Lamisator/fundus/internal/store"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type env struct {
	t     *testing.T
	srv   *httptest.Server
	s     *Server
	admin string // admin API token
}

func setup(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()
	base := os.Getenv("FUNDUS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("FUNDUS_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	name := fmt.Sprintf("fundus_test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DatabaseURL: u.String(), DataDir: t.TempDir(), SiteName: "Fundus", PasswordLogin: true,
		SessionTTL: time.Hour, MaxUploadMB: 5, AdminUser: "admin", AdminPassword: "admin-password-1",
		OIDC: config.OIDC{Scopes: []string{"openid", "profile", "email"}, AutoCreate: true, DefaultRole: "editor"}}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg, store.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, s: s, srv: httptest.NewServer(s.Handler())}
	t.Cleanup(e.srv.Close)
	c := e.client()
	e.must(c.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "admin-password-1"}), 200)
	var tok store.APIToken
	e.decode(c.do("POST", "/api/v1/me/tokens", "", map[string]any{"name": "t", "scopes": []string{"read", "write", "admin"}}), 201, &tok)
	e.admin = tok.Token
	return e
}

type client struct {
	e *env
	h *http.Client
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, h: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// do sends a request; body may be nil, a string (sent as is) or a value (JSON).
func (c *client) do(method, path, token string, body any) *http.Response {
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd, ct = strings.NewReader(b), "application/x-www-form-urlencoded"
	default:
		j, _ := json.Marshal(b)
		rd, ct = bytes.NewReader(j), "application/json"
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.h.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	return resp
}

func (e *env) must(resp *http.Response, status int) []byte {
	e.t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		e.t.Fatalf("%s %s: got %d, want %d: %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, status, b)
	}
	return b
}

func (e *env) decode(resp *http.Response, status int, v any) {
	e.t.Helper()
	b := e.must(resp, status)
	if err := json.Unmarshal(b, v); err != nil {
		e.t.Fatalf("decode %s: %v", b, err)
	}
}

func TestEntriesLifecycle(t *testing.T) {
	e := setup(t, nil)
	c := e.client()

	var created store.Entry
	e.decode(c.do("POST", "/api/v1/entries", e.admin, map[string]any{
		"kind": "problem", "title": "nginx: 413 Request Entity Too Large", "error_text": "413 Request Entity Too Large\nnginx/1.27.2",
		"tags": []string{"Nginx", " HTTP ", "nginx"}, "fields": map[string]string{"version": "1.27.2", "empty": " "},
		"body":      "See [[999]] and uploads.",
		"solutions": []map[string]any{{"title": "Raise the limit", "body": "client_max_body_size 100m;", "accepted": true}},
	}), 201, &created)
	if created.Status != "solved" || created.SolutionCount != 1 {
		t.Fatalf("accepted inline solution should solve the problem: %+v", created)
	}
	if strings.Join(created.Tags, ",") != "nginx,http" || len(created.Fields) != 1 {
		t.Fatalf("tags/fields not normalised: %v %v", created.Tags, created.Fields)
	}
	id := fmt.Sprint(created.ID)

	// validation
	e.must(c.do("POST", "/api/v1/entries", e.admin, map[string]any{"kind": "nope", "title": "x"}), 400)
	e.must(c.do("POST", "/api/v1/entries", e.admin, map[string]any{"kind": "note", "title": "x", "status": "open"}), 400)
	e.must(c.do("POST", "/api/v1/entries", e.admin, map[string]any{"title": "   "}), 400)

	// optimistic locking and no-op saves
	var upd store.Entry
	e.decode(c.do("PATCH", "/api/v1/entries/"+id, e.admin, map[string]any{"summary": "Uploads fail", "version": 1}), 200, &upd)
	if upd.Version != 2 {
		t.Fatalf("version = %d", upd.Version)
	}
	e.must(c.do("PATCH", "/api/v1/entries/"+id, e.admin, map[string]any{"summary": "stale", "version": 1}), 409)
	e.decode(c.do("PATCH", "/api/v1/entries/"+id, e.admin, map[string]any{"summary": "Uploads fail", "tags": []string{"nginx", "HTTP"}}), 200, &upd)
	if upd.Version != 2 {
		t.Fatalf("no-op save created a revision: version %d", upd.Version)
	}
	var revs []store.Revision
	e.decode(c.do("GET", "/api/v1/entries/"+id+"/revisions", e.admin, nil), 200, &revs)
	if len(revs) != 2 {
		t.Fatalf("revisions = %d", len(revs))
	}
	// switching to a kind without statuses clears the status
	e.decode(c.do("PATCH", "/api/v1/entries/"+id, e.admin, map[string]any{"kind": "note"}), 200, &upd)
	if upd.Status != "" {
		t.Fatalf("status should be cleared, got %q", upd.Status)
	}
	e.decode(c.do("POST", "/api/v1/entries/"+id+"/revisions/2/restore", e.admin, nil), 200, &upd)
	if upd.Kind != "problem" || upd.Status != "solved" || upd.Version != 4 {
		t.Fatalf("restore: %+v", upd)
	}

	// search: prefix, phrase and pasted log line
	for _, q := range []string{"entit too lar", "413", `"request entity"`, "2026/10/23 [error] client intended to send too large body: 413 Request Entity Too Large"} {
		var res store.Result
		e.decode(c.do("GET", "/api/v1/search?q="+url.QueryEscape(q), e.admin, nil), 200, &res)
		if res.Total == 0 || res.Items[0].ID != created.ID {
			t.Fatalf("search %q: %+v", q, res)
		}
		if res.Facets == nil || len(res.Facets.Kinds) == 0 {
			t.Fatalf("search %q: no facets", q)
		}
	}
	var none store.Result
	e.decode(c.do("GET", "/api/v1/entries?q=kubernetes", e.admin, nil), 200, &none)
	if none.Total != 0 {
		t.Fatalf("unexpected hits: %+v", none)
	}

	// detail with expansion and rendered body
	var d entryDetail
	e.decode(c.do("GET", "/api/v1/entries/"+id+"?expand=all", e.admin, nil), 200, &d)
	if len(d.Solutions) != 1 || !strings.Contains(d.BodyHTML, `href="/e/999"`) {
		t.Fatalf("detail: %+v", d)
	}

	// links + backlinks
	var other store.Entry
	e.decode(c.do("POST", "/api/v1/entries", e.admin, map[string]any{"kind": "howto", "title": "Reload nginx", "body": fmt.Sprintf("After [[%d]] reload.", created.ID)}), 201, &other)
	e.must(c.do("POST", "/api/v1/entries/"+id+"/links", e.admin, map[string]any{"to": other.ID, "rel": "related"}), 201)
	var links []store.Link
	e.decode(c.do("GET", "/api/v1/entries/"+id+"/links", e.admin, nil), 200, &links)
	if len(links) != 2 {
		t.Fatalf("links: %+v", links)
	}

	// markdown export
	md := string(e.must(c.do("GET", "/api/v1/entries/"+id+"/markdown", e.admin, nil), 200))
	if !strings.HasPrefix(md, "---\nid: ") || !strings.Contains(md, "## Raise the limit ✓") {
		t.Fatalf("markdown: %s", md)
	}

	// trash, restore, purge
	e.must(c.do("DELETE", "/api/v1/entries/"+id, e.admin, nil), 204)
	e.decode(c.do("GET", "/api/v1/entries?trash=1", e.admin, nil), 200, &none)
	if none.Total != 1 {
		t.Fatalf("trash: %+v", none)
	}
	e.must(c.do("POST", "/api/v1/entries/"+id+"/restore", e.admin, nil), 200)
	e.must(c.do("DELETE", "/api/v1/entries/"+id+"?purge=1", e.admin, nil), 204)
	e.must(c.do("GET", "/api/v1/entries/"+id, e.admin, nil), 404)
}

func TestPermissionsAndCSRF(t *testing.T) {
	e := setup(t, nil)
	c := e.client()
	e.must(c.do("POST", "/api/v1/users", e.admin, map[string]any{"username": "vera", "password": "viewer-password", "role": "viewer"}), 201)

	// read-only token cannot write
	var ro store.APIToken
	e.decode(c.do("POST", "/api/v1/me/tokens", "", nil), 401, &map[string]any{})
	vc := e.client()
	e.must(vc.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "vera", "password": "viewer-password"}), 200)
	e.must(vc.do("POST", "/api/v1/me/tokens", "", map[string]any{"name": "w", "scopes": []string{"write"}}), 403)
	e.decode(vc.do("POST", "/api/v1/me/tokens", "", map[string]any{"name": "r"}), 201, &ro)
	e.must(c.do("POST", "/api/v1/entries", ro.Token, map[string]any{"title": "x"}), 403)
	e.must(c.do("GET", "/api/v1/entries", ro.Token, nil), 200)
	e.must(c.do("GET", "/api/v1/users", ro.Token, nil), 403)
	// tokens cannot manage tokens
	e.must(c.do("GET", "/api/v1/me/tokens", e.admin, nil), 403)
	e.must(c.do("GET", "/api/v1/entries", "fdk_wrong", nil), 401)

	// cookie session: a form-encoded cross-site POST is refused, JSON is fine
	ac := e.client()
	e.must(ac.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "admin-password-1"}), 200)
	e.must(ac.do("POST", "/api/v1/entries", "", "title=evil"), 403)
	e.must(ac.do("POST", "/api/v1/entries", "", map[string]any{"title": "fine"}), 201)

	// login rate limit
	bc := e.client()
	for i := 0; i < 10; i++ {
		e.must(bc.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "wrong"}), 401)
	}
	e.must(bc.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "admin-password-1"}), 429)

	// last admin cannot be demoted
	var me struct{ User store.User }
	e.decode(c.do("GET", "/api/v1/me", e.admin, nil), 200, &me)
	e.must(c.do("PATCH", fmt.Sprintf("/api/v1/users/%d", me.User.ID), e.admin, map[string]any{"role": "editor"}), 400)
}

func TestAttachmentsAndShares(t *testing.T) {
	e := setup(t, nil)
	c := e.client()
	var en store.Entry
	e.decode(c.do("POST", "/api/v1/entries", e.admin, map[string]any{"title": "With file"}), 201, &en)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "../../etc/screen shot.png")
	fw.Write([]byte("\x89PNG\r\n\x1a\nfakeimage"))
	mw.WriteField("entry_id", fmt.Sprint(en.ID))
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+e.admin)
	resp, _ := http.DefaultClient.Do(req)
	var att store.Attachment
	e.decode(resp, 201, &att)
	if att.Filename != "screen shot.png" || att.ContentType != "image/png" {
		t.Fatalf("attachment: %+v", att)
	}
	body := fmt.Sprintf("![shot](%s)", att.URL)
	e.must(c.do("PATCH", fmt.Sprint("/api/v1/entries/", en.ID), e.admin, map[string]any{"body": body}), 200)

	anon := e.client()
	e.must(anon.do("GET", att.URL, "", nil), 401)
	e.must(c.do("GET", att.URL, e.admin, nil), 200)

	var sh store.Share
	e.decode(c.do("POST", fmt.Sprint("/api/v1/entries/", en.ID, "/shares"), e.admin, nil), 201, &sh)
	page := string(e.must(anon.do("GET", "/s/"+sh.Token, "", nil), 200))
	if !strings.Contains(page, "With file") || !strings.Contains(page, "?share="+sh.Token) {
		t.Fatalf("share page: %s", page)
	}
	e.must(anon.do("GET", att.URL+"?share="+sh.Token, "", nil), 200)
	e.must(anon.do("GET", att.URL+"?share=wrong", "", nil), 401)
	e.must(c.do("DELETE", fmt.Sprint("/api/v1/entries/", en.ID, "/shares/", sh.Token), e.admin, nil), 204)
	e.must(anon.do("GET", "/s/"+sh.Token, "", nil), 404)
}

func TestMCP(t *testing.T) {
	e := setup(t, nil)
	c := e.client()
	rpc := func(method string, params any) map[string]any {
		var out map[string]any
		e.decode(c.do("POST", "/mcp", e.admin, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}), 200, &out)
		if out["error"] != nil {
			t.Fatalf("%s: %v", method, out["error"])
		}
		return out["result"].(map[string]any)
	}
	init := rpc("initialize", map[string]any{"protocolVersion": "2025-03-26"})
	if init["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize: %v", init)
	}
	e.must(c.do("POST", "/mcp", e.admin, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}), 202)
	tools := rpc("tools/list", nil)["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("tools: %d", len(tools))
	}
	text := func(r map[string]any) string { return r["content"].([]any)[0].(map[string]any)["text"].(string) }
	out := rpc("tools/call", map[string]any{"name": "create_entry", "arguments": map[string]any{
		"title": "Docker: permission denied on /var/run/docker.sock", "error_text": "dial unix /var/run/docker.sock: connect: permission denied",
		"tags": []string{"docker"}, "solution": "usermod -aG docker $USER"}})
	if !strings.Contains(text(out), "Created entry") {
		t.Fatalf("create: %v", out)
	}
	out = rpc("tools/call", map[string]any{"name": "search_knowledge", "arguments": map[string]any{"query": "docker.sock permission"}})
	if !strings.Contains(text(out), "[1] Docker") {
		t.Fatalf("search: %v", text(out))
	}
	out = rpc("tools/call", map[string]any{"name": "get_entry", "arguments": map[string]any{"id": 1}})
	if !strings.Contains(text(out), "usermod -aG docker") || !strings.Contains(text(out), "status: solved") {
		t.Fatalf("get: %v", text(out))
	}
	out = rpc("tools/call", map[string]any{"name": "update_entry", "arguments": map[string]any{"id": 1, "status": "workaround"}})
	if !strings.Contains(text(out), "version 2") {
		t.Fatalf("update: %v", text(out))
	}
	e.must(c.do("POST", "/mcp", "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}), 401)
}

// ---------------------------------------------------------------- OIDC

type fakeIdP struct {
	srv     *httptest.Server
	key     *rsa.PrivateKey
	claims  map[string]any // id token extras
	ui      map[string]any // userinfo
	nonce   string
	scopes  string
	prompt  string
	exchErr bool
}

func newIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeIdP{key: k, claims: map[string]any{}, ui: map[string]any{}}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"userinfo_endpoint": p.srv.URL + "/userinfo", "jwks_uri": p.srv.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		c := map[string]any{"iss": p.srv.URL, "sub": "user-123", "aud": "fundus-client", "exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(), "nonce": p.nonce}
		for k, v := range p.claims {
			c[k] = v
		}
		payload, _ := json.Marshal(c)
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: k, KeyID: "k1"}}, nil)
		jws, _ := signer.Sign(payload)
		idt, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": idt})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(p.ui)
	})
	return p
}

// login walks through /auth/oidc/login → IdP → /auth/oidc/callback.
func (p *fakeIdP) login(t *testing.T, e *env, c *client, extra string) *http.Response {
	resp := c.do("GET", "/auth/oidc/login"+extra, "", nil)
	if resp.StatusCode != 302 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("authorize URL: %s", loc)
	}
	p.nonce, p.scopes, p.prompt = q.Get("nonce"), q.Get("scope"), q.Get("prompt")
	return c.do("GET", "/auth/oidc/callback?code=good-code&state="+url.QueryEscape(q.Get("state")), "", nil)
}

func TestOIDC(t *testing.T) {
	idp := newIdP(t)
	e := setup(t, func(c *config.Config) {
		c.OIDC.Issuer, c.OIDC.ClientID = idp.srv.URL, "fundus-client"
		c.OIDC.RoleClaim = "urn:zitadel:iam:org:project:roles"
		c.OIDC.AllowedDomain = "example.com"
		c.OIDC.Prompt = "select_account"
	})
	// Zitadel style: profile only in userinfo, roles + org domain in the token.
	idp.claims = map[string]any{
		"urn:zitadel:iam:org:project:roles":  map[string]any{"viewer": map[string]any{"1": "example.com"}},
		"urn:zitadel:iam:org:domain:primary": "example.com",
	}
	idp.ui = map[string]any{"sub": "user-123", "preferred_username": "dana@example.com", "name": "Dana Doe", "email": "dana@example.com", "email_verified": true}

	c := e.client()
	resp := idp.login(t, e, c, "?return=/e/1")
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/e/1" {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback: %d %s %s", resp.StatusCode, resp.Header.Get("Location"), b)
	}
	if idp.prompt != "select_account" {
		t.Fatalf("prompt = %q, want select_account (account picker)", idp.prompt)
	}
	if !strings.Contains(idp.scopes, "urn:zitadel:iam:org:domain:primary:example.com") || !strings.Contains(idp.scopes, "urn:zitadel:iam:org:project:roles") {
		t.Fatalf("scopes: %s", idp.scopes)
	}
	var me struct{ User store.User }
	e.decode(c.do("GET", "/api/v1/me", "", nil), 200, &me)
	if me.User.Username != "dana" || me.User.Role != "viewer" || me.User.DisplayName != "Dana Doe" || !me.User.SSO {
		t.Fatalf("provisioned user: %+v", me.User)
	}

	// role change in the IdP is synced on the next login
	idp.claims["urn:zitadel:iam:org:project:roles"] = map[string]any{"fundus-editor": map[string]any{}}
	c2 := e.client()
	idp.login(t, e, c2, "")
	e.decode(c2.do("GET", "/api/v1/me", "", nil), 200, &me)
	if me.User.Role != "editor" {
		t.Fatalf("role not synced: %s", me.User.Role)
	}

	// wrong organisation is refused
	idp.claims["urn:zitadel:iam:org:domain:primary"] = "evil.org"
	c3 := e.client()
	resp = idp.login(t, e, c3, "")
	if !strings.Contains(resp.Header.Get("Location"), "/login?error=") {
		t.Fatalf("foreign org should be refused: %s", resp.Header.Get("Location"))
	}
	e.must(c3.do("GET", "/api/v1/me", "", nil), 401)

	// no role at all is refused
	idp.claims["urn:zitadel:iam:org:domain:primary"] = "example.com"
	idp.claims["urn:zitadel:iam:org:project:roles"] = map[string]any{}
	resp = idp.login(t, e, e.client(), "")
	if !strings.Contains(resp.Header.Get("Location"), "error=") {
		t.Fatalf("missing role should be refused")
	}

	// tampered state
	c4 := e.client()
	c4.do("GET", "/auth/oidc/login", "", nil)
	resp = c4.do("GET", "/auth/oidc/callback?code=good-code&state=forged", "", nil)
	if !strings.Contains(resp.Header.Get("Location"), "error=") {
		t.Fatalf("forged state accepted")
	}
}

func TestOIDCLinksExistingUser(t *testing.T) {
	idp := newIdP(t)
	e := setup(t, func(c *config.Config) {
		c.OIDC.Issuer, c.OIDC.ClientID = idp.srv.URL, "fundus-client"
		c.OIDC.AutoCreate = false
	})
	idp.ui = map[string]any{"preferred_username": "nobody"}
	resp := idp.login(t, e, e.client(), "")
	if !strings.Contains(resp.Header.Get("Location"), "error=") {
		t.Fatalf("auto-create off: unknown user should be refused")
	}
	// the bootstrap admin is linked by username
	idp.ui = map[string]any{"preferred_username": "admin", "email": "admin@example.com"}
	c := e.client()
	resp = idp.login(t, e, c, "")
	if resp.Header.Get("Location") != "/" {
		t.Fatalf("link by username failed: %s", resp.Header.Get("Location"))
	}
	var me struct{ User store.User }
	e.decode(c.do("GET", "/api/v1/me", "", nil), 200, &me)
	if me.User.Username != "admin" || me.User.Role != "admin" || !me.User.SSO {
		t.Fatalf("linked user: %+v", me.User)
	}
}
