// Package server wires the HTTP API, the web UI and the auth flows.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lamisator/fundus/internal/config"
	"github.com/Lamisator/fundus/internal/store"
	"github.com/Lamisator/fundus/web"
)

// Version is set at build time via -ldflags.
var Version = "dev"

type Server struct {
	cfg       config.Config
	st        *store.Store
	static    fs.FS
	limiter   *limiter // failed logins per IP + username
	ipLimiter *limiter // failed logins per IP
	hooks     *dispatcher
	setupTok  string
	oidc      *oidcState
	mux       *http.ServeMux
}

func New(cfg config.Config, st *store.Store) (*Server, error) {
	sub, err := fs.Sub(web.FS, "static")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "attachments"), 0o750); err != nil {
		return nil, fmt.Errorf("%w (the data directory must be writable by uid %d; with Docker: "+
			"mkdir -p data && chown $FUNDUS_UID:$FUNDUS_GID data)", err, os.Getuid())
	}
	s := &Server{
		cfg:       cfg,
		st:        st,
		static:    sub,
		limiter:   newLimiter(10, 15*time.Minute),
		ipLimiter: newLimiter(50, 15*time.Minute),
		hooks:     newDispatcher(st),
		oidc:      &oidcState{},
		mux:       http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

// Bootstrap creates the admin from the environment, or prints a one-time
// setup token when the database has no users yet.
func (s *Server) Bootstrap(ctx context.Context) error {
	n, err := s.st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if s.cfg.AdminUser != "" && s.cfg.AdminPassword != "" {
		hash, err := hashPassword(s.cfg.AdminPassword)
		if err != nil {
			return err
		}
		if _, err := s.st.CreateUser(ctx, store.NewUser{Username: s.cfg.AdminUser, Role: "admin", PasswordHash: hash}); err != nil {
			return err
		}
		slog.Info("created admin user from environment", "username", s.cfg.AdminUser)
		return nil
	}
	s.setupTok = store.RandomToken(12)
	slog.Warn("no users yet: open the web UI and finish setup with this token", "setup_token", s.setupTok)
	return nil
}

func (s *Server) Handler() http.Handler {
	return s.recoverer(s.logger(s.secureHeaders(s.authenticate(s.mux))))
}

// Maintenance runs periodic cleanup until ctx ends.
func (s *Server) Maintenance(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.st.Cleanup(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("cleanup", "err", err)
		}
		s.cleanOrphanFiles(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) routes() {
	m := s.mux
	h := func(pattern string, fn http.HandlerFunc) { m.HandleFunc(pattern, fn) }

	h("GET /healthz", s.handleHealth)

	// auth
	h("GET /api/v1/auth/config", s.handleAuthConfig)
	h("POST /api/v1/auth/setup", s.handleSetup)
	h("POST /api/v1/auth/login", s.handleLogin)
	h("POST /api/v1/auth/logout", s.handleLogout)
	h("POST /api/v1/auth/passkey/begin", s.handlePasskeyLoginBegin)
	h("POST /api/v1/auth/passkey/finish", s.handlePasskeyLoginFinish)
	h("GET /auth/oidc/login", s.handleOIDCLogin)
	h("GET /auth/oidc/callback", s.handleOIDCCallback)

	// me
	h("GET /api/v1/me", s.reader(s.handleMe))
	h("PATCH /api/v1/me", s.reader(s.handleUpdateMe))
	h("POST /api/v1/me/password", s.sessionOnly(s.handleChangePassword))
	h("GET /api/v1/me/sessions", s.sessionOnly(s.handleListSessions))
	h("DELETE /api/v1/me/sessions/{id}", s.sessionOnly(s.handleRevokeSession))
	h("GET /api/v1/me/tokens", s.sessionOnly(s.handleListTokens))
	h("POST /api/v1/me/tokens", s.sessionOnly(s.handleCreateToken))
	h("DELETE /api/v1/me/tokens/{id}", s.sessionOnly(s.handleDeleteToken))
	h("GET /api/v1/me/passkeys", s.sessionOnly(s.handleListPasskeys))
	h("POST /api/v1/me/passkeys/begin", s.sessionOnly(s.handlePasskeyRegisterBegin))
	h("POST /api/v1/me/passkeys/finish", s.sessionOnly(s.handlePasskeyRegisterFinish))
	h("PATCH /api/v1/me/passkeys/{id}", s.sessionOnly(s.handleRenamePasskey))
	h("DELETE /api/v1/me/passkeys/{id}", s.sessionOnly(s.handleDeletePasskey))
	h("DELETE /api/v1/me/sso", s.sessionOnly(s.handleUnlinkSSO))

	// entries
	h("GET /api/v1/entries", s.reader(s.handleListEntries))
	h("GET /api/v1/search", s.reader(s.handleSearch))
	h("POST /api/v1/entries", s.writer(s.handleCreateEntry))
	h("GET /api/v1/entries/{id}", s.reader(s.handleGetEntry))
	h("PATCH /api/v1/entries/{id}", s.writer(s.handleUpdateEntry))
	h("DELETE /api/v1/entries/{id}", s.writer(s.handleDeleteEntry))
	h("POST /api/v1/entries/{id}/restore", s.writer(s.handleRestoreEntry))
	h("PUT /api/v1/entries/{id}/pin", s.writer(s.handlePin(true)))
	h("DELETE /api/v1/entries/{id}/pin", s.writer(s.handlePin(false)))
	h("PUT /api/v1/entries/{id}/star", s.reader(s.handleStar(true)))
	h("DELETE /api/v1/entries/{id}/star", s.reader(s.handleStar(false)))
	h("GET /api/v1/entries/{id}/markdown", s.reader(s.handleEntryMarkdown))
	h("GET /api/v1/entries/{id}/revisions", s.reader(s.handleListRevisions))
	h("GET /api/v1/entries/{id}/revisions/{version}", s.reader(s.handleGetRevision))
	h("POST /api/v1/entries/{id}/revisions/{version}/restore", s.writer(s.handleRestoreRevision))
	h("GET /api/v1/entries/{id}/solutions", s.reader(s.handleListSolutions))
	h("POST /api/v1/entries/{id}/solutions", s.writer(s.handleCreateSolution))
	h("PATCH /api/v1/solutions/{id}", s.writer(s.handleUpdateSolution))
	h("DELETE /api/v1/solutions/{id}", s.writer(s.handleDeleteSolution))
	h("PUT /api/v1/solutions/{id}/accept", s.writer(s.handleAcceptSolution(true)))
	h("DELETE /api/v1/solutions/{id}/accept", s.writer(s.handleAcceptSolution(false)))
	h("PUT /api/v1/solutions/{id}/vote", s.reader(s.handleVoteSolution(true)))
	h("DELETE /api/v1/solutions/{id}/vote", s.reader(s.handleVoteSolution(false)))
	h("GET /api/v1/entries/{id}/links", s.reader(s.handleListLinks))
	h("POST /api/v1/entries/{id}/links", s.writer(s.handleAddLink))
	h("DELETE /api/v1/entries/{id}/links", s.writer(s.handleDeleteLink))
	h("GET /api/v1/entries/{id}/similar", s.reader(s.handleEntrySimilar))
	h("GET /api/v1/entries/{id}/attachments", s.reader(s.handleListAttachments))
	h("GET /api/v1/entries/{id}/shares", s.writer(s.handleListShares))
	h("POST /api/v1/entries/{id}/shares", s.writer(s.handleCreateShare))
	h("DELETE /api/v1/entries/{id}/shares/{token}", s.writer(s.handleDeleteShare))
	h("GET /api/v1/similar", s.reader(s.handleSimilar))
	h("POST /api/v1/render", s.reader(s.handleRender))

	// files
	h("POST /api/v1/attachments", s.writer(s.handleUpload))
	h("DELETE /api/v1/attachments/{id}", s.writer(s.handleDeleteAttachment))
	h("GET /files/{id}/{name...}", s.handleFile)
	h("GET /files/{id}", s.handleFile)

	// taxonomy
	h("GET /api/v1/tags", s.reader(s.handleListTags))
	h("PATCH /api/v1/tags/{name}", s.writer(s.handleRenameTag))
	h("DELETE /api/v1/tags/{name}", s.admin(s.handleDeleteTag))
	h("GET /api/v1/spaces", s.reader(s.handleListSpaces))
	h("POST /api/v1/spaces", s.admin(s.handleCreateSpace))
	h("GET /api/v1/spaces/{id}", s.reader(s.handleGetSpace))
	h("PATCH /api/v1/spaces/{id}", s.admin(s.handleUpdateSpace))
	h("DELETE /api/v1/spaces/{id}", s.admin(s.handleDeleteSpace))
	h("GET /api/v1/kinds", s.reader(s.handleListKinds))
	h("PUT /api/v1/kinds/{key}", s.admin(s.handlePutKind))
	h("DELETE /api/v1/kinds/{key}", s.admin(s.handleDeleteKind))

	// misc
	h("GET /api/v1/stats", s.reader(s.handleStats))
	h("GET /api/v1/export", s.reader(s.handleExport))
	h("POST /api/v1/import", s.admin(s.handleImport))

	// admin
	h("GET /api/v1/users", s.admin(s.handleListUsers))
	h("POST /api/v1/users", s.admin(s.handleCreateUser))
	h("PATCH /api/v1/users/{id}", s.admin(s.handleUpdateUser))
	h("DELETE /api/v1/users/{id}", s.admin(s.handleDeleteUser))
	h("POST /api/v1/users/{id}/password", s.admin(s.handleSetUserPassword))
	h("GET /api/v1/audit", s.admin(s.handleAudit))
	h("GET /api/v1/webhooks", s.admin(s.handleListWebhooks))
	h("POST /api/v1/webhooks", s.admin(s.handleCreateWebhook))
	h("PATCH /api/v1/webhooks/{id}", s.admin(s.handleUpdateWebhook))
	h("DELETE /api/v1/webhooks/{id}", s.admin(s.handleDeleteWebhook))
	h("POST /api/v1/webhooks/{id}/test", s.admin(s.handleTestWebhook))

	// MCP (Model Context Protocol) endpoint for AI assistants.
	h("POST /mcp", s.handleMCP)
	h("GET /mcp", s.handleMCPGet)

	// public share pages + API documentation
	h("GET /s/{token}", s.handleSharePage)
	h("GET /api/openapi.yaml", s.handleOpenAPIYAML)
	h("GET /api/openapi.json", s.handleOpenAPIJSON)

	h("/api/", func(w http.ResponseWriter, r *http.Request) {
		apiError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	h("/", s.handleStatic)
}

// ---------------------------------------------------------------- principal

type ctxKey int

const principalKey ctxKey = 1

// principal is the authenticated caller of a request.
type principal struct {
	store.User
	Scopes    []string // nil for browser sessions (= everything the role allows)
	SessionID string   // hashed session id, empty for tokens
	Via       string   // "session" | "token"
}

func (p *principal) has(scope string) bool {
	if p.Scopes == nil {
		return true
	}
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func (p *principal) canWrite() bool { return p.CanEdit() && p.has("write") }
func (p *principal) isAdmin() bool  { return p.IsAdmin() && p.has("admin") }

func who(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey).(*principal)
	return p
}

const sessionCookie = "fundus_session"

// authenticate resolves the bearer token or session cookie, if any, and
// enforces the CSRF rule for cookie-authenticated writes.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p *principal
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			u, scopes, err := s.st.UserForToken(r.Context(), tok)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				slog.Error("token lookup", "err", err)
			}
			if err != nil || u.Disabled {
				w.Header().Set("WWW-Authenticate", `Bearer realm="fundus"`)
				apiError(w, http.StatusUnauthorized, "invalid_token", "the API token is invalid, expired or revoked")
				return
			}
			p = &principal{User: u, Scopes: scopes, Via: "token"}
		} else if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			u, sid, err := s.st.UserForSession(r.Context(), c.Value, s.cfg.SessionTTL)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				slog.Error("session lookup", "err", err)
			}
			if err == nil && !u.Disabled {
				p = &principal{User: u, SessionID: sid, Via: "session"}
			}
			if p != nil && unsafeMethod(r.Method) && !csrfOK(r) {
				apiError(w, http.StatusForbidden, "csrf", "cross-site request refused; send X-Requested-With or a JSON body")
				return
			}
		}
		if p != nil {
			r = r.WithContext(context.WithValue(r.Context(), principalKey, p))
		}
		next.ServeHTTP(w, r)
	})
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// csrfOK accepts same-origin requests that a cross-site form cannot forge:
// a matching Origin header, a custom header, or a JSON body (which forces a
// CORS preflight that we never answer).
func csrfOK(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != requestHost(r) {
			return false
		}
		return true
	}
	if r.Header.Get("X-Requested-With") != "" {
		return true
	}
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

func (s *Server) reader(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := who(r)
		if p == nil {
			apiError(w, http.StatusUnauthorized, "unauthenticated", "sign in or send an API token")
			return
		}
		if !p.has("read") {
			apiError(w, http.StatusForbidden, "forbidden", "token lacks the read scope")
			return
		}
		fn(w, r)
	}
}

func (s *Server) writer(fn http.HandlerFunc) http.HandlerFunc {
	return s.reader(func(w http.ResponseWriter, r *http.Request) {
		if !who(r).canWrite() {
			apiError(w, http.StatusForbidden, "forbidden", "editor role and write scope required")
			return
		}
		fn(w, r)
	})
}

func (s *Server) admin(fn http.HandlerFunc) http.HandlerFunc {
	return s.reader(func(w http.ResponseWriter, r *http.Request) {
		if !who(r).isAdmin() {
			apiError(w, http.StatusForbidden, "forbidden", "admin role (and admin scope for tokens) required")
			return
		}
		fn(w, r)
	})
}

// sessionOnly guards account-security endpoints that API tokens must not reach.
func (s *Server) sessionOnly(fn http.HandlerFunc) http.HandlerFunc {
	return s.reader(func(w http.ResponseWriter, r *http.Request) {
		if who(r).Via != "session" {
			apiError(w, http.StatusForbidden, "forbidden", "only available to browser sessions")
			return
		}
		fn(w, r)
	})
}

// ---------------------------------------------------------------- middleware

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/static/") {
			return
		}
		lvl := slog.LevelInfo
		if sw.status >= 500 {
			lvl = slog.LevelError
		}
		slog.Log(r.Context(), lvl, "http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "err", v, "stack", string(debug.Stack()))
				apiError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

type apiErr struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	var e apiErr
	e.Error.Code, e.Error.Message = code, msg
	writeJSON(w, status, e)
}

// fail maps store errors onto HTTP responses.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrConflict):
		apiError(w, http.StatusConflict, "conflict", "conflict: the resource already exists or was changed meanwhile")
	case errors.Is(err, context.Canceled):
		apiError(w, 499, "canceled", "request canceled")
	default:
		slog.Error("request failed", "err", err)
		apiError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func badRequest(w http.ResponseWriter, msg string) {
	apiError(w, http.StatusBadRequest, "bad_request", msg)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		badRequest(w, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusNotFound, "not_found", "invalid id")
		return 0, false
	}
	return id, true
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		return strings.TrimSpace(strings.Split(xf, ",")[0])
	}
	if xr := r.Header.Get("X-Real-Ip"); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func requestHost(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return h
	}
	return r.Host
}

func requestScheme(r *http.Request) string {
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// baseURL is the public origin, from config or the request.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	return requestScheme(r) + "://" + requestHost(r)
}

func isHTTPS(r *http.Request) bool { return requestScheme(r) == "https" }

func (s *Server) audit(r *http.Request, action, target, detail string) {
	var uid int64
	if p := who(r); p != nil {
		uid = p.ID
	}
	s.st.Audit(context.WithoutCancel(r.Context()), uid, action, target, detail, clientIP(r))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.st.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

// ---------------------------------------------------------------- login rate limit

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

// blocked reports whether key has used up its failures in the window.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) >= l.max
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[key] = append(l.prune(key), time.Now())
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

func (l *limiter) prune(key string) []time.Time {
	cut := time.Now().Add(-l.window)
	h := l.hits[key]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = h
	}
	return h
}
