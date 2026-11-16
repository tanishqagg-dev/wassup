package engine

import (
	"context"
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
)

// Action describes what a mutating call is about to do.
type Action struct {
	Targets     []types.JID
	Sends       int  // outgoing messages reserved now, for rate limits
	Messages    bool // sends messages even if Sends is 0 (batches account per item)
	Destructive bool // deletes, leaves, removals, logout
	Summary     string
}

// Authorize applies the caller's policy profile. It returns dry=true when the caller should only describe the action.
// For sends it also serializes with other sends until the call finishes, so rate limits stay exact.
func (c *Call) Authorize(a Action) (dry bool, err error) {
	cfg := c.E.Config()
	pol, name, perr := cfg.PolicyStrict(c.Req.Meta.Profile)
	c.profile, c.targets, c.sends = name, a.Targets, a.Sends
	if perr != nil {
		return false, api.Errf(api.CodePolicyDenied, "%v", perr).WithHint("fix --profile / WASSUP_PROFILE or [mcp] profile in: wassup config edit")
	}
	sends := a.Sends > 0 || a.Messages
	ctx := c.Ctx
	method := c.Method.Name

	mode := strings.ToLower(pol.Mode)
	if mode == "read_only" || mode == "readonly" {
		return false, api.Errf(api.CodePolicyDenied, "profile %q is read-only", name).WithHint("change mode in: wassup config edit")
	}
	for _, pat := range pol.DenyActions {
		if ok, _ := path.Match(pat, method); ok || pat == method {
			return false, api.Errf(api.CodePolicyDenied, "profile %q doesn't allow %s", name, method).WithHint("remove it from deny_actions in: wassup config edit")
		}
	}
	for _, t := range a.Targets {
		if c.E.matchList(ctx, pol.Deny, t) {
			return false, api.Errf(api.CodePolicyDenied, "%q is on the deny list of profile %q", c.E.displayName(ctx, t), name)
		}
	}
	if mode == "allowlist" {
		for _, t := range a.Targets {
			if !c.E.matchList(ctx, pol.Allow, t) {
				return false, api.Errf(api.CodePolicyDenied, "%q isn't on the allowlist of profile %q", c.E.displayName(ctx, t), name).
					WithHint(fmt.Sprintf("allow it with: wassup allow add %s --profile %s", t.String(), name)).
					WithDetails(map[string]string{"target": t.String(), "profile": name})
			}
		}
	}
	if c.Req.Meta.DryRun || pol.DryRun {
		c.dry = true
		return true, nil
	}
	if c.Req.Meta.Source == "approved" {
		if err := checkApprovedTargets(ctx, a.Targets); err != nil {
			return false, err
		}
	}
	if !c.Req.Meta.Yes {
		if reason := c.E.needsHuman(ctx, pol, a, sends); reason != "" {
			summary := a.Summary
			if summary == "" {
				summary = method
			}
			if c.Req.Meta.Source == "cli" {
				return false, api.Errf(api.CodeConfirmationRequired, "needs confirmation: %s", reason).
					WithHint("re-run with --yes to go ahead").
					WithDetails(map[string]any{"action": method, "summary": summary, "profile": name})
			}
			// Agents and scripts: park the exact request until a human approves it.
			d, err := c.E.createDraft(c, a, pol, summary, reason)
			if err != nil {
				return false, err
			}
			return false, api.Errf(api.CodeApprovalRequired, "waiting for the user's approval: %s", reason).
				WithHint("nothing was sent; the user approves in chat, by replying on WhatsApp, or with: wassup drafts approve " + fmt.Sprint(d.ID)).
				WithDetails(draftDetails(d))
		}
	}
	if a.Sends > 0 {
		if err := c.rateGate(pol, name, a); err != nil {
			return false, err
		}
	}
	return false, nil
}

// needsHuman returns why a person must OK this action first, or "" if no rule asks for it.
func (e *Engine) needsHuman(ctx context.Context, pol *config.Policy, a Action, sends bool) string {
	newChat := func() string {
		for _, t := range a.Targets {
			if t.Server == types.DefaultUserServer && e.DB.CountMessages(ctx, t.String()) == 0 {
				return fmt.Sprintf("you have no history with %q", e.displayName(ctx, t))
			}
		}
		return ""
	}
	switch {
	case pol.ConfirmBulk > 0 && len(a.Targets) > pol.ConfirmBulk:
		return fmt.Sprintf("this targets %d chats (limit without confirmation: %d)", len(a.Targets), pol.ConfirmBulk)
	case pol.ConfirmDestructive && a.Destructive:
		return "this action can't be undone"
	case pol.ConfirmGroups && sends && anyGroup(a.Targets):
		return "this sends into a group"
	case pol.ConfirmNewChats && sends && newChat() != "":
		return newChat()
	}
	if !sends {
		return ""
	}
	switch strings.ToLower(pol.Approval) {
	case "always", "all":
		return "your policy asks you to approve messages before they go out"
	case "new_chats_and_groups", "new":
		if anyGroup(a.Targets) {
			return "this sends into a group"
		}
		return newChat()
	}
	return ""
}

func anyGroup(ts []types.JID) bool {
	for _, t := range ts {
		if t.Server == types.GroupServer {
			return true
		}
	}
	return false
}

// rateGate enforces per-minute/hour/chat caps and min_interval for a single call.
// The send lock stays held until the call finishes so the audit row lands before the next check.
func (c *Call) rateGate(pol *config.Policy, profile string, a Action) error {
	unlock, err := c.E.acquireSend(c.Ctx, pol, profile, a.Targets, a.Sends, 0)
	if err != nil {
		return err
	}
	c.unlock = unlock
	return nil
}

// acquireSend takes the send lock once the rate limits allow n more messages, then waits out
// min_interval. With maxWait > 0 it waits (lock released) for quota instead of failing.
// The caller must record the send in the audit log before calling the returned unlock.
func (e *Engine) acquireSend(ctx context.Context, pol *config.Policy, profile string, targets []types.JID, n int, maxWait time.Duration) (func(), error) {
	deadline := time.Now().Add(maxWait)
	for {
		e.sendMu.Lock()
		err := e.checkRate(ctx, pol, profile, targets, n)
		if err == nil {
			if iv := pol.MinInterval.Duration; iv > 0 {
				if wait := time.Until(e.DB.LastSend(ctx, profile).Add(iv)); wait > 0 {
					select {
					case <-time.After(wait):
					case <-ctx.Done():
						e.sendMu.Unlock()
						return nil, api.Errf(api.CodeTimeout, "cancelled while spacing sends")
					}
				}
			}
			return e.sendMu.Unlock, nil
		}
		e.sendMu.Unlock()
		retry := time.Second
		if d, ok := api.AsError(err).Details.(map[string]any); ok {
			if s, ok := d["retry_after_seconds"].(int); ok && s > 0 {
				retry = time.Duration(s) * time.Second
			}
		}
		if maxWait <= 0 || time.Now().Add(retry).After(deadline) {
			return nil, err
		}
		select {
		case <-time.After(retry):
		case <-ctx.Done():
			return nil, api.Errf(api.CodeTimeout, "cancelled while waiting for the rate limit")
		}
	}
}

// warmupFactor scales rate limits for a freshly linked account: 25% in week one, 50% in week two.
func (e *Engine) warmupFactor(pol *config.Policy) (float64, int) {
	if !pol.Warmup {
		return 1, 0
	}
	linked, err := time.Parse(time.RFC3339, e.DB.Get(context.Background(), "linked_at"))
	if err != nil {
		return 1, 0
	}
	days := int(time.Since(linked).Hours() / 24)
	switch {
	case days < 7:
		return 0.25, days
	case days < 14:
		return 0.5, days
	}
	return 1, days
}

func scaled(limit int, f float64) int {
	if limit <= 0 || f >= 1 {
		return limit
	}
	return max(1, int(float64(limit)*f))
}

func (e *Engine) checkRate(ctx context.Context, pol *config.Policy, profile string, targets []types.JID, sends int) error {
	now := time.Now()
	factor, days := e.warmupFactor(pol)
	note := ""
	if factor < 1 {
		note = fmt.Sprintf(" (warmup: this account was linked %d days ago, so limits are at %d%% for its first two weeks)", days, int(factor*100))
	}
	limited := func(window time.Duration, limit int, label string) error {
		if limit <= 0 {
			return nil
		}
		used := e.DB.CountSends(ctx, profile, now.Add(-window), "")
		if used+sends <= limit {
			return nil
		}
		oldest := e.DB.OldestSendSince(ctx, profile, now.Add(-window))
		retry := window
		if !oldest.IsZero() {
			retry = time.Until(oldest.Add(window))
		}
		secs := int(math.Ceil(retry.Seconds()))
		return api.Errf(api.CodeRateLimited, "rate limit: %d messages per %s for profile %q%s", limit, label, profile, note).
			WithHint(fmt.Sprintf("retry in %ds, or raise the limit in: wassup config edit", secs)).
			WithDetails(map[string]any{"retry_after_seconds": secs, "limit": limit, "window": label})
	}
	if err := limited(time.Minute, scaled(pol.RatePerMinute, factor), "minute"); err != nil {
		return err
	}
	if err := limited(time.Hour, scaled(pol.RatePerHour, factor), "hour"); err != nil {
		return err
	}
	if perChat := scaled(pol.RatePerChatPerMinute, factor); perChat > 0 && len(targets) == 1 {
		used := e.DB.CountSends(ctx, profile, now.Add(-time.Minute), targets[0].String())
		if used+sends > perChat {
			return api.Errf(api.CodeRateLimited, "rate limit: %d messages per minute to one chat%s", perChat, note).
				WithHint("wait a minute, or raise rate_per_chat_per_minute").
				WithDetails(map[string]any{"retry_after_seconds": 60})
		}
	}
	return nil
}

// matchList reports whether jid matches any entry: "*", a glob over the JID string, a JID, a phone
// number, or an exact contact/group name. Resolution here never touches the network.
func (e *Engine) matchList(ctx context.Context, entries []string, jid types.JID) bool {
	js := jid.String()
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		if strings.ContainsAny(entry, "*?[") {
			if ok, _ := path.Match(entry, js); ok {
				return true
			}
			continue
		}
		if r, err := e.resolveOffline(ctx, entry); err == nil && r == jid {
			return true
		}
	}
	return false
}
