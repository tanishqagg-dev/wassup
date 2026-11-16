package engine

import (
	"crypto/sha256"
	"testing"
	"time"
)

func sha(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

func TestNormalizePhone(t *testing.T) {
	cases := []struct{ in, cc, want string }{
		{"+1 (415) 555-0123", "", "14155550123"},
		{"0044 20 7946 0958", "", "442079460958"},
		{"919876543210", "", "919876543210"},
		{"09876543210", "91", "919876543210"},
		{"98765 43210", "+91", "919876543210"},
	}
	for _, c := range cases {
		got, err := normalizePhone(c.in, c.cc)
		if err != nil || got != c.want {
			t.Errorf("normalizePhone(%q, %q) = %q, %v; want %q", c.in, c.cc, got, err, c.want)
		}
	}
	for _, bad := range []string{"12345", "9876543210", "+1234567890123456"} {
		if _, err := normalizePhone(bad, ""); err == nil {
			t.Errorf("normalizePhone(%q) should fail", bad)
		}
	}
}

func TestNameScore(t *testing.T) {
	if nameScore("mom", "Mom") != 100 || nameScore("fam", "Family Group") != 60 || nameScore("group", "Family Group") != 50 || nameScore("ily", "Family") != 30 || nameScore("x", "Mom") != 0 {
		t.Error("unexpected scores")
	}
}

func TestParseTimeArg(t *testing.T) {
	got, err := parseTimeArg("2h")
	if err != nil || time.Since(got) < 119*time.Minute || time.Since(got) > 121*time.Minute {
		t.Errorf("2h: %v %v", got, err)
	}
	if _, err := parseTimeArg("2026-01-02"); err != nil {
		t.Error(err)
	}
	if _, err := parseTimeArg("next tuesday"); err == nil {
		t.Error("expected error")
	}
}

func TestFirstURL(t *testing.T) {
	if u := firstURL("see https://example.com/a?b=1)."); u != "https://example.com/a?b=1" {
		t.Errorf("got %q", u)
	}
}
