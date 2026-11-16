package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	t.Setenv("WASSUP_HOME", t.TempDir())
	acct, err := config.AccountPaths("test")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(acct, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	return e
}

var (
	alice  = types.NewJID("14155550123", types.DefaultUserServer)
	bob    = types.NewJID("919876543210", types.DefaultUserServer)
	family = types.NewJID("120363025246125244", types.GroupServer)
)

func msgEvt(chat, sender types.JID, id string, ts time.Time, m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: chat.Server == types.GroupServer},
			ID:            id, Timestamp: ts, PushName: "Push " + sender.User,
		},
		Message: m,
	}
}

func text(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func call(t *testing.T, e *Engine, method string, params any, meta api.Meta) (json.RawMessage, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := e.Dispatch(context.Background(), &api.Request{Method: method, Params: raw, Meta: meta}, nil)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	return b, nil
}

func code(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestMessageLifecycle(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)

	e.handleMessage(ctx, msgEvt(alice, alice, "M1", t0, text("hello there, invoice attached")), types.EmptyJID, true)
	e.handleMessage(ctx, msgEvt(alice, alice, "M2", t0.Add(time.Minute), text("second message")), types.EmptyJID, true)
	e.handleMessage(ctx, msgEvt(alice, alice, "M3", t0.Add(2*time.Minute), text("to be deleted")), types.EmptyJID, true)

	// Edit M2, revoke M3, react to M1.
	e.handleMessage(ctx, msgEvt(alice, alice, "E1", t0.Add(3*time.Minute), &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("M2")}, EditedMessage: text("second message, edited")}}), types.EmptyJID, true)
	e.handleMessage(ctx, msgEvt(alice, alice, "R1", t0.Add(4*time.Minute), &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("M3")}}}), types.EmptyJID, true)
	e.handleMessage(ctx, msgEvt(alice, alice, "X1", t0.Add(5*time.Minute), &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
		Key: &waCommon.MessageKey{ID: proto.String("M1")}, Text: proto.String("👍")}}), types.EmptyJID, true)

	// A re-delivered copy (history sync) must not undo the edit.
	e.handleMessage(ctx, msgEvt(alice, alice, "M2", t0.Add(time.Minute), text("second message")), alice, false)

	raw, err := call(t, e, "messages.list", api.ListMessagesParams{Chat: "+1 415 555 0123"}, api.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []api.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d: %s", len(msgs), raw)
	}
	if msgs[0].ID != "M1" || len(msgs[0].Reactions) != 1 || msgs[0].Reactions[0].Emoji != "👍" {
		t.Errorf("reaction not attached: %+v", msgs[0])
	}
	if !msgs[1].Edited || msgs[1].Text != "second message, edited" {
		t.Errorf("edit lost: %+v", msgs[1])
	}
	if !msgs[2].Revoked || msgs[2].Text != "" {
		t.Errorf("revoke not applied: %+v", msgs[2])
	}

	raw, err = call(t, e, "messages.search", api.SearchParams{Query: "invoice"}, api.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &msgs)
	if len(msgs) != 1 || msgs[0].ID != "M1" {
		t.Errorf("search: %s", raw)
	}
	// Prefix and FTS-hostile input must not error.
	for _, q := range []string{"inv*", `"hello there"`, `AND OR (`, `a"b`} {
		if _, err := call(t, e, "messages.search", api.SearchParams{Query: q}, api.Meta{}); err != nil {
			t.Errorf("search %q: %v", q, err)
		}
	}

	ch, _ := e.DB.GetChat(ctx, alice.String())
	if ch == nil || ch.Unread != 3 || ch.LastMessageID != "M3" {
		t.Errorf("chat state: %+v", ch)
	}
}

func TestPollAndMedia(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	now := time.Now()
	poll := &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{
		Name: proto.String("Lunch?"), SelectableOptionsCount: proto.Uint32(1),
		Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Pizza")}, {OptionName: proto.String("Sushi")}},
	}}
	e.handleMessage(ctx, msgEvt(family, bob, "P1", now, poll), types.EmptyJID, true)
	img := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("beach"), Mimetype: proto.String("image/jpeg"), FileLength: proto.Uint64(2048), Width: proto.Uint32(10), Height: proto.Uint32(20)}}
	e.handleMessage(ctx, msgEvt(family, alice, "I1", now.Add(time.Second), img), types.EmptyJID, true)
	voice := &waE2E.Message{AudioMessage: &waE2E.AudioMessage{PTT: proto.Bool(true), Seconds: proto.Uint32(7), Mimetype: proto.String("audio/ogg; codecs=opus")}}
	e.handleMessage(ctx, msgEvt(family, alice, "V1", now.Add(2*time.Second), voice), types.EmptyJID, true)

	raw, err := call(t, e, "messages.list", api.ListMessagesParams{Chat: family.String()}, api.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []api.Message
	_ = json.Unmarshal(raw, &msgs)
	if len(msgs) != 3 {
		t.Fatalf("got %s", raw)
	}
	if msgs[0].Type != "poll" || msgs[0].Poll == nil || len(msgs[0].Poll.Options) != 2 {
		t.Errorf("poll: %+v", msgs[0])
	}
	if msgs[1].Type != "image" || msgs[1].Text != "beach" || msgs[1].Media == nil || msgs[1].Media.Size != 2048 {
		t.Errorf("image: %+v", msgs[1])
	}
	if msgs[2].Type != "voice" || msgs[2].Media.Seconds != 7 {
		t.Errorf("voice: %+v", msgs[2])
	}
	// Option hashes in votes map back to names.
	sm, _ := e.DB.GetMessage(ctx, family.String(), "P1")
	names := pollOptionNames(sm.Raw, [][]byte{sha("Sushi")})
	if len(names) != 1 || names[0] != "Sushi" {
		t.Errorf("poll option mapping: %v", names)
	}
}

func TestResolve(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	_ = e.DB.SetChatName(ctx, family.String(), "group", "Family Group")
	_ = e.DB.SetChatName(ctx, types.NewJID("111111111111", types.DefaultUserServer).String(), "dm", "Rahul Sharma")
	_ = e.DB.SetChatName(ctx, types.NewJID("222222222222", types.DefaultUserServer).String(), "dm", "Rahul Verma")

	cases := []struct {
		q, want, code string
	}{
		{"+14155550123", alice.String(), ""},
		{"0091 98765 43210", bob.String(), ""},
		{"14155550123@s.whatsapp.net", alice.String(), ""},
		{"family", family.String(), ""},
		{"Family Group", family.String(), ""},
		{"rahul", "", api.CodeAmbiguous},
		{"Rahul Verma", "222222222222@s.whatsapp.net", ""},
		{"9876543210", "", api.CodeUsage}, // no country code configured
		{"nobody at all", "", api.CodeNotFound},
	}
	for _, c := range cases {
		j, _, err := e.resolveLocal(ctx, c.q)
		if c.code != "" {
			if code(err) != c.code {
				t.Errorf("%q: want %s, got %v", c.q, c.code, err)
			}
			continue
		}
		if err != nil || j.String() != c.want {
			t.Errorf("%q: want %s, got %s (%v)", c.q, c.want, j, err)
		}
	}
	e.cfg.Send.DefaultCountryCode = "91"
	if j, _, err := e.resolveLocal(ctx, "09876543210"); err != nil || j != bob {
		t.Errorf("default country code: %s %v", j, err)
	}
}

func TestPolicy(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	_ = e.DB.SetChatName(ctx, family.String(), "group", "Family Group")
	authz := func(profile string, yes bool, a Action, method string) error {
		c := &Call{Ctx: ctx, E: e, Method: registry[method], Req: &api.Request{Method: method, Meta: api.Meta{Profile: profile, Yes: yes, Source: "cli"}}}
		_, err := c.Authorize(a)
		if c.unlock != nil {
			c.unlock()
		}
		return err
	}
	send := Action{Targets: []types.JID{alice}, Sends: 1}
	e.cfg.Profiles["ro"] = &config.Policy{Mode: "read_only"}
	e.cfg.Profiles["allow"] = &config.Policy{Mode: "allowlist", Allow: []string{"+14155550123", "*@g.us"}}
	e.cfg.Profiles["careful"] = &config.Policy{Mode: "open", ConfirmGroups: true, ConfirmNewChats: true, ConfirmDestructive: true, ConfirmBulk: 2}
	e.cfg.Profiles["deny"] = &config.Policy{Mode: "open", Deny: []string{"Family Group"}, DenyActions: []string{"groups.*"}}
	e.cfg.Profiles["slow"] = &config.Policy{Mode: "open", RatePerMinute: 2}

	if err := authz("ro", false, send, "send.message"); code(err) != api.CodePolicyDenied {
		t.Errorf("read_only: %v", err)
	}
	if err := authz("allow", false, send, "send.message"); err != nil {
		t.Errorf("allowlisted phone: %v", err)
	}
	if err := authz("allow", false, Action{Targets: []types.JID{bob}, Sends: 1}, "send.message"); code(err) != api.CodePolicyDenied {
		t.Errorf("not allowlisted: %v", err)
	}
	if err := authz("allow", false, Action{Targets: []types.JID{family}, Sends: 1}, "send.message"); err != nil {
		t.Errorf("glob allow: %v", err)
	}
	if err := authz("careful", false, Action{Targets: []types.JID{family}, Sends: 1}, "send.message"); code(err) != api.CodeConfirmationRequired {
		t.Errorf("confirm group: %v", err)
	}
	if err := authz("careful", false, send, "send.message"); code(err) != api.CodeConfirmationRequired {
		t.Errorf("confirm new chat: %v", err)
	}
	if err := authz("careful", true, send, "send.message"); err != nil {
		t.Errorf("--yes should pass: %v", err)
	}
	if err := authz("careful", false, Action{Targets: []types.JID{alice, bob, family}}, "chats.update"); code(err) != api.CodeConfirmationRequired {
		t.Errorf("confirm bulk: %v", err)
	}
	if err := authz("careful", false, Action{Destructive: true}, "groups.leave"); code(err) != api.CodeConfirmationRequired {
		t.Errorf("confirm destructive: %v", err)
	}
	if err := authz("deny", false, Action{Targets: []types.JID{family}, Sends: 1}, "send.message"); code(err) != api.CodePolicyDenied {
		t.Errorf("deny by name: %v", err)
	}
	if err := authz("deny", false, Action{}, "groups.create"); code(err) != api.CodePolicyDenied {
		t.Errorf("deny_actions glob: %v", err)
	}
	for i := 0; i < 2; i++ {
		_ = e.DB.AddAudit(ctx, store.Audit{Time: time.Now(), Profile: "slow", Method: "send.message", OK: true, Sends: 1, Targets: alice.String()})
	}
	err := authz("slow", false, send, "send.message")
	if code(err) != api.CodeRateLimited {
		t.Fatalf("rate limit: %v", err)
	}
	d, _ := api.AsError(err).Details.(map[string]any)
	if s, _ := d["retry_after_seconds"].(int); s < 1 || s > 60 {
		t.Errorf("retry_after_seconds = %v", d["retry_after_seconds"])
	}
	// Dry runs never hit rate limits and never send.
	c := &Call{Ctx: ctx, E: e, Method: registry["send.message"], Req: &api.Request{Meta: api.Meta{Profile: "slow", DryRun: true}}}
	if dry, err := c.Authorize(send); err != nil || !dry {
		t.Errorf("dry run: %v %v", dry, err)
	}
}

func TestLocalDeleteGoesThroughPolicyAndAudit(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()
	e.handleMessage(ctx, msgEvt(alice, alice, "D1", time.Now(), text("bye")), types.EmptyJID, true)
	e.cfg.Profiles["ro"] = &config.Policy{Mode: "read_only"}
	if _, err := call(t, e, "messages.delete", api.DeleteParams{ID: "D1", LocalOnly: true}, api.Meta{Profile: "ro", Source: "test"}); code(err) != api.CodePolicyDenied {
		t.Fatalf("want policy_denied, got %v", err)
	}
	if _, err := call(t, e, "messages.delete", api.DeleteParams{ID: "D1", LocalOnly: true}, api.Meta{Source: "test"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.DB.GetMessage(ctx, "", "D1"); m != nil {
		t.Error("message still stored")
	}
	audit, _ := e.DB.ListAudit(ctx, 10)
	if len(audit) != 2 || audit[0].OK != true || audit[1].OK != false {
		t.Errorf("audit rows: %+v", audit)
	}
	if _, err := call(t, e, "messages.delete", api.DeleteParams{ID: "D1", Chat: "x", LocalOnly: true}, api.Meta{}); err == nil {
		t.Error("expected an error for an unknown chat")
	}
	if _, err := call(t, e, "messages.list", map[string]any{"chatt": "typo"}, api.Meta{}); code(err) != api.CodeUsage {
		t.Errorf("unknown params must fail loudly, got %v", err)
	}
}

func TestEveryMethodHasSchemaAndHandler(t *testing.T) {
	if len(Methods()) < 60 {
		t.Errorf("only %d methods registered", len(Methods()))
	}
	for _, m := range Methods() {
		if m.H == nil || m.Summary == "" || m.Params == nil {
			t.Errorf("method %s is incomplete", m.Name)
		}
	}
}
