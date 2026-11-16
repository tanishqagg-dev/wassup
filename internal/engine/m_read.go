package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func init() {
	register(&Method{Name: "messages.list", Summary: "List stored messages, newest last", Params: api.ListMessagesParams{}, H: mListMessages})
	register(&Method{Name: "messages.get", Summary: "Show one message with reactions", Params: api.MessageRef{}, H: mGetMessage})
	register(&Method{Name: "messages.search", Summary: "Full-text search across stored messages", Params: api.SearchParams{}, H: mSearch})
	register(&Method{Name: "messages.download", Summary: "Download the media of a message (cached after the first time)", Params: api.DownloadParams{}, H: mDownload})
	register(&Method{Name: "messages.wait", Summary: "Block until a matching message arrives", Params: api.WaitParams{}, Stream: true, H: mWait})
	register(&Method{Name: "history.request", Summary: "Ask the phone for older messages in a chat", Params: api.HistoryParams{}, Conn: true, H: mHistory})
	register(&Method{Name: "chats.list", Summary: "List chats, most recent first", Params: api.ListChatsParams{}, H: mListChats})
	register(&Method{Name: "chats.info", Summary: "Show one chat", Params: api.ChatRef{}, H: mChatInfo})
	register(&Method{Name: "chats.update", Summary: "Archive, pin, mute, mark read/unread or set disappearing messages", Params: api.UpdateChatParams{}, Mutating: true, Conn: true, H: mUpdateChat})
	register(&Method{Name: "chats.delete", Summary: "Delete a chat on all your devices", Params: api.ChatRef{}, Mutating: true, Conn: true, H: mDeleteChat})
	register(&Method{Name: "presence.typing", Summary: "Show typing or recording in a chat", Params: api.TypingParams{}, Mutating: true, Conn: true, H: mTyping})
	register(&Method{Name: "presence.set", Summary: "Appear online or offline (online stops phone notifications)", Params: api.PresenceParams{}, Mutating: true, Conn: true, H: mPresence})
	register(&Method{Name: "presence.subscribe", Summary: "Get online/last-seen events for a person", Params: api.UserRef{}, Conn: true, H: mSubscribePresence})
	register(&Method{Name: "events.subscribe", Summary: "Stream live events until cancelled", Params: api.EventsParams{}, Stream: true, H: mEvents})
}

func (e *Engine) withReactions(ctx context.Context, rows []*store.Message) []*api.Message {
	byChat := map[string][]string{}
	for _, r := range rows {
		byChat[r.Chat] = append(byChat[r.Chat], r.ID)
	}
	reacts := map[string]map[string][]store.Reaction{}
	for chat, ids := range byChat {
		if m, err := e.DB.Reactions(ctx, chat, ids); err == nil {
			reacts[chat] = m
		}
	}
	out := make([]*api.Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, e.toAPIMessage(ctx, r, reacts[r.Chat][r.ID]))
	}
	return out
}

// boundArg resolves a before/after argument that may be a time or a message ID.
func (e *Engine) boundArg(ctx context.Context, chat, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := parseTimeArg(v); err == nil {
		return t, nil
	}
	if m, _ := e.DB.GetMessage(ctx, chat, v); m != nil {
		return m.Time, nil
	}
	return time.Time{}, usage("can't read %q as a time or message ID", v)
}

func mListMessages(c *Call) (any, error) {
	var p api.ListMessagesParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	q := store.MessageQuery{Limit: p.Limit, Type: p.Type, FromMe: p.FromMe, Desc: p.Order == "desc", NoStatus: true}
	if q.Limit <= 0 {
		q.Limit = 30
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	if p.Chat != "" {
		j, _, err := c.E.resolveLocal(c.Ctx, p.Chat)
		if err != nil {
			return nil, err
		}
		q.Chat = j.String()
		if p.Unread {
			if ch, _ := c.E.DB.GetChat(c.Ctx, q.Chat); ch != nil && !ch.LastReadAt.IsZero() {
				q.After = ch.LastReadAt.Add(-time.Second)
			}
			f := false
			q.FromMe = &f
		}
	}
	if p.From != "" {
		j, _, err := c.E.resolveLocal(c.Ctx, p.From)
		if err != nil {
			return nil, err
		}
		q.Sender = j.String()
	}
	var err error
	if b, err := c.E.boundArg(c.Ctx, q.Chat, p.Before); err != nil {
		return nil, err
	} else {
		q.Before = b
	}
	if q.After.IsZero() {
		if q.After, err = c.E.boundArg(c.Ctx, q.Chat, p.After); err != nil {
			return nil, err
		}
	}
	rows, err := c.E.DB.ListMessages(c.Ctx, q)
	if err != nil {
		return nil, err
	}
	return c.E.withReactions(c.Ctx, rows), nil
}

func mGetMessage(c *Call) (any, error) {
	var p api.MessageRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	return c.E.withReactions(c.Ctx, []*store.Message{sm})[0], nil
}

func mSearch(c *Call) (any, error) {
	var p api.SearchParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Query) == "" {
		return nil, usage("search query is empty")
	}
	q := store.MessageQuery{Limit: p.Limit, Type: p.Type, Desc: true, NoStatus: true, FTS: store.FTSQuery(p.Query)}
	if p.Chat != "" {
		j, _, err := c.E.resolveLocal(c.Ctx, p.Chat)
		if err != nil {
			return nil, err
		}
		q.Chat = j.String()
	}
	if p.From != "" {
		j, _, err := c.E.resolveLocal(c.Ctx, p.From)
		if err != nil {
			return nil, err
		}
		q.Sender = j.String()
	}
	var err error
	if q.After, err = parseTimeArg(p.After); err != nil {
		return nil, usage("%v", err)
	}
	if q.Before, err = parseTimeArg(p.Before); err != nil {
		return nil, usage("%v", err)
	}
	rows, err := c.E.DB.ListMessages(c.Ctx, q)
	if err != nil {
		// FTS syntax the tokenizer can't handle: fall back to substring search.
		q.FTS, q.Like = "", p.Query
		if rows, err = c.E.DB.ListMessages(c.Ctx, q); err != nil {
			return nil, err
		}
	}
	return c.E.withReactions(c.Ctx, rows), nil
}

func mDownload(c *Call) (any, error) {
	var p api.DownloadParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	if p.Out != "" && c.agentSourced() {
		return nil, api.Errf(api.CodePolicyDenied, "agents can't choose where files are written").WithHint("leave out empty; files go to wassup's media folder")
	}
	if p.Out != "" || sm.MediaPath == "" || !fileExists(sm.MediaPath) {
		if err := c.E.WaitConnected(c.Ctx); err != nil {
			return nil, err
		}
	}
	return c.E.downloadMessage(c.Ctx, sm, p.Out)
}

func mWait(c *Call) (any, error) {
	var p api.WaitParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	timeout := 5 * time.Minute
	if p.Timeout != "" {
		d, err := config.ParseDuration(p.Timeout)
		if err != nil {
			return nil, usage("bad timeout: %v", err)
		}
		timeout = d
	}
	var chat, from string
	if p.Chat != "" {
		j, _, err := c.E.resolveLocal(c.Ctx, p.Chat)
		if err != nil {
			return nil, err
		}
		chat = j.String()
	}
	if p.From != "" {
		j, _, err := c.E.resolveLocal(c.Ctx, p.From)
		if err != nil {
			return nil, err
		}
		from = j.String()
	}
	var re *regexp.Regexp
	if p.Match != "" {
		var err error
		if re, err = regexp.Compile("(?i)" + p.Match); err != nil {
			return nil, usage("bad regular expression: %v", err)
		}
	}
	count := p.Count
	if count <= 0 {
		count = 1
	}
	id, ch := c.E.Bus.Subscribe(256)
	defer c.E.Bus.Unsubscribe(id)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var got []*api.Message
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil, api.Errf(api.CodeNotConnected, "event stream closed")
			}
			if ev.Type != "message" {
				continue
			}
			m, ok := ev.Data.(*api.Message)
			if !ok || (m.FromMe && !p.IncludeFromMe) || (chat != "" && m.Chat != chat) || (from != "" && m.Sender != from) {
				continue
			}
			if m.Chat == types.StatusBroadcastJID.String() && chat == "" {
				continue
			}
			if re != nil && !re.MatchString(m.Text) {
				continue
			}
			got = append(got, m)
			if count > 1 {
				_ = c.Emit(m)
			}
			if len(got) >= count {
				if count == 1 {
					return got[0], nil
				}
				return got, nil
			}
		case <-deadline.C:
			return nil, api.Errf(api.CodeTimeout, "no matching message within %s", timeout).WithDetails(map[string]any{"received": got})
		case <-c.Ctx.Done():
			return nil, api.Errf(api.CodeTimeout, "cancelled")
		}
	}
}

func mHistory(c *Call) (any, error) {
	var p api.HistoryParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	chat, _, err := c.E.resolveLocal(c.Ctx, p.Chat)
	if err != nil {
		return nil, err
	}
	count := p.Count
	if count <= 0 {
		count = 50
	}
	if count > 500 {
		count = 500
	}
	timeout := 30 * time.Second
	if p.Timeout != "" {
		if timeout, err = config.ParseDuration(p.Timeout); err != nil {
			return nil, usage("bad timeout: %v", err)
		}
	}
	oldest, err := c.E.DB.OldestMessage(c.Ctx, chat.String())
	if err != nil {
		return nil, err
	}
	if oldest == nil {
		return nil, notFound("no stored messages in %s to page back from", c.E.displayName(c.Ctx, chat)).
			WithHint("WhatsApp needs at least one known message as an anchor; send or receive one first")
	}
	before := c.E.DB.CountMessages(c.Ctx, chat.String())
	wait := make(chan int, 4)
	c.E.waitMu.Lock()
	c.E.historyWaits[chat.String()] = append(c.E.historyWaits[chat.String()], wait)
	c.E.waitMu.Unlock()
	req := c.E.Client().BuildHistorySyncRequest(c.E.infoFor(oldest), count)
	if _, err := c.E.Client().SendPeerMessage(c.Ctx, req); err != nil {
		return nil, err
	}
	select {
	case <-wait:
	case <-time.After(timeout):
		return map[string]any{"chat": chat.String(), "requested": count, "received": c.E.DB.CountMessages(c.Ctx, chat.String()) - before, "note": "the phone hasn't answered yet; messages will still be stored when it does"}, nil
	case <-c.Ctx.Done():
	}
	return map[string]any{"chat": chat.String(), "requested": count, "received": c.E.DB.CountMessages(c.Ctx, chat.String()) - before}, nil
}

func (e *Engine) toAPIChat(ctx context.Context, ch *store.Chat) api.Chat {
	j, _ := types.ParseJID(ch.JID)
	out := api.Chat{JID: ch.JID, Name: e.displayName(ctx, j), Kind: ch.Kind, LastMessage: ch.LastPreview, Unread: ch.Unread, Archived: ch.Archived, Pinned: ch.Pinned}
	if out.Kind == "" {
		out.Kind = chatKind(j)
	}
	if ch.MarkedUnread && out.Unread == 0 {
		out.Unread = 1
	}
	if !ch.LastMessageAt.IsZero() {
		t := ch.LastMessageAt
		out.LastMessageAt = &t
	}
	if ch.MutedUntil != 0 && (ch.MutedUntil < 0 || ch.MutedUntil > time.Now().Unix()) {
		t := time.Unix(ch.MutedUntil, 0)
		if ch.MutedUntil < 0 {
			t = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
		}
		out.MutedUntil = &t
	}
	if ch.Ephemeral > 0 {
		out.Disappearing = config.FormatDuration(time.Duration(ch.Ephemeral) * time.Second)
	}
	return out
}

func mListChats(c *Call) (any, error) {
	var p api.ListChatsParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	fetch := limit
	if p.Query != "" {
		fetch = 100000
	}
	rows, err := c.E.DB.ListChats(c.Ctx, store.ChatQuery{Kind: p.Kind, Unread: p.Unread, Archived: p.Archived, Limit: fetch})
	if err != nil {
		return nil, err
	}
	out := []api.Chat{}
	for _, r := range rows {
		ch := c.E.toAPIChat(c.Ctx, r)
		if p.Query != "" && nameScore(p.Query, ch.Name) == 0 && !strings.Contains(ch.JID, p.Query) {
			continue
		}
		out = append(out, ch)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func mChatInfo(c *Call) (any, error) {
	var p api.ChatRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, r, err := c.E.resolveLocal(c.Ctx, p.Chat)
	if err != nil {
		return nil, err
	}
	ch, err := c.E.DB.GetChat(c.Ctx, j.String())
	if err != nil {
		return nil, err
	}
	if ch == nil {
		ch = &store.Chat{JID: j.String(), Kind: r.Kind}
	}
	out := c.E.toAPIChat(c.Ctx, ch)
	return map[string]any{"chat": out, "messages_stored": c.E.DB.CountMessages(c.Ctx, j.String())}, nil
}

func (e *Engine) lastKey(ctx context.Context, chat types.JID) (time.Time, *waCommon.MessageKey) {
	rows, _ := e.DB.ListMessages(ctx, store.MessageQuery{Chat: chat.String(), Limit: 1, Desc: true})
	if len(rows) == 0 {
		return time.Now(), nil
	}
	lc, ls := e.keyFor(rows[0])
	return rows[0].Time, e.Client().BuildMessageKey(lc, ls, rows[0].ID)
}

var disappearingValues = map[string]time.Duration{"off": 0, "0": 0, "24h": 24 * time.Hour, "1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "90d": 90 * 24 * time.Hour}

func parseDisappearing(s string) (time.Duration, error) {
	d, ok := disappearingValues[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return 0, usage("disappearing must be off, 24h, 7d or 90d")
	}
	return d, nil
}

func mUpdateChat(c *Call) (any, error) {
	var p api.UpdateChatParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	chat, _, err := c.E.resolve(c.Ctx, p.Chat)
	if err != nil {
		return nil, err
	}
	var changes []string
	if p.Archive != nil {
		changes = append(changes, map[bool]string{true: "archive", false: "unarchive"}[*p.Archive])
	}
	if p.Pin != nil {
		changes = append(changes, map[bool]string{true: "pin", false: "unpin"}[*p.Pin])
	}
	if p.Mute != "" {
		changes = append(changes, "mute "+p.Mute)
	}
	if p.Read != nil {
		changes = append(changes, map[bool]string{true: "mark read", false: "mark unread"}[*p.Read])
	}
	if p.Disappearing != "" {
		changes = append(changes, "disappearing "+p.Disappearing)
	}
	if len(changes) == 0 {
		return nil, usage("nothing to change: set archive, pin, mute, read or disappearing")
	}
	summary := strings.Join(changes, ", ") + " " + c.E.displayName(c.Ctx, chat)
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, summary)
	}
	cl := c.E.Client()
	kind := chatKind(chat)
	if p.Archive != nil {
		ts, key := c.E.lastKey(c.Ctx, chat)
		if err := cl.SendAppState(c.Ctx, appstate.BuildArchive(chat, *p.Archive, ts, key)); err != nil {
			return nil, err
		}
		_ = c.E.DB.SetChatFlags(c.Ctx, chat.String(), kind, store.ChatFlags{Archived: p.Archive})
	}
	if p.Pin != nil {
		if err := cl.SendAppState(c.Ctx, appstate.BuildPin(chat, *p.Pin)); err != nil {
			return nil, err
		}
		_ = c.E.DB.SetChatFlags(c.Ctx, chat.String(), kind, store.ChatFlags{Pinned: p.Pin})
	}
	if p.Mute != "" {
		var until int64
		switch strings.ToLower(p.Mute) {
		case "off", "no", "false", "0", "unmute":
			if err := cl.SendAppState(c.Ctx, appstate.BuildMute(chat, false, 0)); err != nil {
				return nil, err
			}
		case "forever", "always", "on", "true":
			if err := cl.SendAppState(c.Ctx, appstate.BuildMuteAbs(chat, true, new(int64(-1)))); err != nil {
				return nil, err
			}
			until = -1
		default:
			d, err := config.ParseDuration(p.Mute)
			if err != nil || d <= 0 {
				return nil, usage("mute takes a duration like 8h or 7d, 'forever' or 'off'")
			}
			if err := cl.SendAppState(c.Ctx, appstate.BuildMute(chat, true, d)); err != nil {
				return nil, err
			}
			until = time.Now().Add(d).Unix()
		}
		_ = c.E.DB.SetChatFlags(c.Ctx, chat.String(), kind, store.ChatFlags{MutedUntil: &until})
	}
	if p.Read != nil {
		ts, key := c.E.lastKey(c.Ctx, chat)
		if err := cl.SendAppState(c.Ctx, appstate.BuildMarkChatAsRead(chat, *p.Read, ts, key)); err != nil {
			return nil, err
		}
		f := store.ChatFlags{}
		if *p.Read {
			zero, no, now := 0, false, time.Now()
			f.Unread, f.MarkedUnread, f.LastReadAt = &zero, &no, &now
		} else {
			yes := true
			f.MarkedUnread = &yes
		}
		_ = c.E.DB.SetChatFlags(c.Ctx, chat.String(), kind, f)
	}
	if p.Disappearing != "" {
		d, err := parseDisappearing(p.Disappearing)
		if err != nil {
			return nil, err
		}
		if err := cl.SetDisappearingTimer(c.Ctx, chat, d, time.Now()); err != nil {
			return nil, err
		}
		n := int(d.Seconds())
		_ = c.E.DB.SetChatFlags(c.Ctx, chat.String(), kind, store.ChatFlags{Ephemeral: &n})
	}
	ch, _ := c.E.DB.GetChat(c.Ctx, chat.String())
	if ch == nil {
		ch = &store.Chat{JID: chat.String(), Kind: kind}
	}
	return map[string]any{"changed": changes, "chat": c.E.toAPIChat(c.Ctx, ch)}, nil
}

func mDeleteChat(c *Call) (any, error) {
	var p api.ChatRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	chat, _, err := c.E.resolve(c.Ctx, p.Chat)
	if err != nil {
		return nil, err
	}
	summary := "delete chat " + c.E.displayName(c.Ctx, chat)
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Destructive: true, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, summary)
	}
	ts, key := c.E.lastKey(c.Ctx, chat)
	if err := c.E.Client().SendAppState(c.Ctx, appstate.BuildDeleteChat(chat, ts, key, true)); err != nil {
		return nil, err
	}
	_ = c.E.DB.DeleteChat(c.Ctx, chat.String())
	return map[string]any{"deleted": chat.String()}, nil
}

func mTyping(c *Call) (any, error) {
	var p api.TypingParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	chat, _, err := c.E.resolve(c.Ctx, p.Chat)
	if err != nil {
		return nil, err
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Summary: "typing indicator"})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, "show "+p.State+" in "+c.E.displayName(c.Ctx, chat))
	}
	state, media := types.ChatPresenceComposing, types.ChatPresenceMediaText
	switch strings.ToLower(p.State) {
	case "", "typing", "composing":
	case "recording", "audio":
		media = types.ChatPresenceMediaAudio
	case "paused", "stop", "off":
		state = types.ChatPresencePaused
	default:
		return nil, usage("state must be typing, recording or paused")
	}
	return map[string]any{"chat": chat.String(), "state": p.State}, c.E.Client().SendChatPresence(c.Ctx, chat, state, media)
}

func mPresence(c *Call) (any, error) {
	var p api.PresenceParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	dry, err := c.Authorize(Action{Summary: "set presence"})
	if err != nil {
		return nil, err
	}
	if dry {
		return api.DryRun{DryRun: true, Action: c.Method.Name, Summary: fmt.Sprintf("appear online=%v", p.Online)}, nil
	}
	pr := types.PresenceUnavailable
	if p.Online {
		pr = types.PresenceAvailable
	}
	if err := c.E.Client().SendPresence(c.Ctx, pr); err != nil {
		return nil, err
	}
	return map[string]any{"online": p.Online}, nil
}

func mSubscribePresence(c *Call) (any, error) {
	var p api.UserRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveUser(c.Ctx, p.User)
	if err != nil {
		return nil, err
	}
	if err := c.E.Client().SubscribePresence(c.Ctx, j); err != nil {
		return nil, err
	}
	return map[string]any{"subscribed": j.String(), "note": "presence events appear in `wassup events`; WhatsApp only sends them while you appear online"}, nil
}

func mEvents(c *Call) (any, error) {
	var p api.EventsParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	types_ := map[string]bool{}
	for _, t := range p.Types {
		for _, part := range strings.Split(t, ",") {
			if part = strings.TrimSpace(part); part != "" {
				types_[part] = true
			}
		}
	}
	chats := map[string]bool{}
	for _, q := range p.Chats {
		j, _, err := c.E.resolveLocal(c.Ctx, q)
		if err != nil {
			return nil, err
		}
		chats[j.String()] = true
	}
	id, ch := c.E.Bus.Subscribe(1024)
	defer c.E.Bus.Unsubscribe(id)
	state, lastErr := c.E.State()
	_ = c.Emit(api.Event{Type: "subscribed", Time: time.Now(), Data: map[string]any{"state": state, "error": lastErr}})
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil, nil
			}
			if len(types_) > 0 && !types_[ev.Type] && !types_[strings.SplitN(ev.Type, ".", 2)[0]] {
				continue
			}
			if len(chats) > 0 && !chats[ev.Chat] {
				continue
			}
			if err := c.Emit(ev); err != nil {
				return nil, nil
			}
		case <-c.Ctx.Done():
			return nil, nil
		}
	}
}
