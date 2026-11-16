package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFTSQuery(t *testing.T) {
	cases := map[string]string{
		"hello world":      `"hello" "world"`,
		`"exact phrase" x`: `"exact phrase" "x"`,
		"pass*":            `"pass"*`,
		`a"b`:              `"a""b"`,
		"   ":              "",
	}
	for in, want := range cases {
		if got := FTSQuery(in); got != want {
			t.Errorf("FTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusNeverDowngrades(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	_, _ = db.UpsertMessage(ctx, &Message{Chat: "c", ID: "1", FromMe: true, Time: time.Now(), Type: "text", Text: "x", Status: "sent"})
	_ = db.SetStatus(ctx, "c", []string{"1"}, "read")
	_ = db.SetStatus(ctx, "c", []string{"1"}, "delivered")
	m, _ := db.GetMessage(ctx, "c", "1")
	if m.Status != "read" {
		t.Errorf("status = %s", m.Status)
	}
}

func TestCountSendsWindow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now()
	_ = db.AddAudit(ctx, Audit{Time: now.Add(-2 * time.Minute), Profile: "p", Method: "send.message", OK: true, Sends: 1, Targets: "a"})
	_ = db.AddAudit(ctx, Audit{Time: now.Add(-10 * time.Second), Profile: "p", Method: "send.message", OK: true, Sends: 1, Targets: "a"})
	_ = db.AddAudit(ctx, Audit{Time: now.Add(-5 * time.Second), Profile: "p", Method: "send.message", OK: false, Sends: 0, Targets: "a"})
	_ = db.AddAudit(ctx, Audit{Time: now.Add(-5 * time.Second), Profile: "p", Method: "send.message", OK: true, DryRun: true, Targets: "a"})
	if n := db.CountSends(ctx, "p", now.Add(-time.Minute), ""); n != 1 {
		t.Errorf("minute window = %d", n)
	}
	if n := db.CountSends(ctx, "p", now.Add(-time.Hour), "a"); n != 2 {
		t.Errorf("hour window = %d", n)
	}
}

func TestTouchChatKeepsNewest(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now()
	_ = db.TouchChat(ctx, "c", "dm", now, "new", "newest", true)
	_ = db.TouchChat(ctx, "c", "dm", now.Add(-time.Hour), "old", "older", false)
	c, _ := db.GetChat(ctx, "c")
	if c.LastMessageID != "new" || c.LastPreview != "newest" || c.Unread != 1 {
		t.Errorf("chat = %+v", c)
	}
}

func TestAuditDigestIsUnambiguous(t *testing.T) {
	key := []byte("k")
	a := Audit{Source: "cli", Method: "send.message", Targets: "a|b", Params: "c"}
	b := Audit{Source: "cli", Method: "send.message", Targets: "a", Params: "b|c"}
	if auditDigest(key, "", 1, a) == auditDigest(key, "", 1, b) {
		t.Fatal("moving a | between fields doesn't change the hash")
	}
}
