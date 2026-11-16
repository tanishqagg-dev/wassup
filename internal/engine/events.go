package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func (e *Engine) handleEvent(raw any) {
	ctx := e.ctx
	switch evt := raw.(type) {
	case *events.Message:
		e.handleMessage(ctx, evt, types.EmptyJID, true)
	case *events.UndecryptableMessage:
		e.Log.Debug().Str("id", evt.Info.ID).Bool("unavailable", evt.IsUnavailable).Msg("undecryptable message; whatsmeow will re-request it")
	case *events.Receipt:
		e.handleReceipt(ctx, evt)
	case *events.Connected:
		e.setState(StateConnected, "")
		go e.afterConnect()
	case *events.Disconnected:
		if s, _ := e.State(); s == StateConnected {
			e.setState(StateReconnecting, "socket closed")
		}
	case *events.KeepAliveTimeout:
		e.Log.Warn().Int("errors", evt.ErrorCount).Msg("keepalive timeout")
	case *events.KeepAliveRestored:
		e.Log.Info().Msg("keepalive restored")
	case *events.LoggedOut:
		e.setState(StateLoggedOut, "device was unlinked: "+evt.Reason.String())
		go e.resetDevice()
	case *events.StreamReplaced:
		e.setState(StateReplaced, "another client connected with this session")
	case *events.TemporaryBan:
		e.setState(StateBanned, evt.String())
	case *events.ClientOutdated:
		if e.Config().Daemon.AutoUpdateVersion && e.refreshWAVersion() {
			e.setState(StateReconnecting, "client version was outdated; updated and reconnecting")
			go func() { time.Sleep(2 * time.Second); e.connect() }()
		} else {
			e.setState(StateOutdated, "WhatsApp says this client version is outdated")
		}
	case *events.ConnectFailure:
		e.setState(StateReconnecting, fmt.Sprintf("connect failure %d: %s", evt.Reason, evt.Message))
	case *events.StreamError:
		e.Log.Warn().Str("code", evt.Code).Msg("stream error")
	case *events.PairSuccess:
		e.Log.Info().Str("jid", evt.ID.String()).Msg("paired")
		_ = e.DB.Set(ctx, "linked_at", time.Now().Format(time.RFC3339))
		e.publish(api.Event{Type: "paired", Data: map[string]any{"jid": evt.ID.String(), "platform": evt.Platform, "business_name": evt.BusinessName}})
	case *events.HistorySync:
		e.handleHistorySync(ctx, evt)
	case *events.OfflineSyncPreview:
		e.setSyncNote(fmt.Sprintf("catching up on %d offline events", evt.Total))
	case *events.OfflineSyncCompleted:
		e.setSyncNote(fmt.Sprintf("caught up (%d offline events)", evt.Count))
		e.publish(api.Event{Type: "offline_sync.completed", Data: map[string]any{"count": evt.Count}})
	case *events.PushName:
		e.publish(api.Event{Type: "contact.push_name", Data: map[string]any{"jid": e.canonicalUser(ctx, evt.JID, evt.JIDAlt).String(), "name": evt.NewPushName}})
	case *events.Contact:
		e.publish(api.Event{Type: "contact", Data: map[string]any{"jid": evt.JID.String()}})
	case *events.JoinedGroup:
		_ = e.DB.SetChatName(ctx, evt.JID.String(), "group", evt.Name)
		e.publish(api.Event{Type: "group.joined", Chat: evt.JID.String(), Data: map[string]any{"name": evt.Name, "reason": evt.Reason}})
	case *events.GroupInfo:
		e.handleGroupInfo(ctx, evt)
	case *events.Picture:
		e.publish(api.Event{Type: "picture", Chat: evt.JID.String(), Data: map[string]any{"removed": evt.Remove, "author": evt.Author.String()}})
	case *events.UserAbout:
		e.publish(api.Event{Type: "contact.about", Data: map[string]any{"jid": evt.JID.String(), "about": evt.Status}})
	case *events.Presence:
		from := e.canonicalUser(ctx, evt.From, types.EmptyJID)
		data := map[string]any{"jid": from.String(), "name": e.displayName(ctx, from), "online": !evt.Unavailable}
		if !evt.LastSeen.IsZero() {
			data["last_seen"] = evt.LastSeen
		}
		e.publish(api.Event{Type: "presence", Chat: from.String(), Data: data})
	case *events.ChatPresence:
		chat := e.canonicalChat(ctx, evt.MessageSource)
		sender := e.canonicalUser(ctx, evt.Sender, evt.SenderAlt)
		state := string(evt.State)
		if evt.State == types.ChatPresenceComposing && evt.Media == types.ChatPresenceMediaAudio {
			state = "recording"
		}
		e.publish(api.Event{Type: "typing", Chat: chat.String(), Data: map[string]any{"sender": sender.String(), "name": e.displayName(ctx, sender), "state": state}})
	case *events.CallOffer:
		e.handleCallOffer(ctx, evt.BasicCallMeta, callMedia(evt), false)
	case *events.CallOfferNotice:
		e.handleCallOffer(ctx, evt.BasicCallMeta, evt.Media, evt.Type == "group")
	case *events.CallAccept:
		e.updateCall(ctx, evt.CallID, "answered_elsewhere")
	case *events.CallTerminate:
		e.updateCall(ctx, evt.CallID, "ended")
	case *events.CallReject:
		e.updateCall(ctx, evt.CallID, "rejected")
	case *events.Archive:
		v := evt.Action.GetArchived()
		_ = e.DB.SetChatFlags(ctx, e.canonicalUserOrChat(ctx, evt.JID), chatKind(evt.JID), store.ChatFlags{Archived: &v})
	case *events.Pin:
		v := evt.Action.GetPinned()
		_ = e.DB.SetChatFlags(ctx, e.canonicalUserOrChat(ctx, evt.JID), chatKind(evt.JID), store.ChatFlags{Pinned: &v})
	case *events.Mute:
		var until int64
		if evt.Action.GetMuted() {
			until = normalizeEpoch(evt.Action.GetMuteEndTimestamp())
			if until <= 0 {
				until = -1
			}
		}
		_ = e.DB.SetChatFlags(ctx, e.canonicalUserOrChat(ctx, evt.JID), chatKind(evt.JID), store.ChatFlags{MutedUntil: &until})
	case *events.MarkChatAsRead:
		jid := e.canonicalUserOrChat(ctx, evt.JID)
		f := store.ChatFlags{}
		if evt.Action.GetRead() {
			zero, no, now := 0, false, evt.Timestamp
			f.Unread, f.MarkedUnread, f.LastReadAt = &zero, &no, &now
		} else {
			yes := true
			f.MarkedUnread = &yes
		}
		_ = e.DB.SetChatFlags(ctx, jid, chatKind(evt.JID), f)
	case *events.DeleteChat:
		_ = e.DB.DeleteChat(ctx, e.canonicalUserOrChat(ctx, evt.JID))
	case *events.ClearChat:
		_ = e.DB.ClearChat(ctx, e.canonicalUserOrChat(ctx, evt.JID))
	case *events.Star:
		_ = e.DB.SetStarred(ctx, e.canonicalUserOrChat(ctx, evt.ChatJID), evt.MessageID, evt.Action.GetStarred())
	case *events.DeleteForMe:
		_ = e.DB.DeleteMessage(ctx, e.canonicalUserOrChat(ctx, evt.ChatJID), evt.MessageID)
	case *events.MediaRetry:
		e.waitMu.Lock()
		ch := e.mediaRetries[evt.MessageID]
		e.waitMu.Unlock()
		if ch != nil {
			select {
			case ch <- evt:
			default:
			}
		}
	case *events.Blocklist:
		e.publish(api.Event{Type: "blocklist", Data: evt.Changes})
	case *events.IdentityChange:
		e.publish(api.Event{Type: "security.identity_change", Data: map[string]any{"jid": evt.JID.String(), "implicit": evt.Implicit}})
	case *events.NewsletterJoin:
		e.publish(api.Event{Type: "channel.joined", Data: evt.NewsletterMetadata.ID.String()})
	case *events.NewsletterLeave:
		e.publish(api.Event{Type: "channel.left", Data: evt.ID.String()})
	case *events.PrivacySettings:
		e.publish(api.Event{Type: "privacy", Data: evt.NewSettings})
	}
}

func (e *Engine) canonicalUserOrChat(ctx context.Context, j types.JID) string {
	if j.Server == types.HiddenUserServer || j.Server == types.LegacyUserServer {
		return e.canonicalUser(ctx, j, types.EmptyJID).String()
	}
	return j.ToNonAD().String()
}

// normalizeEpoch accepts seconds or milliseconds and returns seconds.
func normalizeEpoch(v int64) int64 {
	if v > 1e12 {
		return v / 1000
	}
	return v
}

func (e *Engine) setSyncNote(s string) {
	e.stateMu.Lock()
	e.syncNote = s
	e.stateMu.Unlock()
}

func (e *Engine) handleReceipt(ctx context.Context, evt *events.Receipt) {
	chat := e.canonicalChat(ctx, evt.MessageSource)
	var status string
	switch evt.Type {
	case types.ReceiptTypeDelivered:
		status = "delivered"
	case types.ReceiptTypeRead:
		status = "read"
	case types.ReceiptTypePlayed:
		status = "played"
	case types.ReceiptTypeReadSelf:
		// You read the chat on another device.
		zero, now := 0, evt.Timestamp
		_ = e.DB.SetChatFlags(ctx, chat.String(), chatKind(chat), store.ChatFlags{Unread: &zero, LastReadAt: &now})
		return
	default:
		return
	}
	if evt.IsFromMe {
		return
	}
	_ = e.DB.SetStatus(ctx, chat.String(), evt.MessageIDs, status)
	sender := e.canonicalUser(ctx, evt.Sender, evt.SenderAlt)
	e.publish(api.Event{Type: "receipt", Chat: chat.String(), Time: evt.Timestamp, Data: map[string]any{
		"ids": evt.MessageIDs, "status": status, "by": sender.String(), "by_name": e.displayName(ctx, sender)}})
}

func (e *Engine) handleGroupInfo(ctx context.Context, evt *events.GroupInfo) {
	chat := evt.JID.String()
	data := map[string]any{}
	if evt.Name != nil {
		_ = e.DB.SetChatName(ctx, chat, "group", evt.Name.Name)
		data["name"] = evt.Name.Name
	}
	if evt.Topic != nil {
		data["description"] = evt.Topic.Topic
	}
	if evt.Ephemeral != nil {
		n := 0
		if evt.Ephemeral.IsEphemeral {
			n = int(evt.Ephemeral.DisappearingTimer)
		}
		_ = e.DB.SetChatFlags(ctx, chat, "group", store.ChatFlags{Ephemeral: &n})
		data["disappearing_seconds"] = n
	}
	if evt.Announce != nil {
		data["announce"] = evt.Announce.IsAnnounce
	}
	if evt.Locked != nil {
		data["locked"] = evt.Locked.IsLocked
	}
	list := func(js []types.JID) []string {
		out := make([]string, len(js))
		for i, j := range js {
			out[i] = e.canonicalUser(ctx, j, types.EmptyJID).String()
		}
		return out
	}
	if len(evt.Join) > 0 {
		data["joined"] = list(evt.Join)
	}
	if len(evt.Leave) > 0 {
		data["left"] = list(evt.Leave)
	}
	if len(evt.Promote) > 0 {
		data["promoted"] = list(evt.Promote)
	}
	if len(evt.Demote) > 0 {
		data["demoted"] = list(evt.Demote)
	}
	if evt.Sender != nil {
		data["by"] = e.canonicalUser(ctx, *evt.Sender, types.EmptyJID).String()
	}
	e.publish(api.Event{Type: "group.update", Chat: chat, Time: evt.Timestamp, Data: data})
}

func (e *Engine) handleHistorySync(ctx context.Context, evt *events.HistorySync) {
	data := evt.Data
	cl := e.Client()
	syncType := data.GetSyncType().String()
	msgs, convs := 0, 0
	touched := map[string]int{}
	for _, conv := range data.GetConversations() {
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		canonical := chatJID.ToNonAD()
		if canonical.Server == types.HiddenUserServer {
			if pn, err := types.ParseJID(conv.GetPnJID()); err == nil && !pn.IsEmpty() {
				canonical = pn.ToNonAD()
			} else {
				canonical = e.canonicalUser(ctx, canonical, types.EmptyJID)
			}
		}
		kind := chatKind(canonical)
		convs++
		name := conv.GetName()
		if name == "" {
			name = conv.GetDisplayName()
		}
		if name != "" {
			_ = e.DB.SetChatName(ctx, canonical.String(), kind, name)
		} else {
			_ = e.DB.EnsureChat(ctx, canonical.String(), kind)
		}
		e.applyConversationFlags(ctx, canonical.String(), kind, conv)
		for _, hm := range conv.GetMessages() {
			wm := hm.GetMessage()
			if wm == nil {
				continue
			}
			pe, err := cl.ParseWebMessage(chatJID, wm)
			if err != nil {
				continue
			}
			// ParseWebMessage turns an edit into the edited content under the original ID.
			if orig := wm.GetKey().GetID(); orig != "" && pe.Info.ID != orig {
				sender := e.canonicalUser(ctx, pe.Info.Sender, pe.Info.SenderAlt)
				e.applyEdit(ctx, canonical, sender, pe.Info.ID, pe.Message, pe.Info.Timestamp, false)
				continue
			}
			e.handleMessage(ctx, pe, canonical, false)
			msgs++
			touched[canonical.String()]++
		}
	}
	for _, wm := range data.GetStatusV3Messages() {
		if pe, err := cl.ParseWebMessage(types.StatusBroadcastJID, wm); err == nil {
			e.handleMessage(ctx, pe, types.StatusBroadcastJID, false)
		}
	}
	note := fmt.Sprintf("history sync %s: %d chats, %d messages", syncType, convs, msgs)
	if p := data.GetProgress(); p > 0 {
		note += fmt.Sprintf(" (%d%%)", p)
	}
	e.setSyncNote(note)
	e.Log.Info().Msg(note)
	e.publish(api.Event{Type: "history_sync", Data: map[string]any{"type": syncType, "chats": convs, "messages": msgs, "progress": data.GetProgress()}})
	if data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND {
		e.waitMu.Lock()
		for chat, n := range touched {
			for _, ch := range e.historyWaits[chat] {
				select {
				case ch <- n:
				default:
				}
			}
			delete(e.historyWaits, chat)
		}
		e.waitMu.Unlock()
	}
}

func (e *Engine) applyConversationFlags(ctx context.Context, jid, kind string, conv *waHistorySync.Conversation) {
	f := store.ChatFlags{}
	arch := conv.GetArchived()
	pinned := conv.GetPinned() > 0
	f.Archived, f.Pinned = &arch, &pinned
	if mu := conv.GetMuteEndTime(); mu > 0 {
		v := normalizeEpoch(int64(mu))
		f.MutedUntil = &v
	}
	unread := int(conv.GetUnreadCount())
	if unread >= 0 {
		f.Unread = &unread
	}
	if mu := conv.GetMarkedAsUnread(); mu {
		f.MarkedUnread = &mu
	}
	if exp := int(conv.GetEphemeralExpiration()); exp > 0 {
		f.Ephemeral = &exp
	}
	_ = e.DB.SetChatFlags(ctx, jid, kind, f)
}

// afterConnect runs once per connection: refresh group names so name lookups work.
func (e *Engine) afterConnect() {
	ctx, cancel := context.WithTimeout(e.ctx, time.Minute)
	defer cancel()
	_ = e.refreshGroups(ctx, false)
}

// refreshGroups pulls every joined group's name. force skips the 6h cache.
func (e *Engine) refreshGroups(ctx context.Context, force bool) error {
	e.waitMu.Lock()
	fresh := time.Since(e.groupsFetched) < 6*time.Hour
	if !force && fresh {
		e.waitMu.Unlock()
		return nil
	}
	e.groupsFetched = time.Now()
	e.waitMu.Unlock()
	cl := e.Client()
	if !cl.IsConnected() {
		return api.Errf(api.CodeNotConnected, "not connected")
	}
	groups, err := cl.GetJoinedGroups(ctx)
	if err != nil {
		e.waitMu.Lock()
		e.groupsFetched = time.Time{}
		e.waitMu.Unlock()
		return err
	}
	for _, g := range groups {
		_ = e.DB.SetChatName(ctx, g.JID.String(), "group", g.Name)
		n := 0
		if g.IsEphemeral {
			n = int(g.DisappearingTimer)
		}
		_ = e.DB.SetChatFlags(ctx, g.JID.String(), "group", store.ChatFlags{Ephemeral: &n})
	}
	return nil
}

var nameFetch sync.Map

// ensureChatName fetches a missing group/channel name in the background, once per chat per run.
func (e *Engine) ensureChatName(chat types.JID) {
	if _, busy := nameFetch.LoadOrStore(chat.String(), true); busy {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
		defer cancel()
		if c, _ := e.DB.GetChat(ctx, chat.String()); c != nil && c.Name != "" {
			return
		}
		cl := e.Client()
		switch chat.Server {
		case types.GroupServer:
			if info, err := cl.GetGroupInfo(ctx, chat); err == nil {
				_ = e.DB.SetChatName(ctx, chat.String(), "group", info.Name)
			}
		case types.NewsletterServer:
			if info, err := cl.GetNewsletterInfo(ctx, chat); err == nil {
				_ = e.DB.SetChatName(ctx, chat.String(), "channel", info.ThreadMeta.Name.Text)
			}
		}
	}()
}

// resetDevice replaces an unlinked session with a fresh one so `wassup login` works again without a restart.
func (e *Engine) resetDevice() {
	time.Sleep(500 * time.Millisecond)
	e.setClient(e.container.NewDevice())
	e.setState(StateLoggedOut, "device was unlinked; run wassup login")
}

func callMedia(evt *events.CallOffer) string {
	if evt.Data != nil {
		for _, child := range evt.Data.GetChildren() {
			if child.Tag == "video" {
				return "video"
			}
		}
	}
	return "audio"
}

func (e *Engine) handleCallOffer(ctx context.Context, meta types.BasicCallMeta, media string, group bool) {
	caller := meta.CallCreator
	if caller.IsEmpty() {
		caller = meta.From
	}
	from := e.canonicalUser(ctx, caller, meta.CallCreatorAlt)
	c := store.Call{ID: meta.CallID, From: from.String(), Media: media, Time: meta.Timestamp, Status: "ringing"}
	if !meta.GroupJID.IsEmpty() {
		c.Group = meta.GroupJID.String()
	}
	if c.Time.IsZero() {
		c.Time = time.Now()
	}
	_ = e.DB.PutCall(ctx, c)
	e.publish(api.Event{Type: "call", Chat: from.String(), Data: map[string]any{
		"id": c.ID, "from": c.From, "name": e.displayName(ctx, from), "media": media, "group": c.Group, "status": "ringing"}})

	cfg := e.Config().Calls
	if !cfg.AutoReject || e.matchList(ctx, cfg.Except, from) {
		return
	}
	if err := e.Client().RejectCall(ctx, meta.From, meta.CallID); err != nil {
		e.Log.Warn().Err(err).Msg("auto-reject failed")
		return
	}
	e.updateCall(ctx, meta.CallID, "auto_rejected")
	if cfg.RejectMessage != "" && c.Group == "" && !group {
		msg := &waE2E.Message{Conversation: new(cfg.RejectMessage)}
		if resp, err := e.Client().SendMessage(ctx, from, msg); err == nil {
			e.recordOutgoing(ctx, from, resp.ID, resp.Timestamp, msg, resp.Sender)
			_ = e.DB.AddAudit(ctx, store.Audit{Time: time.Now(), Source: "auto", Profile: "calls", Method: "calls.auto_reply", Targets: from.String(), OK: true})
		}
	}
}

func (e *Engine) updateCall(ctx context.Context, id, status string) {
	c, _ := e.DB.GetCall(ctx, id)
	if c == nil {
		return
	}
	if status == "ended" && c.Status != "ringing" {
		return // keep rejected/answered states
	}
	if status == "ended" {
		status = "missed"
	}
	c.Status = status
	_ = e.DB.PutCall(ctx, *c)
	e.publish(api.Event{Type: "call", Chat: c.From, Data: map[string]any{"id": c.ID, "from": c.From, "status": status}})
}
