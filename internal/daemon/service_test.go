package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tanishqagg-dev/wassup/internal/config"
)

func TestServiceLabelKeepsDefaultHomeName(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	t.Setenv("WASSUP_HOME", filepath.Join(home, ".wassup"))
	if got := serviceLabel(&config.Account{Name: "default"}); got != "dev.wassup.default" {
		t.Fatalf("default home label = %q; changing it would orphan existing installs", got)
	}
	t.Setenv("WASSUP_HOME", "")
	if got := serviceLabel(&config.Account{Name: "default"}); got != "dev.wassup.default" {
		t.Fatalf("unset WASSUP_HOME label = %q", got)
	}
}

func TestServiceLabelSeparatesOtherHomes(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	acct := &config.Account{Name: "default"}
	t.Setenv("WASSUP_HOME", a)
	la, ua := serviceLabel(acct), systemdUnit(acct)
	t.Setenv("WASSUP_HOME", b)
	lb := serviceLabel(acct)
	if la == "dev.wassup.default" || la == lb {
		t.Fatalf("labels collide: %q %q", la, lb)
	}
	if !strings.HasPrefix(la, "dev.wassup.default-") || !strings.HasPrefix(ua, "wassup-default-") {
		t.Fatalf("unexpected names %q %q", la, ua)
	}
}
