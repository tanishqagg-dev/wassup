package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tanishqagg-dev/wassup/internal/store"
)

var page = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect {{.ClientName}} to WhatsApp</title>
<style>
body{font:16px/1.5 -apple-system,system-ui,Segoe UI,Roboto,sans-serif;background:#f4f1ea;color:#1d1d1b;margin:0;padding:24px}
main{max-width:440px;margin:6vh auto;background:#fff;border-radius:16px;padding:28px;box-shadow:0 1px 3px rgba(0,0,0,.08)}
h1{font-size:22px;margin:0 0 12px}.muted{color:#6b6b66;font-size:14px}.warn{background:#fff4d6;border-radius:10px;padding:10px 12px;font-size:14px}
.err{background:#fde2e1;border-radius:10px;padding:10px 12px;font-size:14px}
dl{display:grid;grid-template-columns:auto 1fr;gap:4px 12px;font-size:14px}dt{color:#6b6b66}dd{margin:0;word-break:break-all}
input[type=text]{font-size:28px;letter-spacing:.3em;width:100%;box-sizing:border-box;padding:10px;border:1px solid #ccc;border-radius:10px;text-align:center}
button{font-size:16px;padding:12px 16px;border-radius:10px;border:0;cursor:pointer;width:100%;margin-top:10px}
.go{background:#1f7a5c;color:#fff}.no{background:#eee;color:#333}
</style></head><body><main>
<h1>Connect {{.ClientName}} to WhatsApp</h1>
<p>{{.ClientName}} will be able to read and send WhatsApp messages as <b>{{.Phone}}</b> through wassup. Your policy profile <b>{{.Profile}}</b> still applies: approvals, allowlists and rate limits.</p>
<dl><dt>Returns to</dt><dd>{{.RedirectHost}}</dd><dt>Access</dt><dd>{{.Scope}}</dd></dl>
{{if .Loopback}}<p class="warn">This app runs on a computer (it returns to {{.RedirectHost}}). Only continue if you started this connection yourself just now.</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Sent}}
<form method="post" action="/authorize/decide">
<input type="hidden" name="req" value="{{.ID}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p>We sent a 6-digit code to your WhatsApp <b>Message yourself</b> chat{{if .Fallback}} (or run <code>wassup remote codes</code> on the computer running wassup){{end}}.</p>
<input type="text" name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" pattern="[0-9]{6}" required autofocus>
<button class="go" name="action" value="approve">Approve</button>
</form>
<form method="post" action="/authorize/decide"><input type="hidden" name="req" value="{{.ID}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="no" name="action" value="deny">Deny</button></form>
{{else}}
<form method="post" action="/authorize/send-code"><input type="hidden" name="req" value="{{.ID}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="muted">To prove this is your account, wassup sends a one-time code to your own WhatsApp.</p>
<button class="go">Send me a code</button></form>
<form method="post" action="/authorize/decide"><input type="hidden" name="req" value="{{.ID}}"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="no" name="action" value="deny">Deny</button></form>
{{end}}
<p class="muted">wassup, open source and self-hosted. Revoke any time: <code>wassup remote revoke</code></p>
</main></body></html>`))

func (s *Server) maskedPhone() string {
	own := s.E.OwnJID()
	if own.IsEmpty() {
		return "your account (not linked yet)"
	}
	u := own.User
	if len(u) <= 4 {
		return "+" + u
	}
	return "+" + u[:2] + strings.Repeat("•", len(u)-4) + u[len(u)-2:]
}

func (s *Server) render(w http.ResponseWriter, p *pendingAuth, sent bool, errMsg string) {
	host := p.RedirectURI
	loop := false
	if u, err := url.Parse(p.RedirectURI); err == nil {
		host = u.Host
		loop = u.Scheme == "http"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// No form-action here: Chrome applies it to the redirect back to the client after approval.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	_ = page.Execute(w, map[string]any{
		"ID": p.ID, "CSRF": p.CSRF, "ClientName": p.ClientName, "Phone": s.maskedPhone(), "Profile": s.cfg.Profile,
		"RedirectHost": host, "Scope": p.Scope, "Loopback": loop, "Sent": sent, "Error": errMsg, "Fallback": true,
	})
}

func redirectWith(w http.ResponseWriter, r *http.Request, redirectURI string, q map[string]string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "bad redirect", 400)
		return
	}
	v := u.Query()
	for k, val := range q {
		if val != "" {
			v.Set(k, val)
		}
	}
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ip := s.clientIP(r)
	// Admission control first, before any outbound fetch for client metadata.
	s.mu.Lock()
	for k, p := range s.pending {
		if time.Since(p.Created) > 15*time.Minute {
			delete(s.pending, k)
		}
	}
	perIP := 0
	for _, p := range s.pending {
		if p.IP == ip {
			perIP++
		}
	}
	if len(s.pending) >= 50 {
		// Make room by dropping the oldest sign-in that never got a code: cheap for an attacker
		// to create, so they can't crowd out the real owner. Code-holding ones are capped by the
		// account-wide code budget.
		var victim *pendingAuth
		for _, p := range s.pending {
			if p.OTP == "" && (victim == nil || p.Created.Before(victim.Created)) {
				victim = p
			}
		}
		if victim != nil {
			delete(s.pending, victim.ID)
		}
	}
	full := len(s.pending) >= 50 || perIP >= 5 || !s.allow("authorize:"+ip, 30, time.Hour) || !s.allow("authorize", 300, time.Hour)
	s.mu.Unlock()
	if full {
		http.Error(w, "Too many sign-ins in progress; try again in a few minutes.", 429)
		return
	}
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	name, allowed, err := s.lookupClient(r.Context(), clientID)
	if err != nil {
		http.Error(w, "Unknown or invalid client: "+err.Error(), 400)
		return
	}
	if redirectURI == "" && len(allowed) == 1 {
		redirectURI = allowed[0]
	}
	if !redirectMatches(allowed, redirectURI) {
		http.Error(w, "The redirect_uri isn't registered for this client.", 400)
		return
	}
	state := q.Get("state")
	fail := func(code, desc string) {
		redirectWith(w, r, redirectURI, map[string]string{"error": code, "error_description": desc, "state": state, "iss": s.base(r)})
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with S256 is required")
		return
	}
	scope := q.Get("scope")
	if scope == "" {
		scope = scopeName
	}
	p := &pendingAuth{ID: randToken(16), CSRF: randToken(16), ClientID: clientID, ClientName: name, RedirectURI: redirectURI,
		State: state, Scope: scope, Challenge: q.Get("code_challenge"), Resource: q.Get("resource"), Created: time.Now(), IP: ip}
	s.mu.Lock()
	s.pending[p.ID] = p
	s.mu.Unlock()
	s.render(w, p, false, "")
}

func (s *Server) formPending(w http.ResponseWriter, r *http.Request) *pendingAuth {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return nil
	}
	s.mu.Lock()
	p := s.pending[r.PostForm.Get("req")]
	s.mu.Unlock()
	if p == nil || time.Since(p.Created) > 15*time.Minute {
		http.Error(w, "This sign-in expired. Start again from your app.", 400)
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(p.CSRF), []byte(r.PostForm.Get("csrf"))) != 1 {
		http.Error(w, "bad form token", 400)
		return nil
	}
	return p
}

func sixDigits() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

func (s *Server) sendCode(w http.ResponseWriter, r *http.Request) {
	p := s.formPending(w, r)
	if p == nil {
		return
	}
	ip := s.clientIP(r)
	s.mu.Lock()
	if s.pending[p.ID] != p {
		s.mu.Unlock()
		http.Error(w, "This sign-in expired. Start again from your app.", 400)
		return
	}
	switch {
	case p.Sends >= 3 || (!p.OTPSent.IsZero() && time.Since(p.OTPSent) < 30*time.Second):
		s.mu.Unlock()
		s.render(w, p, p.OTP != "", "Please wait a moment before asking for another code.")
		return
	case !s.allow("otp:"+ip, 5, time.Hour) || !s.allow("otp", 10, time.Hour):
		// Account-wide cap too, so nobody can flood your chat by opening many sign-ins.
		s.mu.Unlock()
		s.render(w, p, p.OTP != "", "Too many codes were requested recently. Try again later.")
		return
	}
	p.OTP, p.OTPSent = sixDigits(), time.Now()
	p.Sends++
	code := p.OTP
	s.mu.Unlock()
	msg := fmt.Sprintf("*wassup sign-in code: %s*\n\n%q wants to connect to your WhatsApp. Enter this code on the sign-in page only if you started this yourself. Never share it.", code, p.ClientName)
	errMsg := ""
	if err := s.E.SendToSelf(r.Context(), msg); err != nil {
		s.Log.Warn().Err(err).Msg("couldn't send sign-in code over WhatsApp")
		errMsg = "WhatsApp isn't connected right now, so the code wasn't sent there. Run `wassup remote codes` on the computer running wassup to see it."
	}
	s.render(w, p, true, errMsg)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	p := s.formPending(w, r)
	if p == nil {
		return
	}
	iss := s.base(r)
	ip := s.clientIP(r)
	if r.PostForm.Get("action") != "approve" {
		s.mu.Lock()
		delete(s.pending, p.ID)
		s.mu.Unlock()
		redirectWith(w, r, p.RedirectURI, map[string]string{"error": "access_denied", "error_description": "the user denied access", "state": p.State, "iss": iss})
		return
	}
	// Check membership, verify and consume under one lock so a code can mint at most one grant.
	s.mu.Lock()
	if s.pending[p.ID] != p {
		s.mu.Unlock()
		http.Error(w, "This sign-in was already used or expired. Start again from your app.", 400)
		return
	}
	if p.OTP == "" || time.Since(p.OTPSent) > 10*time.Minute {
		s.mu.Unlock()
		s.render(w, p, false, "That code expired. Send a new one.")
		return
	}
	if !s.allow("verify", 30, time.Hour) || !s.allow("verify:"+ip, 10, time.Hour) {
		s.mu.Unlock()
		http.Error(w, "Too many attempts. Try again later.", 429)
		return
	}
	p.Attempts++
	if p.Attempts > 5 {
		delete(s.pending, p.ID)
		s.mu.Unlock()
		http.Error(w, "Too many wrong codes. Start the connection again from your app.", 429)
		return
	}
	if subtle.ConstantTimeCompare([]byte(p.OTP), []byte(strings.TrimSpace(r.PostForm.Get("code")))) != 1 {
		s.mu.Unlock()
		s.render(w, p, true, "That code doesn't match. Check your \"Message yourself\" chat and try again.")
		return
	}
	delete(s.pending, p.ID)
	code := randToken(32)
	s.codes[hashToken(code)] = &authCode{ClientID: p.ClientID, ClientName: p.ClientName, RedirectURI: p.RedirectURI, Challenge: p.Challenge,
		Scope: p.Scope, Expires: time.Now().Add(2 * time.Minute), Trusted: s.trustedRedirect(p.RedirectURI)}
	s.mu.Unlock()
	s.Log.Info().Str("client", p.ClientName).Msg("remote client authorized")
	_ = s.E.DB.AddAudit(r.Context(), store.Audit{Time: time.Now(), Source: "remote", Profile: s.cfg.Profile, Method: "remote.authorize", Targets: p.ClientName, OK: true})
	redirectWith(w, r, p.RedirectURI, map[string]string{"code": code, "state": p.State, "iss": iss})
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func (s *Server) newTokens(clientID, clientName, scope, family string, trusted bool) (string, string, store.OAuthToken, store.OAuthToken) {
	now := time.Now()
	access, refresh := randToken(32), randToken(32)
	a := store.OAuthToken{Hash: hashToken(access), Kind: "access", ClientID: clientID, ClientName: clientName, Family: family, Scope: scope, Created: now, Expires: now.Add(s.cfg.TokenTTL.Duration), Trusted: trusted}
	r := store.OAuthToken{Hash: hashToken(refresh), Kind: "refresh", ClientID: clientID, ClientName: clientName, Family: family, Scope: scope, Created: now, Expires: now.Add(s.cfg.RefreshTTL.Duration), Trusted: trusted}
	return access, refresh, a, r
}

func (s *Server) tokenResponse(w http.ResponseWriter, access, refresh, scope string) {
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(s.cfg.TokenTTL.Duration.Seconds()), "refresh_token": refresh, "scope": scope})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "send application/x-www-form-urlencoded")
		return
	}
	f := r.PostForm
	clientID := f.Get("client_id")
	switch f.Get("grant_type") {
	case "authorization_code":
		h := hashToken(f.Get("code"))
		s.mu.Lock()
		c := s.codes[h]
		delete(s.codes, h) // single use, even on failure
		s.mu.Unlock()
		if c == nil || time.Now().After(c.Expires) {
			oauthError(w, 400, "invalid_grant", "the code is invalid or expired")
			return
		}
		if clientID != "" && clientID != c.ClientID {
			oauthError(w, 400, "invalid_grant", "the code was issued to another client")
			return
		}
		if ru := f.Get("redirect_uri"); ru != "" && ru != c.RedirectURI {
			oauthError(w, 400, "invalid_grant", "redirect_uri doesn't match")
			return
		}
		if !pkceOK(f.Get("code_verifier"), c.Challenge) {
			oauthError(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		access, refresh, at, rt := s.newTokens(c.ClientID, c.ClientName, c.Scope, randToken(12), c.Trusted)
		if err := s.E.DB.IssueFamily(r.Context(), at, rt); err != nil {
			oauthError(w, 500, "server_error", "couldn't issue tokens")
			return
		}
		s.tokenResponse(w, access, refresh, c.Scope)
	case "refresh_token":
		access, refresh, at, rt := s.newTokens("", "", "", "", false)
		t, err := s.E.DB.RotateRefresh(r.Context(), hashToken(f.Get("refresh_token")), clientID, at, rt)
		switch {
		case errors.Is(err, store.ErrTokenReused):
			// A spent refresh token came back: assume it leaked; the whole connection is now revoked.
			s.Log.Warn().Msg("refresh token reuse detected; connection revoked")
			oauthError(w, 400, "invalid_grant", "the refresh token was already used")
		case errors.Is(err, store.ErrTokenInvalid):
			oauthError(w, 400, "invalid_grant", "the refresh token is invalid or expired")
		case err != nil:
			oauthError(w, 500, "server_error", "token store error")
		default:
			s.tokenResponse(w, access, refresh, t.Scope)
		}
	default:
		oauthError(w, 400, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if t, _ := s.E.DB.GetOAuthToken(r.Context(), hashToken(r.PostForm.Get("token"))); t != nil {
		_ = s.E.DB.RevokeFamily(r.Context(), t.Family)
	}
	w.WriteHeader(200)
}

// requireToken guards /mcp. Unauthenticated calls get the 401 that starts Claude's sign-in.
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challenge := fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="%s"`, s.base(r), scopeName)
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, "sign-in required", http.StatusUnauthorized)
			return
		}
		h := hashToken(strings.TrimSpace(strings.TrimPrefix(authz, "Bearer ")))
		t, err := s.E.DB.GetOAuthToken(r.Context(), h)
		if err != nil || t == nil || t.Kind != "access" || t.Revoked || time.Now().After(t.Expires) {
			w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		if time.Since(t.LastUsed) > time.Minute {
			s.E.DB.TouchOAuthToken(r.Context(), h)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, t)))
	})
}

// PendingCodes lists sign-ins waiting for a code, for `wassup remote codes`.
func (s *Server) PendingCodes() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, p := range s.pending {
		if p.OTP != "" && time.Since(p.OTPSent) < 10*time.Minute {
			out = append(out, map[string]any{"client": p.ClientName, "code": p.OTP, "sent": p.OTPSent.Format(time.RFC3339)})
		}
	}
	return out
}
