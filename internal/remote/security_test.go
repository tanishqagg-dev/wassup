package remote

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanishqagg-dev/wassup/internal/store"
)

func registerClient(t *testing.T, base, redirect string) string {
	t.Helper()
	reg, _ := http.Post(base+"/register", "application/json", strings.NewReader(`{"client_name":"x","redirect_uris":["`+redirect+`"]}`))
	var c map[string]any
	_ = json.NewDecoder(reg.Body).Decode(&c)
	return c["client_id"].(string)
}

func exchange(t *testing.T, base, clientID, redirect, code string) map[string]any {
	t.Helper()
	_, body := post(t, base+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {strings.Repeat("v", 50)}})
	var tok map[string]any
	_ = json.Unmarshal([]byte(body), &tok)
	return tok
}

func TestConcurrentRefreshRevokesConnection(t *testing.T) {
	s, base := newServer(t)
	id := registerClient(t, base, claudeCallback)
	tok := exchange(t, base, id, claudeCallback, signIn(t, s, base, id, claudeCallback))
	refresh := tok["refresh_token"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []string
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, body := post(t, base+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {id}})
			if r.StatusCode == 200 {
				var nt map[string]any
				_ = json.Unmarshal([]byte(body), &nt)
				mu.Lock()
				winners = append(winners, nt["access_token"].(string))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("%d concurrent refreshes succeeded; want exactly 1", len(winners))
	}
	// The replays revoked the connection, including the winner's fresh tokens.
	h := hashToken(winners[0])
	if tk, _ := s.E.DB.GetOAuthToken(t.Context(), h); tk == nil || !tk.Revoked {
		t.Fatal("tokens issued during a reuse race stay valid")
	}
}

func TestOTPConsumedOnceUnderConcurrency(t *testing.T) {
	s, base := newServer(t)
	id := registerClient(t, base, claudeCallback)
	sum := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" // S256 of 50 'v's is irrelevant here
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {claudeCallback}, "code_challenge": {sum}, "code_challenge_method": {"S256"}}
	resp, _ := http.Get(base + "/authorize?" + q.Encode())
	b := new(strings.Builder)
	_, _ = io_copy(b, resp)
	form := formFields(t, b.String())
	post(t, base+"/authorize/send-code", form)
	code := s.PendingCodes()[0]["code"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	grants := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := post(t, base+"/authorize/decide", url.Values{"req": form["req"], "csrf": form["csrf"], "action": {"approve"}, "code": {code}})
			if r.StatusCode == http.StatusFound {
				mu.Lock()
				grants++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if grants != 1 {
		t.Fatalf("one OTP produced %d authorization codes", grants)
	}
}

func TestTrustFollowsRedirectHost(t *testing.T) {
	s, base := newServer(t)
	claude := registerClient(t, base, claudeCallback)
	other := registerClient(t, base, "https://evil.example/cb")
	a := exchange(t, base, claude, claudeCallback, signIn(t, s, base, claude, claudeCallback))
	b := exchange(t, base, other, "https://evil.example/cb", signIn(t, s, base, other, "https://evil.example/cb"))
	ta, _ := s.E.DB.GetOAuthToken(t.Context(), hashToken(a["access_token"].(string)))
	tb, _ := s.E.DB.GetOAuthToken(t.Context(), hashToken(b["access_token"].(string)))
	if !ta.Trusted || tb.Trusted {
		t.Fatalf("trust: claude=%v other=%v", ta.Trusted, tb.Trusted)
	}
}

func TestCIMDRefusesPrivateHosts(t *testing.T) {
	s, _ := newServer(t)
	meta := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer meta.Close()
	_, _, err := s.lookupClient(t.Context(), meta.URL+"/m") // 127.0.0.1, default (guarded) client
	if err == nil || !strings.Contains(err.Error(), "public address") {
		t.Fatalf("private CIMD host not refused: %v", err)
	}
}

func TestOTPSendBudget(t *testing.T) {
	s, base := newServer(t)
	id := registerClient(t, base, claudeCallback)
	limited := false
	for i := 0; i < 8 && !limited; i++ {
		q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {claudeCallback}, "code_challenge": {"x"}, "code_challenge_method": {"S256"}}
		resp, _ := http.Get(base + "/authorize?" + q.Encode())
		if resp.StatusCode == 429 {
			limited = true
			break
		}
		b := new(strings.Builder)
		_, _ = io_copy(b, resp)
		_, html := post(t, base+"/authorize/send-code", formFields(t, b.String()))
		if strings.Contains(html, "Too many codes") {
			limited = true
		}
	}
	if !limited {
		t.Fatal("one visitor could request unlimited sign-in codes")
	}
	_ = s
}

var _ = store.ErrTokenReused

func mcpPost(t *testing.T, base, token, session, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSessionCantBeHijackedWithAnotherToken(t *testing.T) {
	s, base := newServer(t)
	claude := registerClient(t, base, claudeCallback)
	other := registerClient(t, base, "https://evil.example/cb")
	trusted := exchange(t, base, claude, claudeCallback, signIn(t, s, base, claude, claudeCallback))["access_token"].(string)
	untrusted := exchange(t, base, other, "https://evil.example/cb", signIn(t, s, base, other, "https://evil.example/cb"))["access_token"].(string)
	resp := mcpPost(t, base, trusted, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{}},"clientInfo":{"name":"t","version":"1"}}}`)
	resp.Body.Close()
	session := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != 200 || session == "" {
		t.Fatalf("initialize: %d session=%q", resp.StatusCode, session)
	}
	hijack := mcpPost(t, base, untrusted, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	hijack.Body.Close()
	if hijack.StatusCode != http.StatusForbidden {
		t.Fatalf("another connection's token reused a trusted session: %d", hijack.StatusCode)
	}
	own := mcpPost(t, base, trusted, session, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	own.Body.Close()
	if own.StatusCode >= 400 {
		t.Fatalf("legitimate session use refused: %d", own.StatusCode)
	}
}

func TestBudgetMapIsBounded(t *testing.T) {
	s, _ := newServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < 6000; i++ {
		s.allow(fmt.Sprintf("authorize:10.0.%d.%d", i/250, i%250), 30, time.Hour)
	}
	if len(s.budget) > 5001 {
		t.Fatalf("budget map grew to %d keys", len(s.budget))
	}
}

func TestSpoofedForwardingHeaderIgnored(t *testing.T) {
	s, _ := newServer(t)
	r := httptest.NewRequest("GET", "/authorize", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	r.Header.Set("CF-Connecting-IP", "6.6.6.6")
	if ip := s.clientIP(r); ip != "203.0.113.9" {
		t.Fatalf("clientIP = %s; want the proxy-appended address", ip)
	}
	r.Header.Del("X-Forwarded-For")
	if ip := s.clientIP(r); ip != "proxy" {
		t.Fatalf("unverified header believed: %s", ip)
	}
}
