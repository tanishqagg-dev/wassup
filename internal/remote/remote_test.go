package remote

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/engine"
)

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("WASSUP_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Remote.Enabled, cfg.Remote.Listen = true, "127.0.0.1:0"
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	acct, _ := config.AccountPaths("t")
	e, err := engine.New(acct, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	s, err := Start(e, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, "http://" + s.Addr()
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func getJSON(t *testing.T, u string, out any) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", u, resp.StatusCode)
	}
	_ = json.NewDecoder(resp.Body).Decode(out)
}

var hidden = regexp.MustCompile(`name="(req|csrf)" value="([^"]+)"`)

func formFields(t *testing.T, html string) url.Values {
	v := url.Values{}
	for _, m := range hidden.FindAllStringSubmatch(html, -1) {
		v.Set(m[1], m[2])
	}
	if v.Get("req") == "" || v.Get("csrf") == "" {
		t.Fatalf("consent page has no form tokens:\n%s", html)
	}
	return v
}

func post(t *testing.T, u string, v url.Values) (*http.Response, string) {
	t.Helper()
	resp, err := noRedirect.PostForm(u, v)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// signIn walks the whole consent flow and returns the authorization code.
func signIn(t *testing.T, s *Server, base, clientID, redirect string) (code string) {
	t.Helper()
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"st8"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "scope": {"whatsapp"}}
	resp, err := http.Get(base + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Send me a code") {
		t.Fatalf("consent page: %d %s", resp.StatusCode, body)
	}
	form := formFields(t, string(body))
	if _, html := post(t, base+"/authorize/send-code", form); !strings.Contains(html, "connected right now") {
		t.Fatalf("expected the not-connected fallback note:\n%s", html)
	}
	codes := s.PendingCodes()
	if len(codes) != 1 {
		t.Fatalf("pending codes: %v", codes)
	}
	bad := url.Values{"req": form["req"], "csrf": form["csrf"], "action": {"approve"}, "code": {"000000"}}
	if codes[0]["code"] == "000000" {
		bad.Set("code", "111111")
	}
	if r, html := post(t, base+"/authorize/decide", bad); r.StatusCode != 200 || !strings.Contains(html, "match. Check") {
		t.Fatalf("wrong code accepted: %d", r.StatusCode)
	}
	forged := url.Values{"req": form["req"], "csrf": {"nope"}, "action": {"approve"}, "code": {codes[0]["code"].(string)}}
	if r, _ := post(t, base+"/authorize/decide", forged); r.StatusCode != 400 {
		t.Fatalf("CSRF check missing: %d", r.StatusCode)
	}
	good := url.Values{"req": form["req"], "csrf": form["csrf"], "action": {"approve"}, "code": {codes[0]["code"].(string)}}
	r, _ := post(t, base+"/authorize/decide", good)
	if r.StatusCode != http.StatusFound {
		t.Fatalf("approve: %d", r.StatusCode)
	}
	loc, _ := url.Parse(r.Header.Get("Location"))
	if loc.Query().Get("state") != "st8" || loc.Query().Get("code") == "" || loc.Query().Get("iss") == "" {
		t.Fatalf("redirect: %s", loc)
	}
	return loc.Query().Get("code")
}

func TestOAuthAndMCPEndToEnd(t *testing.T) {
	s, base := newServer(t)

	// 1. Unauthenticated MCP call starts discovery.
	resp, _ := http.Post(base+"/mcp", "application/json", strings.NewReader(`{}`))
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="`+base+`/.well-known/oauth-protected-resource"`) {
		t.Fatalf("401 challenge: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	var prm, asm map[string]any
	getJSON(t, base+"/.well-known/oauth-protected-resource", &prm)
	getJSON(t, base+"/.well-known/oauth-authorization-server", &asm)
	if prm["resource"] != base+"/mcp" || asm["registration_endpoint"] != base+"/register" || asm["client_id_metadata_document_supported"] != true {
		t.Fatalf("metadata: %v %v", prm, asm)
	}

	// 2. Dynamic client registration; non-https redirects are refused.
	bad, _ := http.Post(base+"/register", "application/json", strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`))
	if bad.StatusCode != 400 {
		t.Fatalf("insecure redirect registered: %d", bad.StatusCode)
	}
	reg, _ := http.Post(base+"/register", "application/json", strings.NewReader(`{"client_name":"Claude","redirect_uris":["`+claudeCallback+`"]}`))
	var client map[string]any
	_ = json.NewDecoder(reg.Body).Decode(&client)
	clientID, _ := client["client_id"].(string)
	if reg.StatusCode != 201 || clientID == "" {
		t.Fatalf("register: %d %v", reg.StatusCode, client)
	}

	// 3. Consent with the WhatsApp code, then PKCE exchange.
	code := signIn(t, s, base, clientID, claudeCallback)
	verifier := strings.Repeat("v", 50)
	if r, _ := post(t, base+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {claudeCallback}, "code_verifier": {"wrong" + verifier}}); r.StatusCode != 400 {
		t.Fatal("bad PKCE verifier accepted")
	}
	// The code was burned by the failed attempt; sign in again.
	code = signIn(t, s, base, clientID, claudeCallback)
	r, body := post(t, base+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {claudeCallback}, "code_verifier": {verifier}})
	var tok map[string]any
	_ = json.Unmarshal([]byte(body), &tok)
	access, _ := tok["access_token"].(string)
	refresh, _ := tok["refresh_token"].(string)
	if r.StatusCode != 200 || access == "" || refresh == "" {
		t.Fatalf("token: %d %s", r.StatusCode, body)
	}
	if r, _ := post(t, base+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "code_verifier": {verifier}}); r.StatusCode != 400 {
		t.Fatal("authorization code reused")
	}

	// 4. Authenticated MCP initialize.
	mcpCall := func(token string) int {
		req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 && !strings.Contains(string(b), `"wassup"`) {
			t.Fatalf("initialize body: %s", b)
		}
		return resp.StatusCode
	}
	if st := mcpCall(access); st != 200 {
		t.Fatalf("authenticated MCP: %d", st)
	}

	// 5. Refresh rotates; replaying the old refresh token revokes the whole connection.
	r, body = post(t, base+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	var tok2 map[string]any
	_ = json.Unmarshal([]byte(body), &tok2)
	if r.StatusCode != 200 || tok2["refresh_token"] == refresh {
		t.Fatalf("refresh: %d %s", r.StatusCode, body)
	}
	if r, body := post(t, base+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}); r.StatusCode != 400 || !strings.Contains(body, "invalid_grant") {
		t.Fatalf("refresh reuse not caught: %d %s", r.StatusCode, body)
	}
	if st := mcpCall(tok2["access_token"].(string)); st != 401 {
		t.Fatalf("family not revoked after reuse: %d", st)
	}
}

func TestCIMDAndLoopbackRedirects(t *testing.T) {
	s, base := newServer(t)
	var metaURL string
	meta := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": metaURL, "client_name": "Claude Code", "redirect_uris": []string{"http://localhost/callback", "http://127.0.0.1/callback"}})
	}))
	defer meta.Close()
	metaURL = meta.URL + "/client-metadata"
	orig := cimdClient.Transport
	cimdClient.Transport = meta.Client().Transport
	defer func() { cimdClient.Transport = orig }()
	code := signIn(t, s, base, metaURL, "http://localhost:51234/callback")
	if code == "" {
		t.Fatal("no code")
	}
	if !redirectMatches([]string{"http://127.0.0.1/callback"}, "http://127.0.0.1:9999/callback") {
		t.Error("loopback port should be ignored")
	}
	if redirectMatches([]string{"http://127.0.0.1/callback"}, "http://127.0.0.1:9999/other") || redirectMatches([]string{claudeCallback}, "https://claude.ai.evil.com/api/mcp/auth_callback") {
		t.Error("redirect matching too loose")
	}
}

func io_copy(b *strings.Builder, resp *http.Response) (int64, error) {
	defer resp.Body.Close()
	n, err := io.Copy(b, resp.Body)
	return n, err
}
