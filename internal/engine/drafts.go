package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func init() {
	register(&Method{Name: "drafts.list", Summary: "Actions waiting for (or past) your approval", Params: api.DraftListParams{}, H: mDraftsList})
	register(&Method{Name: "drafts.get", Summary: "One approval request and its outcome", Params: api.DraftRef{}, H: mDraftGet})
	register(&Method{Name: "drafts.approve", Summary: "Approve a pending action (humans only; agents can't call this)", Params: api.DraftApproveParams{}, Mutating: true, H: mDraftApprove})
	register(&Method{Name: "drafts.reject", Summary: "Discard a pending action", Params: api.DraftRef{}, Mutating: true, H: mDraftReject})
	register(&Method{Name: "drafts.notify", Summary: "Ask for approval in your WhatsApp \"Message yourself\" chat", Params: api.DraftRef{}, Conn: true, H: mDraftNotify})
}

// Sources an agent controls. They may create drafts but never resolve them.
func agentSource(src string) bool { return src == "mcp" || src == "remote" }

func randomCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(9000))
	if err != nil {
		return fmt.Sprint(time.Now().UnixNano()%9000 + 1000)
	}
	return fmt.Sprint(n.Int64() + 1000)
}

func (e *Engine) createDraft(c *Call, a Action, pol *config.Policy, summary, reason string) (*store.Draft, error) {
	ts := make([]string, len(a.Targets))
	for i, t := range a.Targets {
		ts[i] = t.String()
	}
	ttl := pol.ApprovalTimeout.Duration
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	code := randomCode()
	for i := 0; i < 5; i++ {
		if d, _ := e.DB.PendingDraftByCode(c.Ctx, code); d == nil {
			break
		}
		code = randomCode()
	}
	params := freezeRecipients(c.Req.Params, a.Targets)
	d := &store.Draft{
		Code: code, Created: time.Now(), Expires: time.Now().Add(ttl), Source: c.Req.Meta.Source, Profile: c.profile,
		Method: c.Method.Name, Params: string(params), Summary: summary, Preview: e.previewFor(c.Ctx, c.Method.Name, c.Req.Params, a, summary),
		Targets: strings.Join(ts, ","), Reason: reason,
	}
	if err := e.DB.CreateDraft(c.Ctx, d); err != nil {
		return nil, err
	}
	c.audited = false
	e.publish(api.Event{Type: "draft.created", Data: map[string]any{"id": d.ID, "summary": d.Summary, "preview": d.Preview, "reason": reason}})
	return d, nil
}

// freezeRecipients replaces names in "to" with the JIDs the human is shown, so renaming a chat
// while the draft waits can't redirect it.
func freezeRecipients(params json.RawMessage, targets []types.JID) json.RawMessage {
	var m map[string]any
	if len(targets) == 0 || json.Unmarshal(params, &m) != nil {
		return params
	}
	switch v := m["to"].(type) {
	case string:
		if len(targets) == 1 {
			m["to"] = targets[0].String()
		}
	case []any:
		if len(v) > 0 {
			js := make([]string, len(targets))
			for i, t := range targets {
				js[i] = t.String()
			}
			m["to"] = js
		}
	default:
		return params
	}
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return out
}

type approvedTargetsKey struct{}

// checkApprovedTargets fails a replay whose recipients differ from what the human approved.
func checkApprovedTargets(ctx context.Context, got []types.JID) error {
	want, ok := ctx.Value(approvedTargetsKey{}).(string)
	if !ok {
		return nil
	}
	have := make([]string, len(got))
	for i, t := range got {
		have[i] = t.String()
	}
	if strings.Join(have, ",") != want {
		return api.Errf(api.CodePolicyDenied, "the recipients changed since this was approved (approved: %s, now: %s); nothing was sent", want, strings.Join(have, ","))
	}
	return nil
}

func draftDetails(d *store.Draft) map[string]any {
	return map[string]any{"draft_id": d.ID, "summary": d.Summary, "preview": d.Preview, "reason": d.Reason, "expires_at": d.Expires.Format(time.RFC3339)}
}

// previewFor renders exactly what a human is approving, with full message text.
func (e *Engine) previewFor(ctx context.Context, method string, params json.RawMessage, a Action, summary string) string {
	names := make([]string, 0, len(a.Targets))
	for _, t := range a.Targets {
		n := e.displayName(ctx, t)
		if t.Server == types.DefaultUserServer && !strings.HasPrefix(n, "+") {
			n += " (+" + t.User + ")"
		}
		names = append(names, n)
	}
	to := strings.Join(names, ", ")
	if len(names) > 6 {
		to = strings.Join(names[:6], ", ") + fmt.Sprintf(" and %d more", len(names)-6)
	}
	var b strings.Builder
	switch method {
	case "send.message", "send.many", "schedule.message":
		var p struct {
			api.SendParams
			Delay string `json:"delay"`
			At    string `json:"at"`
			In    string `json:"in"`
		}
		_ = json.Unmarshal(params, &p)
		fmt.Fprintf(&b, "To: %s\n", to)
		if p.At != "" || p.In != "" {
			fmt.Fprintf(&b, "When: %s%s\n", p.At, p.In)
		}
		switch {
		case p.File != "":
			fmt.Fprintf(&b, "File: %s", p.File)
			if p.As != "" {
				fmt.Fprintf(&b, " (as %s)", p.As)
			}
			b.WriteString("\n")
		case p.Location != nil:
			fmt.Fprintf(&b, "Location: %.5f,%.5f %s\n", p.Location.Lat, p.Location.Lng, p.Location.Name)
		case p.ContactCard != nil:
			fmt.Fprintf(&b, "Contact card: %s\n", p.ContactCard.Name)
		}
		if p.ReplyTo != "" {
			if q, _ := e.DB.GetMessage(ctx, "", p.ReplyTo); q != nil {
				fmt.Fprintf(&b, "Replying to: %q\n", trunc(q.Text, 80))
			}
		}
		if p.Text != "" {
			b.WriteString("\n" + p.Text)
		}
	case "polls.create":
		var p api.PollCreateParams
		_ = json.Unmarshal(params, &p)
		fmt.Fprintf(&b, "To: %s\nPoll: %s\n- %s", to, p.Question, strings.Join(p.Options, "\n- "))
	case "messages.delete", "messages.edit", "messages.forward", "messages.react":
		var p struct {
			Chat, ID, Text, Emoji, To string
		}
		_ = json.Unmarshal(params, &p)
		fmt.Fprintf(&b, "%s\n", summary)
		if m, _ := e.DB.GetMessage(ctx, "", p.ID); m != nil {
			fmt.Fprintf(&b, "Message: %q\n", trunc(m.Text, 120))
		}
		if p.Text != "" {
			fmt.Fprintf(&b, "New text: %s\n", p.Text)
		}
	default:
		b.WriteString(summary)
		if to != "" {
			b.WriteString("\nAffects: " + to)
		}
	}
	return strings.TrimSpace(b.String())
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (e *Engine) toAPIDraft(d *store.Draft, withCode bool) api.Draft {
	out := api.Draft{ID: d.ID, Status: d.Status, Method: d.Method, Summary: d.Summary, Preview: d.Preview, Reason: d.Reason,
		Source: d.Source, Profile: d.Profile, Created: d.Created.Format(time.RFC3339), Expires: d.Expires.Format(time.RFC3339),
		ResolvedBy: d.ResolvedBy, Error: d.Error}
	if d.Status == "pending" && time.Now().After(d.Expires) {
		out.Status = "expired"
	}
	if d.Targets != "" {
		out.Targets = strings.Split(d.Targets, ",")
	}
	if !d.ResolvedAt.IsZero() {
		out.ResolvedAt = d.ResolvedAt.Format(time.RFC3339)
	}
	if d.Result != "" {
		out.Result = json.RawMessage(d.Result)
	}
	if withCode && out.Status == "pending" {
		out.Code = d.Code
	}
	return out
}

func mDraftsList(c *Call) (any, error) {
	var p api.DraftListParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.Status == "" {
		p.Status = "pending"
	}
	_, _ = c.E.DB.ExpireDrafts(c.Ctx)
	rows, err := c.E.DB.ListDrafts(c.Ctx, p.Status, p.Limit)
	if err != nil {
		return nil, err
	}
	out := []api.Draft{}
	for _, d := range rows {
		out = append(out, c.E.toAPIDraft(d, c.Req.Meta.Source == "cli"))
	}
	return out, nil
}

func mDraftGet(c *Call) (any, error) {
	var p api.DraftRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	d, err := c.E.DB.GetDraft(c.Ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, notFound("no draft #%d", p.ID)
	}
	return c.E.toAPIDraft(d, c.Req.Meta.Source == "cli"), nil
}

func mDraftApprove(c *Call) (any, error) {
	var p api.DraftApproveParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if agentSource(c.Req.Meta.Source) {
		return nil, api.Errf(api.CodePolicyDenied, "approvals have to come from the user, not the agent").
			WithHint("the user approves in the chat prompt, on WhatsApp, or with: wassup drafts approve")
	}
	c.audited = true // the replayed action writes its own audit row
	res, err := c.E.approveDraft(c.Ctx, p.ID, c.Req.Meta.Source, p.Text)
	_ = c.E.DB.AddAudit(context.Background(), store.Audit{Time: time.Now(), Source: c.Req.Meta.Source, Method: "drafts.approve", Targets: fmt.Sprint(p.ID), OK: err == nil, Error: errString(err)})
	return res, err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// approveDraft replays the parked request with Yes=true. Allow/deny lists and rate limits still apply.
func (e *Engine) approveDraft(ctx context.Context, id int64, by string, text *string) (any, error) {
	d, err := e.DB.GetDraft(ctx, id)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, notFound("no draft #%d", id)
	}
	if d.Status != "pending" {
		return nil, api.Errf(api.CodeUsage, "draft #%d is already %s", id, d.Status)
	}
	if time.Now().After(d.Expires) {
		_, _ = e.DB.ExpireDrafts(ctx)
		return nil, api.Errf(api.CodeTimeout, "draft #%d expired at %s", id, d.Expires.Format("15:04"))
	}
	params := json.RawMessage(d.Params)
	if text != nil && *text != "" {
		var m map[string]any
		if json.Unmarshal(params, &m) == nil {
			if _, ok := m["text"]; ok || d.Method == "send.message" || d.Method == "schedule.message" || d.Method == "send.many" {
				m["text"] = *text
				params, _ = json.Marshal(m)
			}
		}
	}
	ok, err := e.DB.ClaimDraft(ctx, id, by)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, api.Errf(api.CodeUsage, "draft #%d was already handled", id)
	}
	rctx := context.WithValue(ctx, approvedTargetsKey{}, d.Targets)
	res, derr := e.Dispatch(rctx, &api.Request{Method: d.Method, Params: params, Meta: api.Meta{Source: "approved", Profile: d.Profile, Yes: true}}, nil)
	status, result := "sent", ""
	if derr != nil {
		status = "failed"
	} else if b, err := json.Marshal(res); err == nil {
		result = string(b)
	}
	_ = e.DB.FinishDraft(ctx, id, status, result, errString(derr), by)
	e.publish(api.Event{Type: "draft.resolved", Data: map[string]any{"id": id, "status": status, "by": by, "error": errString(derr)}})
	return res, derr
}

func mDraftReject(c *Call) (any, error) {
	var p api.DraftRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if c.Req.Meta.Source == "mcp" || c.Req.Meta.Source == "remote" {
		// An agent withdrawing its own request is fine; it can't approve, but it can cancel.
		c.Req.Meta.Source += "-withdraw"
	}
	ok, err := c.E.DB.RejectDraft(c.Ctx, p.ID, c.Req.Meta.Source)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, api.Errf(api.CodeUsage, "draft #%d isn't pending", p.ID)
	}
	c.E.publish(api.Event{Type: "draft.resolved", Data: map[string]any{"id": p.ID, "status": "rejected", "by": c.Req.Meta.Source}})
	return map[string]any{"id": p.ID, "status": "rejected"}, nil
}

type outgoingKey struct{}

// sendSystem posts a wassup notice into your own chat. It's tagged so the approval listener ignores it.
func (e *Engine) sendSystem(ctx context.Context, text string) error {
	own := e.OwnJID()
	if own.IsEmpty() {
		return api.Errf(api.CodeNotLoggedIn, "not linked")
	}
	msg := &waE2E.Message{Conversation: proto.String(text)}
	resp, err := e.Client().SendMessage(ctx, own, msg)
	if err != nil {
		return err
	}
	e.recordOutgoing(context.WithValue(ctx, outgoingKey{}, true), own, resp.ID, resp.Timestamp, msg, resp.Sender)
	return nil
}

func mDraftNotify(c *Call) (any, error) {
	var p api.DraftRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	d, err := c.E.DB.GetDraft(c.Ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if d == nil || d.Status != "pending" {
		return nil, notFound("no pending draft #%d", p.ID)
	}
	if d.Notified {
		return map[string]any{"id": d.ID, "notified": true, "already": true}, nil
	}
	who := map[string]string{"mcp": "your AI assistant (MCP)", "remote": "your AI assistant (remote MCP)", "rpc": "a script"}[d.Source]
	if who == "" {
		who = d.Source
	}
	text := fmt.Sprintf("*wassup* approval #%d\n%s wants to:\n\n%s\n\nReply *yes %s* to go ahead or *no %s* to cancel. Expires %s.",
		d.ID, who, d.Preview, d.Code, d.Code, d.Expires.Local().Format("15:04"))
	if err := c.E.sendSystem(c.Ctx, text); err != nil {
		return nil, err
	}
	_ = c.E.DB.MarkDraftNotified(c.Ctx, d.ID)
	return map[string]any{"id": d.ID, "notified": true}, nil
}

var approvalReply = regexp.MustCompile(`(?i)^\s*(yes|y|ok|okay|send|approve|go|no|n|cancel|reject|stop)\s*#?\s*(\d{4})\s*[.!]*\s*$`)

func (e *Engine) isSelfChat(chat types.JID) bool {
	own := e.OwnJID()
	if own.IsEmpty() {
		return false
	}
	lid := e.Client().Store.LID
	return chat.User == own.User || (!lid.IsEmpty() && chat.User == lid.User)
}

// handleApprovalReply reacts to "yes 1234" / "no 1234" typed on your phone in "Message yourself".
func (e *Engine) handleApprovalReply(text string) {
	m := approvalReply.FindStringSubmatch(text)
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Minute)
	defer cancel()
	d, _ := e.DB.PendingDraftByCode(ctx, m[2])
	if d == nil {
		_ = e.sendSystem(ctx, fmt.Sprintf("No pending wassup request has code %s (it may have expired or already been handled).", m[2]))
		return
	}
	switch strings.ToLower(m[1]) {
	case "no", "n", "cancel", "reject", "stop":
		if ok, _ := e.DB.RejectDraft(ctx, d.ID, "whatsapp"); ok {
			e.publish(api.Event{Type: "draft.resolved", Data: map[string]any{"id": d.ID, "status": "rejected", "by": "whatsapp"}})
			_ = e.sendSystem(ctx, fmt.Sprintf("Cancelled #%d. Nothing was sent.", d.ID))
		}
	default:
		if _, err := e.approveDraft(ctx, d.ID, "whatsapp", nil); err != nil {
			_ = e.sendSystem(ctx, fmt.Sprintf("Approved #%d but it failed: %s", d.ID, api.AsError(err).Message))
		} else {
			_ = e.sendSystem(ctx, fmt.Sprintf("Done: #%d went through.", d.ID))
		}
	}
}

// draftSweeper expires stale approvals once a minute.
func (e *Engine) draftSweeper() {
	defer e.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
			if gone, _ := e.DB.ExpireDrafts(e.ctx); len(gone) > 0 {
				for _, d := range gone {
					e.publish(api.Event{Type: "draft.resolved", Data: map[string]any{"id": d.ID, "status": "expired"}})
				}
			}
		}
	}
}

// SendToSelf posts a notice in the user's own chat (used for sign-in codes).
func (e *Engine) SendToSelf(ctx context.Context, text string) error {
	if err := e.WaitConnected(ctx); err != nil {
		return err
	}
	return e.sendSystem(ctx, text)
}
