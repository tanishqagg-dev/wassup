package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

func init() {
	register(&Method{Name: "auth.status", Summary: "Connection and account status", Params: api.Empty{}, H: mStatus})
	register(&Method{Name: "auth.login", Summary: "Link this CLI to a phone by QR code or pairing code", Params: api.LoginParams{}, Stream: true, H: mLogin})
	register(&Method{Name: "auth.logout", Summary: "Unlink this device from the phone", Params: api.Empty{}, Mutating: true, H: mLogout})
	register(&Method{Name: "resolve", Summary: "Show which chat a name, phone or JID points to", Params: api.ResolveParams{}, H: mResolve})
	register(&Method{Name: "contacts.list", Summary: "List or search contacts", Params: api.ListContactsParams{}, H: mContacts})
	register(&Method{Name: "users.check", Summary: "Check whether phone numbers are on WhatsApp", Params: api.CheckParams{}, Conn: true, H: mCheck})
	register(&Method{Name: "users.info", Summary: "Profile details of a person", Params: api.UserRef{}, Conn: true, H: mUserInfo})
	register(&Method{Name: "users.avatar", Summary: "Download a profile or group picture", Params: api.AvatarParams{}, Conn: true, H: mAvatar})
	register(&Method{Name: "users.block", Summary: "Block or unblock someone", Params: api.BlockParams{}, Mutating: true, Conn: true, H: mBlock})
	register(&Method{Name: "users.blocklist", Summary: "List blocked people", Params: api.Empty{}, Conn: true, H: mBlocklist})
	register(&Method{Name: "profile.get", Summary: "Your own profile", Params: api.Empty{}, Conn: true, H: mProfile})
	register(&Method{Name: "profile.update", Summary: "Change your name, about text or photo", Params: api.ProfileUpdateParams{}, Mutating: true, Conn: true, H: mProfileUpdate})
	register(&Method{Name: "privacy.get", Summary: "Your privacy settings", Params: api.Empty{}, Conn: true, H: mPrivacy})
	register(&Method{Name: "privacy.set", Summary: "Change a privacy setting", Params: api.PrivacySetParams{}, Mutating: true, Conn: true, H: mPrivacySet})
}

// StatusInfo is also used by the daemon when the socket is up but nothing else is.
func (e *Engine) StatusInfo(ctx context.Context) api.Status {
	cl := e.Client()
	state, lastErr := e.State()
	st := api.Status{
		Account: e.Acct.Name, State: state, LoggedIn: cl.Store.ID != nil, Connected: cl.IsConnected() && cl.IsLoggedIn(),
		LastError: lastErr, Version: Version, WAVersion: wastore.GetWAVersion().String(),
		Messages: e.DB.CountMessages(ctx, ""), Chats: e.DB.CountChats(ctx), DaemonPID: os.Getpid(),
	}
	started := e.started
	st.DaemonStarted = &started
	if cl.Store.ID != nil {
		own := cl.Store.ID.ToNonAD()
		st.JID, st.Phone, st.PushName = own.String(), "+"+own.User, cl.Store.PushName
		if !cl.Store.LID.IsEmpty() {
			st.LID = cl.Store.LID.ToNonAD().String()
		}
		st.Platform = cl.Store.Platform
	}
	e.stateMu.RLock()
	if !e.connectedAt.IsZero() && st.Connected {
		t := e.connectedAt
		st.ConnectedAt = &t
	}
	st.HistorySync = e.syncNote
	e.stateMu.RUnlock()
	return st
}

func mStatus(c *Call) (any, error) { return c.E.StatusInfo(c.Ctx), nil }

func mLogin(c *Call) (any, error) {
	var p api.LoginParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	e := c.E
	if !e.loginMu.TryLock() {
		return nil, api.Errf(api.CodeUsage, "a login is already in progress").WithHint("finish it, or run: wassup daemon restart")
	}
	defer e.loginMu.Unlock()
	cl := e.Client()
	if cl.Store.ID != nil {
		return nil, api.Errf(api.CodeAlreadyLoggedIn, "already linked as +%s", cl.Store.ID.User).
			WithHint("run wassup logout first, or add a second account with --account NAME")
	}
	phone := ""
	if p.Phone != "" {
		d, err := normalizePhone(p.Phone, e.Config().Send.DefaultCountryCode)
		if err != nil {
			return nil, err
		}
		phone = d
	}
	cl.Disconnect()
	e.setState(StatePairing, "")
	paired := false
	defer func() {
		if !paired {
			e.Client().Disconnect()
			e.setState(StateLoggedOut, "")
		}
	}()
	qrCtx, cancel := context.WithCancel(c.Ctx)
	defer cancel()
	ch, err := cl.GetQRChannel(qrCtx)
	if err != nil {
		return nil, err
	}
	if err := cl.ConnectContext(e.ctx); err != nil {
		return nil, api.Errf(api.CodeNotConnected, "couldn't reach WhatsApp: %v", err)
	}
	codeSent := false
	for item := range ch {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			if phone == "" {
				_ = c.Emit(map[string]any{"type": "qr", "code": item.Code, "expires_in": int(item.Timeout.Seconds())})
			} else if !codeSent {
				code, err := cl.PairPhone(c.Ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
				if err != nil {
					return nil, api.Errf(api.CodeWhatsApp, "pairing code request failed: %v", err).WithHint("check the number, or use QR login without --phone")
				}
				codeSent = true
				_ = c.Emit(map[string]any{"type": "pair_code", "code": code, "phone": "+" + phone})
			}
		case whatsmeow.QRChannelSuccess.Event:
			paired = true
			// The server drops the socket right after pairing; whatsmeow reconnects on its own.
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) && !(cl.IsConnected() && cl.IsLoggedIn()) {
				time.Sleep(200 * time.Millisecond)
			}
			st := e.StatusInfo(c.Ctx)
			return map[string]any{"linked": true, "jid": st.JID, "phone": st.Phone, "push_name": st.PushName, "connected": st.Connected,
				"note": "your phone is now sending chat history; it keeps arriving in the background"}, nil
		case whatsmeow.QRChannelTimeout.Event:
			return nil, api.Errf(api.CodeTimeout, "the code expired before it was used").WithHint("run wassup login again")
		case whatsmeow.QRChannelClientOutdated.Event:
			e.refreshWAVersion()
			return nil, api.Errf(api.CodeWhatsApp, "WhatsApp said this client version is outdated; updated it, please run wassup login again")
		case whatsmeow.QRChannelEventError:
			return nil, api.Errf(api.CodeWhatsApp, "pairing failed: %v", item.Error)
		default:
			return nil, api.Errf(api.CodeWhatsApp, "pairing failed: %s", item.Event)
		}
	}
	return nil, api.Errf(api.CodeTimeout, "login cancelled")
}

func mLogout(c *Call) (any, error) {
	dry, err := c.Authorize(Action{Destructive: true, Summary: "unlink this device"})
	if err != nil {
		return nil, err
	}
	if dry {
		return api.DryRun{DryRun: true, Action: c.Method.Name, Summary: "unlink this device from the phone"}, nil
	}
	cl := c.E.Client()
	if cl.Store.ID == nil {
		return nil, api.Errf(api.CodeNotLoggedIn, "not logged in")
	}
	own := cl.Store.ID.ToNonAD().String()
	if err := cl.Logout(c.Ctx); err != nil {
		// Unlinking from the server failed (offline?): still drop the local session.
		_ = cl.Store.Delete(c.Ctx)
	}
	c.E.resetDevice()
	return map[string]any{"logged_out": own}, nil
}

func mResolve(c *Call) (any, error) {
	var p api.ResolveParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	_, r, err := c.E.resolve(c.Ctx, p.Query)
	return r, err
}

func (e *Engine) contactFrom(j types.JID, info types.ContactInfo) api.Contact {
	ct := api.Contact{JID: j.String(), FirstName: info.FirstName, PushName: info.PushName, BusinessName: info.BusinessName}
	if j.Server == types.DefaultUserServer {
		ct.Phone = "+" + j.User
	}
	for _, n := range []string{info.FullName, info.FirstName, info.BusinessName, info.PushName} {
		if n != "" {
			ct.Name = n
			break
		}
	}
	return ct
}

func mContacts(c *Call) (any, error) {
	var p api.ListContactsParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	all, err := c.E.allContacts(c.Ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	type scored struct {
		c api.Contact
		s int
	}
	var list []scored
	q := strings.TrimSpace(p.Query)
	qDigits := nonDigit.ReplaceAllString(q, "")
	for j, info := range all {
		cj := c.E.canonicalUser(c.Ctx, j, types.EmptyJID)
		if seen[cj.String()] {
			continue
		}
		ct := c.E.contactFrom(cj, info)
		if ct.Name == "" && q == "" {
			continue
		}
		s := 1
		if q != "" {
			s = 0
			for _, n := range []string{info.FullName, info.FirstName, info.BusinessName, info.PushName} {
				s = max(s, nameScore(q, n))
			}
			if len(qDigits) >= 4 && strings.Contains(cj.User, qDigits) {
				s = max(s, 40)
			}
			if s == 0 {
				continue
			}
		}
		seen[cj.String()] = true
		list = append(list, scored{ct, s})
	}
	sort.Slice(list, func(i, k int) bool {
		if list[i].s != list[k].s {
			return list[i].s > list[k].s
		}
		return strings.ToLower(list[i].c.Name) < strings.ToLower(list[k].c.Name)
	})
	out := []api.Contact{}
	for i, s := range list {
		if i >= limit {
			break
		}
		out = append(out, s.c)
	}
	return out, nil
}

func mCheck(c *Call) (any, error) {
	var p api.CheckParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	var phones []string
	var queries []string
	for _, raw := range p.Phones {
		for _, ph := range splitList(raw) {
			d, err := normalizePhone(ph, c.E.Config().Send.DefaultCountryCode)
			if err != nil {
				return nil, err
			}
			phones = append(phones, "+"+d)
			queries = append(queries, ph)
		}
	}
	if len(phones) == 0 {
		return nil, usage("give at least one phone number")
	}
	resp, err := c.E.Client().IsOnWhatsApp(c.Ctx, phones)
	if err != nil {
		return nil, err
	}
	byQuery := map[string]types.IsOnWhatsAppResponse{}
	for _, r := range resp {
		byQuery[nonDigit.ReplaceAllString(r.Query, "")] = r
	}
	out := make([]api.CheckResult, 0, len(phones))
	for i, ph := range phones {
		r, ok := byQuery[nonDigit.ReplaceAllString(ph, "")]
		cr := api.CheckResult{Query: queries[i]}
		if ok {
			cr.OnWhatsApp = r.IsIn
			if r.IsIn {
				cr.JID = c.E.canonicalUser(c.Ctx, r.JID, types.EmptyJID).String()
			}
			if r.VerifiedName != nil && r.VerifiedName.Details != nil {
				cr.BusinessName = r.VerifiedName.Details.GetVerifiedName()
			}
		}
		out = append(out, cr)
	}
	return out, nil
}

func (e *Engine) isBlocked(ctx context.Context, j types.JID) bool {
	bl, err := e.Client().GetBlocklist(ctx, "")
	if err != nil || bl == nil {
		return false
	}
	for _, it := range bl.Items {
		if it.PN.User == j.User || it.LID.User == j.User {
			return true
		}
	}
	return false
}

func mUserInfo(c *Call) (any, error) {
	var p api.UserRef
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveUser(c.Ctx, p.User)
	if err != nil {
		return nil, err
	}
	cl := c.E.Client()
	info, err := cl.GetUserInfo(c.Ctx, []types.JID{j})
	if err != nil {
		return nil, err
	}
	out := api.UserInfo{JID: j.String(), Phone: "+" + j.User, Name: c.E.displayName(c.Ctx, j)}
	if ct, err := cl.Store.Contacts.GetContact(c.Ctx, j); err == nil {
		out.PushName = ct.PushName
	}
	for k, ui := range info {
		if k.User != j.User && len(info) > 1 {
			continue
		}
		out.About, out.PictureID, out.Devices = ui.Status, ui.PictureID, len(ui.Devices)
		if !ui.LID.IsEmpty() {
			out.LID = ui.LID.String()
		}
		if ui.VerifiedName != nil {
			out.Business = true
			if ui.VerifiedName.Details != nil {
				out.BusinessName = ui.VerifiedName.Details.GetVerifiedName()
			}
		}
	}
	if pic, err := cl.GetProfilePictureInfo(c.Ctx, j, &whatsmeow.GetProfilePictureParams{}); err == nil && pic != nil {
		out.PictureURL = pic.URL
	}
	if out.Business {
		if bp, err := cl.GetBusinessProfile(c.Ctx, j); err == nil && bp != nil {
			out.Email, out.Address = bp.Email, bp.Address
			for k, v := range bp.ProfileOptions {
				if strings.Contains(k, "website") {
					out.Website = append(out.Website, v)
				}
			}
		}
	}
	out.Blocked = c.E.isBlocked(c.Ctx, j)
	return out, nil
}

func mAvatar(c *Call) (any, error) {
	var p api.AvatarParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.Out != "" && c.agentSourced() {
		return nil, api.Errf(api.CodePolicyDenied, "agents can't choose where files are written")
	}
	j, _, err := c.E.resolve(c.Ctx, p.User)
	if err != nil {
		return nil, err
	}
	params := &whatsmeow.GetProfilePictureParams{Preview: p.Preview, IsCommunity: false}
	pic, err := c.E.Client().GetProfilePictureInfo(c.Ctx, j, params)
	if err != nil {
		return nil, err
	}
	if pic == nil || pic.URL == "" {
		return nil, notFound("%s has no profile picture", c.E.displayName(c.Ctx, j))
	}
	req, _ := http.NewRequestWithContext(c.Ctx, http.MethodGet, pic.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	out := p.Out
	if out == "" {
		out = filepath.Join(c.E.Acct.MediaDir, "avatars", j.User+".jpg")
	}
	out = expandPath(out)
	if st, err := os.Stat(out); err == nil && st.IsDir() {
		out = filepath.Join(out, j.User+".jpg")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		return nil, err
	}
	abs, _ := filepath.Abs(out)
	return api.DownloadResult{Path: abs, Mime: "image/jpeg", Size: int64(len(data))}, nil
}

func mBlock(c *Call) (any, error) {
	var p api.BlockParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	j, err := c.E.resolveUser(c.Ctx, p.User)
	if err != nil {
		return nil, err
	}
	verb := "block"
	action := events.BlocklistChangeActionBlock
	if p.Unblock {
		verb, action = "unblock", events.BlocklistChangeActionUnblock
	}
	dry, err := c.Authorize(Action{Targets: []types.JID{j}, Summary: verb + " " + c.E.displayName(c.Ctx, j)})
	if err != nil || dry {
		return dryResult(c, dry, err, j, verb+" "+c.E.displayName(c.Ctx, j))
	}
	cur, _ := c.E.Client().GetBlocklist(c.Ctx, "")
	dhash := ""
	if cur != nil {
		dhash = cur.DHash
	}
	if _, err := c.E.Client().UpdateBlocklist(c.Ctx, j, action, dhash); err != nil {
		return nil, err
	}
	return map[string]any{"jid": j.String(), "blocked": !p.Unblock}, nil
}

func mBlocklist(c *Call) (any, error) {
	bl, err := c.E.Client().GetBlocklist(c.Ctx, "")
	if err != nil {
		return nil, err
	}
	out := []api.Contact{}
	if bl == nil {
		return out, nil
	}
	for _, it := range bl.Items {
		j := it.PN
		if j.IsEmpty() {
			j = c.E.canonicalUser(c.Ctx, it.LID, types.EmptyJID)
		}
		info, _ := c.E.Client().Store.Contacts.GetContact(c.Ctx, j)
		out = append(out, c.E.contactFrom(j, info))
	}
	return out, nil
}

func mProfile(c *Call) (any, error) {
	cl := c.E.Client()
	own := c.E.OwnJID()
	out := api.Profile{JID: own.String(), Phone: "+" + own.User, Name: cl.Store.PushName, Business: cl.Store.BusinessName, Platform: cl.Store.Platform}
	if !cl.Store.LID.IsEmpty() {
		out.LID = cl.Store.LID.ToNonAD().String()
	}
	if info, err := cl.GetUserInfo(c.Ctx, []types.JID{own}); err == nil {
		for _, ui := range info {
			out.About = ui.Status
		}
	}
	if pic, err := cl.GetProfilePictureInfo(c.Ctx, own, &whatsmeow.GetProfilePictureParams{}); err == nil && pic != nil {
		out.PictureURL = pic.URL
	}
	return out, nil
}

func mProfileUpdate(c *Call) (any, error) {
	var p api.ProfileUpdateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	var changes []string
	if p.Name != "" {
		changes = append(changes, "name")
	}
	if p.About != nil {
		changes = append(changes, "about")
	}
	if p.Photo != "" || p.RemovePhoto {
		changes = append(changes, "photo")
	}
	if len(changes) == 0 {
		return nil, usage("nothing to change: set name, about or photo")
	}
	summary := "update profile " + strings.Join(changes, ", ")
	own := c.E.OwnJID()
	dry, err := c.Authorize(Action{Summary: summary})
	if err != nil || dry {
		return dryResult(c, dry, err, own, summary)
	}
	cl := c.E.Client()
	if p.Name != "" {
		if err := cl.SendAppState(c.Ctx, appstate.BuildSettingPushName(p.Name)); err != nil {
			return nil, err
		}
		cl.Store.PushName = p.Name
		_ = cl.Store.Save(c.Ctx)
	}
	if p.About != nil {
		about := *p.About
		if err := cl.SetStatusMessage(c.Ctx, types.SetStatusInput{Text: &about}); err != nil {
			return nil, err
		}
	}
	if p.Photo != "" || p.RemovePhoto {
		var data []byte
		if p.Photo != "" {
			if err := c.checkReadable(p.Photo); err != nil {
				return nil, err
			}
			if data, err = squareJPEG(c.Ctx, p.Photo); err != nil {
				return nil, err
			}
		}
		if _, err := cl.SetGroupPhoto(c.Ctx, types.EmptyJID, data); err != nil {
			return nil, err
		}
	}
	return map[string]any{"updated": changes}, nil
}

func privacyStruct(s types.PrivacySettings) map[string]string {
	return map[string]string{
		"groupadd": string(s.GroupAdd), "last": string(s.LastSeen), "status": string(s.Status), "profile": string(s.Profile),
		"readreceipts": string(s.ReadReceipts), "calladd": string(s.CallAdd), "online": string(s.Online),
		"messages": string(s.Messages), "defense": string(s.Defense), "stickers": string(s.Stickers),
	}
}

func mPrivacy(c *Call) (any, error) {
	s, err := c.E.Client().TryFetchPrivacySettings(c.Ctx, true)
	if err != nil {
		return nil, err
	}
	return privacyStruct(*s), nil
}

func mPrivacySet(c *Call) (any, error) {
	var p api.PrivacySetParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	aliases := map[string]string{"last_seen": "last", "lastseen": "last", "read_receipts": "readreceipts", "group_add": "groupadd", "groups": "groupadd", "photo": "profile", "calls": "calladd", "call_add": "calladd"}
	setting := strings.ToLower(p.Setting)
	if a, ok := aliases[setting]; ok {
		setting = a
	}
	valid := map[string]bool{"groupadd": true, "last": true, "status": true, "profile": true, "readreceipts": true, "online": true, "calladd": true, "messages": true, "defense": true, "stickers": true}
	if !valid[setting] {
		return nil, usage("unknown privacy setting %q", p.Setting)
	}
	val := strings.ToLower(p.Value)
	if val == "everyone" {
		val = "all"
	} else if val == "nobody" {
		val = "none"
	}
	dry, err := c.Authorize(Action{Summary: fmt.Sprintf("set privacy %s=%s", setting, val)})
	if err != nil || dry {
		return dryResult(c, dry, err, c.E.OwnJID(), fmt.Sprintf("set privacy %s=%s", setting, val))
	}
	s, err := c.E.Client().SetPrivacySetting(c.Ctx, types.PrivacySettingType(setting), types.PrivacySetting(val))
	if err != nil {
		return nil, err
	}
	return privacyStruct(s), nil
}
