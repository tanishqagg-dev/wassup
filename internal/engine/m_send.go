package engine

import (
	"context"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func init() {
	register(&Method{Name: "send.message", Summary: "Send text, a file, a location or a contact card", Params: api.SendParams{}, Mutating: true, Conn: true, H: mSend})
	register(&Method{Name: "send.many", Summary: "Send the same message to several chats, paced to avoid spam flags", Params: api.SendManyParams{}, Mutating: true, Conn: true, H: mSendMany})
	register(&Method{Name: "polls.create", Summary: "Send a poll", Params: api.PollCreateParams{}, Mutating: true, Conn: true, H: mPollCreate})
	register(&Method{Name: "polls.vote", Summary: "Vote in a poll", Params: api.PollVoteParams{}, Mutating: true, Conn: true, H: mPollVote})
	register(&Method{Name: "polls.results", Summary: "Show poll votes collected so far", Params: api.MessageRef{}, H: mPollResults})
	register(&Method{Name: "messages.react", Summary: "React to a message with an emoji (empty removes)", Params: api.ReactParams{}, Mutating: true, Conn: true, H: mReact})
	register(&Method{Name: "messages.edit", Summary: "Edit one of your messages", Params: api.EditParams{}, Mutating: true, Conn: true, H: mEdit})
	register(&Method{Name: "messages.delete", Summary: "Delete a message for everyone (or only locally)", Params: api.DeleteParams{}, Mutating: true, H: mDelete})
	register(&Method{Name: "messages.forward", Summary: "Forward a message to another chat", Params: api.ForwardParams{}, Mutating: true, Conn: true, H: mForward})
	register(&Method{Name: "messages.pin", Summary: "Pin or unpin a message in its chat", Params: api.PinParams{}, Mutating: true, Conn: true, H: mPin})
	register(&Method{Name: "messages.star", Summary: "Star or unstar a message", Params: api.StarParams{}, Mutating: true, Conn: true, H: mStar})
	register(&Method{Name: "messages.read", Summary: "Mark messages (default: the whole chat) as read", Params: api.MarkReadParams{}, Mutating: true, Conn: true, H: mMarkRead})
}

var mentionRe = regexp.MustCompile(`@\+?(\d{7,15})\b`)

// setContextInfo attaches ci to whichever content field the message uses.
func setContextInfo(m *waE2E.Message, ci *waE2E.ContextInfo) {
	switch {
	case m.ExtendedTextMessage != nil:
		m.ExtendedTextMessage.ContextInfo = ci
	case m.ImageMessage != nil:
		m.ImageMessage.ContextInfo = ci
	case m.VideoMessage != nil:
		m.VideoMessage.ContextInfo = ci
	case m.PtvMessage != nil:
		m.PtvMessage.ContextInfo = ci
	case m.AudioMessage != nil:
		m.AudioMessage.ContextInfo = ci
	case m.DocumentMessage != nil:
		m.DocumentMessage.ContextInfo = ci
	case m.StickerMessage != nil:
		m.StickerMessage.ContextInfo = ci
	case m.LocationMessage != nil:
		m.LocationMessage.ContextInfo = ci
	case m.ContactMessage != nil:
		m.ContactMessage.ContextInfo = ci
	case m.Conversation != nil:
		m.ExtendedTextMessage = &waE2E.ExtendedTextMessage{Text: m.Conversation, ContextInfo: ci}
		m.Conversation = nil
	}
}

func buildVCard(name, phone string) string {
	digits := nonDigit.ReplaceAllString(phone, "")
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\nVERSION:3.0\n")
	b.WriteString("FN:" + name + "\n")
	if digits != "" {
		b.WriteString(fmt.Sprintf("TEL;type=CELL;type=VOICE;waid=%s:+%s\n", digits, digits))
	}
	b.WriteString("END:VCARD")
	return b.String()
}

// content builds the message body shared by single and bulk sends (uploading media once).
func (e *Engine) content(ctx context.Context, to types.JID, p *api.SendParams) (*waE2E.Message, string, string, error) {
	switch {
	case p.Location != nil:
		lm := &waE2E.LocationMessage{DegreesLatitude: new(p.Location.Lat), DegreesLongitude: new(p.Location.Lng), Name: optStr(p.Location.Name), Address: optStr(p.Location.Address)}
		return &waE2E.Message{LocationMessage: lm}, "location", "", nil
	case p.ContactCard != nil:
		vc := p.ContactCard.VCard
		if vc == "" {
			if p.ContactCard.Phone != "" {
				digits, err := normalizePhone(p.ContactCard.Phone, e.Config().Send.DefaultCountryCode)
				if err != nil {
					return nil, "", "", err
				}
				vc = buildVCard(p.ContactCard.Name, digits)
			} else {
				vc = buildVCard(p.ContactCard.Name, "")
			}
		}
		return &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: new(p.ContactCard.Name), Vcard: new(vc)}}, "contact", "", nil
	case p.File != "":
		pm, err := e.prepareMedia(ctx, to, p.File, p.As, p.Text, p.FileName)
		if err != nil {
			return nil, "", "", err
		}
		return pm.msg, pm.kind, pm.handle, nil
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, "", "", usage("nothing to send: give text, a file, a location or a contact")
	}
	if !p.NoPreview && e.Config().Send.LinkPreviews && to.Server != types.NewsletterServer {
		if u := firstURL(p.Text); u != "" {
			if meta := fetchLinkPreview(ctx, u); meta != nil {
				et := &waE2E.ExtendedTextMessage{
					Text: new(p.Text), MatchedText: new(u), Title: new(meta.Title), Description: optStr(meta.Description),
					PreviewType: waE2E.ExtendedTextMessage_NONE.Enum(),
				}
				if len(meta.Thumb) > 0 {
					et.JPEGThumbnail = meta.Thumb
				}
				return &waE2E.Message{ExtendedTextMessage: et}, "text", "", nil
			}
		}
	}
	return &waE2E.Message{Conversation: new(p.Text)}, "text", "", nil
}

// decorate adds per-chat context: reply quote, mentions, disappearing timer, view once.
func (e *Engine) decorate(ctx context.Context, to types.JID, msg *waE2E.Message, p *api.SendParams) (*waE2E.Message, error) {
	ci := &waE2E.ContextInfo{}
	used := false
	if p.ReplyTo != "" {
		q, err := e.DB.GetMessage(ctx, to.String(), p.ReplyTo)
		if err != nil || q == nil {
			q, err = e.findMessage(ctx, "", p.ReplyTo)
			if err != nil {
				return nil, err
			}
		}
		ci.StanzaID = new(q.ID)
		_, qs := e.keyFor(q)
		ci.Participant = new(qs.ToNonAD().String())
		if q.Chat != to.String() {
			ci.RemoteJID = new(q.Chat)
		}
		if len(q.Raw) > 0 {
			var qm waE2E.Message
			if proto.Unmarshal(q.Raw, &qm) == nil {
				setContextInfo(&qm, nil)
				ci.QuotedMessage = &qm
			}
		}
		used = true
	}
	text := ""
	switch {
	case msg.Conversation != nil:
		text = msg.GetConversation()
	case msg.ExtendedTextMessage != nil:
		text = msg.ExtendedTextMessage.GetText()
	}
	var mentions []types.JID
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		mentions = append(mentions, types.NewJID(m[1], types.DefaultUserServer))
	}
	if len(p.Mentions) > 0 {
		extra, err := e.resolveUsers(ctx, p.Mentions)
		if err != nil {
			return nil, err
		}
		for _, j := range extra {
			if !strings.Contains(text, "@"+j.User) {
				text = strings.TrimSpace(text + " @" + j.User)
			}
			mentions = append(mentions, j)
		}
	}
	if p.MentionAll {
		if to.Server != types.GroupServer {
			return nil, usage("mention_all only works in groups")
		}
		info, err := e.Client().GetGroupInfo(ctx, to)
		if err != nil {
			return nil, err
		}
		for _, pt := range info.Participants {
			mentions = append(mentions, pt.JID)
		}
	}
	if len(mentions) > 0 {
		lidGroup := false
		if to.Server == types.GroupServer {
			if info, err := e.Client().GetGroupInfo(ctx, to); err == nil {
				lidGroup = info.AddressingMode == types.AddressingModeLID
			}
		}
		seen := map[string]bool{}
		for _, j := range mentions {
			if lidGroup && j.Server == types.DefaultUserServer {
				if lid, err := e.Client().Store.LIDs.GetLIDForPN(ctx, j); err == nil && !lid.IsEmpty() { // LIDs exist once linked
					text = strings.ReplaceAll(text, "@"+j.User, "@"+lid.User)
					j = lid
				}
			}
			if !seen[j.String()] {
				seen[j.String()] = true
				ci.MentionedJID = append(ci.MentionedJID, j.String())
			}
		}
		if msg.Conversation != nil {
			msg.Conversation = new(text)
		} else if msg.ExtendedTextMessage != nil {
			msg.ExtendedTextMessage.Text = new(text)
		}
		used = true
	}
	if c, _ := e.DB.GetChat(ctx, to.String()); c != nil && c.Ephemeral > 0 {
		ci.Expiration = new(uint32(c.Ephemeral))
		used = true
	}
	if used {
		setContextInfo(msg, ci)
	}
	if p.ViewOnce {
		switch {
		case msg.ImageMessage != nil:
			msg.ImageMessage.ViewOnce = new(true)
		case msg.VideoMessage != nil:
			msg.VideoMessage.ViewOnce = new(true)
		case msg.AudioMessage != nil:
			msg.AudioMessage.ViewOnce = new(true)
		default:
			return nil, usage("view once only works for images, videos and voice notes")
		}
		msg = &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{Message: msg}}
	}
	return msg, nil
}

func (e *Engine) simulateTyping(ctx context.Context, to types.JID, text string) {
	cl := e.Client()
	_ = cl.SendChatPresence(ctx, to, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	d := time.Duration(len([]rune(text))) * 45 * time.Millisecond
	d = min(max(d, 700*time.Millisecond), 4*time.Second)
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
	_ = cl.SendChatPresence(ctx, to, types.ChatPresencePaused, types.ChatPresenceMediaText)
}

func (e *Engine) sendBuilt(ctx context.Context, to types.JID, msg *waE2E.Message, kind, handle string) (*api.SendResult, error) {
	resp, err := e.Client().SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{MediaHandle: handle})
	if err != nil {
		return nil, err
	}
	e.recordOutgoing(ctx, to, resp.ID, resp.Timestamp, msg, resp.Sender)
	return &api.SendResult{ID: resp.ID, Chat: to.String(), ChatName: e.displayName(ctx, to), Time: resp.Timestamp, Type: kind, ServerID: int(resp.ServerID)}, nil
}

func describeSend(p *api.SendParams) string {
	switch {
	case p.File != "":
		return "send file " + p.File
	case p.Location != nil:
		return "send a location"
	case p.ContactCard != nil:
		return "send contact " + p.ContactCard.Name
	}
	t := p.Text
	if len([]rune(t)) > 60 {
		t = string([]rune(t)[:60]) + "…"
	}
	return fmt.Sprintf("send %q", t)
}

func mSend(c *Call) (any, error) {
	var p api.SendParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if err := c.checkReadable(p.File); err != nil {
		return nil, err
	}
	to, _, err := c.E.resolve(c.Ctx, p.To)
	if err != nil {
		return nil, err
	}
	if p.Text == "" && p.File == "" && p.Location == nil && p.ContactCard == nil {
		return nil, usage("nothing to send: give text, a file, a location or a contact")
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{to}, Sends: 1, Summary: describeSend(&p) + " to " + c.E.displayName(c.Ctx, to)})
	if err != nil {
		return nil, err
	}
	if dry {
		return api.DryRun{DryRun: true, Action: c.Method.Name, Targets: []string{to.String()}, Summary: describeSend(&p) + " to " + c.E.displayName(c.Ctx, to)}, nil
	}
	msg, kind, handle, err := c.E.content(c.Ctx, to, &p)
	if err != nil {
		return nil, err
	}
	if msg, err = c.E.decorate(c.Ctx, to, msg, &p); err != nil {
		return nil, err
	}
	if (p.Typing || c.E.Config().Send.SimulateTyping) && kind == "text" {
		c.E.simulateTyping(c.Ctx, to, p.Text)
	}
	return c.E.sendBuilt(c.Ctx, to, msg, kind, handle)
}

func mSendMany(c *Call) (any, error) {
	var p api.SendManyParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if err := c.checkReadable(p.File); err != nil {
		return nil, err
	}
	var targets []types.JID
	var inputs []string
	for _, q := range p.To {
		for _, part := range splitList(q) {
			j, _, err := c.E.resolve(c.Ctx, part)
			if err != nil {
				return nil, api.AsError(err).WithDetails(map[string]string{"recipient": part})
			}
			targets = append(targets, j)
			inputs = append(inputs, part)
		}
	}
	if len(targets) == 0 {
		return nil, usage("no recipients")
	}
	sp := &api.SendParams{Text: p.Text, File: p.File, As: p.As}
	summary := fmt.Sprintf("%s to %d chats", describeSend(sp), len(targets))
	// Messages=true keeps group/new-chat confirmations; quota is reserved per item below.
	dry, err := c.Authorize(Action{Targets: targets, Messages: true, Summary: summary})
	if err != nil {
		return nil, err
	}
	if dry {
		ts := make([]string, len(targets))
		for i, t := range targets {
			ts[i] = t.String()
		}
		return api.DryRun{DryRun: true, Action: c.Method.Name, Targets: ts, Summary: summary}, nil
	}
	delay := 2 * time.Second
	if p.Delay != "" {
		if delay, err = parseDur(p.Delay); err != nil {
			return nil, usage("bad delay: %v", err)
		}
	}
	// Channels take plaintext uploads, chats take encrypted ones, so prepare each kind once.
	type body struct {
		msg          *waE2E.Message
		kind, handle string
	}
	bodies := map[bool]*body{}
	contentFor := func(to types.JID) (*body, error) {
		channel := to.Server == types.NewsletterServer
		if b := bodies[channel]; b != nil {
			return b, nil
		}
		msg, kind, handle, err := c.E.content(c.Ctx, to, sp)
		if err != nil {
			return nil, err
		}
		bodies[channel] = &body{msg, kind, handle}
		return bodies[channel], nil
	}
	pol, profile, _ := c.E.Config().PolicyStrict(c.Req.Meta.Profile)
	res := api.SendManyResult{}
	for i, to := range targets {
		if i > 0 && delay > 0 {
			jitter := time.Duration(rand.Int64N(int64(delay)/2 + 1))
			select {
			case <-time.After(delay + jitter):
			case <-c.Ctx.Done():
				return res, api.Errf(api.CodeTimeout, "cancelled after %d of %d", i, len(targets))
			}
		}
		b, err := contentFor(to)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			res.Failed = append(res.Failed, api.SendFailure{To: inputs[i], Error: api.AsError(translateError(err))})
			continue
		}
		// Shared gate: waits (up to 2 min) for quota instead of failing the batch, and holds the
		// send lock until this item's audit row is written so concurrent senders can't overshoot.
		unlock, err := c.E.acquireSend(c.Ctx, pol, profile, []types.JID{to}, 1, 2*time.Minute)
		if err != nil {
			res.Failed = append(res.Failed, api.SendFailure{To: inputs[i], Error: api.AsError(err)})
			if c.Ctx.Err() != nil {
				return res, nil
			}
			continue
		}
		msg, err := c.E.decorate(c.Ctx, to, proto.Clone(b.msg).(*waE2E.Message), sp)
		var out *api.SendResult
		if err == nil {
			out, err = c.E.sendBuilt(c.Ctx, to, msg, b.kind, b.handle)
		}
		c.recordItem(to, err)
		unlock()
		if err != nil {
			res.Failed = append(res.Failed, api.SendFailure{To: inputs[i], Error: api.AsError(translateError(err))})
			continue
		}
		res.Sent = append(res.Sent, *out)
	}
	return res, nil
}

func mPollCreate(c *Call) (any, error) {
	var p api.PollCreateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if len(p.Options) < 2 || len(p.Options) > 12 {
		return nil, usage("a poll needs 2 to 12 options")
	}
	if strings.TrimSpace(p.Question) == "" {
		return nil, usage("poll question is empty")
	}
	to, _, err := c.E.resolve(c.Ctx, p.To)
	if err != nil {
		return nil, err
	}
	sel := p.Selectable
	if sel == 0 {
		sel = 1
	}
	if sel < 0 || sel >= len(p.Options) {
		sel = 0 // any number
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{to}, Sends: 1, Summary: "send poll " + p.Question})
	if err != nil || dry {
		return dryResult(c, dry, err, to, "send poll "+p.Question)
	}
	msg := c.E.Client().BuildPollCreation(p.Question, p.Options, sel)
	return c.E.sendBuilt(c.Ctx, to, msg, "poll", "")
}

func dryResult(c *Call, dry bool, err error, to types.JID, summary string) (any, error) {
	if err != nil {
		return nil, err
	}
	return api.DryRun{DryRun: true, Action: c.Method.Name, Targets: []string{to.String()}, Summary: summary}, nil
}

// keyFor builds the WhatsApp message key of a stored message.
func (e *Engine) keyFor(sm *store.Message) (types.JID, types.JID) {
	chat, _ := types.ParseJID(sm.Chat)
	if sm.FromMe && sm.SenderRaw == "" {
		return chat, e.OwnJID()
	}
	raw := sm.SenderRaw
	if raw == "" {
		raw = sm.Sender
	}
	sender, _ := types.ParseJID(raw)
	return chat, sender
}

func (e *Engine) infoFor(sm *store.Message) *types.MessageInfo {
	chat, sender := e.keyFor(sm)
	return &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: sm.FromMe, IsGroup: chat.Server == types.GroupServer},
		ID:            sm.ID,
		Timestamp:     sm.Time,
	}
}

func mPollVote(c *Call) (any, error) {
	var p api.PollVoteParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	if sm.Type != "poll" {
		return nil, usage("message %s isn't a poll", p.ID)
	}
	chat, _ := types.ParseJID(sm.Chat)
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Sends: 1, Summary: "vote " + strings.Join(p.Options, ", ")})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, "vote "+strings.Join(p.Options, ", "))
	}
	var pm waE2E.Message
	_ = proto.Unmarshal(sm.Raw, &pm)
	valid := map[string]bool{}
	if poll := pollOf(&pm); poll != nil {
		for _, o := range poll.GetOptions() {
			valid[o.GetOptionName()] = true
		}
	}
	for _, o := range p.Options {
		if len(valid) > 0 && !valid[o] {
			return nil, usage("%q isn't an option of this poll", o)
		}
	}
	msg, err := c.E.Client().BuildPollVote(c.Ctx, c.E.infoFor(sm), p.Options)
	if err != nil {
		return nil, err
	}
	resp, err := c.E.Client().SendMessage(c.Ctx, chat, msg)
	if err != nil {
		return nil, err
	}
	_ = c.E.DB.PutPollVote(c.Ctx, store.PollVote{Chat: sm.Chat, MessageID: sm.ID, Voter: c.E.OwnJID().String(), Options: p.Options, Time: resp.Timestamp})
	return map[string]any{"id": resp.ID, "poll": sm.ID, "options": p.Options}, nil
}

func mPollResults(c *Call) (any, error) {
	var p api.MessageRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	am := c.E.toAPIMessage(c.Ctx, sm, nil)
	if am.Poll == nil {
		return nil, usage("message %s isn't a poll", p.ID)
	}
	votes, err := c.E.DB.PollVotes(c.Ctx, sm.Chat, sm.ID)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, o := range am.Poll.Options {
		idx[o.Name] = i
	}
	for _, v := range votes {
		vj, _ := types.ParseJID(v.Voter)
		for _, o := range v.Options {
			if i, ok := idx[o]; ok {
				am.Poll.Options[i].Votes++
				am.Poll.Options[i].Voters = append(am.Poll.Options[i].Voters, c.E.displayName(c.Ctx, vj))
			}
		}
	}
	return map[string]any{"id": sm.ID, "chat": sm.Chat, "question": am.Poll.Question, "options": am.Poll.Options, "voters": len(votes)}, nil
}

func mReact(c *Call) (any, error) {
	var p api.ReactParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	chat, sender := c.E.keyFor(sm)
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Sends: 1, Summary: "react " + p.Emoji})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, "react "+p.Emoji)
	}
	msg := c.E.Client().BuildReaction(chat, sender, sm.ID, p.Emoji)
	resp, err := c.E.Client().SendMessage(c.Ctx, chat, msg)
	if err != nil {
		return nil, err
	}
	_ = c.E.DB.PutReaction(c.Ctx, store.Reaction{Chat: sm.Chat, MessageID: sm.ID, Sender: c.E.OwnJID().String(), Emoji: p.Emoji, Time: resp.Timestamp})
	return map[string]any{"id": sm.ID, "chat": sm.Chat, "emoji": p.Emoji, "removed": p.Emoji == ""}, nil
}

func mEdit(c *Call) (any, error) {
	var p api.EditParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	if !sm.FromMe {
		return nil, usage("you can only edit your own messages")
	}
	if time.Since(sm.Time) > 15*time.Minute {
		return nil, api.Errf(api.CodeWhatsApp, "WhatsApp only allows edits within 15 minutes of sending (this was %s ago)", time.Since(sm.Time).Round(time.Minute))
	}
	chat, _ := types.ParseJID(sm.Chat)
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Sends: 1, Summary: "edit message"})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, "edit message "+sm.ID)
	}
	var nc *waE2E.Message
	switch sm.Type {
	case "text":
		nc = &waE2E.Message{Conversation: new(p.Text)}
	case "image", "video", "document":
		var orig waE2E.Message
		_ = proto.Unmarshal(sm.Raw, &orig)
		switch {
		case orig.ImageMessage != nil:
			nc = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: new(p.Text)}}
		case orig.VideoMessage != nil:
			nc = &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Caption: new(p.Text)}}
		case orig.DocumentMessage != nil:
			nc = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{Caption: new(p.Text)}}
		}
	}
	if nc == nil {
		return nil, usage("%s messages can't be edited", sm.Type)
	}
	if _, err := c.E.Client().SendMessage(c.Ctx, chat, c.E.Client().BuildEdit(chat, sm.ID, nc)); err != nil {
		return nil, err
	}
	_, _ = c.E.DB.EditMessage(c.Ctx, sm.Chat, sm.ID, p.Text, mergeText(sm.Raw, p.Text))
	return map[string]any{"id": sm.ID, "chat": sm.Chat, "text": p.Text, "edited": true}, nil
}

func mDelete(c *Call) (any, error) {
	var p api.DeleteParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	chat, sender := c.E.keyFor(sm)
	if p.LocalOnly {
		dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Summary: "delete local copy"})
		if err != nil || dry {
			return dryResult(c, dry, err, chat, "delete local copy of "+sm.ID)
		}
		return map[string]any{"id": sm.ID, "deleted_locally": true}, c.E.DB.DeleteMessage(c.Ctx, sm.Chat, sm.ID)
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Sends: 1, Destructive: true, Summary: "delete message for everyone"})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, "delete message "+sm.ID+" for everyone")
	}
	if err := c.E.WaitConnected(c.Ctx); err != nil {
		return nil, err
	}
	if _, err := c.E.Client().SendMessage(c.Ctx, chat, c.E.Client().BuildRevoke(chat, sender, sm.ID)); err != nil {
		return nil, err
	}
	_, _ = c.E.DB.MarkRevoked(c.Ctx, sm.Chat, sm.ID)
	return map[string]any{"id": sm.ID, "chat": sm.Chat, "deleted_for_everyone": true}, nil
}

func mForward(c *Call) (any, error) {
	var p api.ForwardParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	if sm.ViewOnce {
		return nil, usage("view-once messages can't be forwarded")
	}
	if len(sm.Raw) == 0 {
		return nil, usage("message %s has no stored content to forward", sm.ID)
	}
	to, _, err := c.E.resolve(c.Ctx, p.To)
	if err != nil {
		return nil, err
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{to}, Sends: 1, Summary: "forward message"})
	if err != nil || dry {
		return dryResult(c, dry, err, to, "forward "+sm.ID+" to "+c.E.displayName(c.Ctx, to))
	}
	var msg waE2E.Message
	if err := proto.Unmarshal(sm.Raw, &msg); err != nil {
		return nil, err
	}
	msg.MessageContextInfo = nil
	old := contextInfo(&msg)
	score := uint32(1)
	if old != nil {
		score = old.GetForwardingScore() + 1
	}
	ci := &waE2E.ContextInfo{IsForwarded: new(true), ForwardingScore: new(score)}
	if ch, _ := c.E.DB.GetChat(c.Ctx, to.String()); ch != nil && ch.Ephemeral > 0 {
		ci.Expiration = new(uint32(ch.Ephemeral))
	}
	setContextInfo(&msg, ci)
	return c.E.sendBuilt(c.Ctx, to, &msg, sm.Type, "")
}

func mPin(c *Call) (any, error) {
	var p api.PinParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	chat, sender := c.E.keyFor(sm)
	dur := 7 * 24 * time.Hour
	if p.Duration != "" {
		if dur, err = parseDur(p.Duration); err != nil {
			return nil, usage("bad duration: %v", err)
		}
	}
	verb := "pin"
	if p.Unpin {
		verb = "unpin"
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Sends: 1, Summary: verb + " message"})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, verb+" message "+sm.ID)
	}
	typ := waE2E.PinInChatMessage_PIN_FOR_ALL
	if p.Unpin {
		typ = waE2E.PinInChatMessage_UNPIN_FOR_ALL
	}
	msg := &waE2E.Message{
		PinInChatMessage: &waE2E.PinInChatMessage{
			Key:               c.E.Client().BuildMessageKey(chat, sender, sm.ID),
			Type:              typ.Enum(),
			SenderTimestampMS: new(time.Now().UnixMilli()),
		},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageAddOnDurationInSecs: new(uint32(dur.Seconds()))},
	}
	if _, err := c.E.Client().SendMessage(c.Ctx, chat, msg); err != nil {
		return nil, err
	}
	return map[string]any{"id": sm.ID, "chat": sm.Chat, "pinned": !p.Unpin}, nil
}

func mStar(c *Call) (any, error) {
	var p api.StarParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	sm, err := c.E.findMessage(c.Ctx, p.Chat, p.ID)
	if err != nil {
		return nil, err
	}
	chat, sender := c.E.keyFor(sm)
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Summary: "star message"})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, "star message "+sm.ID)
	}
	if err := c.E.Client().SendAppState(c.Ctx, appstate.BuildStar(chat, sender, sm.ID, sm.FromMe, !p.Unstar)); err != nil {
		return nil, err
	}
	_ = c.E.DB.SetStarred(c.Ctx, sm.Chat, sm.ID, !p.Unstar)
	return map[string]any{"id": sm.ID, "starred": !p.Unstar}, nil
}

func mMarkRead(c *Call) (any, error) {
	var p api.MarkReadParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	chat, _, err := c.E.resolve(c.Ctx, p.Chat)
	if err != nil {
		return nil, err
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{chat}, Summary: "mark read"})
	if err != nil || dry {
		return dryResult(c, dry, err, chat, "mark "+c.E.displayName(c.Ctx, chat)+" read")
	}
	var msgs []*store.Message
	if len(p.IDs) > 0 {
		for _, id := range p.IDs {
			if m, _ := c.E.DB.GetMessage(c.Ctx, chat.String(), id); m != nil && !m.FromMe {
				msgs = append(msgs, m)
			}
		}
	} else if msgs, err = c.E.DB.UnreadIncoming(c.Ctx, chat.String(), 200); err != nil {
		return nil, err
	}
	bySender := map[types.JID][]types.MessageID{}
	var latest *store.Message
	for _, m := range msgs {
		_, s := c.E.keyFor(m)
		if chat.Server != types.GroupServer {
			s = types.EmptyJID
		}
		bySender[s] = append(bySender[s], m.ID)
		if latest == nil || m.Time.After(latest.Time) {
			latest = m
		}
	}
	now := time.Now()
	for s, ids := range bySender {
		if err := c.E.Client().MarkRead(c.Ctx, ids, now, chat, s); err != nil {
			return nil, err
		}
	}
	if len(p.IDs) == 0 {
		var key *waCommon.MessageKey
		ts := now
		if latest != nil {
			lc, ls := c.E.keyFor(latest)
			key = c.E.Client().BuildMessageKey(lc, ls, latest.ID)
			ts = latest.Time
		}
		if err := c.E.Client().SendAppState(c.Ctx, appstate.BuildMarkChatAsRead(chat, true, ts, key)); err != nil {
			c.E.Log.Debug().Err(err).Msg("app state mark-read failed")
		}
		zero, no := 0, false
		_ = c.E.DB.SetChatFlags(c.Ctx, chat.String(), chatKind(chat), store.ChatFlags{Unread: &zero, MarkedUnread: &no, LastReadAt: &now})
	}
	return map[string]any{"chat": chat.String(), "marked": len(msgs)}, nil
}
