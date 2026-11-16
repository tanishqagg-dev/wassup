package engine

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

func parseDur(s string) (time.Duration, error) { return config.ParseDuration(s) }

var (
	nonDigit     = regexp.MustCompile(`\D`)
	phoneLike    = regexp.MustCompile(`^\+?[\d\s\-().]{6,}$`)
	inviteLinkRe = regexp.MustCompile(`(?:chat\.whatsapp\.com/(?:invite/)?)([A-Za-z0-9]{10,})`)
	channelRe    = regexp.MustCompile(`whatsapp\.com/channel/([A-Za-z0-9]+)`)
)

func chatKind(j types.JID) string {
	switch j.Server {
	case types.GroupServer:
		return "group"
	case types.NewsletterServer:
		return "channel"
	case types.BroadcastServer:
		if j.User == types.StatusBroadcastJID.User {
			return "status"
		}
		return "broadcast"
	default:
		return "dm"
	}
}

// normalizePhone returns digits with country code, or an error explaining what's missing.
func normalizePhone(s, defaultCC string) (string, error) {
	s = strings.TrimSpace(s)
	plus := strings.HasPrefix(s, "+")
	d := nonDigit.ReplaceAllString(s, "")
	if strings.HasPrefix(d, "00") {
		d, plus = d[2:], true
	}
	// International numbers never start with 0, so a leading 0 is a local trunk prefix.
	if !plus && (len(d) <= 10 || strings.HasPrefix(d, "0")) {
		if defaultCC == "" {
			return "", api.Errf(api.CodeUsage, "%q looks like a phone number without a country code", s).
				WithHint("write it as +<country code><number>, or set send.default_country_code in wassup config")
		}
		d = nonDigit.ReplaceAllString(defaultCC, "") + strings.TrimLeft(d, "0")
	}
	if len(d) < 8 || len(d) > 15 {
		return "", api.Errf(api.CodeUsage, "%q isn't a valid international phone number", s)
	}
	return d, nil
}

// canonicalUser maps LID and legacy JIDs to the phone-number JID when we know it.
func (e *Engine) canonicalUser(ctx context.Context, jid, alt types.JID) types.JID {
	jid = jid.ToNonAD()
	switch jid.Server {
	case types.LegacyUserServer:
		jid.Server = types.DefaultUserServer
	case types.HiddenUserServer:
		if !alt.IsEmpty() && alt.Server == types.DefaultUserServer {
			return alt.ToNonAD()
		}
		if lids := e.Client().Store.LIDs; lids == nil {
			return jid
		} else if pn, err := lids.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD()
		}
	}
	return jid
}

// canonicalChat picks the stable chat JID for a message (phone-number form for DMs when known).
func (e *Engine) canonicalChat(ctx context.Context, src types.MessageSource) types.JID {
	chat := src.Chat.ToNonAD()
	if chat.Server == types.HiddenUserServer {
		alt := src.SenderAlt
		if src.IsFromMe {
			alt = src.RecipientAlt
		}
		return e.canonicalUser(ctx, chat, alt)
	}
	if chat.Server == types.LegacyUserServer {
		chat.Server = types.DefaultUserServer
	}
	return chat
}

// resolve turns user input into a JID. It may ask WhatsApp whether a raw phone number exists.
func (e *Engine) resolve(ctx context.Context, query string) (types.JID, *api.Resolved, error) {
	j, r, err := e.resolveLocal(ctx, query)
	if err != nil {
		return j, r, err
	}
	if r.Via == "phone" && e.Config().Send.VerifyNumbers && e.Client().IsConnected() {
		resp, err := e.Client().IsOnWhatsApp(ctx, []string{"+" + j.User})
		if err == nil && len(resp) > 0 {
			if !resp[0].IsIn {
				return j, r, api.Errf(api.CodeNotOnWhatsApp, "+%s isn't on WhatsApp", j.User).
					WithHint("check the number and country code")
			}
			if !resp[0].JID.IsEmpty() {
				j = e.canonicalUser(ctx, resp[0].JID, types.EmptyJID)
				r.JID = j.String()
			}
		}
	}
	return j, r, nil
}

// resolveOffline is resolve without any network call; used by policy and webhook matching.
func (e *Engine) resolveOffline(ctx context.Context, query string) (types.JID, error) {
	j, _, err := e.resolveLocal(ctx, query)
	return j, err
}

func (e *Engine) resolveLocal(ctx context.Context, query string) (types.JID, *api.Resolved, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return types.EmptyJID, nil, usage("recipient is empty")
	}
	res := &api.Resolved{Query: q}
	switch strings.ToLower(q) {
	case "me", "self", "myself", "saved", "note-to-self":
		own := e.OwnJID()
		if own.IsEmpty() {
			return own, nil, api.Errf(api.CodeNotLoggedIn, "not logged in").WithHint("run: wassup login")
		}
		res.JID, res.Kind, res.Via, res.Name, res.Phone = own.String(), "dm", "self", "me", "+"+own.User
		return own, res, nil
	case "status", "status@broadcast":
		res.JID, res.Kind, res.Via, res.Name = types.StatusBroadcastJID.String(), "status", "jid", "Status"
		return types.StatusBroadcastJID, res, nil
	}
	if strings.Contains(q, "@") && !strings.Contains(q, " ") {
		j, err := types.ParseJID(strings.TrimPrefix(q, "@"))
		if err == nil && j.Server != "" && j.User != "" {
			if j.Server == types.HiddenUserServer || j.Server == types.LegacyUserServer || j.Server == types.DefaultUserServer {
				j = e.canonicalUser(ctx, j, types.EmptyJID)
			}
			res.JID, res.Kind, res.Via = j.String(), chatKind(j), "jid"
			res.Name = e.displayName(ctx, j)
			if j.Server == types.DefaultUserServer {
				res.Phone = "+" + j.User
			}
			return j, res, nil
		}
		if !strings.HasPrefix(q, "@") {
			return types.EmptyJID, nil, usage("can't parse JID %q", q)
		}
	}
	if m := channelRe.FindStringSubmatch(q); m != nil {
		return types.EmptyJID, nil, usage("that's a channel invite link; use: wassup channels info %s", q)
	}
	if phoneLike.MatchString(q) {
		digits, err := normalizePhone(q, e.Config().Send.DefaultCountryCode)
		if err != nil {
			return types.EmptyJID, nil, err
		}
		j := types.NewJID(digits, types.DefaultUserServer)
		res.JID, res.Kind, res.Via, res.Phone = j.String(), "dm", "phone", "+"+digits
		res.Name = e.displayName(ctx, j)
		return j, res, nil
	}
	cands, err := e.searchNames(ctx, q, 20)
	if err != nil {
		return types.EmptyJID, nil, err
	}
	if len(cands) == 0 {
		return types.EmptyJID, nil, notFound("no contact, group or chat matches %q", q).
			WithHint("try a phone number with country code, or list names with: wassup contacts / wassup chats")
	}
	top := cands[0].score
	var best []nameCandidate
	for _, c := range cands {
		if c.score == top {
			best = append(best, c)
		}
	}
	if len(best) > 1 {
		list := make([]api.Candidate, 0, len(best))
		for _, c := range best {
			list = append(list, api.Candidate{JID: c.jid.String(), Name: c.name, Kind: chatKind(c.jid)})
		}
		return types.EmptyJID, nil, api.Errf(api.CodeAmbiguous, "%q matches %d chats", q, len(best)).
			WithHint("use one of the JIDs listed in details").WithDetails(list)
	}
	c := best[0]
	res.JID, res.Kind, res.Name = c.jid.String(), chatKind(c.jid), c.name
	res.Via = c.via
	if c.jid.Server == types.DefaultUserServer {
		res.Phone = "+" + c.jid.User
	}
	return c.jid, res, nil
}

type nameCandidate struct {
	jid    types.JID
	name   string
	via    string
	score  int
	recent int64
}

func normName(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func nameScore(query, name string) int {
	n, q := normName(name), normName(query)
	if n == "" || q == "" {
		return 0
	}
	switch {
	case n == q:
		return 100
	case strings.HasPrefix(n, q):
		return 60
	}
	for _, w := range strings.Fields(n) {
		if strings.HasPrefix(w, q) {
			return 50
		}
	}
	if strings.Contains(n, q) {
		return 30
	}
	return 0
}

// searchNames scores contacts and chats against a name query, best first.
func (e *Engine) searchNames(ctx context.Context, query string, limit int) ([]nameCandidate, error) {
	byJID := map[types.JID]*nameCandidate{}
	consider := func(j types.JID, name, via string, recent int64) {
		s := nameScore(query, name)
		if s == 0 {
			return
		}
		cur := byJID[j]
		if cur == nil || s > cur.score {
			byJID[j] = &nameCandidate{jid: j, name: name, via: via, score: s, recent: recent}
		} else if cur != nil && recent > cur.recent {
			cur.recent = recent
		}
	}
	chats, err := e.DB.AllChatNames(ctx)
	if err != nil {
		return nil, err
	}
	contacts, err := e.allContacts(ctx)
	if err != nil {
		return nil, err
	}
	for j, info := range contacts {
		cj := e.canonicalUser(ctx, j, types.EmptyJID)
		var recent int64
		if c, ok := chats[cj.String()]; ok {
			recent = c.LastMessageAt.Unix()
		}
		for _, n := range []string{info.FullName, info.FirstName, info.BusinessName, info.PushName} {
			consider(cj, n, "contact", recent)
		}
	}
	for js, c := range chats {
		j, err := types.ParseJID(js)
		if err != nil || c.Name == "" {
			continue
		}
		consider(j, c.Name, "chat", c.LastMessageAt.Unix())
	}
	out := make([]nameCandidate, 0, len(byJID))
	for _, c := range byJID {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].score != out[k].score {
			return out[i].score > out[k].score
		}
		return out[i].recent > out[k].recent
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// resolveUser resolves to a person (not a group or channel).
func (e *Engine) resolveUser(ctx context.Context, q string) (types.JID, error) {
	j, _, err := e.resolve(ctx, q)
	if err != nil {
		return j, err
	}
	if j.Server != types.DefaultUserServer && j.Server != types.HiddenUserServer {
		return j, usage("%q is a %s, not a person", q, chatKind(j))
	}
	return j, nil
}

func (e *Engine) resolveUsers(ctx context.Context, qs []string) ([]types.JID, error) {
	out := make([]types.JID, 0, len(qs))
	for _, q := range qs {
		for _, part := range splitList(q) {
			j, err := e.resolveUser(ctx, part)
			if err != nil {
				return nil, err
			}
			out = append(out, j)
		}
	}
	return out, nil
}

// splitList lets CLI users pass "a,b,c" where a list is expected.
func splitList(s string) []string {
	if !strings.Contains(s, ",") || strings.Contains(s, "@g.us") {
		return []string{s}
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveGroup resolves a group by name, JID or invite link.
func (e *Engine) resolveGroup(ctx context.Context, q string) (types.JID, error) {
	if m := inviteLinkRe.FindStringSubmatch(q); m != nil {
		if err := e.WaitConnected(ctx); err != nil {
			return types.EmptyJID, err
		}
		info, err := e.Client().GetGroupInfoFromLink(ctx, m[1])
		if err != nil {
			return types.EmptyJID, err
		}
		return info.JID, nil
	}
	j, _, err := e.resolveLocal(ctx, q)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Code == api.CodeNotFound {
			// Group names may not be stored yet; refresh from the server once and retry.
			if e.refreshGroups(ctx, false) == nil {
				j, _, err = e.resolveLocal(ctx, q)
			}
		}
		if err != nil {
			return j, err
		}
	}
	if j.Server != types.GroupServer {
		return j, usage("%q is a %s, not a group", q, chatKind(j))
	}
	return j, nil
}

// displayName is the best human name for a JID without network calls.
func (e *Engine) displayName(ctx context.Context, j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	if j.Server == types.DefaultUserServer || j.Server == types.HiddenUserServer {
		if own := e.OwnJID(); !own.IsEmpty() && j.User == own.User {
			return "me"
		}
		if cs := e.Client().Store.Contacts; cs == nil {
			// Not linked yet: no contact store.
		} else if info, err := cs.GetContact(ctx, j); err == nil && info.Found {
			for _, n := range []string{info.FullName, info.FirstName, info.BusinessName, info.PushName} {
				if n != "" {
					return n
				}
			}
		}
		if c, _ := e.DB.GetChat(ctx, j.String()); c != nil && c.Name != "" {
			return c.Name
		}
		if j.Server == types.DefaultUserServer {
			return "+" + j.User
		}
		return j.User
	}
	if c, _ := e.DB.GetChat(ctx, j.String()); c != nil && c.Name != "" {
		return c.Name
	}
	if j == types.StatusBroadcastJID {
		return "Status"
	}
	return j.User
}

// findMessage looks a message up in the local store, resolving the optional chat first.
func (e *Engine) findMessage(ctx context.Context, chatQuery, id string) (*store.Message, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, usage("message ID is empty")
	}
	chat := ""
	if chatQuery != "" {
		j, _, err := e.resolveLocal(ctx, chatQuery)
		if err != nil {
			return nil, err
		}
		chat = j.String()
	}
	m, err := e.DB.GetMessage(ctx, chat, id)
	if errors.Is(err, store.ErrAmbiguousID) {
		return nil, api.Errf(api.CodeAmbiguous, "message %s exists in several chats", id).WithHint("pass the chat too")
	} else if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, notFound("message %s isn't in the local store", id).
			WithHint("message IDs come from wassup messages <chat>; older messages can be fetched with wassup history <chat>")
	}
	return m, nil
}

// allContacts returns the synced address book, or nothing before the account is linked.
func (e *Engine) allContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	cs := e.Client().Store.Contacts
	if cs == nil {
		return map[types.JID]types.ContactInfo{}, nil
	}
	return cs.GetAllContacts(ctx)
}
