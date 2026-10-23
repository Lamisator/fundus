package server

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Lamisator/fundus/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func hashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

// dummyHash keeps login timing uniform for unknown users.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("fundus-timing-pad"), 12)

func checkPassword(hash, pw string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-@]{1,63}$`)

func validPassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(pw) > 72 {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}

func validRole(r string) bool { return r == "admin" || r == "editor" || r == "viewer" }

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User, method string) error {
	tok, err := s.st.CreateSession(r.Context(), u.ID, s.cfg.SessionTTL, method, r.UserAgent(), clientIP(r))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	s.st.Audit(r.Context(), u.ID, "login", u.Username, method, clientIP(r))
	return nil
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.CountUsers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"site_name":      s.cfg.SiteName,
		"version":        Version,
		"password_login": s.cfg.PasswordLogin,
		"passkeys":       true,
		"oidc":           s.cfg.OIDC.Enabled(),
		"oidc_label":     s.cfg.OIDC.ButtonLabel,
		"setup_required": n == 0,
		"authenticated":  who(r) != nil,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SetupToken  string `json:"setup_token"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	n, err := s.st.CountUsers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if n > 0 || s.setupTok == "" {
		apiError(w, http.StatusConflict, "already_setup", "setup has already been completed")
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.SetupToken), []byte(s.setupTok)) != 1 {
		s.limiter.fail("setup:" + clientIP(r))
		apiError(w, http.StatusForbidden, "bad_setup_token", "wrong setup token (see the server log)")
		return
	}
	if !usernameRe.MatchString(in.Username) {
		badRequest(w, "username: 2-64 characters, letters, digits and . _ - @")
		return
	}
	if err := validPassword(in.Password); err != nil {
		badRequest(w, err.Error())
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		fail(w, err)
		return
	}
	u, err := s.st.CreateUser(r.Context(), store.NewUser{Username: in.Username, DisplayName: in.DisplayName, Role: "admin", PasswordHash: hash})
	if err != nil {
		fail(w, err)
		return
	}
	s.setupTok = ""
	if err := s.startSession(w, r, u, "password"); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.PasswordLogin {
		apiError(w, http.StatusForbidden, "disabled", "password login is disabled; use SSO or a passkey")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	key := clientIP(r) + "|" + strings.ToLower(in.Username)
	if s.limiter.blocked(key) || s.ipLimiter.blocked(clientIP(r)) {
		apiError(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts, try again later")
		return
	}
	u, err := s.st.GetUserByUsername(r.Context(), in.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		fail(w, err)
		return
	}
	if !checkPassword(u.PasswordHash, in.Password) || u.Disabled {
		s.limiter.fail(key)
		s.ipLimiter.fail(clientIP(r))
		s.st.Audit(r.Context(), 0, "login.failed", in.Username, "password", clientIP(r))
		apiError(w, http.StatusUnauthorized, "bad_credentials", "wrong username or password")
		return
	}
	s.limiter.reset(key)
	if err := s.startSession(w, r, u, "password"); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.st.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- me

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	pk, err := s.st.ListPasskeys(r.Context(), p.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":     p.User,
		"via":      p.Via,
		"scopes":   p.Scopes,
		"passkeys": len(pk),
		"can_edit": p.canWrite(),
		"is_admin": p.isAdmin(),
	})
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	u, err := s.st.UpdateUser(r.Context(), who(r).ID, store.UserPatch{DisplayName: in.DisplayName, Email: in.Email})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	p := who(r)
	if p.HasPassword && !checkPassword(p.PasswordHash, in.Current) {
		apiError(w, http.StatusForbidden, "bad_credentials", "current password is wrong")
		return
	}
	if in.New == "" {
		// Removing the password is only allowed when another way in exists.
		pk, _ := s.st.ListPasskeys(r.Context(), p.ID)
		if len(pk) == 0 && !p.SSO {
			badRequest(w, "add a passkey or link SSO before removing your password")
			return
		}
		if err := s.st.SetPassword(r.Context(), p.ID, ""); err != nil {
			fail(w, err)
			return
		}
		s.audit(r, "password.removed", p.Username, "")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := validPassword(in.New); err != nil {
		badRequest(w, err.Error())
		return
	}
	hash, err := hashPassword(in.New)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.st.SetPassword(r.Context(), p.ID, hash); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "password.changed", p.Username, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	list, err := s.st.ListSessions(r.Context(), p.ID, p.SessionID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	if err := s.st.RevokeSession(r.Context(), who(r).ID, r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListTokens(r.Context(), who(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresIn int      `json:"expires_in_days"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		badRequest(w, "name is required (max 100 characters)")
		return
	}
	p := who(r)
	if len(in.Scopes) == 0 {
		in.Scopes = []string{"read"}
	}
	seen := map[string]bool{}
	scopes := []string{}
	for _, sc := range in.Scopes {
		switch sc {
		case "read", "write", "admin":
		default:
			badRequest(w, "scopes: read, write, admin")
			return
		}
		if sc == "write" && !p.CanEdit() || sc == "admin" && !p.IsAdmin() {
			apiError(w, http.StatusForbidden, "forbidden", "your role does not allow the "+sc+" scope")
			return
		}
		if !seen[sc] {
			seen[sc] = true
			scopes = append(scopes, sc)
		}
	}
	if !seen["read"] {
		scopes = append([]string{"read"}, scopes...)
	}
	var exp *time.Time
	if in.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresIn) * 24 * time.Hour)
		exp = &t
	}
	t, err := s.st.CreateToken(r.Context(), p.ID, in.Name, scopes, exp)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "token.created", in.Name, strings.Join(scopes, ","))
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.st.DeleteToken(r.Context(), who(r).ID, id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "token.deleted", strconv.FormatInt(id, 10), "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnlinkSSO(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	pk, _ := s.st.ListPasskeys(r.Context(), p.ID)
	if !p.HasPassword && len(pk) == 0 {
		badRequest(w, "set a password or add a passkey before unlinking SSO")
		return
	}
	if err := s.st.UnlinkOIDC(r.Context(), p.ID); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "sso.unlinked", p.Username, "")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- user admin

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListUsers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		Role        string `json:"role"`
		Password    string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !usernameRe.MatchString(in.Username) {
		badRequest(w, "username: 2-64 characters, letters, digits and . _ - @")
		return
	}
	if in.Role == "" {
		in.Role = "editor"
	}
	if !validRole(in.Role) {
		badRequest(w, "role must be admin, editor or viewer")
		return
	}
	var hash string
	if in.Password != "" {
		if err := validPassword(in.Password); err != nil {
			badRequest(w, err.Error())
			return
		}
		var err error
		if hash, err = hashPassword(in.Password); err != nil {
			fail(w, err)
			return
		}
	}
	u, err := s.st.CreateUser(r.Context(), store.NewUser{Username: in.Username, DisplayName: in.DisplayName, Email: in.Email,
		Role: in.Role, PasswordHash: hash})
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "user.created", u.Username, u.Role)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in store.UserPatch
	if !readJSON(w, r, &in) {
		return
	}
	if in.Role != nil && !validRole(*in.Role) {
		badRequest(w, "role must be admin, editor or viewer")
		return
	}
	cur, err := s.st.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	demote := (in.Role != nil && *in.Role != "admin") || (in.Disabled != nil && *in.Disabled)
	if cur.IsAdmin() && demote {
		if n, _ := s.st.CountAdmins(r.Context()); n <= 1 {
			badRequest(w, "cannot demote or disable the last admin")
			return
		}
	}
	u, err := s.st.UpdateUser(r.Context(), id, in)
	if err != nil {
		fail(w, err)
		return
	}
	if u.Disabled {
		_ = s.st.DeleteUserSessions(r.Context(), id)
	}
	s.audit(r, "user.updated", u.Username, "role="+u.Role+" disabled="+strconv.FormatBool(u.Disabled))
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if id == who(r).ID {
		badRequest(w, "you cannot delete yourself")
		return
	}
	u, err := s.st.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if u.IsAdmin() {
		if n, _ := s.st.CountAdmins(r.Context()); n <= 1 {
			badRequest(w, "cannot delete the last admin")
			return
		}
	}
	if err := s.st.DeleteUser(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "user.deleted", u.Username, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	hash := ""
	if in.Password != "" {
		if err := validPassword(in.Password); err != nil {
			badRequest(w, err.Error())
			return
		}
		var err error
		if hash, err = hashPassword(in.Password); err != nil {
			fail(w, err)
			return
		}
	}
	if err := s.st.SetPassword(r.Context(), id, hash); err != nil {
		fail(w, err)
		return
	}
	_ = s.st.DeleteUserSessions(r.Context(), id)
	s.audit(r, "user.password_set", strconv.FormatInt(id, 10), "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.st.ListAudit(r.Context(), limit, offset)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}
