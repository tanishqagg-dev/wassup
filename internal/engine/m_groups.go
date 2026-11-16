package engine

import (
	"bytes"
	"context"
	"image"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
)

func init() {
	register(&Method{Name: "groups.list", Summary: "List groups you're in", Params: api.GroupListParams{}, Conn: true, H: mGroups})
	register(&Method{Name: "groups.info", Summary: "Group details and members", Params: api.GroupRef{}, Conn: true, H: mGroupInfo})
	register(&Method{Name: "groups.create", Summary: "Create a group or community", Params: api.GroupCreateParams{}, Mutating: true, Conn: true, H: mGroupCreate})
	register(&Method{Name: "groups.update", Summary: "Rename, describe, change settings or photo", Params: api.GroupUpdateParams{}, Mutating: true, Conn: true, H: mGroupUpdate})
	register(&Method{Name: "groups.participants", Summary: "Add, remove, promote or demote members", Params: api.GroupParticipantsParams{}, Mutating: true, Conn: true, H: mGroupParticipants})
	register(&Method{Name: "groups.leave", Summary: "Leave a group", Params: api.GroupRef{}, Mutating: true, Conn: true, H: mGroupLeave})
	register(&Method{Name: "groups.invite_link", Summary: "Get (or reset) a group's invite link", Params: api.GroupInviteParams{}, Mutating: true, Conn: true, H: mGroupInvite})
	register(&Method{Name: "groups.join", Summary: "Join a group with an invite link", Params: api.JoinParams{}, Mutating: true, Conn: true, H: mGroupJoin})
	register(&Method{Name: "groups.preview", Summary: "Show a group behind an invite link without joining", Params: api.JoinParams{}, Conn: true, H: mGroupPreview})
	register(&Method{Name: "groups.requests", Summary: "List, approve or reject join requests", Params: api.GroupRequestsParams{}, Mutating: true, Conn: true, H: mGroupRequests})
	register(&Method{Name: "communities.link", Summary: "Link or unlink a group to a community", Params: api.CommunityLinkParams{}, Mutating: true, Conn: true, H: mCommunityLink})
	register(&Method{Name: "communities.groups", Summary: "List the groups in a community", Params: api.GroupRef{}, Conn: true, H: mCommunityGroups})
}

func (e *Engine) toAPIGroup(ctx context.Context, g *types.GroupInfo, members bool) api.Group {
	out := api.Group{
		JID: g.JID.String(), Name: g.Name, Description: g.Topic, ParticipantCount: len(g.Participants),
		Announce: g.IsAnnounce, Locked: g.IsLocked, ApprovalRequired: g.IsJoinApprovalRequired, IsCommunity: g.IsParent,
	}
	if g.ParticipantCount > out.ParticipantCount {
		out.ParticipantCount = g.ParticipantCount
	}
	switch g.MemberAddMode {
	case types.GroupMemberAddModeAdmin:
		out.MemberAddMode = "admins"
	case types.GroupMemberAddModeAllMember:
		out.MemberAddMode = "all"
	}
	if !g.LinkedParentJID.IsEmpty() {
		out.Community = g.LinkedParentJID.String()
	}
	if !g.OwnerPN.IsEmpty() {
		out.Owner = g.OwnerPN.String()
	} else if !g.OwnerJID.IsEmpty() {
		out.Owner = e.canonicalUser(ctx, g.OwnerJID, types.EmptyJID).String()
	}
	if !g.GroupCreated.IsZero() {
		t := g.GroupCreated
		out.Created = &t
	}
	if g.IsEphemeral && g.DisappearingTimer > 0 {
		out.Disappearing = config.FormatDuration(time.Duration(g.DisappearingTimer) * time.Second)
	}
	own := e.OwnJID()
	ownLID := e.Client().Store.LID
	for _, p := range g.Participants {
		pj := p.PhoneNumber
		if pj.IsEmpty() {
			pj = e.canonicalUser(ctx, p.JID, types.EmptyJID)
		}
		if pj.User == own.User || (!ownLID.IsEmpty() && p.JID.User == ownLID.User) {
			out.IsAdmin = p.IsAdmin || p.IsSuperAdmin
		}
		if members {
			ap := api.Participant{JID: pj.String(), Admin: p.IsAdmin, SuperAdmin: p.IsSuperAdmin, Name: e.displayName(ctx, pj), Error: p.Error}
			if pj.Server == types.DefaultUserServer {
				ap.Phone = "+" + pj.User
			}
			if !p.LID.IsEmpty() {
				ap.LID = p.LID.String()
			}
			if ap.Name == "" || strings.HasPrefix(ap.Name, "+") {
				if p.DisplayName != "" {
					ap.Name = p.DisplayName
				}
			}
			out.Participants = append(out.Participants, ap)
		}
	}
	return out
}

func mGroups(c *Call) (any, error) {
	var p api.GroupListParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	groups, err := c.E.Client().GetJoinedGroups(c.Ctx)
	if err != nil {
		return nil, err
	}
	out := []api.Group{}
	for _, g := range groups {
		_ = c.E.DB.SetChatName(c.Ctx, g.JID.String(), "group", g.Name)
		if p.Query != "" && nameScore(p.Query, g.Name) == 0 {
			continue
		}
		out = append(out, c.E.toAPIGroup(c.Ctx, g, false))
	}
	return out, nil
}

func mGroupInfo(c *Call) (any, error) {
	var p api.GroupRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	g, err := c.E.Client().GetGroupInfo(c.Ctx, j)
	if err != nil {
		return nil, err
	}
	_ = c.E.DB.SetChatName(c.Ctx, g.JID.String(), "group", g.Name)
	return c.E.toAPIGroup(c.Ctx, g, true), nil
}

func mGroupCreate(c *Call) (any, error) {
	var p api.GroupCreateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, usage("group name is empty")
	}
	if len([]rune(p.Name)) > 100 {
		return nil, usage("group names are limited to 100 characters")
	}
	members, err := c.E.resolveUsers(c.Ctx, p.Participants)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 && !p.Community {
		return nil, usage("add at least one participant")
	}
	kind := "group"
	if p.Community {
		kind = "community"
	}
	summary := "create " + kind + " " + p.Name + " with " + describeJIDs(members)
	dry, err := c.Authorize(Action{Targets: members, Summary: summary})
	if err != nil || dry {
		if err != nil {
			return nil, err
		}
		return api.DryRun{DryRun: true, Action: c.Method.Name, Summary: summary}, nil
	}
	req := whatsmeow.ReqCreateGroup{Name: p.Name, Participants: members}
	req.IsParent = p.Community
	if p.Parent != "" {
		parent, err := c.E.resolveGroup(c.Ctx, p.Parent)
		if err != nil {
			return nil, err
		}
		req.LinkedParentJID = parent
	}
	g, err := c.E.Client().CreateGroup(c.Ctx, req)
	if err != nil {
		return nil, err
	}
	_ = c.E.DB.SetChatName(c.Ctx, g.JID.String(), "group", g.Name)
	if p.Description != "" {
		if err := c.E.Client().SetGroupDescription(c.Ctx, g.JID, p.Description); err != nil {
			c.E.Log.Warn().Err(err).Msg("set description after create failed")
		}
	}
	out := c.E.toAPIGroup(c.Ctx, g, true)
	var failed []api.Participant
	for _, pt := range out.Participants {
		if pt.Error != 0 {
			failed = append(failed, pt)
		}
	}
	res := map[string]any{"group": out}
	if len(failed) > 0 {
		res["not_added"] = failed
		res["note"] = "some people couldn't be added directly (privacy settings); send them the invite link"
	}
	return res, nil
}

// squareJPEG center-crops and scales an image for group and profile photos.
func squareJPEG(ctx context.Context, path string) ([]byte, error) {
	path = expandPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, usage("can't read %s: %v", path, err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		if _, img, err = toJPEG(ctx, path, data); err != nil {
			return nil, usage("can't read %s as an image", path)
		}
	}
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		img = si.SubImage(image.Rect(x0, y0, x0+side, y0+side))
	}
	return jpegBytes(resizeFit(img, 640), 88), nil
}

func mGroupUpdate(c *Call) (any, error) {
	var p api.GroupUpdateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	var changes []string
	add := func(cond bool, s string) {
		if cond {
			changes = append(changes, s)
		}
	}
	add(p.Name != "", "name")
	add(p.Description != nil, "description")
	add(p.Announce != nil, "announce")
	add(p.Locked != nil, "locked")
	add(p.Approval != nil, "approval")
	add(p.MemberAdd != "", "member_add")
	add(p.Photo != "" || p.RemovePhoto, "photo")
	add(p.Disappearing != "", "disappearing")
	if len(changes) == 0 {
		return nil, usage("nothing to change")
	}
	summary := "update " + strings.Join(changes, ", ") + " of " + c.E.displayName(c.Ctx, j)
	dry, err := c.Authorize(Action{Targets: []types.JID{j}, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, j, summary)
	}
	cl := c.E.Client()
	ctx := c.Ctx
	if p.Name != "" {
		if err := cl.SetGroupName(ctx, j, p.Name); err != nil {
			return nil, err
		}
		_ = c.E.DB.SetChatName(ctx, j.String(), "group", p.Name)
	}
	if p.Description != nil {
		if err := cl.SetGroupDescription(ctx, j, *p.Description); err != nil {
			return nil, err
		}
	}
	if p.Announce != nil {
		if err := cl.SetGroupAnnounce(ctx, j, *p.Announce); err != nil {
			return nil, err
		}
	}
	if p.Locked != nil {
		if err := cl.SetGroupLocked(ctx, j, *p.Locked); err != nil {
			return nil, err
		}
	}
	if p.Approval != nil {
		if err := cl.SetGroupJoinApprovalMode(ctx, j, *p.Approval); err != nil {
			return nil, err
		}
	}
	if p.MemberAdd != "" {
		mode := types.GroupMemberAddModeAllMember
		switch strings.ToLower(p.MemberAdd) {
		case "admins", "admin", "admin_add":
			mode = types.GroupMemberAddModeAdmin
		case "all", "everyone", "all_member_add":
		default:
			return nil, usage("member_add must be admins or all")
		}
		if err := cl.SetGroupMemberAddMode(ctx, j, mode); err != nil {
			return nil, err
		}
	}
	if p.Photo != "" || p.RemovePhoto {
		var data []byte
		if p.Photo != "" {
			if err := c.checkReadable(p.Photo); err != nil {
				return nil, err
			}
			if data, err = squareJPEG(ctx, p.Photo); err != nil {
				return nil, err
			}
		}
		if _, err := cl.SetGroupPhoto(ctx, j, data); err != nil {
			return nil, err
		}
	}
	if p.Disappearing != "" {
		d, err := parseDisappearing(p.Disappearing)
		if err != nil {
			return nil, err
		}
		if err := cl.SetDisappearingTimer(ctx, j, d, time.Now()); err != nil {
			return nil, err
		}
	}
	return map[string]any{"group": j.String(), "changed": changes}, nil
}

func mGroupParticipants(c *Call) (any, error) {
	var p api.GroupParticipantsParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	var action whatsmeow.ParticipantChange
	switch strings.ToLower(p.Action) {
	case "add":
		action = whatsmeow.ParticipantChangeAdd
	case "remove", "kick":
		action = whatsmeow.ParticipantChangeRemove
	case "promote", "admin":
		action = whatsmeow.ParticipantChangePromote
	case "demote":
		action = whatsmeow.ParticipantChangeDemote
	default:
		return nil, usage("action must be add, remove, promote or demote")
	}
	members, err := c.E.resolveUsers(c.Ctx, p.Participants)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, usage("no participants given")
	}
	summary := string(action) + " " + describeJIDs(members) + " in " + c.E.displayName(c.Ctx, j)
	targets := append([]types.JID{j}, members...)
	dry, err := c.Authorize(Action{Targets: targets, Destructive: action == whatsmeow.ParticipantChangeRemove, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, j, summary)
	}
	res, err := c.E.Client().UpdateGroupParticipants(c.Ctx, j, members, action)
	if err != nil {
		return nil, err
	}
	out := []api.Participant{}
	for _, r := range res {
		pj := r.PhoneNumber
		if pj.IsEmpty() {
			pj = c.E.canonicalUser(c.Ctx, r.JID, types.EmptyJID)
		}
		out = append(out, api.Participant{JID: pj.String(), Name: c.E.displayName(c.Ctx, pj), Admin: r.IsAdmin, Error: r.Error})
	}
	result := map[string]any{"group": j.String(), "action": string(action), "results": out}
	for _, r := range out {
		if r.Error == 403 && action == whatsmeow.ParticipantChangeAdd {
			result["note"] = "error 403 means their privacy settings block direct adds; send them the invite link instead"
		}
	}
	return result, nil
}

func mGroupLeave(c *Call) (any, error) {
	var p api.GroupRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	summary := "leave " + c.E.displayName(c.Ctx, j)
	dry, err := c.Authorize(Action{Targets: []types.JID{j}, Destructive: true, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, j, summary)
	}
	return map[string]any{"left": j.String()}, c.E.Client().LeaveGroup(c.Ctx, j)
}

func mGroupInvite(c *Call) (any, error) {
	var p api.GroupInviteParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	if p.Reset {
		summary := "reset invite link of " + c.E.displayName(c.Ctx, j)
		dry, err := c.Authorize(Action{Targets: []types.JID{j}, Destructive: true, Summary: summary})
		if err != nil || dry {
			return dryResult(c, dry, err, j, summary)
		}
	} else {
		c.audited = true // read-only use: don't log
	}
	link, err := c.E.Client().GetGroupInviteLink(c.Ctx, j, p.Reset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"group": j.String(), "link": link, "reset": p.Reset}, nil
}

func inviteCode(link string) string {
	if m := inviteLinkRe.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return strings.TrimSpace(link)
}

func mGroupJoin(c *Call) (any, error) {
	var p api.JoinParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	code := inviteCode(p.Link)
	info, err := c.E.Client().GetGroupInfoFromLink(c.Ctx, code)
	if err != nil {
		return nil, err
	}
	summary := "join " + info.Name
	dry, err := c.Authorize(Action{Targets: []types.JID{info.JID}, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, info.JID, summary)
	}
	j, err := c.E.Client().JoinGroupWithLink(c.Ctx, code)
	if err != nil {
		return nil, err
	}
	_ = c.E.DB.SetChatName(c.Ctx, j.String(), "group", info.Name)
	res := map[string]any{"joined": j.String(), "name": info.Name}
	if info.IsJoinApprovalRequired {
		res["note"] = "this group needs admin approval; you'll be added once they accept"
	}
	return res, nil
}

func mGroupPreview(c *Call) (any, error) {
	var p api.JoinParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	info, err := c.E.Client().GetGroupInfoFromLink(c.Ctx, inviteCode(p.Link))
	if err != nil {
		return nil, err
	}
	return c.E.toAPIGroup(c.Ctx, info, false), nil
}

func mGroupRequests(c *Call) (any, error) {
	var p api.GroupRequestsParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	cl := c.E.Client()
	reqs, err := cl.GetGroupRequestParticipants(c.Ctx, j)
	if err != nil {
		return nil, err
	}
	action := strings.ToLower(p.Action)
	if action == "" || action == "list" {
		c.audited = true
		out := []map[string]any{}
		for _, r := range reqs {
			pj := c.E.canonicalUser(c.Ctx, r.JID, types.EmptyJID)
			out = append(out, map[string]any{"jid": pj.String(), "name": c.E.displayName(c.Ctx, pj), "requested_at": r.RequestedAt})
		}
		return out, nil
	}
	var change whatsmeow.ParticipantRequestChange
	switch action {
	case "approve", "accept":
		change = whatsmeow.ParticipantChangeApprove
	case "reject", "deny":
		change = whatsmeow.ParticipantChangeReject
	default:
		return nil, usage("action must be list, approve or reject")
	}
	var who []types.JID
	if len(p.Participants) == 0 {
		for _, r := range reqs {
			who = append(who, r.JID)
		}
	} else if who, err = c.E.resolveUsers(c.Ctx, p.Participants); err != nil {
		return nil, err
	}
	if len(who) == 0 {
		return map[string]any{"group": j.String(), "changed": 0, "note": "no pending requests"}, nil
	}
	summary := action + " " + describeJIDs(who) + " for " + c.E.displayName(c.Ctx, j)
	dry, err := c.Authorize(Action{Targets: append([]types.JID{j}, who...), Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, j, summary)
	}
	res, err := cl.UpdateGroupRequestParticipants(c.Ctx, j, who, change)
	if err != nil {
		return nil, err
	}
	return map[string]any{"group": j.String(), "action": action, "changed": len(res)}, nil
}

func mCommunityLink(c *Call) (any, error) {
	var p api.CommunityLinkParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	parent, err := c.E.resolveGroup(c.Ctx, p.Community)
	if err != nil {
		return nil, err
	}
	child, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	verb := "link"
	if p.Unlink {
		verb = "unlink"
	}
	summary := verb + " " + c.E.displayName(c.Ctx, child) + " and " + c.E.displayName(c.Ctx, parent)
	dry, err := c.Authorize(Action{Targets: []types.JID{parent, child}, Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, parent, summary)
	}
	if p.Unlink {
		err = c.E.Client().UnlinkGroup(c.Ctx, parent, child)
	} else {
		err = c.E.Client().LinkGroup(c.Ctx, parent, child)
	}
	return map[string]any{"community": parent.String(), "group": child.String(), "linked": !p.Unlink}, err
}

func mCommunityGroups(c *Call) (any, error) {
	var p api.GroupRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveGroup(c.Ctx, p.Group)
	if err != nil {
		return nil, err
	}
	subs, err := c.E.Client().GetSubGroups(c.Ctx, j)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, s := range subs {
		out = append(out, map[string]any{"jid": s.JID.String(), "name": s.Name, "default": s.IsDefaultSubGroup})
	}
	return out, nil
}
