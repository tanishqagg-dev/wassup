package engine

import (
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func init() {
	register(&Method{Name: "status.post", Summary: "Post a WhatsApp Status (story): text, image or video", Params: api.StatusPostParams{}, Mutating: true, Conn: true, H: mStatusPost})
	register(&Method{Name: "status.list", Summary: "Status updates received from contacts", Params: api.LimitParams{}, H: mStatusList})
	register(&Method{Name: "channels.list", Summary: "Channels you follow", Params: api.Empty{}, Conn: true, H: mChannels})
	register(&Method{Name: "channels.info", Summary: "Channel details by JID, name or invite link", Params: api.ChannelRef{}, Conn: true, H: mChannelInfo})
	register(&Method{Name: "channels.follow", Summary: "Follow or unfollow a channel", Params: api.ChannelFollowParams{}, Mutating: true, Conn: true, H: mChannelFollow})
	register(&Method{Name: "channels.create", Summary: "Create a channel you own", Params: api.ChannelCreateParams{}, Mutating: true, Conn: true, H: mChannelCreate})
	register(&Method{Name: "channels.messages", Summary: "Recent posts in a channel", Params: api.ChannelMessagesParams{}, Conn: true, H: mChannelMessages})
	register(&Method{Name: "channels.react", Summary: "React to a channel post", Params: api.ChannelReactParams{}, Mutating: true, Conn: true, H: mChannelReact})
	register(&Method{Name: "channels.mute", Summary: "Mute or unmute a channel", Params: api.ChannelMuteParams{}, Mutating: true, Conn: true, H: mChannelMute})
	register(&Method{Name: "calls.list", Summary: "Recent incoming calls", Params: api.LimitParams{}, H: mCalls})
	register(&Method{Name: "calls.reject", Summary: "Reject a ringing call", Params: api.CallRejectParams{}, Mutating: true, Conn: true, H: mCallReject})
	register(&Method{Name: "audit.list", Summary: "Recent actions taken through wassup, with the profile that allowed them", Params: api.LimitParams{}, H: mAudit})
}

func parseARGB(hex string) (uint32, error) {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) == 6 {
		h = "FF" + h
	}
	if len(h) != 8 {
		return 0, usage("color must be #RRGGBB")
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, usage("color must be #RRGGBB")
	}
	return uint32(v), nil
}

func mStatusPost(c *Call) (any, error) {
	var p api.StatusPostParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	to := types.StatusBroadcastJID
	summary := "post status"
	dry, err := c.Authorize(Action{Targets: []types.JID{to}, Sends: 1, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, to, summary)
	}
	var msg *waE2E.Message
	kind, handle := "text", ""
	if err := c.checkReadable(p.File); err != nil {
		return nil, err
	}
	if p.File != "" {
		pm, err := c.E.prepareMedia(c.Ctx, to, p.File, "", p.Text, "")
		if err != nil {
			return nil, err
		}
		if pm.kind != "image" && pm.kind != "video" {
			return nil, usage("a status can be text, an image or a video")
		}
		msg, kind, handle = pm.msg, pm.kind, pm.handle
	} else {
		if strings.TrimSpace(p.Text) == "" {
			return nil, usage("give text or a file")
		}
		bg := uint32(0xFF1F7A5C)
		if p.Background != "" {
			if bg, err = parseARGB(p.Background); err != nil {
				return nil, err
			}
		}
		font := waE2E.ExtendedTextMessage_FontType(p.Font)
		msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: new(p.Text), BackgroundArgb: new(bg), TextArgb: new(uint32(0xFFFFFFFF)), Font: font.Enum(),
		}}
	}
	resp, err := c.E.Client().SendMessage(c.Ctx, to, msg, whatsmeow.SendRequestExtra{MediaHandle: handle})
	if err != nil {
		return nil, err
	}
	c.E.recordOutgoing(c.Ctx, to, resp.ID, resp.Timestamp, msg, resp.Sender)
	return api.SendResult{ID: resp.ID, Chat: to.String(), ChatName: "Status", Time: resp.Timestamp, Type: kind}, nil
}

func mStatusList(c *Call) (any, error) {
	var p api.LimitParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	rows, err := c.E.DB.ListMessages(c.Ctx, store.MessageQuery{Chat: types.StatusBroadcastJID.String(), Limit: p.Limit, Desc: true, After: time.Now().Add(-24 * time.Hour)})
	if err != nil {
		return nil, err
	}
	return c.E.withReactions(c.Ctx, rows), nil
}

func (e *Engine) toAPIChannel(n *types.NewsletterMetadata) api.Channel {
	ch := api.Channel{JID: n.ID.String(), Name: n.ThreadMeta.Name.Text, Description: n.ThreadMeta.Description.Text,
		Subscribers: n.ThreadMeta.SubscriberCount, Verified: n.ThreadMeta.VerificationState == types.NewsletterVerificationStateVerified}
	if n.ThreadMeta.InviteCode != "" {
		ch.Invite = "https://whatsapp.com/channel/" + n.ThreadMeta.InviteCode
	}
	if n.ViewerMeta != nil {
		ch.Role = string(n.ViewerMeta.Role)
		ch.Muted = n.ViewerMeta.Mute == types.NewsletterMuteOn
	}
	return ch
}

func mChannels(c *Call) (any, error) {
	list, err := c.E.Client().GetSubscribedNewsletters(c.Ctx)
	if err != nil {
		return nil, err
	}
	out := []api.Channel{}
	for _, n := range list {
		_ = c.E.DB.SetChatName(c.Ctx, n.ID.String(), "channel", n.ThreadMeta.Name.Text)
		out = append(out, c.E.toAPIChannel(n))
	}
	return out, nil
}

// resolveChannel accepts a newsletter JID, an invite link/code, or a followed channel's name.
func (c *Call) resolveChannel(q string) (*types.NewsletterMetadata, error) {
	cl := c.E.Client()
	if m := channelRe.FindStringSubmatch(q); m != nil {
		return cl.GetNewsletterInfoWithInvite(c.Ctx, m[1])
	}
	if strings.HasSuffix(q, "@newsletter") {
		j, err := types.ParseJID(q)
		if err != nil {
			return nil, usage("bad channel JID")
		}
		return cl.GetNewsletterInfo(c.Ctx, j)
	}
	j, _, err := c.E.resolveLocal(c.Ctx, q)
	if err == nil && j.Server == types.NewsletterServer {
		return cl.GetNewsletterInfo(c.Ctx, j)
	}
	if !strings.Contains(q, " ") && len(q) >= 16 {
		if n, err := cl.GetNewsletterInfoWithInvite(c.Ctx, q); err == nil {
			return n, nil
		}
	}
	return nil, notFound("no channel matches %q", q).WithHint("use the channel invite link or its @newsletter JID")
}

func mChannelInfo(c *Call) (any, error) {
	var p api.ChannelRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	n, err := c.resolveChannel(p.Channel)
	if err != nil {
		return nil, err
	}
	return c.E.toAPIChannel(n), nil
}

func mChannelFollow(c *Call) (any, error) {
	var p api.ChannelFollowParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	n, err := c.resolveChannel(p.Channel)
	if err != nil {
		return nil, err
	}
	verb := "follow"
	if p.Unfollow {
		verb = "unfollow"
	}
	summary := verb + " channel " + n.ThreadMeta.Name.Text
	dry, err := c.Authorize(Action{Targets: []types.JID{n.ID}, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, n.ID, summary)
	}
	if p.Unfollow {
		err = c.E.Client().UnfollowNewsletter(c.Ctx, n.ID)
	} else {
		err = c.E.Client().FollowNewsletter(c.Ctx, n.ID)
		_ = c.E.DB.SetChatName(c.Ctx, n.ID.String(), "channel", n.ThreadMeta.Name.Text)
	}
	return map[string]any{"channel": n.ID.String(), "name": n.ThreadMeta.Name.Text, "following": !p.Unfollow}, err
}

func mChannelCreate(c *Call) (any, error) {
	var p api.ChannelCreateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	summary := "create channel " + p.Name
	dry, err := c.Authorize(Action{Summary: summary})
	if err != nil {
		return nil, err
	}
	if dry {
		return api.DryRun{DryRun: true, Action: c.Method.Name, Summary: summary}, nil
	}
	params := whatsmeow.CreateNewsletterParams{Name: p.Name, Description: p.Description}
	if err := c.checkReadable(p.Photo); err != nil {
		return nil, err
	}
	if p.Photo != "" {
		if params.Picture, err = squareJPEG(c.Ctx, p.Photo); err != nil {
			return nil, err
		}
	}
	n, err := c.E.Client().CreateNewsletter(c.Ctx, params)
	if err != nil {
		return nil, err
	}
	_ = c.E.DB.SetChatName(c.Ctx, n.ID.String(), "channel", p.Name)
	return c.E.toAPIChannel(n), nil
}

func mChannelMessages(c *Call) (any, error) {
	var p api.ChannelMessagesParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	n, err := c.resolveChannel(p.Channel)
	if err != nil {
		return nil, err
	}
	count := p.Count
	if count <= 0 {
		count = 20
	}
	msgs, err := c.E.Client().GetNewsletterMessages(c.Ctx, n.ID, &whatsmeow.GetNewsletterMessagesParams{Count: count, Before: types.MessageServerID(p.Before)})
	if err != nil {
		return nil, err
	}
	out := []api.ChannelMessage{}
	for _, m := range msgs {
		cm := api.ChannelMessage{ServerID: int(m.MessageServerID), ID: m.MessageID, Time: m.Timestamp.Format(time.RFC3339), Type: m.Type, Views: m.ViewsCount, Reactions: m.ReactionCounts}
		if m.Message != nil {
			if sm, _, ok := classify(m.Message); ok {
				cm.Text = sm.Text
				if sm.Type != "text" {
					cm.Type = sm.Type
				}
			}
		}
		out = append(out, cm)
	}
	return out, nil
}

func mChannelReact(c *Call) (any, error) {
	var p api.ChannelReactParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	n, err := c.resolveChannel(p.Channel)
	if err != nil {
		return nil, err
	}
	summary := "react " + p.Emoji + " in channel " + n.ThreadMeta.Name.Text
	dry, err := c.Authorize(Action{Targets: []types.JID{n.ID}, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, n.ID, summary)
	}
	err = c.E.Client().NewsletterSendReaction(c.Ctx, n.ID, types.MessageServerID(p.ServerID), p.Emoji, "")
	return map[string]any{"channel": n.ID.String(), "server_id": p.ServerID, "emoji": p.Emoji}, err
}

func mChannelMute(c *Call) (any, error) {
	var p api.ChannelMuteParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	n, err := c.resolveChannel(p.Channel)
	if err != nil {
		return nil, err
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{n.ID}, Summary: "mute channel"})
	if err != nil || dry {
		return dryResult(c, dry, err, n.ID, "mute channel "+n.ThreadMeta.Name.Text)
	}
	err = c.E.Client().NewsletterToggleMute(c.Ctx, n.ID, !p.Unmute)
	return map[string]any{"channel": n.ID.String(), "muted": !p.Unmute}, err
}

func mCalls(c *Call) (any, error) {
	var p api.LimitParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	rows, err := c.E.DB.ListCalls(c.Ctx, p.Limit)
	if err != nil {
		return nil, err
	}
	out := []api.Call{}
	for _, r := range rows {
		fj, _ := types.ParseJID(r.From)
		out = append(out, api.Call{ID: r.ID, From: r.From, Name: c.E.displayName(c.Ctx, fj), Group: r.Group, Media: r.Media, Time: r.Time.Format(time.RFC3339), Status: r.Status})
	}
	return out, nil
}

func mCallReject(c *Call) (any, error) {
	var p api.CallRejectParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	from := p.From
	if from == "" {
		call, _ := c.E.DB.GetCall(c.Ctx, p.ID)
		if call == nil {
			return nil, notFound("call %s isn't in the local history", p.ID).WithHint("pass --from too")
		}
		from = call.From
	}
	j, _, err := c.E.resolveLocal(c.Ctx, from)
	if err != nil {
		return nil, err
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{j}, Summary: "reject call"})
	if err != nil || dry {
		return dryResult(c, dry, err, j, "reject call "+p.ID)
	}
	if err := c.E.Client().RejectCall(c.Ctx, j, p.ID); err != nil {
		return nil, err
	}
	c.E.updateCall(c.Ctx, p.ID, "rejected")
	return map[string]any{"call": p.ID, "rejected": true}, nil
}

func mAudit(c *Call) (any, error) {
	var p api.LimitParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	rows, err := c.E.DB.ListAudit(c.Ctx, p.Limit)
	if err != nil {
		return nil, err
	}
	out := []api.AuditEntry{}
	for _, r := range rows {
		out = append(out, api.AuditEntry{Time: r.Time.Format(time.RFC3339), Source: r.Source, Profile: r.Profile, Method: r.Method, Targets: r.Targets, OK: r.OK, DryRun: r.DryRun, Sends: r.Sends, Error: r.Error})
	}
	return out, nil
}
