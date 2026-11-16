// Package api defines the wire protocol shared by the CLI, the daemon and the MCP server.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Error codes. Each maps to a stable process exit code (see ExitCode).
const (
	CodeInternal             = "internal"
	CodeUsage                = "usage"
	CodeNotLoggedIn          = "not_logged_in"
	CodeAlreadyLoggedIn      = "already_logged_in"
	CodeNotConnected         = "not_connected"
	CodeNotFound             = "not_found"
	CodeAmbiguous            = "ambiguous"
	CodeNotOnWhatsApp        = "not_on_whatsapp"
	CodePolicyDenied         = "policy_denied"
	CodeConfirmationRequired = "confirmation_required"
	CodeApprovalRequired     = "approval_required"
	CodeRateLimited          = "rate_limited"
	CodeTimeout              = "timeout"
	CodeDaemonUnavailable    = "daemon_unavailable"
	CodeWhatsApp             = "whatsapp_error"
	CodeUnsupported          = "unsupported"
)

// ExitCode maps an error code to the process exit status documented in docs/agents.md.
func ExitCode(code string) int {
	switch code {
	case "":
		return 0
	case CodeUsage:
		return 2
	case CodeNotLoggedIn, CodeAlreadyLoggedIn:
		return 3
	case CodeNotFound, CodeNotOnWhatsApp:
		return 4
	case CodeAmbiguous:
		return 5
	case CodePolicyDenied, CodeConfirmationRequired, CodeApprovalRequired, CodeRateLimited:
		return 6
	case CodeTimeout:
		return 7
	case CodeNotConnected, CodeDaemonUnavailable:
		return 8
	case CodeWhatsApp:
		return 9
	default:
		return 1
	}
}

// Error is the structured error returned by every method.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return e.Message + " (" + e.Hint + ")"
	}
	return e.Message
}

func Errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) WithHint(h string) *Error { e.Hint = h; return e }
func (e *Error) WithDetails(d any) *Error { e.Details = d; return e }
func (e *Error) Is(target error) bool     { t, ok := target.(*Error); return ok && t.Code == e.Code }
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// Meta travels with every request and feeds the policy engine.
type Meta struct {
	Source  string `json:"source,omitempty"`  // cli, mcp or rpc
	Profile string `json:"profile,omitempty"` // policy profile name
	Yes     bool   `json:"yes,omitempty"`     // user confirmed risky actions
	DryRun  bool   `json:"dry_run,omitempty"` // report instead of doing
}

// Request is one line of JSON sent to the daemon socket.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Meta   Meta            `json:"meta"`
}

// Frame is one line of JSON sent back. Streams send Event frames, then exactly one Final frame.
type Frame struct {
	ID     int64           `json:"id"`
	Event  json.RawMessage `json:"event,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	Final  bool            `json:"final,omitempty"`
}

// Event is pushed to `wassup events`, webhooks and streaming calls.
type Event struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
	Chat string    `json:"chat,omitempty"`
	Data any       `json:"data,omitempty"`
}

// ---------- data types ----------

type Media struct {
	Mime     string `json:"mime,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Seconds  int    `json:"seconds,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Path     string `json:"path,omitempty"` // set once downloaded
}

type Location struct {
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	Name    string  `json:"name,omitempty"`
	Address string  `json:"address,omitempty"`
	URL     string  `json:"url,omitempty"`
	Live    bool    `json:"live,omitempty"`
}

type Reaction struct {
	Sender     string    `json:"sender"`
	SenderName string    `json:"sender_name,omitempty"`
	Emoji      string    `json:"emoji"`
	Time       time.Time `json:"time"`
}

type PollOption struct {
	Name   string   `json:"name"`
	Votes  int      `json:"votes"`
	Voters []string `json:"voters,omitempty"`
}

type Poll struct {
	Question   string       `json:"question"`
	Options    []PollOption `json:"options"`
	Selectable int          `json:"selectable"`
}

type Message struct {
	ID         string     `json:"id"`
	Chat       string     `json:"chat"`
	ChatName   string     `json:"chat_name,omitempty"`
	Sender     string     `json:"sender,omitempty"`
	SenderName string     `json:"sender_name,omitempty"`
	FromMe     bool       `json:"from_me"`
	Time       time.Time  `json:"time"`
	Type       string     `json:"type"`
	Text       string     `json:"text,omitempty"`
	Media      *Media     `json:"media,omitempty"`
	Location   *Location  `json:"location,omitempty"`
	Poll       *Poll      `json:"poll,omitempty"`
	QuotedID   string     `json:"quoted_id,omitempty"`
	Mentions   []string   `json:"mentions,omitempty"`
	Forwarded  bool       `json:"forwarded,omitempty"`
	ViewOnce   bool       `json:"view_once,omitempty"`
	Edited     bool       `json:"edited,omitempty"`
	Revoked    bool       `json:"revoked,omitempty"`
	Starred    bool       `json:"starred,omitempty"`
	Status     string     `json:"status,omitempty"` // for your own messages: sent, delivered, read, played
	Transcript string     `json:"transcript,omitempty"`
	Reactions  []Reaction `json:"reactions,omitempty"`
}

type Chat struct {
	JID           string     `json:"jid"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"` // dm, group, channel, broadcast, status
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
	LastMessage   string     `json:"last_message,omitempty"`
	Unread        int        `json:"unread"`
	Archived      bool       `json:"archived,omitempty"`
	Pinned        bool       `json:"pinned,omitempty"`
	MutedUntil    *time.Time `json:"muted_until,omitempty"`
	Disappearing  string     `json:"disappearing,omitempty"`
}

type Contact struct {
	JID          string `json:"jid"`
	Phone        string `json:"phone,omitempty"`
	Name         string `json:"name"`
	FirstName    string `json:"first_name,omitempty"`
	PushName     string `json:"push_name,omitempty"`
	BusinessName string `json:"business_name,omitempty"`
}

type Participant struct {
	JID        string `json:"jid"`
	Phone      string `json:"phone,omitempty"`
	LID        string `json:"lid,omitempty"`
	Name       string `json:"name,omitempty"`
	Admin      bool   `json:"admin,omitempty"`
	SuperAdmin bool   `json:"super_admin,omitempty"`
	Error      int    `json:"error,omitempty"`
}

type Group struct {
	JID              string        `json:"jid"`
	Name             string        `json:"name"`
	Description      string        `json:"description,omitempty"`
	Owner            string        `json:"owner,omitempty"`
	Created          *time.Time    `json:"created,omitempty"`
	ParticipantCount int           `json:"participant_count"`
	Participants     []Participant `json:"participants,omitempty"`
	Announce         bool          `json:"announce"`
	Locked           bool          `json:"locked"`
	ApprovalRequired bool          `json:"approval_required"`
	MemberAddMode    string        `json:"member_add_mode,omitempty"`
	Disappearing     string        `json:"disappearing,omitempty"`
	IsCommunity      bool          `json:"is_community,omitempty"`
	Community        string        `json:"community,omitempty"`
	IsAdmin          bool          `json:"is_admin"`
}

type Resolved struct {
	Query string `json:"query"`
	JID   string `json:"jid"`
	Name  string `json:"name,omitempty"`
	Kind  string `json:"kind"`
	Phone string `json:"phone,omitempty"`
	Via   string `json:"via"` // self, jid, phone, contact, chat
}

type Candidate struct {
	JID  string `json:"jid"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type SendResult struct {
	ID       string    `json:"id"`
	Chat     string    `json:"chat"`
	ChatName string    `json:"chat_name,omitempty"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	ServerID int       `json:"server_id,omitempty"`
}

type DryRun struct {
	DryRun  bool     `json:"dry_run"`
	Action  string   `json:"action"`
	Targets []string `json:"targets,omitempty"`
	Summary string   `json:"summary"`
}

type Status struct {
	Account       string     `json:"account"`
	State         string     `json:"state"`
	LoggedIn      bool       `json:"logged_in"`
	Connected     bool       `json:"connected"`
	JID           string     `json:"jid,omitempty"`
	LID           string     `json:"lid,omitempty"`
	Phone         string     `json:"phone,omitempty"`
	PushName      string     `json:"push_name,omitempty"`
	Platform      string     `json:"platform,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	ConnectedAt   *time.Time `json:"connected_at,omitempty"`
	DaemonPID     int        `json:"daemon_pid,omitempty"`
	DaemonStarted *time.Time `json:"daemon_started,omitempty"`
	Version       string     `json:"version"`
	WAVersion     string     `json:"wa_version,omitempty"`
	Messages      int        `json:"messages_stored"`
	Chats         int        `json:"chats_stored"`
	HistorySync   string     `json:"history_sync,omitempty"`
}
