package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tanishqagg-dev/wassup/internal/config"
)

func TestJSONHasWassupNeedsSameBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if jsonHasWassup(path, "/bin/wassup") {
		t.Fatal("missing file counted as connected")
	}
	if err := mergeMCPJSON(path, "/old/wassup"); err != nil {
		t.Fatal(err)
	}
	if jsonHasWassup(path, "/new/wassup") {
		t.Fatal("a config pointing at another binary counts as connected; setup would skip fixing it")
	}
	if !jsonHasWassup(path, "/old/wassup") {
		t.Fatal("merged config not detected")
	}
}

func TestMergeMCPJSONKeepsOtherServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"other":{"command":"x"}},"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeMCPJSON(path, "/bin/wassup"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{`"other"`, `"theme"`, `"whatsapp"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("merged config lost %s: %s", want, b)
		}
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("no backup written")
	}
}

func TestAddCodexAddsUpdatesAndLeavesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model = \"x\"\n\n[mcp_servers.other]\ncommand = \"other\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"added", "unchanged"} {
		got, err := addCodex(path, "/old/wassup")
		if err != nil || got != want {
			t.Fatalf("call %d: %q %v, want %q", i, got, err, want)
		}
	}
	if got, err := addCodex(path, "/new/wassup"); err != nil || got != "updated" {
		t.Fatalf("moved binary: %q %v", got, err)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	if n := strings.Count(text, "[mcp_servers.whatsapp]"); n != 1 {
		t.Fatalf("block written %d times:\n%s", n, text)
	}
	if !strings.Contains(text, `command = "/new/wassup"`) || strings.Contains(text, "/old/wassup") {
		t.Fatalf("stale path not replaced:\n%s", text)
	}
	if !strings.Contains(text, `command = "other"`) || !strings.Contains(text, `model = "x"`) {
		t.Fatalf("other settings changed:\n%s", text)
	}
	if !codexConnected(path, "/new/wassup") || codexConnected(path, "/old/wassup") {
		t.Fatal("codexConnected disagrees with the file")
	}
}

func TestSetApprovalPersists(t *testing.T) {
	t.Setenv("WASSUP_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := setApproval(cfg, "none"); err != nil {
		t.Fatal(err)
	}
	again, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := again.PolicyStrict(again.MCP.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if p.Approval != "none" {
		t.Fatalf("approval = %q after reload", p.Approval)
	}
}

func TestSetApprovalRejectsMissingProfile(t *testing.T) {
	t.Setenv("WASSUP_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.MCP.Profile = "typo"
	if err := setApproval(cfg, "none"); err == nil {
		t.Fatal("a missing profile was silently accepted")
	}
}
