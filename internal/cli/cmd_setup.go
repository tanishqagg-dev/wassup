package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/daemon"
	"github.com/tanishqagg-dev/wassup/skills"
)

var errSetupStopped = errors.New("setup stopped")

// asker reads answers from the terminal. With --yes every question takes its default.
type asker struct {
	in  *bufio.Reader
	out io.Writer
	yes bool
	app *App
}

func (a *asker) line(q string) string {
	fmt.Fprintf(a.out, "  %s ", q)
	if a.yes {
		fmt.Fprintln(a.out)
		return ""
	}
	s, err := a.in.ReadString('\n')
	if err != nil && s == "" {
		// Ctrl-D: stop instead of quietly taking defaults that install things.
		panic(errSetupStopped)
	}
	return strings.TrimSpace(s)
}

func (a *asker) yesNo(q string, def bool) bool {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	switch strings.ToLower(a.line(q + " " + a.app.paint("2", hint))) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}

// choose shows numbered options and returns the picked index (0-based).
func (a *asker) choose(opts []string, def int) int {
	for i, o := range opts {
		fmt.Fprintf(a.out, "    %s  %s\n", a.app.paint("1", strconv.Itoa(i+1)), o)
	}
	for {
		ans := a.line(fmt.Sprintf("Pick 1-%d %s", len(opts), a.app.paint("2", fmt.Sprintf("[%d]", def+1))))
		if ans == "" {
			return def
		}
		if n, err := strconv.Atoi(ans); err == nil && n >= 1 && n <= len(opts) {
			return n - 1
		}
		fmt.Fprintf(a.out, "  Type a number from 1 to %d.\n", len(opts))
	}
}

func (app *App) step(n, of int, title string) {
	fmt.Fprintf(app.out, "\n  %s  %s\n", app.paint("2", fmt.Sprintf("%d of %d", n, of)), app.paint("1", title))
}

func (app *App) done(format string, args ...any) {
	fmt.Fprintf(app.out, "  %s %s\n", app.paint("32", "✓"), fmt.Sprintf(format, args...))
}

func (app *App) note(format string, args ...any) {
	fmt.Fprintf(app.out, "  %s\n", app.paint("2", fmt.Sprintf(format, args...)))
}

func (app *App) warn(format string, args ...any) {
	fmt.Fprintf(app.out, "  %s %s\n", app.paint("33", "!"), fmt.Sprintf(format, args...))
}

// aiApp is one MCP client the wizard can connect.
type aiApp struct {
	id, name  string
	found     bool
	connected bool
	connect   func() (string, error)
}

func detectApps(exe string) []aiApp {
	home, _ := os.UserHomeDir()
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	desktopCfg := claudeDesktopConfig()
	cursorCfg := filepath.Join(home, ".cursor", "mcp.json")
	codexCfg := filepath.Join(home, ".codex", "config.toml")
	_, claudeErr := exec.LookPath("claude")

	apps := []aiApp{
		{
			id: "claude-desktop", name: "Claude Desktop",
			found:     exists(filepath.Dir(desktopCfg)) || exists("/Applications/Claude.app"),
			connected: jsonHasWassup(desktopCfg, exe),
			connect: func() (string, error) {
				return "quit and reopen Claude Desktop to load it", mergeMCPJSON(desktopCfg, exe)
			},
		},
		{
			id: "claude-code", name: "Claude Code",
			found:     claudeErr == nil,
			connected: claudeErr == nil && exec.Command("claude", "mcp", "get", "whatsapp").Run() == nil,
			connect: func() (string, error) {
				if _, err := addClaudeCode(exe); err != nil {
					return "", err
				}
				return "also taught it the CLI", writeSkill(filepath.Join(home, ".claude", "skills", "wassup"))
			},
		},
		{
			id: "codex", name: "Codex",
			found:     exists(filepath.Join(home, ".codex")),
			connected: codexConnected(codexCfg, exe),
			connect: func() (string, error) {
				_, err := addCodex(codexCfg, exe)
				return "", err
			},
		},
		{
			id: "cursor", name: "Cursor",
			found:     exists(filepath.Join(home, ".cursor")) || exists("/Applications/Cursor.app"),
			connected: jsonHasWassup(cursorCfg, exe),
			connect: func() (string, error) {
				return "", mergeMCPJSON(cursorCfg, exe)
			},
		},
	}
	return apps
}

// jsonHasWassup reports whether an MCP JSON config already runs this exact binary.
func jsonHasWassup(path, exe string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return false
	}
	s, ok := doc.MCPServers["whatsapp"]
	return ok && s.Command == exe
}

func codexConnected(path, exe string) bool {
	cmd, found := codexCommand(path)
	return found && cmd == exe
}

func writeSkill(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skills.WhatsApp), 0o644)
}

var approvalModes = []struct{ value, label, short string }{
	{"always", "Ask me before it sends anything " + "(recommended)", "Your AI asks before every message"},
	{"new_chats_and_groups", "Ask only for new chats and groups", "Your AI asks before messaging someone new or a group"},
	{"none", "Don't ask. I trust it", "Your AI sends without asking (rate limits still apply)"},
}

func cmdSetup(app *App) *cobra.Command {
	var phone string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Guided setup: link your phone, connect your AI apps, pick your safety level",
		Long: `Walks you through everything in plain language, one question at a time:

  1. Link your WhatsApp (QR code or a code typed on your phone)
  2. Keep wassup running in the background
  3. Connect the AI apps it finds (Claude Desktop, Claude Code, Codex, Cursor)
  4. Choose whether your AI asks before it sends

Safe to run again. Steps that are already done are skipped.
Add --yes to accept every default without questions.`,
		Example: "  wassup setup\n  wassup setup --phone +919876543210\n  wassup setup --yes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			defer func() {
				if r := recover(); r != nil {
					if r != errSetupStopped {
						panic(r)
					}
					fmt.Fprintf(app.out, "\n\n  Stopped. Run wassup setup again any time; finished steps are skipped.\n")
					err = exitError{130}
				}
			}()
			if app.JSON {
				return app.fail(api.Errf(api.CodeUsage, "setup is interactive and has no --json mode").
					WithHint("scripts can run the steps directly: wassup login, wassup daemon install, wassup connect <app>"))
			}
			if !app.Yes && !term.IsTerminal(int(os.Stdin.Fd())) {
				return app.fail(api.Errf(api.CodeUsage, "setup asks questions, so it needs a terminal").
					WithHint("run it in a terminal, or add --yes to accept the defaults"))
			}
			ask := &asker{in: bufio.NewReader(os.Stdin), out: app.out, yes: app.Yes, app: app}
			var problems []string

			fmt.Fprintf(app.out, "\n  %s\n", app.paint("1", "wassup setup"))
			app.note("WhatsApp for your AI apps. Four quick steps, about a minute.")

			// 1. Link.
			app.step(1, 4, "Link your WhatsApp")
			var st api.Status
			if err := app.callQuiet("auth.status", nil, &st); err != nil {
				return app.fail(err)
			}
			if st.LoggedIn {
				app.done("Linked as %s%s", st.Phone, nameSuffix(st.PushName))
			} else {
				app.note("Open WhatsApp on your phone, then Settings > Linked devices > Link a device.")
				if phone == "" {
					fmt.Fprintln(app.out)
					if ask.choose([]string{"Scan a QR code " + app.paint("2", "(quickest)"), "Type a code on your phone instead"}, 0) == 1 {
						for phone == "" {
							phone = ask.line("Your number with country code, like +919876543210:")
							if ask.yes {
								break
							}
						}
					}
				}
				res, err := app.runLogin(phone, "")
				if err != nil {
					// runLogin already printed the reason.
					fmt.Fprintln(app.out)
					app.note("You can link later with: wassup login")
					problems = append(problems, "WhatsApp isn't linked yet: run wassup login")
				} else {
					app.done("Linked as %v%s", res["phone"], nameSuffix(fmt.Sprint(orEmpty(res["push_name"]))))
					app.note("Your recent chats are syncing in the background.")
				}
			}

			// 2. Background service.
			app.step(2, 4, "Keep it running")
			if daemon.Installed(app.acct) {
				app.done("Starts automatically when you log in")
			} else {
				app.note("wassup runs quietly in the background so your AI can reach WhatsApp any time.")
				if ask.yesNo("Start it whenever you log in?", true) {
					if _, err := daemon.Install(app.acct); err != nil {
						app.warn("Couldn't set that up: %v", err)
						problems = append(problems, "background service: "+err.Error())
					} else {
						app.done("Starts automatically when you log in")
					}
				} else {
					app.note("Skipped. It still starts on its own whenever an app needs it.")
				}
			}

			// 3. AI apps.
			app.step(3, 4, "Connect your AI apps")
			exe := wassupPath()
			var connected, missing []string
			for _, a := range detectApps(exe) {
				switch {
				case !a.found:
					missing = append(missing, a.name)
				case a.connected:
					app.done("%s %s", a.name, app.paint("2", "(already connected)"))
					connected = append(connected, a.name)
				case ask.yesNo(fmt.Sprintf("Connect %s?", a.name), true):
					extra, err := a.connect()
					if err != nil {
						app.warn("%s: %v", a.name, err)
						problems = append(problems, fmt.Sprintf("%s: %v (try: wassup connect %s)", a.name, err, a.id))
						continue
					}
					if extra != "" {
						app.done("%s %s", a.name, app.paint("2", "("+extra+")"))
					} else {
						app.done("%s", a.name)
					}
					connected = append(connected, a.name)
				}
			}
			if len(missing) > 0 {
				app.note("Not installed: %s. Install one later, then run wassup connect.", strings.Join(missing, ", "))
			}
			app.note("Claude on the web or your phone: wassup remote setup")

			// 4. Approval.
			app.step(4, 4, "How careful should your AI be?")
			app.note("Your AI can always read. This is about sending.")
			fmt.Fprintln(app.out)
			profile := app.cfg.MCP.Profile
			p, _, perr := app.cfg.PolicyStrict(profile)
			if perr != nil {
				app.warn("%v", perr)
				problems = append(problems, perr.Error())
			} else {
				def := 0
				for i, m := range approvalModes {
					if m.value == p.Approval {
						def = i
					}
				}
				labels := make([]string, len(approvalModes))
				for i, m := range approvalModes {
					labels[i] = m.label
				}
				pick := approvalModes[ask.choose(labels, def)]
				if err := setApproval(app.cfg, pick.value); err != nil {
					app.warn("Couldn't save that: %v", err)
					problems = append(problems, "approval setting: "+err.Error())
				} else {
					app.done("%s", pick.short)
				}
			}

			// Summary.
			fmt.Fprintln(app.out)
			if len(problems) == 0 {
				fmt.Fprintf(app.out, "  %s\n", app.paint("1;32", "You're set."))
			} else {
				fmt.Fprintf(app.out, "  %s\n", app.paint("1;33", "Almost there. A few things need a look:"))
				for _, p := range problems {
					fmt.Fprintf(app.out, "    · %s\n", p)
				}
			}
			if len(connected) > 0 {
				fmt.Fprintf(app.out, "\n  Open %s and try: %s\n", connected[0], app.paint("1", "\"catch me up on WhatsApp\""))
			}
			app.note("When your AI wants to send something, you'll get a message in your")
			app.note("WhatsApp \"Message yourself\" chat. Reply yes <code> to send it, or no <code> to stop it.")
			fmt.Fprintln(app.out)
			if len(problems) > 0 {
				return exitError{1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "link with a code typed on the phone instead of a QR code")
	return cmd
}

func nameSuffix(name string) string {
	if name == "" || name == "<nil>" {
		return ""
	}
	return " (" + name + ")"
}

// setApproval sets the approval mode on the profiles AI apps use (local MCP and remote MCP).
func setApproval(cfg *config.Config, mode string) error {
	changed := false
	for _, name := range []string{cfg.MCP.Profile, cfg.Remote.Profile} {
		p, _, err := cfg.PolicyStrict(name)
		if err != nil {
			return err
		}
		if p.Approval != mode {
			p.Approval = mode
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return config.Save(cfg)
}

func cmdApproval(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "approval [always|new|never]",
		Short: "Show or set whether your AI asks you before it sends",
		Long: `Controls the policy profile AI apps use (local MCP and remote MCP).

  always  your AI asks before every message it sends (the default)
  new     it asks only before messaging someone new or a group
  never   it sends without asking; rate limits and deny lists still apply

Approvals arrive in the AI app, and in your WhatsApp "Message yourself" chat.`,
		Example: "  wassup approval\n  wassup approval always\n  wassup approval new --json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				var mode string
				switch strings.ToLower(args[0]) {
				case "always", "ask", "on":
					mode = "always"
				case "new", "new_chats_and_groups", "new-chats", "new_chats":
					mode = "new_chats_and_groups"
				case "never", "none", "off":
					mode = "none"
				default:
					return app.fail(api.Errf(api.CodeUsage, "unknown approval mode %q", args[0]).WithHint("always, new or never"))
				}
				if err := setApproval(app.cfg, mode); err != nil {
					return app.fail(err)
				}
			}
			p, name, err := app.cfg.PolicyStrict(app.cfg.MCP.Profile)
			if err != nil {
				return app.fail(err)
			}
			short := p.Approval
			for _, m := range approvalModes {
				if m.value == p.Approval {
					short = m.short
				}
			}
			return app.emit(map[string]any{"approval": p.Approval, "profile": name, "description": short}, func() {
				app.ok("%s", short)
			})
		},
	}
}
