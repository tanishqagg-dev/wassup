package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func TestUnknownProfileFailsClosed(t *testing.T) {
	e := newTestEngine(t)
	c := &Call{Ctx: context.Background(), E: e, Method: registry["send.message"], Req: &api.Request{Meta: api.Meta{Profile: "restricted-agnet"}}}
	if _, err := c.Authorize(Action{Targets: []types.JID{alice}, Sends: 1}); code(err) != api.CodePolicyDenied {
		t.Fatalf("typo'd profile must be denied, got %v", err)
	}
}

func TestBatchKeepsGroupConfirmation(t *testing.T) {
	e := newTestEngine(t)
	e.cfg.Profiles["careful"] = &config.Policy{Mode: "open", ConfirmGroups: true}
	c := &Call{Ctx: context.Background(), E: e, Method: registry["send.many"], Req: &api.Request{Meta: api.Meta{Profile: "careful", Source: "cli"}}}
	if _, err := c.Authorize(Action{Targets: []types.JID{family}, Messages: true}); code(err) != api.CodeConfirmationRequired {
		t.Fatalf("send-many into a group must need confirmation, got %v", err)
	}
}

func TestDryRunLogoutDoesNothing(t *testing.T) {
	e := newTestEngine(t)
	raw, err := call(t, e, "auth.logout", nil, api.Meta{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var dr api.DryRun
	if json.Unmarshal(raw, &dr) != nil || !dr.DryRun {
		t.Fatalf("expected a dry-run description, got %s", raw)
	}
}

func TestHistoryEditBeforeOriginal(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	now := time.Now()
	// The edit arrives first (history order), then the original.
	e.applyEdit(ctx, alice, alice, "H1", text("fixed text"), now, false)
	e.handleMessage(ctx, msgEvt(alice, alice, "H1", now.Add(-time.Minute), text("typo text")), alice, false)
	m, _ := e.DB.GetMessage(ctx, alice.String(), "H1")
	if m == nil || !m.Edited || m.Text != "fixed text" {
		t.Fatalf("edit lost: %+v", m)
	}
	var stored waE2E.Message
	_ = proto.Unmarshal(m.Raw, &stored)
	if stored.GetConversation() != "fixed text" {
		t.Errorf("raw still holds old content: %v", stored.String())
	}
}

func TestEditMergesCaptionIntoRaw(t *testing.T) {
	orig := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("Monday"), MediaKey: []byte{1, 2, 3}, DirectPath: proto.String("/p")}}
	raw, _ := proto.Marshal(orig)
	var m waE2E.Message
	_ = proto.Unmarshal(mergeText(raw, "Tuesday"), &m)
	if m.GetImageMessage().GetCaption() != "Tuesday" || string(m.GetImageMessage().GetMediaKey()) != string([]byte{1, 2, 3}) || m.GetImageMessage().GetDirectPath() != "/p" {
		t.Errorf("merge lost data: %v", m.String())
	}
}

func TestOwnMessageKeepsLIDIdentity(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	lid := types.NewJID("123456789012345", types.HiddenUserServer)
	ev := msgEvt(family, lid, "OWN1", time.Now(), text("my poll"))
	ev.Info.IsFromMe = true
	e.handleMessage(ctx, ev, types.EmptyJID, true)
	m, _ := e.DB.GetMessage(ctx, family.String(), "OWN1")
	if m == nil || m.SenderRaw != lid.String() {
		t.Fatalf("own LID identity dropped: %+v", m)
	}
	if info := e.infoFor(m); info.Sender != lid || !info.IsFromMe {
		t.Errorf("infoFor = %+v", info)
	}
}

func TestSharedGateIsAtomic(t *testing.T) {
	e := newTestEngine(t)
	pol := &config.Policy{Mode: "open", RatePerMinute: 1}
	ctx := context.Background()
	var mu sync.Mutex
	passed := 0
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := e.acquireSend(ctx, pol, "p", []types.JID{alice}, 1, 0)
			if err != nil {
				return
			}
			_ = e.DB.AddAudit(ctx, store.Audit{Time: time.Now(), Profile: "p", Method: "send.message", OK: true, Sends: 1, Targets: alice.String()})
			mu.Lock()
			passed++
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	if passed != 1 {
		t.Fatalf("%d concurrent sends passed a limit of 1", passed)
	}
}

func TestCaptionEditBeforeOriginalKeepsMediaAndOwnership(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	e.applyEdit(ctx, family, alice, "C1", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("new caption")}}, t0.Add(time.Minute), false)
	orig := msgEvt(family, alice, "C1", t0, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("old"), MediaKey: []byte{9, 9}, Mimetype: proto.String("image/jpeg")}})
	orig.Info.IsFromMe = true
	e.handleMessage(ctx, orig, types.EmptyJID, false)
	m, _ := e.DB.GetMessage(ctx, family.String(), "C1")
	if m == nil || !m.Edited || m.Text != "new caption" || !m.FromMe || !m.Time.Equal(t0) {
		t.Fatalf("metadata wrong: %+v", m)
	}
	var raw waE2E.Message
	_ = proto.Unmarshal(m.Raw, &raw)
	if raw.GetImageMessage().GetCaption() != "new caption" || len(raw.GetImageMessage().GetMediaKey()) != 2 {
		t.Errorf("media keys lost: %v", raw.String())
	}
}

func TestHistoryDoesNotReplaceStoredIdentity(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	lid := types.NewJID("123456789012345", types.HiddenUserServer)
	live := msgEvt(family, lid, "ID1", time.Now(), text("poll-ish"))
	live.Info.IsFromMe = true
	e.handleMessage(ctx, live, types.EmptyJID, true)
	replay := msgEvt(family, types.NewJID("14155550123", types.DefaultUserServer), "ID1", time.Now(), text("poll-ish"))
	replay.Info.IsFromMe = true
	e.handleMessage(ctx, replay, family, false)
	if m, _ := e.DB.GetMessage(ctx, family.String(), "ID1"); m.SenderRaw != lid.String() {
		t.Errorf("history replaced LID with %s", m.SenderRaw)
	}
}

func TestAgentApprovalFlow(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	params := api.ScheduleParams{SendParams: api.SendParams{To: "+14155550123", Text: "see you at 8"}, In: "2h"}
	// The agent profile requires approval for every message: the request is parked, nothing is scheduled.
	_, err := call(t, e, "schedule.message", params, api.Meta{Source: "mcp", Profile: "agent"})
	if code(err) != api.CodeApprovalRequired {
		t.Fatalf("want approval_required, got %v", err)
	}
	d, _ := api.AsError(err).Details.(map[string]any)
	id, _ := d["draft_id"].(int64)
	if id == 0 || !strings.Contains(d["preview"].(string), "see you at 8") {
		t.Fatalf("draft details: %+v", d)
	}
	if rows, _ := e.DB.ListScheduled(ctx, "all", 10); len(rows) != 0 {
		t.Fatal("scheduled before approval")
	}
	// The agent can't approve its own request.
	if _, err := call(t, e, "drafts.approve", api.DraftApproveParams{ID: id}, api.Meta{Source: "mcp"}); code(err) != api.CodePolicyDenied {
		t.Fatalf("agent self-approval must be denied, got %v", err)
	}
	// A human approves, with an edit; the exact request replays.
	edited := "see you at 9"
	if _, err := call(t, e, "drafts.approve", api.DraftApproveParams{ID: id, Text: &edited}, api.Meta{Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.DB.ListScheduled(ctx, "all", 10)
	if len(rows) != 1 || !strings.Contains(rows[0].Params, "see you at 9") {
		t.Fatalf("replay didn't schedule the edited message: %+v", rows)
	}
	dr, _ := e.DB.GetDraft(ctx, id)
	if dr.Status != "sent" || dr.ResolvedBy != "cli" {
		t.Errorf("draft state: %+v", dr)
	}
	if _, err := call(t, e, "drafts.approve", api.DraftApproveParams{ID: id}, api.Meta{Source: "cli"}); err == nil {
		t.Error("a draft must not replay twice")
	}
}

func TestApprovalReplyPattern(t *testing.T) {
	for _, ok := range []string{"yes 4821", "YES 4821", "ok #4821", "no 4821", " send 4821 ", "Yes 4821!"} {
		if !approvalReply.MatchString(ok) {
			t.Errorf("%q should match", ok)
		}
	}
	for _, bad := range []string{"yes", "yes 482", "sure 4821", "yes 4821 please send it", "4821"} {
		if approvalReply.MatchString(bad) {
			t.Errorf("%q should not match", bad)
		}
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 11, 16, 14, 0, 0, 0, time.Local)
	cases := map[[2]string]time.Time{
		{"", "2h"}:               now.Add(2 * time.Hour),
		{"9am", ""}:              time.Date(2026, 11, 17, 9, 0, 0, 0, time.Local),
		{"3:30pm", ""}:           time.Date(2026, 11, 16, 15, 30, 0, 0, time.Local),
		{"tomorrow 9:15am", ""}:  time.Date(2026, 11, 17, 9, 15, 0, 0, time.Local),
		{"2026-12-01 08:00", ""}: time.Date(2026, 12, 1, 8, 0, 0, 0, time.Local),
		{"21:45", ""}:            time.Date(2026, 11, 16, 21, 45, 0, 0, time.Local),
	}
	for in, want := range cases {
		got, err := ParseWhen(in[0], in[1], now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseWhen(%q,%q) = %v, %v; want %v", in[0], in[1], got, err, want)
		}
	}
	if _, err := ParseWhen("lunchtime", "", now); err == nil {
		t.Error("expected error")
	}
}

func TestAuditChainDetectsTampering(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = e.DB.AddAudit(ctx, store.Audit{Time: time.Now(), Source: "cli", Profile: "default", Method: "send.message", OK: true, Sends: 1})
	}
	if n, bad, err := e.DB.VerifyAudit(ctx); err != nil || bad != 0 || n != 3 {
		t.Fatalf("intact chain: %d %d %v", n, bad, err)
	}
	if err := e.DB.ExecForTest(ctx, `UPDATE audit SET method='messages.delete' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if _, bad, _ := e.DB.VerifyAudit(ctx); bad != 2 {
		t.Errorf("tampering not detected at row 2 (got %d)", bad)
	}
}

func TestWarmupScalesLimits(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	_ = e.DB.Set(ctx, "linked_at", time.Now().Add(-2*24*time.Hour).Format(time.RFC3339))
	pol := &config.Policy{Mode: "open", RatePerMinute: 8, Warmup: true}
	for i := 0; i < 2; i++ {
		_ = e.DB.AddAudit(ctx, store.Audit{Time: time.Now(), Profile: "w", Method: "send.message", OK: true, Sends: 1})
	}
	// 25% of 8 = 2 per minute during week one, so a third send is refused.
	if err := e.checkRate(ctx, pol, "w", nil, 1); code(err) != api.CodeRateLimited || !strings.Contains(err.Error(), "warmup") {
		t.Fatalf("warmup not applied: %v", err)
	}
	pol.Warmup = false
	if err := e.checkRate(ctx, pol, "w", nil, 1); err != nil {
		t.Fatalf("without warmup 8/min should allow it: %v", err)
	}
}

func TestAgentsCantPickOutputPaths(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	e.handleMessage(ctx, msgEvt(alice, alice, "F1", time.Now(), &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("x.pdf"), Mimetype: proto.String("application/pdf")}}), types.EmptyJID, true)
	for _, src := range []string{"mcp", "remote"} {
		if _, err := call(t, e, "messages.download", api.DownloadParams{ID: "F1", Out: "/tmp/../Users/x/.wassup/config.toml"}, api.Meta{Source: src}); code(err) != api.CodePolicyDenied {
			t.Errorf("%s chose a download path: %v", src, err)
		}
	}
}

func TestRemoteAgentsOnlyAttachMediaFolderFiles(t *testing.T) {
	e := newTestEngine(t)
	c := &Call{Ctx: context.Background(), E: e, Method: registry["send.message"], Req: &api.Request{Meta: api.Meta{Source: "remote"}}}
	secret := filepath.Join(t.TempDir(), "id_rsa")
	_ = os.WriteFile(secret, []byte("key"), 0o600)
	if err := c.checkReadable(secret); code(err) != api.CodePolicyDenied {
		t.Errorf("remote agent could attach %s: %v", secret, err)
	}
	inside := filepath.Join(e.Acct.MediaDir, "photo.jpg")
	_ = os.WriteFile(inside, []byte("jpg"), 0o600)
	if err := c.checkReadable(inside); err != nil {
		t.Errorf("media folder file refused: %v", err)
	}
	link := filepath.Join(e.Acct.MediaDir, "sneaky.jpg")
	_ = os.Symlink(secret, link)
	if err := c.checkReadable(link); code(err) != api.CodePolicyDenied {
		t.Errorf("symlink out of the media folder allowed: %v", err)
	}
	c.Req.Meta.Source = "mcp" // local agents keep normal file access
	if err := c.checkReadable(secret); err != nil {
		t.Errorf("local MCP refused: %v", err)
	}
}

func TestApprovalIsBoundToRecipients(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	_ = e.DB.SetChatName(ctx, family.String(), "group", "Family Group")
	_, err := call(t, e, "schedule.message", api.ScheduleParams{SendParams: api.SendParams{To: "Family Group", Text: "hi all"}, In: "2h"}, api.Meta{Source: "mcp", Profile: "agent"})
	d, _ := api.AsError(err).Details.(map[string]any)
	id, _ := d["draft_id"].(int64)
	dr, _ := e.DB.GetDraft(ctx, id)
	if !strings.Contains(dr.Params, family.String()) {
		t.Fatalf("recipient not frozen to a JID: %s", dr.Params)
	}
	// Point the parked request somewhere else behind the human's back: replay must refuse.
	other := types.NewJID("120363999999999999", types.GroupServer)
	_ = e.DB.SetChatName(ctx, other.String(), "group", "Other")
	if err := e.DB.ExecForTest(ctx, `UPDATE drafts SET params=replace(params, '`+family.String()+`', '`+other.String()+`') WHERE id=`+fmt.Sprint(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, e, "drafts.approve", api.DraftApproveParams{ID: id}, api.Meta{Source: "cli"}); code(err) != api.CodePolicyDenied {
		t.Fatalf("replay to changed recipients allowed: %v", err)
	}
	if rows, _ := e.DB.ListScheduled(ctx, "all", 5); len(rows) != 0 {
		t.Fatal("scheduled despite recipient change")
	}
}
