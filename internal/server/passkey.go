package server

// Passkeys (WebAuthn). Lessons carried over from earlier projects
// (noctalum/heinen passkey notes):
//   - the RP ID is derived per request from the Host header unless
//     FUNDUS_RP_ID is set, so dev ports and proxies just work;
//   - both http:// and https:// origins are accepted, because behind a TLS
//     terminating proxy r.TLS is nil while the browser sees https;
//   - resident keys are required, otherwise Bitwarden stores
//     non-discoverable credentials that the usernameless login cannot find;
//   - FinishRegistration reads r.Body itself, so the passkey name travels in
//     the query string;
//   - the JSON fetch carries no CSRF token, which the CSRF rule allows.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Lamisator/fundus/internal/store"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	passkeyCookie = "fundus_webauthn"
	passkeyTTL    = 5 * time.Minute
)

func (s *Server) webAuthn(r *http.Request) (*webauthn.WebAuthn, error) {
	host := requestHost(r)
	rpid := s.cfg.RPID
	if rpid == "" {
		rpid = host
		if h, _, err := net.SplitHostPort(host); err == nil {
			rpid = h
		}
	}
	origins := []string{"http://" + host, "https://" + host}
	if s.cfg.RPOrigin != "" {
		origins = strings.Split(s.cfg.RPOrigin, ",")
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName: s.cfg.SiteName,
		RPID:          rpid,
		RPOrigins:     origins,
	})
}

type waUser struct {
	u     store.User
	creds []webauthn.Credential
}

func (w *waUser) WebAuthnID() []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(w.u.ID))
	return b
}
func (w *waUser) WebAuthnName() string                       { return w.u.Username }
func (w *waUser) WebAuthnDisplayName() string                { return w.u.Name() }
func (w *waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func (s *Server) loadWAUser(r *http.Request, u store.User) (*waUser, error) {
	list, err := s.st.ListPasskeys(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	wu := &waUser{u: u}
	for _, p := range list {
		var c webauthn.Credential
		if err := json.Unmarshal(p.Data, &c); err == nil {
			wu.creds = append(wu.creds, c)
		}
	}
	return wu, nil
}

func (s *Server) saveCeremony(w http.ResponseWriter, r *http.Request, sd *webauthn.SessionData) error {
	data, err := json.Marshal(sd)
	if err != nil {
		return err
	}
	id := store.RandomToken(24)
	if err := s.st.SaveWebAuthnSession(r.Context(), id, data, passkeyTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: passkeyCookie, Value: id, Path: "/api/v1/", HttpOnly: true, Secure: isHTTPS(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(passkeyTTL.Seconds()),
	})
	return nil
}

func (s *Server) takeCeremony(w http.ResponseWriter, r *http.Request) (webauthn.SessionData, error) {
	var sd webauthn.SessionData
	c, err := r.Cookie(passkeyCookie)
	if err != nil {
		return sd, errors.New("passkey ceremony expired, please try again")
	}
	http.SetCookie(w, &http.Cookie{Name: passkeyCookie, Value: "", Path: "/api/v1/", HttpOnly: true, MaxAge: -1})
	data, err := s.st.TakeWebAuthnSession(r.Context(), c.Value)
	if err != nil {
		return sd, errors.New("passkey ceremony expired, please try again")
	}
	return sd, json.Unmarshal(data, &sd)
}

// ---------------------------------------------------------------- registration

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webAuthn(r)
	if err != nil {
		fail(w, err)
		return
	}
	wu, err := s.loadWAUser(r, who(r).User)
	if err != nil {
		fail(w, err)
		return
	}
	var exclude []protocol.CredentialDescriptor
	for i := range wu.creds {
		exclude = append(exclude, wu.creds[i].Descriptor())
	}
	opts, sd, err := wa.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclude),
	)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.saveCeremony(w, r, sd); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	sd, err := s.takeCeremony(w, r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	wa, err := s.webAuthn(r)
	if err != nil {
		fail(w, err)
		return
	}
	p := who(r)
	wu, err := s.loadWAUser(r, p.User)
	if err != nil {
		fail(w, err)
		return
	}
	cred, err := wa.FinishRegistration(wu, sd, r)
	if err != nil {
		badRequest(w, "passkey registration failed: "+webauthnErr(err))
		return
	}
	data, _ := json.Marshal(cred)
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	if err := s.st.AddPasskey(r.Context(), p.ID, id, name, data); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "passkey.added", p.Username, name)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": name})
}

// ---------------------------------------------------------------- login

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	wa, err := s.webAuthn(r)
	if err != nil {
		fail(w, err)
		return
	}
	opts, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.saveCeremony(w, r, sd); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.ipLimiter.blocked(clientIP(r)) {
		apiError(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts, try again later")
		return
	}
	sd, err := s.takeCeremony(w, r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	wa, err := s.webAuthn(r)
	if err != nil {
		fail(w, err)
		return
	}
	var found *waUser
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		if len(userHandle) != 8 {
			return nil, errors.New("unknown user handle")
		}
		u, err := s.st.GetUser(r.Context(), int64(binary.BigEndian.Uint64(userHandle)))
		if err != nil {
			return nil, err
		}
		if u.Disabled {
			return nil, errors.New("account disabled")
		}
		found, err = s.loadWAUser(r, u)
		return found, err
	}
	cred, err := wa.FinishDiscoverableLogin(handler, sd, r)
	if err != nil || found == nil {
		s.ipLimiter.fail(clientIP(r))
		s.st.Audit(r.Context(), 0, "login.failed", "", "passkey", clientIP(r))
		apiError(w, http.StatusUnauthorized, "bad_credentials", "passkey not recognised: "+webauthnErr(err))
		return
	}
	data, _ := json.Marshal(cred)
	_ = s.st.UpdatePasskeyData(r.Context(), base64.RawURLEncoding.EncodeToString(cred.ID), data)
	if err := s.startSession(w, r, found.u, "passkey"); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, found.u)
}

// ---------------------------------------------------------------- management

func (s *Server) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListPasskeys(r.Context(), who(r).ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRenamePasskey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		badRequest(w, "name: 1-80 characters")
		return
	}
	if err := s.st.RenamePasskey(r.Context(), who(r).ID, r.PathValue("id"), in.Name); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	list, _ := s.st.ListPasskeys(r.Context(), p.ID)
	if len(list) <= 1 && !p.HasPassword && !p.SSO {
		badRequest(w, "this is your only way to sign in; set a password or link SSO first")
		return
	}
	if err := s.st.DeletePasskey(r.Context(), p.ID, r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "passkey.deleted", p.Username, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func webauthnErr(err error) string {
	if err == nil {
		return "unknown error"
	}
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		return pe.Details + " (" + pe.DevInfo + ")"
	}
	return err.Error()
}
