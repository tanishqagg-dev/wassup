package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

// Handler implements one API method.
type Handler func(c *Call) (any, error)

// Method describes one operation exposed over the socket, the CLI and MCP.
type Method struct {
	Name     string
	Summary  string
	Params   any  // zero value of the params struct, used for schemas
	Mutating bool // goes through policy and the audit log
	Stream   bool // emits events before the final result
	Conn     bool // needs a live WhatsApp connection
	H        Handler
}

var registry = map[string]*Method{}

func register(m *Method) {
	if _, dup := registry[m.Name]; dup {
		panic("duplicate method " + m.Name)
	}
	registry[m.Name] = m
}

// Methods lists every method sorted by name.
func Methods() []*Method {
	out := make([]*Method, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func LookupMethod(name string) *Method { return registry[name] }

// Call carries one request through a handler.
type Call struct {
	Ctx    context.Context
	E      *Engine
	Method *Method
	Req    *api.Request
	emit   func(any) error

	profile string
	targets []types.JID
	sends   int
	dry     bool
	unlock  func()
	audited bool
}

// Bind decodes params strictly, so a typo in a field name fails loudly instead of being ignored.
func (c *Call) Bind(v any) error {
	p := bytes.TrimSpace(c.Req.Params)
	if len(p) == 0 || string(p) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return api.Errf(api.CodeUsage, "invalid parameters for %s: %v", c.Method.Name, err).WithHint("see: wassup schema " + c.Method.Name)
	}
	return nil
}

// Emit sends a stream event to the caller.
func (c *Call) Emit(v any) error {
	if c.emit == nil {
		return nil
	}
	return c.emit(v)
}

// Dispatch runs one request.
func (e *Engine) Dispatch(ctx context.Context, req *api.Request, emit func(any) error) (res any, err error) {
	m := registry[req.Method]
	if m == nil {
		return nil, api.Errf(api.CodeUsage, "unknown method %q", req.Method).WithHint("list methods with: wassup schema")
	}
	if req.Meta.Source == "" {
		req.Meta.Source = "rpc"
	}
	c := &Call{Ctx: ctx, E: e, Method: m, Req: req, emit: emit}
	defer func() {
		if r := recover(); r != nil {
			e.Log.Error().Interface("panic", r).Str("stack", string(debug.Stack())).Str("method", m.Name).Msg("handler panicked")
			res, err = nil, api.Errf(api.CodeInternal, "internal error in %s: %v", m.Name, r)
		}
		err = translateError(err)
		if m.Mutating && !c.audited {
			e.writeAudit(c, err)
		}
		if c.unlock != nil {
			c.unlock()
		}
	}()
	if m.Conn {
		if err := e.WaitConnected(ctx); err != nil {
			return nil, err
		}
	}
	return m.H(c)
}

func (e *Engine) writeAudit(c *Call, err error) {
	c.audited = true
	targets := make([]string, len(c.targets))
	for i, t := range c.targets {
		targets[i] = t.String()
	}
	profile := c.profile
	if profile == "" {
		_, profile = e.Config().Policy(c.Req.Meta.Profile)
	}
	a := store.Audit{
		Time:    time.Now(),
		Source:  c.Req.Meta.Source,
		Profile: profile,
		Method:  c.Method.Name,
		Targets: strings.Join(targets, ","),
		Params:  redactParams(c.Req.Params),
		OK:      err == nil,
		DryRun:  c.dry,
	}
	if err == nil && !c.dry {
		a.Sends = c.sends
	}
	if err != nil {
		a.Error = err.Error()
	}
	if werr := e.DB.AddAudit(context.Background(), a); werr != nil {
		e.Log.Warn().Err(werr).Msg("audit write failed")
	}
}

// recordItem writes an audit row for one message inside a batch, so rate limits see it immediately.
func (c *Call) recordItem(target types.JID, err error) {
	a := store.Audit{
		Time: time.Now(), Source: c.Req.Meta.Source, Profile: c.profile, Method: "send.message",
		Targets: target.String(), OK: err == nil,
	}
	if err == nil {
		a.Sends = 1
	} else {
		a.Error = err.Error()
	}
	_ = c.E.DB.AddAudit(context.Background(), a)
}

func redactParams(p json.RawMessage) string {
	if len(p) > 2000 {
		return string(p[:2000]) + "…"
	}
	return string(p)
}

// translateError turns whatsmeow errors into structured API errors with useful hints.
func translateError(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae
	}
	msg := err.Error()
	switch {
	case errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return api.Errf(api.CodeNotLoggedIn, "not logged in").WithHint("run: wassup login")
	case errors.Is(err, whatsmeow.ErrNotConnected):
		return api.Errf(api.CodeNotConnected, "not connected to WhatsApp").WithHint("wassup doctor shows why")
	case errors.Is(err, whatsmeow.ErrIQTimedOut), errors.Is(err, whatsmeow.ErrMessageTimedOut), errors.Is(err, context.DeadlineExceeded):
		return api.Errf(api.CodeTimeout, "WhatsApp didn't answer in time: %s", msg).WithHint("retry; if it keeps happening run wassup doctor")
	case errors.Is(err, whatsmeow.ErrGroupNotFound), errors.Is(err, whatsmeow.ErrIQNotFound):
		return api.Errf(api.CodeNotFound, "not found: %s", msg)
	case errors.Is(err, whatsmeow.ErrNotInGroup):
		return api.Errf(api.CodeWhatsApp, "you're not a member of that group")
	case errors.Is(err, whatsmeow.ErrNoPermissionInGroup), errors.Is(err, whatsmeow.ErrIQNotAuthorized), errors.Is(err, whatsmeow.ErrIQForbidden), errors.Is(err, whatsmeow.ErrGroupInviteLinkUnauthorized):
		return api.Errf(api.CodeWhatsApp, "WhatsApp refused: %s", msg).WithHint("this usually needs group admin rights")
	case errors.Is(err, whatsmeow.ErrInviteLinkInvalid), errors.Is(err, whatsmeow.ErrInviteLinkRevoked):
		return api.Errf(api.CodeNotFound, "%s", msg)
	case errors.Is(err, whatsmeow.ErrRecipientNotOnWhatsApp):
		return api.Errf(api.CodeNotOnWhatsApp, "that number isn't on WhatsApp")
	case errors.Is(err, whatsmeow.ErrIQRateOverLimit), errors.Is(err, whatsmeow.ErrAppStateRateLimited):
		return api.Errf(api.CodeRateLimited, "WhatsApp rate limit hit: %s", msg).WithHint("slow down and retry in a minute")
	case errors.Is(err, whatsmeow.ErrInvalidImageFormat):
		return api.Errf(api.CodeUsage, "WhatsApp rejected the image").WithHint("use a square JPEG, at least 192x192")
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		return api.Errf(api.CodeNotFound, "no profile picture set")
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return api.Errf(api.CodeWhatsApp, "their privacy settings hide the profile picture from you")
	case errors.Is(err, whatsmeow.ErrIQBadRequest), errors.Is(err, whatsmeow.ErrIQNotAcceptable), errors.Is(err, whatsmeow.ErrIQNotAllowed), errors.Is(err, whatsmeow.ErrServerReturnedError):
		return api.Errf(api.CodeWhatsApp, "WhatsApp rejected the request: %s", msg)
	}
	return api.Errf(api.CodeInternal, "%s", msg)
}

func usage(format string, args ...any) *api.Error { return api.Errf(api.CodeUsage, format, args...) }
func notFound(format string, args ...any) *api.Error {
	return api.Errf(api.CodeNotFound, format, args...)
}

// parseTimeArg accepts RFC3339, a date, or a relative duration like "2h" or "3d" (meaning that long ago).
func parseTimeArg(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	if d, err := parseDur(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("can't read time %q (use RFC3339, YYYY-MM-DD or a duration like 2h)", s)
}

// RegisterMethod lets other packages (the remote server) add methods to the registry at init.
func RegisterMethod(m *Method) { register(m) }

// agentSourced is true for calls an AI agent controls end to end.
func (c *Call) agentSourced() bool {
	src := c.Req.Meta.Source
	return src == "mcp" || src == "remote"
}

// checkReadable limits which local files a remote agent may attach: only wassup's own media
// folder, so a prompt-injected agent can't send ~/.ssh or similar to someone.
func (c *Call) checkReadable(path string) error {
	if path == "" || c.Req.Meta.Source != "remote" {
		return nil
	}
	real, err := filepath.EvalSymlinks(expandPath(path))
	if err != nil {
		return api.Errf(api.CodeUsage, "can't read %s", path)
	}
	root, err := filepath.EvalSymlinks(c.E.Acct.MediaDir)
	if err != nil {
		return api.Errf(api.CodePolicyDenied, "remote apps can only attach files from wassup's media folder")
	}
	if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return api.Errf(api.CodePolicyDenied, "remote apps can only attach files from wassup's media folder (%s)", c.E.Acct.MediaDir).
			WithHint("forward the original message with forward_message instead")
	}
	return nil
}
