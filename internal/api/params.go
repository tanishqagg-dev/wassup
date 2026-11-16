package api

import "encoding/json"

// Parameter structs for every method. Fields without omitempty are required.
// The jsonschema tags become tool descriptions for MCP clients and `wassup schema`.

type Empty struct{}

type LoginParams struct {
	Phone string `json:"phone,omitempty" jsonschema:"Phone number with country code. Set this to link with an 8-character pairing code instead of a QR code"`
}

// ----- sending -----

type SendParams struct {
	To          string         `json:"to" jsonschema:"Recipient: phone number with country code, contact or group name, JID, or 'me' for your own chat"`
	Text        string         `json:"text,omitempty" jsonschema:"Message text, or the caption when a file is attached"`
	File        string         `json:"file,omitempty" jsonschema:"Absolute path of a file to attach"`
	As          string         `json:"as,omitempty" jsonschema:"How to send the file: auto (default), image, video, gif, audio, voice, document or sticker"`
	FileName    string         `json:"file_name,omitempty" jsonschema:"File name shown for documents"`
	ReplyTo     string         `json:"reply_to,omitempty" jsonschema:"Message ID to quote and reply to"`
	Mentions    []string       `json:"mentions,omitempty" jsonschema:"People to @mention (phones, names or JIDs). @<number> in the text is detected too"`
	MentionAll  bool           `json:"mention_all,omitempty" jsonschema:"Notify every group member without adding names to the text"`
	ViewOnce    bool           `json:"view_once,omitempty" jsonschema:"Send image, video or voice as view once"`
	NoPreview   bool           `json:"no_preview,omitempty" jsonschema:"Skip the link preview for URLs in the text"`
	Location    *LocationInput `json:"location,omitempty" jsonschema:"Send a map pin instead of text"`
	ContactCard *ContactInput  `json:"contact,omitempty" jsonschema:"Send a contact card"`
	Typing      bool           `json:"typing,omitempty" jsonschema:"Show typing... briefly before sending"`
}

type LocationInput struct {
	Lat     float64 `json:"lat" jsonschema:"Latitude"`
	Lng     float64 `json:"lng" jsonschema:"Longitude"`
	Name    string  `json:"name,omitempty" jsonschema:"Place name"`
	Address string  `json:"address,omitempty" jsonschema:"Street address"`
}

type ContactInput struct {
	Name  string `json:"name" jsonschema:"Display name on the card"`
	Phone string `json:"phone,omitempty" jsonschema:"Phone number with country code"`
	VCard string `json:"vcard,omitempty" jsonschema:"Raw vCard text; overrides name/phone"`
}

type SendManyParams struct {
	To    []string `json:"to" jsonschema:"Recipients, same formats as send"`
	Text  string   `json:"text,omitempty" jsonschema:"Message text or caption"`
	File  string   `json:"file,omitempty" jsonschema:"Absolute path of a file to attach"`
	As    string   `json:"as,omitempty" jsonschema:"auto, image, video, audio, voice, document or sticker"`
	Delay string   `json:"delay,omitempty" jsonschema:"Pause between recipients, e.g. 3s (default 2s plus jitter)"`
}

type SendManyResult struct {
	Sent   []SendResult  `json:"sent"`
	Failed []SendFailure `json:"failed,omitempty"`
}

type SendFailure struct {
	To    string `json:"to"`
	Error *Error `json:"error"`
}

type PollCreateParams struct {
	To         string   `json:"to" jsonschema:"Recipient chat"`
	Question   string   `json:"question" jsonschema:"Poll question"`
	Options    []string `json:"options" jsonschema:"2 to 12 answer options"`
	Selectable int      `json:"selectable,omitempty" jsonschema:"How many options a voter may pick (default 1; 0 or more than options = any)"`
}

type PollVoteParams struct {
	Chat    string   `json:"chat,omitempty" jsonschema:"Chat containing the poll (optional if the ID is unique locally)"`
	ID      string   `json:"id" jsonschema:"Poll message ID"`
	Options []string `json:"options" jsonschema:"Option names to vote for; empty list clears your vote"`
}

// MessageRef addresses one stored message.
type MessageRef struct {
	Chat string `json:"chat,omitempty" jsonschema:"Chat of the message (optional if the ID is unique locally)"`
	ID   string `json:"id" jsonschema:"Message ID"`
}

type ReactParams struct {
	Chat  string `json:"chat,omitempty" jsonschema:"Chat of the message (optional)"`
	ID    string `json:"id" jsonschema:"Message ID to react to"`
	Emoji string `json:"emoji,omitempty" jsonschema:"Emoji; empty removes your reaction"`
}

type EditParams struct {
	Chat string `json:"chat,omitempty" jsonschema:"Chat of the message (optional)"`
	ID   string `json:"id" jsonschema:"ID of your own message to edit (within 15 minutes of sending)"`
	Text string `json:"text" jsonschema:"Replacement text"`
}

type DeleteParams struct {
	Chat      string `json:"chat,omitempty" jsonschema:"Chat of the message (optional)"`
	ID        string `json:"id" jsonschema:"Message ID"`
	LocalOnly bool   `json:"local_only,omitempty" jsonschema:"Only remove from the local store, nothing is sent"`
}

type ForwardParams struct {
	Chat string `json:"chat,omitempty" jsonschema:"Chat of the original message (optional)"`
	ID   string `json:"id" jsonschema:"Message ID to forward"`
	To   string `json:"to" jsonschema:"Destination chat"`
}

type PinParams struct {
	Chat     string `json:"chat,omitempty" jsonschema:"Chat of the message (optional)"`
	ID       string `json:"id" jsonschema:"Message ID"`
	Unpin    bool   `json:"unpin,omitempty" jsonschema:"Unpin instead of pin"`
	Duration string `json:"duration,omitempty" jsonschema:"24h, 7d or 30d (default 7d)"`
}

type StarParams struct {
	Chat   string `json:"chat,omitempty" jsonschema:"Chat of the message (optional)"`
	ID     string `json:"id" jsonschema:"Message ID"`
	Unstar bool   `json:"unstar,omitempty" jsonschema:"Remove the star"`
}

type MarkReadParams struct {
	Chat string   `json:"chat" jsonschema:"Chat to mark as read"`
	IDs  []string `json:"ids,omitempty" jsonschema:"Specific message IDs; default is every unread message"`
}

type DownloadParams struct {
	Chat string `json:"chat,omitempty" jsonschema:"Chat of the message (optional)"`
	ID   string `json:"id" jsonschema:"Message ID with media"`
	Out  string `json:"out,omitempty" jsonschema:"Output file or folder; default is the account media folder"`
}

type DownloadResult struct {
	Path string `json:"path"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// ----- reading -----

type ListMessagesParams struct {
	Chat   string `json:"chat,omitempty" jsonschema:"Chat to read; empty = all chats"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Max messages (default 30, max 1000)"`
	Before string `json:"before,omitempty" jsonschema:"Only messages before this time (RFC3339, 2h, 3d) or message ID"`
	After  string `json:"after,omitempty" jsonschema:"Only messages after this time (RFC3339, 2h, 3d) or message ID"`
	From   string `json:"from,omitempty" jsonschema:"Only messages from this sender"`
	FromMe *bool  `json:"from_me,omitempty" jsonschema:"true = only yours, false = only others'"`
	Type   string `json:"type,omitempty" jsonschema:"text, image, video, audio, voice, document, sticker, location, contact, poll"`
	Unread bool   `json:"unread,omitempty" jsonschema:"Only messages received after the chat was last read"`
	Order  string `json:"order,omitempty" jsonschema:"asc (oldest first, default) or desc"`
}

type SearchParams struct {
	Query  string `json:"query" jsonschema:"Words to find; supports \"exact phrases\" and prefix*"`
	Chat   string `json:"chat,omitempty" jsonschema:"Limit to one chat"`
	From   string `json:"from,omitempty" jsonschema:"Limit to one sender"`
	Type   string `json:"type,omitempty" jsonschema:"Limit to a message type"`
	After  string `json:"after,omitempty" jsonschema:"Only after this time (RFC3339, 2h, 3d)"`
	Before string `json:"before,omitempty" jsonschema:"Only before this time"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Max results (default 30)"`
}

type WaitParams struct {
	Chat          string `json:"chat,omitempty" jsonschema:"Only messages in this chat"`
	From          string `json:"from,omitempty" jsonschema:"Only messages from this sender"`
	Match         string `json:"match,omitempty" jsonschema:"Regular expression the text must match (case-insensitive)"`
	Timeout       string `json:"timeout,omitempty" jsonschema:"How long to wait, e.g. 90s or 10m (default 5m)"`
	IncludeFromMe bool   `json:"include_from_me,omitempty" jsonschema:"Also match messages you send from other devices"`
	Count         int    `json:"count,omitempty" jsonschema:"Return after this many matching messages (default 1)"`
}

type HistoryParams struct {
	Chat    string `json:"chat" jsonschema:"Chat to backfill"`
	Count   int    `json:"count,omitempty" jsonschema:"Messages to request from the phone (default 50, max 500)"`
	Timeout string `json:"timeout,omitempty" jsonschema:"How long to wait for the phone (default 30s)"`
}

type ListChatsParams struct {
	Query    string `json:"query,omitempty" jsonschema:"Filter by name"`
	Kind     string `json:"kind,omitempty" jsonschema:"dm, group, channel or broadcast"`
	Unread   bool   `json:"unread,omitempty" jsonschema:"Only chats with unread messages"`
	Archived string `json:"archived,omitempty" jsonschema:"exclude (default), include or only"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max chats (default 50)"`
}

type ChatRef struct {
	Chat string `json:"chat" jsonschema:"Chat: name, phone or JID"`
}

type UpdateChatParams struct {
	Chat         string `json:"chat" jsonschema:"Chat to change"`
	Archive      *bool  `json:"archive,omitempty" jsonschema:"Archive (true) or unarchive (false)"`
	Pin          *bool  `json:"pin,omitempty" jsonschema:"Pin (true) or unpin (false)"`
	Mute         string `json:"mute,omitempty" jsonschema:"Mute for a duration like 8h or 7d, 'forever', or 'off' to unmute"`
	Read         *bool  `json:"read,omitempty" jsonschema:"Mark read (true) or unread (false)"`
	Disappearing string `json:"disappearing,omitempty" jsonschema:"Disappearing messages: off, 24h, 7d or 90d"`
}

type TypingParams struct {
	Chat  string `json:"chat" jsonschema:"Chat to show the indicator in"`
	State string `json:"state,omitempty" jsonschema:"typing (default), recording or paused"`
}

type PresenceParams struct {
	Online bool `json:"online" jsonschema:"true = online, false = offline"`
}

type UserRef struct {
	User string `json:"user" jsonschema:"Person: phone, contact name or JID"`
}

type ResolveParams struct {
	Query string `json:"query" jsonschema:"Name, phone number or JID to resolve"`
}

type ListContactsParams struct {
	Query string `json:"query,omitempty" jsonschema:"Filter by name or number"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max results (default 100)"`
}

type CheckParams struct {
	Phones []string `json:"phones" jsonschema:"Phone numbers with country code"`
}

type CheckResult struct {
	Query        string `json:"query"`
	OnWhatsApp   bool   `json:"on_whatsapp"`
	JID          string `json:"jid,omitempty"`
	BusinessName string `json:"business_name,omitempty"`
}

type UserInfo struct {
	JID          string   `json:"jid"`
	Phone        string   `json:"phone,omitempty"`
	LID          string   `json:"lid,omitempty"`
	Name         string   `json:"name,omitempty"`
	PushName     string   `json:"push_name,omitempty"`
	About        string   `json:"about,omitempty"`
	PictureID    string   `json:"picture_id,omitempty"`
	PictureURL   string   `json:"picture_url,omitempty"`
	Devices      int      `json:"devices"`
	Business     bool     `json:"business"`
	BusinessName string   `json:"business_name,omitempty"`
	Blocked      bool     `json:"blocked"`
	Email        string   `json:"email,omitempty"`
	Website      []string `json:"website,omitempty"`
	Address      string   `json:"address,omitempty"`
}

type AvatarParams struct {
	User    string `json:"user" jsonschema:"Person or group"`
	Out     string `json:"out,omitempty" jsonschema:"Output file path"`
	Preview bool   `json:"preview,omitempty" jsonschema:"Small thumbnail instead of full size"`
}

type BlockParams struct {
	User    string `json:"user" jsonschema:"Person to block"`
	Unblock bool   `json:"unblock,omitempty" jsonschema:"Unblock instead"`
}

// ----- groups -----

type GroupRef struct {
	Group string `json:"group" jsonschema:"Group: name, JID or invite link"`
}

type GroupListParams struct {
	Query string `json:"query,omitempty" jsonschema:"Filter by name"`
}

type GroupCreateParams struct {
	Name         string   `json:"name" jsonschema:"Group name (max 100 characters)"`
	Participants []string `json:"participants" jsonschema:"People to add (phones, names or JIDs)"`
	Community    bool     `json:"community,omitempty" jsonschema:"Create a community instead of a group"`
	Parent       string   `json:"parent,omitempty" jsonschema:"Community to create the group inside"`
	Description  string   `json:"description,omitempty" jsonschema:"Group description"`
}

type GroupUpdateParams struct {
	Group        string  `json:"group" jsonschema:"Group to change"`
	Name         string  `json:"name,omitempty" jsonschema:"New name"`
	Description  *string `json:"description,omitempty" jsonschema:"New description; empty string clears it"`
	Announce     *bool   `json:"announce,omitempty" jsonschema:"Only admins can send messages"`
	Locked       *bool   `json:"locked,omitempty" jsonschema:"Only admins can edit group info"`
	Approval     *bool   `json:"approval,omitempty" jsonschema:"Admins must approve new members"`
	MemberAdd    string  `json:"member_add,omitempty" jsonschema:"Who can add members: admins or all"`
	Photo        string  `json:"photo,omitempty" jsonschema:"Path to a new group photo"`
	RemovePhoto  bool    `json:"remove_photo,omitempty" jsonschema:"Remove the group photo"`
	Disappearing string  `json:"disappearing,omitempty" jsonschema:"off, 24h, 7d or 90d"`
}

type GroupParticipantsParams struct {
	Group        string   `json:"group" jsonschema:"Group to change"`
	Action       string   `json:"action" jsonschema:"add, remove, promote or demote"`
	Participants []string `json:"participants" jsonschema:"People (phones, names or JIDs)"`
}

type GroupInviteParams struct {
	Group string `json:"group" jsonschema:"Group"`
	Reset bool   `json:"reset,omitempty" jsonschema:"Revoke the old link and make a new one"`
}

type JoinParams struct {
	Link string `json:"link" jsonschema:"Invite link (chat.whatsapp.com/...) or code"`
}

type GroupRequestsParams struct {
	Group        string   `json:"group" jsonschema:"Group"`
	Action       string   `json:"action,omitempty" jsonschema:"list (default), approve or reject"`
	Participants []string `json:"participants,omitempty" jsonschema:"Who to approve or reject; empty = everyone pending"`
}

type CommunityLinkParams struct {
	Community string `json:"community" jsonschema:"Community"`
	Group     string `json:"group" jsonschema:"Group to link or unlink"`
	Unlink    bool   `json:"unlink,omitempty" jsonschema:"Unlink instead of link"`
}

// ----- profile, privacy, status, channels, calls -----

type ProfileUpdateParams struct {
	Name        string  `json:"name,omitempty" jsonschema:"New display (push) name"`
	About       *string `json:"about,omitempty" jsonschema:"New about text"`
	Photo       string  `json:"photo,omitempty" jsonschema:"Path to a new profile photo"`
	RemovePhoto bool    `json:"remove_photo,omitempty" jsonschema:"Remove the profile photo"`
}

type Profile struct {
	JID        string `json:"jid"`
	Phone      string `json:"phone"`
	LID        string `json:"lid,omitempty"`
	Name       string `json:"name"`
	About      string `json:"about,omitempty"`
	PictureURL string `json:"picture_url,omitempty"`
	Business   string `json:"business_name,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

type PrivacySetParams struct {
	Setting string `json:"setting" jsonschema:"groupadd, last, status, profile, readreceipts, online, calladd, messages, defense or stickers"`
	Value   string `json:"value" jsonschema:"all, contacts, contact_blacklist, none, match_last_seen, known, on_standard or off"`
}

type StatusPostParams struct {
	Text       string `json:"text,omitempty" jsonschema:"Text status, or caption for a media status"`
	File       string `json:"file,omitempty" jsonschema:"Image or video to post"`
	Background string `json:"background,omitempty" jsonschema:"Background color for text statuses, #RRGGBB"`
	Font       int    `json:"font,omitempty" jsonschema:"Font style 0-9 for text statuses"`
}

type ChannelRef struct {
	Channel string `json:"channel" jsonschema:"Channel JID, name or invite link (whatsapp.com/channel/...)"`
}

type ChannelFollowParams struct {
	Channel  string `json:"channel" jsonschema:"Channel JID, name or invite link"`
	Unfollow bool   `json:"unfollow,omitempty" jsonschema:"Unfollow instead"`
}

type ChannelCreateParams struct {
	Name        string `json:"name" jsonschema:"Channel name"`
	Description string `json:"description,omitempty" jsonschema:"Description"`
	Photo       string `json:"photo,omitempty" jsonschema:"Path to a JPEG picture"`
}

type ChannelMessagesParams struct {
	Channel string `json:"channel" jsonschema:"Channel"`
	Count   int    `json:"count,omitempty" jsonschema:"How many (default 20)"`
	Before  int    `json:"before,omitempty" jsonschema:"Server ID to page back from"`
}

type ChannelReactParams struct {
	Channel  string `json:"channel" jsonschema:"Channel"`
	ServerID int    `json:"server_id" jsonschema:"Message server ID (from channel messages)"`
	Emoji    string `json:"emoji,omitempty" jsonschema:"Emoji; empty removes"`
}

type ChannelMuteParams struct {
	Channel string `json:"channel" jsonschema:"Channel"`
	Unmute  bool   `json:"unmute,omitempty" jsonschema:"Unmute instead"`
}

type Channel struct {
	JID         string `json:"jid"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Subscribers int    `json:"subscribers"`
	Verified    bool   `json:"verified"`
	Invite      string `json:"invite,omitempty"`
	Role        string `json:"role,omitempty"`
	Muted       bool   `json:"muted"`
}

type ChannelMessage struct {
	ServerID  int            `json:"server_id"`
	ID        string         `json:"id"`
	Time      string         `json:"time"`
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Views     int            `json:"views"`
	Reactions map[string]int `json:"reactions,omitempty"`
}

type Call struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	Name   string `json:"name,omitempty"`
	Group  string `json:"group,omitempty"`
	Media  string `json:"media,omitempty"`
	Time   string `json:"time"`
	Status string `json:"status"`
}

type CallRejectParams struct {
	ID   string `json:"id" jsonschema:"Call ID"`
	From string `json:"from,omitempty" jsonschema:"Caller (optional if the call is in local history)"`
}

type LimitParams struct {
	Limit int `json:"limit,omitempty" jsonschema:"Max items (default 50)"`
}

type EventsParams struct {
	Types []string `json:"types,omitempty" jsonschema:"Event types to include, e.g. message, reaction, receipt, call. Empty = all"`
	Chats []string `json:"chats,omitempty" jsonschema:"Only events from these chats"`
}

type AuditEntry struct {
	Time    string `json:"time"`
	Source  string `json:"source"`
	Profile string `json:"profile"`
	Method  string `json:"method"`
	Targets string `json:"targets,omitempty"`
	OK      bool   `json:"ok"`
	DryRun  bool   `json:"dry_run,omitempty"`
	Sends   int    `json:"sends,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ----- approvals, schedules, transcription -----

type DraftListParams struct {
	Status string `json:"status,omitempty" jsonschema:"pending (default), sent, rejected, expired, failed or all"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Max items (default 50)"`
}

type DraftRef struct {
	ID int64 `json:"id" jsonschema:"Draft number"`
}

type DraftApproveParams struct {
	ID   int64   `json:"id" jsonschema:"Draft number"`
	Text *string `json:"text,omitempty" jsonschema:"Replace the message text before sending"`
}

type Draft struct {
	ID         int64           `json:"id"`
	Status     string          `json:"status"`
	Method     string          `json:"method"`
	Summary    string          `json:"summary"`
	Preview    string          `json:"preview"`
	Reason     string          `json:"reason,omitempty"`
	Targets    []string        `json:"targets,omitempty"`
	Source     string          `json:"source"`
	Profile    string          `json:"profile"`
	Created    string          `json:"created"`
	Expires    string          `json:"expires"`
	ResolvedAt string          `json:"resolved_at,omitempty"`
	ResolvedBy string          `json:"resolved_by,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Code       string          `json:"code,omitempty"`
}

type ScheduleParams struct {
	SendParams
	At string `json:"at,omitempty" jsonschema:"When to send: RFC3339, 'YYYY-MM-DD HH:MM', 'HH:MM', '9am', 'tomorrow 9:30am' (local time)"`
	In string `json:"in,omitempty" jsonschema:"Or a delay from now, e.g. 45m, 2h, 1d"`
}

type ScheduleRef struct {
	ID int64 `json:"id" jsonschema:"Scheduled message number"`
}

type ScheduleListParams struct {
	Status string `json:"status,omitempty" jsonschema:"pending (default), sent, failed, cancelled, missed or all"`
}

type Scheduled struct {
	ID      int64           `json:"id"`
	Due     string          `json:"due"`
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	To      string          `json:"to,omitempty"`
	Source  string          `json:"source"`
	Created string          `json:"created"`
	SentAt  string          `json:"sent_at,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type Transcript struct {
	ID     string `json:"id"`
	Chat   string `json:"chat"`
	Text   string `json:"text"`
	Engine string `json:"engine"`
	Cached bool   `json:"cached,omitempty"`
}
