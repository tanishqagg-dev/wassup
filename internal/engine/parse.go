package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

type extraData struct {
	Location *api.Location `json:"location,omitempty"`
	Poll     *api.Poll     `json:"poll,omitempty"`
	VCard    string        `json:"vcard,omitempty"`
	Seconds  int           `json:"seconds,omitempty"`
	Width    int           `json:"width,omitempty"`
	Height   int           `json:"height,omitempty"`
	URL      string        `json:"url,omitempty"`
	Title    string        `json:"title,omitempty"`
}

// contextInfo finds the ContextInfo of whichever content field is set.
func contextInfo(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.GetExtendedTextMessage() != nil:
		return m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		return m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		return m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		return m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		return m.GetDocumentMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		return m.GetStickerMessage().GetContextInfo()
	case m.GetLocationMessage() != nil:
		return m.GetLocationMessage().GetContextInfo()
	case m.GetLiveLocationMessage() != nil:
		return m.GetLiveLocationMessage().GetContextInfo()
	case m.GetContactMessage() != nil:
		return m.GetContactMessage().GetContextInfo()
	case m.GetContactsArrayMessage() != nil:
		return m.GetContactsArrayMessage().GetContextInfo()
	case pollOf(m) != nil:
		return pollOf(m).GetContextInfo()
	}
	return nil
}

func pollOf(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3(), m.GetPollCreationMessageV5()} {
		if p != nil {
			return p
		}
	}
	return nil
}

// classify extracts the type, text and media fields of a content message. ok=false means it carries no user content.
func classify(m *waE2E.Message) (sm store.Message, ex extraData, ok bool) {
	sm.Type = "text"
	switch {
	case m.GetConversation() != "":
		sm.Text = m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		et := m.GetExtendedTextMessage()
		sm.Text = et.GetText()
		ex.URL, ex.Title = et.GetMatchedText(), et.GetTitle()
	case m.GetImageMessage() != nil:
		im := m.GetImageMessage()
		sm.Type, sm.Text, sm.MediaMime, sm.MediaSize = "image", im.GetCaption(), im.GetMimetype(), int64(im.GetFileLength())
		ex.Width, ex.Height = int(im.GetWidth()), int(im.GetHeight())
	case m.GetVideoMessage() != nil:
		vm := m.GetVideoMessage()
		sm.Type, sm.Text, sm.MediaMime, sm.MediaSize = "video", vm.GetCaption(), vm.GetMimetype(), int64(vm.GetFileLength())
		if vm.GetGifPlayback() {
			sm.Type = "gif"
		}
		ex.Seconds, ex.Width, ex.Height = int(vm.GetSeconds()), int(vm.GetWidth()), int(vm.GetHeight())
	case m.GetPtvMessage() != nil:
		vm := m.GetPtvMessage()
		sm.Type, sm.MediaMime, sm.MediaSize = "video_note", vm.GetMimetype(), int64(vm.GetFileLength())
		ex.Seconds = int(vm.GetSeconds())
	case m.GetAudioMessage() != nil:
		am := m.GetAudioMessage()
		sm.Type, sm.MediaMime, sm.MediaSize = "audio", am.GetMimetype(), int64(am.GetFileLength())
		if am.GetPTT() {
			sm.Type = "voice"
		}
		ex.Seconds = int(am.GetSeconds())
	case m.GetDocumentMessage() != nil:
		dm := m.GetDocumentMessage()
		sm.Type, sm.Text, sm.MediaMime, sm.MediaSize, sm.MediaName = "document", dm.GetCaption(), dm.GetMimetype(), int64(dm.GetFileLength()), dm.GetFileName()
		if sm.MediaName == "" {
			sm.MediaName = dm.GetTitle()
		}
	case m.GetStickerMessage() != nil:
		st := m.GetStickerMessage()
		sm.Type, sm.MediaMime, sm.MediaSize = "sticker", st.GetMimetype(), int64(st.GetFileLength())
	case m.GetLocationMessage() != nil:
		lm := m.GetLocationMessage()
		sm.Type = "location"
		ex.Location = &api.Location{Lat: lm.GetDegreesLatitude(), Lng: lm.GetDegreesLongitude(), Name: lm.GetName(), Address: lm.GetAddress(), URL: lm.GetURL()}
		sm.Text = strings.TrimSpace(lm.GetName() + " " + lm.GetAddress())
	case m.GetLiveLocationMessage() != nil:
		lm := m.GetLiveLocationMessage()
		sm.Type = "location"
		ex.Location = &api.Location{Lat: lm.GetDegreesLatitude(), Lng: lm.GetDegreesLongitude(), Live: true}
		sm.Text = lm.GetCaption()
	case m.GetContactMessage() != nil:
		cm := m.GetContactMessage()
		sm.Type, sm.Text, ex.VCard = "contact", cm.GetDisplayName(), cm.GetVcard()
	case m.GetContactsArrayMessage() != nil:
		ca := m.GetContactsArrayMessage()
		var names, cards []string
		for _, c := range ca.GetContacts() {
			names = append(names, c.GetDisplayName())
			cards = append(cards, c.GetVcard())
		}
		sm.Type, sm.Text, ex.VCard = "contact", strings.Join(names, ", "), strings.Join(cards, "\n")
	case pollOf(m) != nil:
		p := pollOf(m)
		sm.Type = "poll"
		poll := &api.Poll{Question: p.GetName(), Selectable: int(p.GetSelectableOptionsCount())}
		var lines []string
		for _, o := range p.GetOptions() {
			poll.Options = append(poll.Options, api.PollOption{Name: o.GetOptionName()})
			lines = append(lines, o.GetOptionName())
		}
		ex.Poll = poll
		sm.Text = p.GetName() + "\n" + strings.Join(lines, "\n")
	case m.GetGroupInviteMessage() != nil:
		gi := m.GetGroupInviteMessage()
		sm.Type, sm.Text = "group_invite", "Invite to "+gi.GetGroupName()+": "+gi.GetCaption()
	case m.GetEventMessage() != nil:
		ev := m.GetEventMessage()
		sm.Type, sm.Text = "event", strings.TrimSpace(ev.GetName()+"\n"+ev.GetDescription())
	case m.GetButtonsResponseMessage() != nil:
		sm.Text = m.GetButtonsResponseMessage().GetSelectedDisplayText()
	case m.GetListResponseMessage() != nil:
		sm.Text = m.GetListResponseMessage().GetTitle()
	case m.GetTemplateButtonReplyMessage() != nil:
		sm.Text = m.GetTemplateButtonReplyMessage().GetSelectedDisplayText()
	case m.GetInteractiveMessage() != nil:
		sm.Text = m.GetInteractiveMessage().GetBody().GetText()
	case m.GetButtonsMessage() != nil:
		sm.Text = m.GetButtonsMessage().GetContentText()
	case m.GetListMessage() != nil:
		sm.Text = m.GetListMessage().GetDescription()
	case m.GetTemplateMessage() != nil:
		sm.Text = m.GetTemplateMessage().GetHydratedTemplate().GetHydratedContentText()
	case m.GetProductMessage() != nil:
		sm.Type, sm.Text = "product", m.GetProductMessage().GetProduct().GetTitle()
	case m.GetOrderMessage() != nil:
		sm.Type, sm.Text = "order", m.GetOrderMessage().GetMessage()
	case m.GetCall() != nil:
		sm.Type = "call"
	default:
		return sm, ex, false
	}
	return sm, ex, true
}

// preview is the one-line summary used in chat lists.
func preview(sm *store.Message) string {
	t := strings.ReplaceAll(sm.Text, "\n", " ")
	if len([]rune(t)) > 80 {
		t = string([]rune(t)[:80]) + "…"
	}
	if sm.Revoked {
		return "[deleted]"
	}
	if sm.Type != "text" {
		label := "[" + sm.Type + "]"
		if sm.MediaName != "" {
			label = "[" + sm.Type + ": " + sm.MediaName + "]"
		}
		if t != "" {
			return label + " " + t
		}
		return label
	}
	return t
}

// handleMessage stores an incoming (or history) message and applies edits, revokes, reactions and votes.
func (e *Engine) handleMessage(ctx context.Context, evt *events.Message, chatOverride types.JID, live bool) {
	msg := evt.Message
	if msg == nil {
		return
	}
	chat := chatOverride
	if chat.IsEmpty() {
		chat = e.canonicalChat(ctx, evt.Info.MessageSource)
	}
	sender := e.canonicalUser(ctx, evt.Info.Sender, evt.Info.SenderAlt)
	if evt.Info.IsFromMe {
		if own := e.OwnJID(); !own.IsEmpty() {
			sender = own
		}
	}
	ts := evt.Info.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	kind := chatKind(chat)

	if pm := msg.GetProtocolMessage(); pm != nil {
		e.handleProtocol(ctx, chat, sender, pm, ts, live)
		return
	}
	if r := msg.GetReactionMessage(); r != nil {
		e.storeReaction(ctx, chat, sender, r.GetKey().GetID(), r.GetText(), ts, live)
		return
	}
	if msg.GetEncReactionMessage() != nil {
		if dec, err := e.Client().DecryptReaction(ctx, evt); err == nil {
			e.storeReaction(ctx, chat, sender, msg.GetEncReactionMessage().GetTargetMessageKey().GetID(), dec.GetText(), ts, live)
		}
		return
	}
	if pu := msg.GetPollUpdateMessage(); pu != nil {
		e.handlePollVote(ctx, evt, chat, sender, pu, ts, live)
		return
	}
	if pin := msg.GetPinInChatMessage(); pin != nil {
		if live {
			e.publish(api.Event{Type: "message.pinned", Chat: chat.String(), Data: map[string]any{
				"id": pin.GetKey().GetID(), "by": sender.String(), "pinned": pin.GetType() == waE2E.PinInChatMessage_PIN_FOR_ALL}})
		}
		return
	}

	sm, ex, ok := classify(msg)
	if !ok {
		return
	}
	sm.Chat, sm.ID, sm.Sender, sm.FromMe, sm.Time = chat.String(), evt.Info.ID, sender.String(), evt.Info.IsFromMe, ts
	sm.SenderName = evt.Info.PushName
	// Keep the identity WhatsApp used (a LID in LID-addressed groups, also for our own messages):
	// message keys, quotes, poll votes and media retries must reference it exactly.
	if !evt.Info.Sender.IsEmpty() {
		sm.SenderRaw = evt.Info.Sender.ToNonAD().String()
	}
	sm.ViewOnce = evt.IsViewOnce
	if ci := contextInfo(msg); ci != nil {
		sm.QuotedID = ci.GetStanzaID()
		sm.Forwarded = ci.GetIsForwarded()
		sm.Mentions = strings.Join(ci.GetMentionedJID(), ",")
		if exp := ci.GetExpiration(); exp > 0 && live {
			n := int(exp)
			_ = e.DB.SetChatFlags(ctx, chat.String(), kind, store.ChatFlags{Ephemeral: &n})
		}
	}
	if b, err := json.Marshal(ex); err == nil && string(b) != "{}" {
		sm.Extra = string(b)
	}
	if raw, err := proto.Marshal(msg); err == nil {
		sm.Raw = raw
	}
	if sm.FromMe {
		sm.Status = "sent"
	}
	inserted, err := e.DB.UpsertMessage(ctx, &sm)
	if err != nil {
		e.Log.Warn().Err(err).Str("id", sm.ID).Msg("store message failed")
		return
	}
	if text, ok := e.DB.TakePendingEdit(ctx, sm.Chat, sm.ID); ok {
		_, _ = e.DB.EditMessage(ctx, sm.Chat, sm.ID, text, mergeText(sm.Raw, text))
		sm.Text, sm.Edited = text, true
	}
	unread := live && inserted && !sm.FromMe && kind != "status"
	if err := e.DB.TouchChat(ctx, sm.Chat, kind, ts, sm.ID, preview(&sm), unread); err != nil {
		e.Log.Warn().Err(err).Msg("touch chat failed")
	}
	if kind == "group" || kind == "channel" {
		e.ensureChatName(chat)
	}
	// "yes 1234" typed on your phone in "Message yourself" answers an approval prompt.
	// Messages wassup itself sent are tagged and never count, so an agent can't approve itself.
	if live && inserted && sm.FromMe && ctx.Value(outgoingKey{}) == nil && ctx.Value(sentByUsKey{}) == nil && e.isSelfChat(chat) && approvalReply.MatchString(sm.Text) {
		go e.handleApprovalReply(sm.Text)
	}
	if live && inserted && !sm.FromMe && sm.Type == "voice" && e.Config().Transcribe.Auto {
		go e.autoTranscribe(sm)
	}
	if live && inserted {
		am := e.toAPIMessage(ctx, &sm, nil)
		e.publish(api.Event{Type: "message", Chat: sm.Chat, Time: ts, Data: am})
		if !sm.FromMe {
			e.maybeAutoDownload(&sm)
		}
	}
}

func (e *Engine) handleProtocol(ctx context.Context, chat, sender types.JID, pm *waE2E.ProtocolMessage, ts time.Time, live bool) {
	target := pm.GetKey().GetID()
	switch pm.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		if ok, _ := e.DB.MarkRevoked(ctx, chat.String(), target); ok && live {
			e.publish(api.Event{Type: "message.revoked", Chat: chat.String(), Time: ts, Data: map[string]any{"id": target, "by": sender.String()}})
		}
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		if nm := pm.GetEditedMessage(); nm != nil {
			e.applyEdit(ctx, chat, sender, target, nm, ts, live)
		}
	case waE2E.ProtocolMessage_EPHEMERAL_SETTING:
		n := int(pm.GetEphemeralExpiration())
		_ = e.DB.SetChatFlags(ctx, chat.String(), chatKind(chat), store.ChatFlags{Ephemeral: &n})
		if live {
			e.publish(api.Event{Type: "chat.disappearing", Chat: chat.String(), Time: ts, Data: map[string]any{"seconds": n}})
		}
	}
}

// applyEdit records new content for an existing message, merging it into the stored protobuf so
// forwards and quotes carry the edited text. If the original isn't stored yet (history sync can
// deliver the edit first), the edit waits in pending_edits and is applied when the original lands,
// so the original's sender, timestamp and media keys are kept.
func (e *Engine) applyEdit(ctx context.Context, chat, sender types.JID, target string, nm *waE2E.Message, ts time.Time, live bool) {
	sm, _, ok := classify(nm)
	if !ok || target == "" {
		return
	}
	cur, _ := e.DB.GetMessage(ctx, chat.String(), target)
	if cur == nil {
		_ = e.DB.PutPendingEdit(ctx, chat.String(), target, sm.Text, ts)
		return
	}
	if changed, _ := e.DB.EditMessage(ctx, chat.String(), target, sm.Text, mergeText(cur.Raw, sm.Text)); changed && live {
		e.publish(api.Event{Type: "message.edited", Chat: chat.String(), Time: ts, Data: map[string]any{"id": target, "text": sm.Text, "by": sender.String()}})
	}
}

// mergeText puts new text or caption into an existing message protobuf, keeping media keys and context.
func mergeText(raw []byte, text string) []byte {
	var m waE2E.Message
	if len(raw) == 0 || proto.Unmarshal(raw, &m) != nil {
		out, _ := proto.Marshal(&waE2E.Message{Conversation: proto.String(text)})
		return out
	}
	switch {
	case m.ExtendedTextMessage != nil:
		m.ExtendedTextMessage.Text = proto.String(text)
	case m.ImageMessage != nil:
		m.ImageMessage.Caption = proto.String(text)
	case m.VideoMessage != nil:
		m.VideoMessage.Caption = proto.String(text)
	case m.DocumentMessage != nil:
		m.DocumentMessage.Caption = proto.String(text)
	default:
		m.Conversation = proto.String(text)
	}
	out, _ := proto.Marshal(&m)
	return out
}

func (e *Engine) storeReaction(ctx context.Context, chat, sender types.JID, target, emoji string, ts time.Time, live bool) {
	if target == "" {
		return
	}
	_ = e.DB.PutReaction(ctx, store.Reaction{Chat: chat.String(), MessageID: target, Sender: sender.String(), Emoji: emoji, Time: ts})
	if live {
		e.publish(api.Event{Type: "reaction", Chat: chat.String(), Time: ts, Data: map[string]any{
			"id": target, "sender": sender.String(), "sender_name": e.displayName(ctx, sender), "emoji": emoji, "removed": emoji == ""}})
	}
}

func (e *Engine) handlePollVote(ctx context.Context, evt *events.Message, chat, sender types.JID, pu *waE2E.PollUpdateMessage, ts time.Time, live bool) {
	target := pu.GetPollCreationMessageKey().GetID()
	vote, err := e.Client().DecryptPollVote(ctx, evt)
	if err != nil {
		e.Log.Debug().Err(err).Msg("couldn't decrypt poll vote")
		return
	}
	var names []string
	if pm, _ := e.DB.GetMessage(ctx, chat.String(), target); pm != nil {
		names = pollOptionNames(pm.Raw, vote.GetSelectedOptions())
	}
	_ = e.DB.PutPollVote(ctx, store.PollVote{Chat: chat.String(), MessageID: target, Voter: sender.String(), Options: names, Time: ts})
	if live {
		e.publish(api.Event{Type: "poll.vote", Chat: chat.String(), Time: ts, Data: map[string]any{
			"id": target, "voter": sender.String(), "voter_name": e.displayName(ctx, sender), "options": names}})
	}
}

// pollOptionNames maps the SHA-256 hashes in a vote back to option names.
func pollOptionNames(raw []byte, hashes [][]byte) []string {
	var m waE2E.Message
	if err := proto.Unmarshal(raw, &m); err != nil {
		return nil
	}
	p := pollOf(&m)
	if p == nil {
		return nil
	}
	byHash := map[string]string{}
	for _, o := range p.GetOptions() {
		sum := sha256.Sum256([]byte(o.GetOptionName()))
		byHash[string(sum[:])] = o.GetOptionName()
	}
	var out []string
	for _, h := range hashes {
		if n, ok := byHash[string(h)]; ok {
			out = append(out, n)
		}
	}
	return out
}

// toAPIMessage converts a stored row for output. reactions may be nil.
func (e *Engine) toAPIMessage(ctx context.Context, sm *store.Message, reactions []store.Reaction) *api.Message {
	chat, _ := types.ParseJID(sm.Chat)
	sender, _ := types.ParseJID(sm.Sender)
	m := &api.Message{
		ID: sm.ID, Chat: sm.Chat, ChatName: e.displayName(ctx, chat), Sender: sm.Sender, FromMe: sm.FromMe,
		Time: sm.Time, Type: sm.Type, Text: sm.Text, QuotedID: sm.QuotedID, Forwarded: sm.Forwarded, ViewOnce: sm.ViewOnce,
		Edited: sm.Edited, Revoked: sm.Revoked, Starred: sm.Starred, Status: sm.Status,
	}
	if !sender.IsEmpty() {
		m.SenderName = e.displayName(ctx, sender)
		if (m.SenderName == "" || strings.HasPrefix(m.SenderName, "+")) && sm.SenderName != "" {
			m.SenderName = sm.SenderName
		}
	}
	if sm.Mentions != "" {
		m.Mentions = strings.Split(sm.Mentions, ",")
	}
	var ex extraData
	if sm.Extra != "" {
		_ = json.Unmarshal([]byte(sm.Extra), &ex)
	}
	if sm.MediaMime != "" || sm.MediaSize > 0 || sm.MediaPath != "" {
		m.Media = &api.Media{Mime: sm.MediaMime, FileName: sm.MediaName, Size: sm.MediaSize, Path: sm.MediaPath, Seconds: ex.Seconds, Width: ex.Width, Height: ex.Height}
	}
	m.Location = ex.Location
	if ex.Poll != nil {
		m.Poll = ex.Poll
	}
	if sm.Type == "voice" || sm.Type == "audio" {
		if t, _, ok := e.DB.Transcript(ctx, sm.Chat, sm.ID); ok {
			m.Transcript = t
		}
	}
	for _, r := range reactions {
		rj, _ := types.ParseJID(r.Sender)
		m.Reactions = append(m.Reactions, api.Reaction{Sender: r.Sender, SenderName: e.displayName(ctx, rj), Emoji: r.Emoji, Time: r.Time})
	}
	return m
}

// recordOutgoing stores a message we just sent so it shows up in history and events immediately.
// sender is the identity WhatsApp says we sent as (LID or phone); empty falls back to our phone JID.
func (e *Engine) recordOutgoing(ctx context.Context, to types.JID, id string, ts time.Time, msg *waE2E.Message, sender types.JID) {
	own := sender
	if own.IsEmpty() {
		own = e.OwnJID()
	}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: to, Sender: own, IsFromMe: true, IsGroup: to.Server == types.GroupServer},
			ID:            id,
			Timestamp:     ts,
		},
		Message: msg,
	}
	if vo := msg.GetViewOnceMessage().GetMessage(); vo != nil {
		evt.Message, evt.IsViewOnce = vo, true
	}
	e.handleMessage(context.WithValue(ctx, sentByUsKey{}, true), evt, to, true)
}

func describeJIDs(js []types.JID) string {
	parts := make([]string, len(js))
	for i, j := range js {
		parts[i] = j.String()
	}
	return strings.Join(parts, ", ")
}

// sentByUsKey marks messages wassup sent itself (as opposed to ones typed on your phone).
type sentByUsKey struct{}

var _ = fmt.Sprintf
