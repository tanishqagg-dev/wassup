package config

import (
	"os"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{"7d": 7 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "90s": 90 * time.Second, "": 0, "1.5d": 36 * time.Hour}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v", in, got, err)
		}
	}
	if FormatDuration(7*24*time.Hour) != "7d" || FormatDuration(90*time.Second) != "1m30s" {
		t.Error("FormatDuration")
	}
}

func TestDefaultsRoundTrip(t *testing.T) {
	t.Setenv("WASSUP_HOME", t.TempDir())
	cfg, err := Load() // creates the file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path()); err != nil {
		t.Fatal("config file not written")
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.Profiles["agent"].MinInterval.Duration != 1500*time.Millisecond || again.Profiles["agent"].ConfirmBulk != 5 {
		t.Errorf("agent profile lost: %+v", again.Profiles["agent"])
	}
	if again.Daemon.ConnectTimeout.Duration != cfg.Daemon.ConnectTimeout.Duration || !again.Send.VerifyNumbers {
		t.Error("daemon/send defaults lost")
	}
	p, name := again.Policy("nope")
	if name != "default" || p == nil {
		t.Error("unknown profile should fall back to default")
	}
}

func TestBadConfigIsReported(t *testing.T) {
	t.Setenv("WASSUP_HOME", t.TempDir())
	_ = os.MkdirAll(Home(), 0o700)
	_ = os.WriteFile(Path(), []byte("[daemon\nauto_start = maybe"), 0o600)
	if _, err := Load(); err == nil {
		t.Error("expected a parse error")
	}
}

func TestAccountNames(t *testing.T) {
	if _, err := AccountPaths("../etc"); err == nil {
		t.Error("path traversal accepted")
	}
	a, err := AccountPaths("work")
	if err != nil || a.Name != "work" {
		t.Error(err)
	}
}
