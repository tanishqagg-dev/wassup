// Package remote serves wassup's MCP over HTTP with a built-in OAuth 2.1 authorization server,
// so claude.ai (web, Desktop, mobile), Claude Code and other remote MCP clients can connect to
// a user's own wassup. Each user runs their own instance; nothing is shared between users.
//
// Sign-in proves you own the WhatsApp account: the consent page sends a one-time code to your
// "Message yourself" chat (also visible with `wassup remote codes` on the machine).
package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/engine"
	"github.com/tanishqagg-dev/wassup/internal/mcpserver"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

const (
	scopeName      = "whatsapp"
	claudeCallback = "https://claude.ai/api/mcp/auth_callback"
)

type Server struct {
	E   *engine.Engine
	Log zerolog.Logger
	cfg config.RemoteConfig

	public  atomic.Value // string: https://host, no trailing slash ("" = derive from request)
	mcp     http.Handler
	httpSrv *http.Server
	addr    string
	tunnel  *tunnel

	mu      sync.Mutex
	pending map[string]*pendingAuth
	codes   map[string]*authCode
	cimd    map[string]cimdEntry
	regLog  []time.Time
	budget  map[string][]time.Time // sliding-window counters: "otp", "otp:<ip>", "pending:<ip>", "verify", "cimd"
}

type pendingAuth struct {
	ID, CSRF                                        string
	ClientID, ClientName, RedirectURI, State, Scope string
	Challenge, Resource, IP                         string
	Created                                         time.Time
	OTP                                             string
	OTPSent                                         time.Time
	Sends, Attempts                                 int
}

type authCode struct {
	ClientID, ClientName, RedirectURI, Challenge, Scope string
	Expires                                             time.Time
	Trusted                                             bool
}

type cimdEntry struct {
	name     string
	redirect []string
	fetched  time.Time
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// Start serves on cfg.Listen until Close. It never fails the daemon: problems are logged.
func Start(e *engine.Engine, log zerolog.Logger) (*Server, error) {
	cfg := e.Config().Remote
	s := &Server{E: e, Log: log.With().Str("mod", "remote").Logger(), cfg: cfg,
		pending: map[string]*pendingAuth{}, codes: map[string]*authCode{}, cimd: map[string]cimdEntry{}}
	s.public.Store(strings.TrimRight(cfg.PublicURL, "/"))
	s.budget = map[string][]time.Time{}
	// Two servers: one for first-party clients that may relay in-app approvals, one that always
	// uses the out-of-band WhatsApp prompt. The token decides which a session gets.
	mk := func(trusted bool) (*mcp.Server, error) {
		return mcpserver.New(mcpserver.Options{
			Backend: mcpserver.EngineBackend{E: e}, Source: "remote", Profile: cfg.Profile,
			Fence: e.Config().MCP.Fence, ApprovalWait: e.Config().MCP.ApprovalWait.Duration, WhatsAppApprovals: true,
			TrustClientApproval: trusted,
		})
	}
	trustedSrv, err := mk(true)
	if err != nil {
		return nil, err
	}
	plainSrv, err := mk(false)
	if err != nil {
		return nil, err
	}
	s.mcp = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if t, _ := r.Context().Value(tokenKey{}).(*store.OAuthToken); t != nil && t.Trusted {
			return trustedSrv
		}
		return plainSrv
	}, &mcp.StreamableHTTPOptions{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.authServerMeta)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp", s.authServerMeta)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /authorize/send-code", s.sendCode)
	mux.HandleFunc("POST /authorize/decide", s.decide)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("POST /revoke", s.revoke)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	// After our check, the SDK's own bearer middleware records an identity per connection and
	// trust class, so the streamable transport refuses a session ID presented with another token.
	bound := auth.RequireBearerToken(s.sessionIdentity, &auth.RequireBearerTokenOptions{Scopes: []string{scopeName}})(s.mcp)
	mux.Handle("/mcp", s.requireToken(bound))
	mux.Handle("/mcp/", s.requireToken(bound))
	mux.HandleFunc("GET /{$}", s.home)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("remote MCP can't listen on %s: %w", cfg.Listen, err)
	}
	s.addr = ln.Addr().String()
	s.httpSrv = &http.Server{Handler: securityHeaders(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.Log.Error().Err(err).Msg("remote server stopped")
		}
	}()
	if strings.EqualFold(cfg.Tunnel, "cloudflare") {
		s.tunnel = startCloudflare(s, cfg.Listen)
	}
	go s.janitor()
	s.Log.Info().Str("listen", cfg.Listen).Str("public", s.PublicURL()).Msg("remote MCP ready")
	return s, nil
}

func (s *Server) Close() {
	if s.tunnel != nil {
		s.tunnel.stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.httpSrv.Shutdown(ctx)
}

// Addr is the local listen address.
func (s *Server) Addr() string { return s.addr }

// PublicURL is the configured or tunnel-provided https origin ("" until known).
func (s *Server) PublicURL() string { v, _ := s.public.Load().(string); return v }

func (s *Server) setPublic(u string) {
	s.public.Store(strings.TrimRight(u, "/"))
	_ = s.E.DB.Set(context.Background(), "remote_public_url", s.PublicURL())
	s.Log.Info().Str("url", s.PublicURL()).Msg("public URL")
}

// base is the external origin for this request: configured URL, else what the proxy reports.
func (s *Server) base(r *http.Request) string {
	if u := s.PublicURL(); u != "" {
		return u
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// ---------- discovery ----------

func (s *Server) protectedResource(w http.ResponseWriter, r *http.Request) {
	b := s.base(r)
	writeJSON(w, 200, map[string]any{
		"resource":                 b + "/mcp",
		"authorization_servers":    []string{b},
		"scopes_supported":         []string{scopeName},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "wassup (WhatsApp)",
	})
}

func (s *Server) authServerMeta(w http.ResponseWriter, r *http.Request) {
	b := s.base(r)
	writeJSON(w, 200, map[string]any{
		"issuer":                                         b,
		"authorization_endpoint":                         b + "/authorize",
		"token_endpoint":                                 b + "/token",
		"registration_endpoint":                          b + "/register",
		"revocation_endpoint":                            b + "/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"scopes_supported":                               []string{scopeName, "offline_access"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "wassup remote MCP is running.\n\nAdd this URL as a custom connector in Claude (Settings > Connectors):\n%s/mcp\n", s.base(r))
}

// ---------- client registration ----------

// redirectAllowed accepts https redirects and loopback http redirects (any port).
func redirectAllowed(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Fragment != "" {
		return false
	}
	switch p.Scheme {
	case "https":
		return p.Host != ""
	case "http":
		h := p.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// redirectMatches compares exactly, except loopback hosts match on any port (RFC 8252).
func redirectMatches(registered []string, got string) bool {
	g, err := url.Parse(got)
	if err != nil {
		return false
	}
	for _, r := range registered {
		if r == got {
			return true
		}
		rp, err := url.Parse(r)
		if err != nil || rp.Scheme != "http" || g.Scheme != "http" {
			continue
		}
		loop := func(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }
		if loop(rp.Hostname()) && loop(g.Hostname()) && rp.Hostname() == g.Hostname() && rp.Path == g.Path {
			return true
		}
	}
	return false
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cut := time.Now().Add(-time.Hour)
	kept := s.regLog[:0]
	for _, t := range s.regLog {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.regLog = kept
	if len(s.regLog) >= 30 {
		s.mu.Unlock()
		oauthError(w, 429, "invalid_request", "too many registrations; try again later")
		return
	}
	s.regLog = append(s.regLog, time.Now())
	s.mu.Unlock()
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, 400, "invalid_client_metadata", "body must be JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, 400, "invalid_redirect_uri", "give 1 to 10 redirect_uris")
		return
	}
	for _, u := range req.RedirectURIs {
		if !redirectAllowed(u) {
			oauthError(w, 400, "invalid_redirect_uri", "redirect URIs must be https, or http on localhost: "+u)
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "MCP client"
	}
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	id := "wsp_" + randToken(18)
	if err := s.E.DB.PutOAuthClient(r.Context(), store.OAuthClient{ID: id, Name: name, RedirectURIs: strings.Join(req.RedirectURIs, "\n"), Created: time.Now(), Kind: "dcr"}); err != nil {
		oauthError(w, 500, "server_error", "couldn't save the client")
		return
	}
	writeJSON(w, 201, map[string]any{
		"client_id": id, "client_id_issued_at": time.Now().Unix(), "client_name": name, "redirect_uris": req.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}

type tokenKey struct{}

// trustedRedirect reports whether tokens for this redirect URI can only reach a first-party app.
func (s *Server) trustedRedirect(redirectURI string) bool {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, t := range s.cfg.TrustedRedirectHosts {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" && h == t {
			return true
		}
	}
	return false
}

// allow records an event under key and reports whether it stays within n per window.
// The map is bounded; overflow keys share one bucket. The janitor prunes stale keys.
func (s *Server) allow(key string, n int, window time.Duration) bool {
	if _, ok := s.budget[key]; !ok && len(s.budget) >= 5000 {
		key = "overflow"
	}
	cut := time.Now().Add(-window)
	kept := s.budget[key][:0]
	for _, t := range s.budget[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= n {
		s.budget[key] = kept
		return false
	}
	s.budget[key] = append(kept, time.Now())
	return true
}

// sessionIdentity gives the SDK a stable identity per connection and trust class.
func (s *Server) sessionIdentity(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	t, err := s.E.DB.GetOAuthToken(ctx, hashToken(token))
	if err != nil || t == nil || t.Kind != "access" || t.Revoked || time.Now().After(t.Expires) {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{UserID: fmt.Sprintf("%s|trusted=%v", t.Family, t.Trusted), Expiration: t.Expires, Scopes: []string{scopeName}}, nil
}

// clientIP returns the visitor's address for per-IP limits. Only headers the configured tunnel
// sets (and overwrites) are believed; anything else falls back to one shared bucket, and the
// account-wide limits still apply either way.
func (s *Server) clientIP(r *http.Request) string {
	valid := func(v string) string {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			return ip.String()
		}
		return ""
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return ip.String() // direct connection, no proxy in between
	}
	switch strings.ToLower(s.cfg.Tunnel) {
	case "cloudflare":
		if v := valid(r.Header.Get("CF-Connecting-IP")); v != "" {
			return v
		}
	default:
		// A local proxy appends the real peer last; earlier entries are client-controlled.
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if v := valid(parts[len(parts)-1]); v != "" {
				return v
			}
		}
	}
	return "proxy"
}

func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.Equal(net.IPv4bcast) ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64)) // 100.64/10 (CGNAT, Tailscale)
}

// cimdClient refuses non-public destinations at connect time, so DNS tricks can't reach the LAN.
var cimdClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{Timeout: 4 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !publicIP(ip) {
				return fmt.Errorf("client metadata host %s isn't a public address", host)
			}
			return nil
		}}).DialContext,
		MaxResponseHeaderBytes: 16 << 10,
	},
}

// lookupClient returns a client's name and redirect URIs from DCR storage or its metadata document.
func (s *Server) lookupClient(ctx context.Context, id string) (string, []string, error) {
	if strings.HasPrefix(id, "https://") {
		if len(id) > 512 {
			return "", nil, errors.New("client_id URL too long")
		}
		s.mu.Lock()
		c, ok := s.cimd[id]
		fresh := ok && time.Since(c.fetched) < time.Hour
		allowed := fresh || s.allow("cimd", 30, time.Minute)
		s.mu.Unlock()
		if fresh {
			return c.name, c.redirect, nil
		}
		if !allowed {
			return "", nil, errors.New("too many client metadata lookups; try again in a minute")
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
		req.Header.Set("Accept", "application/json")
		resp, err := cimdClient.Do(req)
		if err != nil {
			return "", nil, fmt.Errorf("couldn't fetch client metadata: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", nil, fmt.Errorf("client metadata returned %d", resp.StatusCode)
		}
		var doc struct {
			ClientID     string   `json:"client_id"`
			ClientName   string   `json:"client_name"`
			RedirectURIs []string `json:"redirect_uris"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc); err != nil {
			return "", nil, fmt.Errorf("bad client metadata: %w", err)
		}
		if doc.ClientID != id {
			return "", nil, errors.New("client metadata client_id doesn't match its URL")
		}
		for _, u := range doc.RedirectURIs {
			if !redirectAllowed(u) {
				return "", nil, fmt.Errorf("client metadata has a disallowed redirect URI: %s", u)
			}
		}
		name := doc.ClientName
		if name == "" {
			if p, err := url.Parse(id); err == nil {
				name = p.Host
			}
		}
		s.mu.Lock()
		if len(s.cimd) >= 200 { // bounded cache: drop the stalest entry
			var oldest string
			for k, v := range s.cimd {
				if oldest == "" || v.fetched.Before(s.cimd[oldest].fetched) {
					oldest = k
				}
			}
			delete(s.cimd, oldest)
		}
		s.cimd[id] = cimdEntry{name: name, redirect: doc.RedirectURIs, fetched: time.Now()}
		s.mu.Unlock()
		return name, doc.RedirectURIs, nil
	}
	c, err := s.E.DB.GetOAuthClient(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if c == nil {
		return "", nil, errors.New("unknown client_id")
	}
	return c.Name, strings.Split(c.RedirectURIs, "\n"), nil
}

// janitor drops stale sign-in attempts and codes.
func (s *Server) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		for k, p := range s.pending {
			if time.Since(p.Created) > 15*time.Minute {
				delete(s.pending, k)
			}
		}
		for k, c := range s.codes {
			if time.Now().After(c.Expires) {
				delete(s.codes, k)
			}
		}
		for k, ts := range s.budget {
			if len(ts) == 0 || time.Since(ts[len(ts)-1]) > 2*time.Hour {
				delete(s.budget, k)
			}
		}
		s.mu.Unlock()
	}
}
