// Package cli implements the `wassup` command tree.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/daemon"
	"github.com/tanishqagg-dev/wassup/internal/engine"
	"github.com/tanishqagg-dev/wassup/internal/rpc"
)

// App holds global flags and the connection to the daemon for one invocation.
type App struct {
	JSON     bool
	Account  string
	Profile  string
	Yes      bool
	DryRun   bool
	NoDaemon bool
	Timeout  time.Duration

	cfg    *config.Config
	acct   *config.Account
	caller caller
	out    io.Writer
	errw   io.Writer
	color  bool
}

type caller interface {
	Call(ctx context.Context, method string, params any, onEvent func(json.RawMessage) error) (json.RawMessage, error)
	Close()
}

// exitError carries a code to main without printing twice.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	app := &App{out: os.Stdout, errw: os.Stderr}
	root := newRoot(app)
	err := root.Execute()
	if app.caller != nil {
		app.caller.Close()
	}
	if err == nil {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	// Cobra usage errors (unknown flag, wrong arg count).
	app.printError(api.Errf(api.CodeUsage, "%s", err.Error()).WithHint("see: " + root.Name() + " --help"))
	return api.ExitCode(api.CodeUsage)
}

func envBool(k string) bool {
	v := strings.ToLower(os.Getenv(k))
	return v == "1" || v == "true" || v == "yes"
}

func newRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "wassup",
		Short: "WhatsApp from your terminal, for people and AI agents",
		Long: `wassup links to your WhatsApp account as a device (like WhatsApp Web) and gives you
every feature from the command line: messages, media, reactions, polls, groups,
communities, channels, status, calls, search and live events.

A small background daemon holds the single connection WhatsApp allows, so
commands return instantly and nothing is missed while you're away.

Start with:  wassup setup
Agents:      wassup guide   (or add --json to any command)`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       engine.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return app.init(cmd)
		},
	}
	root.SetVersionTemplate("wassup {{.Version}}\n")
	pf := root.PersistentFlags()
	pf.BoolVar(&app.JSON, "json", envBool("WASSUP_JSON"), "machine-readable JSON output (env WASSUP_JSON=1)")
	pf.StringVarP(&app.Account, "account", "a", os.Getenv("WASSUP_ACCOUNT"), "account to use when several are linked (env WASSUP_ACCOUNT)")
	pf.StringVar(&app.Profile, "profile", os.Getenv("WASSUP_PROFILE"), "policy profile from config.toml (env WASSUP_PROFILE)")
	pf.BoolVarP(&app.Yes, "yes", "y", envBool("WASSUP_YES"), "confirm actions the policy wants confirmed")
	pf.BoolVar(&app.DryRun, "dry-run", envBool("WASSUP_DRY_RUN"), "show what would happen without doing it")
	pf.BoolVar(&app.NoDaemon, "no-daemon", envBool("WASSUP_NO_DAEMON"), "connect in this process instead of using the background daemon")
	pf.DurationVar(&app.Timeout, "timeout", 0, "give up after this long (e.g. 30s)")

	root.AddGroup(
		&cobra.Group{ID: "start", Title: "Getting started:"},
		&cobra.Group{ID: "msg", Title: "Messages:"},
		&cobra.Group{ID: "read", Title: "Reading and search:"},
		&cobra.Group{ID: "chats", Title: "Chats and people:"},
		&cobra.Group{ID: "groups", Title: "Groups, communities and channels:"},
		&cobra.Group{ID: "me", Title: "Your account:"},
		&cobra.Group{ID: "agent", Title: "AI apps, approvals and automation:"},
		&cobra.Group{ID: "sys", Title: "Setup and maintenance:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("start", cmdSetup(app), cmdLogin(app), cmdLogout(app), cmdStatus(app), cmdDoctor(app))
	add("msg", cmdSend(app), cmdSchedule(app), cmdSendMany(app), cmdReply(app), cmdReact(app), cmdUnreact(app), cmdEdit(app), cmdDelete(app),
		cmdForward(app), cmdPin(app), cmdStar(app), cmdPoll(app), cmdDownload(app))
	add("read", cmdMessages(app), cmdMessage(app), cmdSearch(app), cmdWait(app), cmdHistory(app), cmdRead(app), cmdTranscribe(app))
	add("chats", cmdChats(app), cmdChat(app), cmdContacts(app), cmdCheck(app), cmdResolve(app), cmdUser(app),
		cmdBlock(app, false), cmdBlock(app, true), cmdBlocklist(app), cmdTyping(app), cmdPresence(app))
	add("groups", cmdGroups(app), cmdGroup(app), cmdCommunity(app), cmdChannels(app), cmdChannel(app))
	add("me", cmdProfile(app), cmdPrivacy(app), cmdStory(app), cmdCalls(app))
	add("agent", cmdConnect(app), cmdApproval(app), cmdRemote(app), cmdMCP(app), cmdDrafts(app), cmdEvents(app), cmdGuide(app), cmdSkill(app), cmdSchema(app), cmdAudit(app))
	add("sys", cmdDaemon(app), cmdConfig(app), cmdAllow(app, "allow"), cmdAllow(app, "deny"), cmdAccounts(app), cmdVersion(app))
	return root
}

func (a *App) init(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		a.printError(api.Errf(api.CodeUsage, "config error: %v", err).WithHint("fix it with: wassup config edit"))
		return exitError{api.ExitCode(api.CodeUsage)}
	}
	a.cfg = cfg
	if a.Account == "" {
		a.Account = cfg.DefaultAccount
	}
	acct, err := config.AccountPaths(a.Account)
	if err != nil {
		a.printError(api.Errf(api.CodeUsage, "%v", err))
		return exitError{2}
	}
	a.acct = acct
	if !a.JSON {
		if f, ok := a.out.(*os.File); ok {
			a.color = term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
		}
	}
	return nil
}

// connect returns a caller, starting the daemon when allowed.
func (a *App) connect() (caller, error) {
	if a.caller != nil {
		return a.caller, nil
	}
	meta := api.Meta{Source: "cli", Profile: a.Profile, Yes: a.Yes, DryRun: a.DryRun}
	client := &rpc.Client{Socket: a.acct.Socket}
	if !a.NoDaemon {
		if client.Available() {
			a.caller = &socketCaller{c: client, meta: meta}
			return a.caller, nil
		}
		if a.cfg.Daemon.AutoStart {
			if err := daemon.EnsureRunning(a.acct, 15*time.Second); err != nil {
				return nil, err
			}
			a.caller = &socketCaller{c: client, meta: meta}
			return a.caller, nil
		}
	}
	lc, err := newLocalCaller(a.acct, meta)
	if err != nil {
		return nil, err
	}
	a.caller = lc
	return lc, nil
}

// ctx gives commands a context that honours --timeout and Ctrl-C.
func (a *App) ctx(def time.Duration) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	t := a.Timeout
	if t == 0 {
		t = def
	}
	if t <= 0 {
		return ctx, stop
	}
	tctx, cancel := context.WithTimeout(ctx, t)
	return tctx, func() { cancel(); stop() }
}

// call runs a method and decodes the result into out (if non-nil).
func (a *App) call(method string, params any, out any) error {
	return a.callStream(method, params, out, nil, 3*time.Minute)
}

func (a *App) callStream(method string, params any, out any, onEvent func(json.RawMessage) error, def time.Duration) error {
	c, err := a.connect()
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.ctx(def)
	defer cancel()
	raw, err := c.Call(ctx, method, params, onEvent)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = api.Errf(api.CodeTimeout, "%s timed out", method)
		}
		return a.fail(err)
	}
	// A dry run answers with a description instead of the normal result. Print it and stop, so
	// commands never mistake it for a real result or run follow-up steps (--wipe, --wait).
	if dr, ok := asDryRun(raw); ok {
		if err := a.emit(dr, nil); err != nil {
			return err
		}
		return exitError{0}
	}
	if out != nil && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, out); err != nil {
			return a.fail(api.Errf(api.CodeInternal, "decode %s result: %v", method, err))
		}
	}
	return nil
}

// fail prints an error in the right format and converts it into an exit code.
func (a *App) fail(err error) error {
	ae := api.AsError(err)
	a.printError(ae)
	return exitError{api.ExitCode(ae.Code)}
}

func (a *App) printError(ae *api.Error) {
	if a.JSON {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": ae})
		fmt.Fprintln(a.out, string(b))
		return
	}
	fmt.Fprintf(a.errw, "%s %s\n", a.paint("31", "error:"), ae.Message)
	if ae.Hint != "" {
		fmt.Fprintf(a.errw, "  %s %s\n", a.paint("2", "hint:"), ae.Hint)
	}
	if ae.Code == api.CodeAmbiguous {
		if b, err := json.Marshal(ae.Details); err == nil {
			var cands []api.Candidate
			if json.Unmarshal(b, &cands) == nil {
				for _, c := range cands {
					fmt.Fprintf(a.errw, "  %-40s %s (%s)\n", c.JID, c.Name, c.Kind)
				}
			}
		}
	}
	if ae.Code == api.CodeConfirmationRequired {
		fmt.Fprintf(a.errw, "  re-run with --yes to go ahead\n")
	}
}

// emit prints the result: JSON envelope, or the human renderer.
func (a *App) emit(v any, human func()) error {
	if a.JSON {
		b, err := json.Marshal(map[string]any{"ok": true, "data": v})
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintln(a.out, string(b))
		return nil
	}
	if dr, ok := asDryRun(v); ok {
		fmt.Fprintf(a.out, "%s %s\n", a.paint("33", "dry run:"), dr.Summary)
		return nil
	}
	if human != nil {
		human()
	}
	return nil
}

func asDryRun(v any) (api.DryRun, bool) {
	switch d := v.(type) {
	case api.DryRun:
		return d, d.DryRun
	case *api.DryRun:
		return *d, d != nil && d.DryRun
	case map[string]any:
		if d["dry_run"] == true {
			s, _ := d["summary"].(string)
			return api.DryRun{DryRun: true, Summary: s}, true
		}
	case json.RawMessage:
		if !bytes.Contains(d, []byte(`"dry_run":true`)) {
			return api.DryRun{}, false
		}
		var dr api.DryRun
		if json.Unmarshal(d, &dr) == nil && dr.DryRun {
			return dr, true
		}
	}
	return api.DryRun{}, false
}

func (a *App) paint(code, s string) string {
	if !a.color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// ---------- callers ----------

type socketCaller struct {
	c    *rpc.Client
	meta api.Meta
}

func (s *socketCaller) Call(ctx context.Context, method string, params any, onEvent func(json.RawMessage) error) (json.RawMessage, error) {
	return s.c.Call(ctx, method, params, s.meta, onEvent)
}
func (s *socketCaller) Close() {}

// localCaller runs the engine inside this process (no daemon).
type localCaller struct {
	e      *engine.Engine
	meta   api.Meta
	unlock func()
}

func newLocalCaller(acct *config.Account, meta api.Meta) (*localCaller, error) {
	unlock, err := daemon.Lock(acct.Lock)
	if err != nil {
		return nil, api.Errf(api.CodeDaemonUnavailable, "the session is in use by a running wassup daemon").WithHint("drop --no-daemon, or stop it with: wassup daemon stop")
	}
	log, closer, err := daemon.Logger(acct, false, false)
	if err != nil {
		unlock()
		return nil, err
	}
	_ = closer
	e, err := engine.New(acct, log)
	if err != nil {
		unlock()
		return nil, err
	}
	e.Start()
	return &localCaller{e: e, meta: meta, unlock: unlock}, nil
}

func (l *localCaller) Call(ctx context.Context, method string, params any, onEvent func(json.RawMessage) error) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	req := &api.Request{Method: method, Params: raw, Meta: l.meta}
	emit := func(v any) error {
		if onEvent == nil {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return onEvent(b)
	}
	res, err := l.e.Dispatch(ctx, req, emit)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return json.Marshal(res)
}

func (l *localCaller) Close() {
	// Give receipts and app-state writes a moment to flush before disconnecting.
	time.Sleep(300 * time.Millisecond)
	l.e.Stop()
	l.unlock()
}
