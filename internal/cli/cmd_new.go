package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/daemon"
	"github.com/tanishqagg-dev/wassup/internal/rpc"
)

func parseID(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
}

// ---------- drafts ----------

func (a *App) printDrafts(list []api.Draft) {
	if len(list) == 0 {
		fmt.Fprintln(a.out, a.paint("2", "nothing waiting for approval"))
		return
	}
	for _, d := range list {
		status := d.Status
		switch d.Status {
		case "pending":
			status = a.paint("33", "pending")
		case "sent":
			status = a.paint("32", "sent")
		case "failed", "rejected", "expired":
			status = a.paint("31", d.Status)
		}
		fmt.Fprintf(a.out, "%s #%d %s  %s\n", status, d.ID, a.paint("2", "from "+d.Source+", "+d.Reason), "")
		fmt.Fprintf(a.out, "    %s\n", strings.ReplaceAll(d.Preview, "\n", "\n    "))
		if d.Status == "pending" {
			t, _ := time.Parse(time.RFC3339, d.Expires)
			fmt.Fprintf(a.out, "    %s\n", a.paint("2", fmt.Sprintf("approve: wassup drafts approve %d   reject: wassup drafts reject %d   expires %s", d.ID, d.ID, when(t))))
			if d.Code != "" {
				fmt.Fprintf(a.out, "    %s\n", a.paint("2", fmt.Sprintf("or reply \"yes %s\" in your WhatsApp \"Message yourself\" chat", d.Code)))
			}
		}
		if d.Error != "" {
			fmt.Fprintf(a.out, "    %s %s\n", a.paint("31", "error:"), d.Error)
		}
		fmt.Fprintln(a.out)
	}
}

func cmdDrafts(app *App) *cobra.Command {
	var all bool
	var status string
	cmd := &cobra.Command{
		Use:     "drafts",
		Aliases: []string{"approvals", "pending"},
		Short:   "Actions waiting for your approval (agent sends, risky actions)",
		Long: `When your policy asks for approval, an agent's send is parked here instead of
going out. Approve it here, in the chat app's prompt, or by replying "yes <code>"
in your WhatsApp "Message yourself" chat. Agents can never approve their own requests.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				status = "all"
			}
			var list []api.Draft
			if err := app.call("drafts.list", api.DraftListParams{Status: status}, &list); err != nil {
				return err
			}
			if list == nil {
				list = []api.Draft{}
			}
			return app.emit(list, func() { app.printDrafts(list) })
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include sent, rejected and expired")
	cmd.Flags().StringVar(&status, "status", "", "pending, sent, failed, rejected or expired")
	var text string
	approve := &cobra.Command{
		Use: "approve <id>", Short: "Approve and run it now", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return app.fail(api.Errf(api.CodeUsage, "bad draft number %q", args[0]))
			}
			p := api.DraftApproveParams{ID: id}
			if c.Flags().Changed("text") {
				p.Text = &text
			}
			// At a terminal, show exactly what's being approved and ask (skip with --yes).
			if !app.Yes && !app.JSON && term.IsTerminal(int(os.Stdin.Fd())) {
				var d api.Draft
				if err := app.call("drafts.get", api.DraftRef{ID: id}, &d); err != nil {
					return err
				}
				app.printDrafts([]api.Draft{d})
				fmt.Fprint(app.out, "Approve this? [y/N] ")
				var answer string
				_, _ = fmt.Scanln(&answer)
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					fmt.Fprintln(app.out, "not approved")
					return nil
				}
			}
			var res json.RawMessage
			if err := app.call("drafts.approve", p, &res); err != nil {
				return err
			}
			return app.emit(res, func() {
				var sr api.SendResult
				if json.Unmarshal(res, &sr) == nil && sr.ID != "" {
					app.printSent(sr)
				} else {
					app.ok("approved #%d", id)
				}
			})
		},
	}
	approve.Flags().StringVar(&text, "text", "", "change the message text before it goes out")
	reject := &cobra.Command{
		Use: "reject <id>", Aliases: []string{"deny", "cancel"}, Short: "Discard it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return app.fail(api.Errf(api.CodeUsage, "bad draft number %q", args[0]))
			}
			var res map[string]any
			if err := app.call("drafts.reject", api.DraftRef{ID: id}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("rejected #%d; nothing was sent", id) })
		},
	}
	show := &cobra.Command{
		Use: "show <id>", Short: "One request and its outcome", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return app.fail(api.Errf(api.CodeUsage, "bad draft number %q", args[0]))
			}
			var d api.Draft
			if err := app.call("drafts.get", api.DraftRef{ID: id}, &d); err != nil {
				return err
			}
			return app.emit(d, func() { app.printDrafts([]api.Draft{d}) })
		},
	}
	cmd.AddCommand(approve, reject, show)
	return cmd
}

// ---------- schedule ----------

func cmdSchedule(app *App) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Messages queued to send later (create them with: wassup send ... --at / --in)",
		Long: `Lists scheduled messages. Schedule one with:
  wassup send "Mom" "Happy birthday!" --at "tomorrow 9am"
  wassup send Team "standup in 5" --in 25m

The daemon must be running at send time; install it with wassup daemon install.
Messages more than an hour late (daemon was off) are marked missed, not sent.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			st := "pending"
			if all {
				st = "all"
			}
			var list []api.Scheduled
			if err := app.call("schedule.list", api.ScheduleListParams{Status: st}, &list); err != nil {
				return err
			}
			if list == nil {
				list = []api.Scheduled{}
			}
			return app.emit(list, func() {
				if len(list) == 0 {
					fmt.Fprintln(app.out, app.paint("2", "nothing scheduled"))
					return
				}
				tw := table(app.out)
				fmt.Fprintln(tw, "#\tWHEN\tSTATUS\tWHAT")
				for _, s := range list {
					t, _ := time.Parse(time.RFC3339, s.Due)
					fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", s.ID, t.Local().Format("Mon Jan 2 15:04"), s.Status, trunc(s.Summary, 70))
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include sent, failed, cancelled and missed")
	cancel := &cobra.Command{
		Use: "cancel <id>", Short: "Cancel a scheduled message", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return app.fail(api.Errf(api.CodeUsage, "bad number %q", args[0]))
			}
			var res map[string]any
			if err := app.call("schedule.cancel", api.ScheduleRef{ID: id}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("cancelled #%d", id) })
		},
	}
	cmd.AddCommand(cancel)
	return cmd
}

// ---------- transcribe ----------

func cmdTranscribe(app *App) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:   "transcribe <message-id>",
		Short: "Turn a voice note into text (local whisper.cpp or an API)",
		Long: `Transcribes voice notes and audio. Uses whisper.cpp locally when installed
(brew install whisper-cpp; the model downloads once), or any OpenAI-compatible
API if $OPENAI_API_KEY is set. Results are cached. Configure under [transcribe].`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var t api.Transcript
			if err := app.callStream("messages.transcribe", api.MessageRef{Chat: chat, ID: args[0]}, &t, nil, 20*time.Minute); err != nil {
				return err
			}
			return app.emit(t, func() {
				fmt.Fprintln(app.out, t.Text)
				fmt.Fprintln(app.errw, app.paint("2", "("+t.Engine+")"))
			})
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat of the message, if the ID isn't unique")
	return cmd
}

// ---------- remote MCP (claude.ai, phone, any remote client) ----------

func tailscaleHost() (string, error) {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale isn't running (%v)", err)
	}
	var st struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return "", err
	}
	if st.BackendState != "Running" || st.Self.DNSName == "" {
		return "", fmt.Errorf("tailscale isn't connected (state %s); run: tailscale up", st.BackendState)
	}
	return strings.TrimSuffix(st.Self.DNSName, "."), nil
}

func (a *App) restartDaemon() error {
	return daemon.Restart(a.acct, 20*time.Second)
}

func checkReachable(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/.well-known/oauth-protected-resource", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *App) printConnectorSteps(url string) {
	fmt.Fprintf(a.out, "\nConnector URL:  %s\n\n", a.paint("1", url+"/mcp"))
	fmt.Fprintln(a.out, "Claude (claude.ai, Desktop app, phone):")
	fmt.Fprintln(a.out, "  1. Settings > Connectors > Add custom connector")
	fmt.Fprintln(a.out, "  2. Name it WhatsApp and paste the connector URL")
	fmt.Fprintln(a.out, "  3. Connect, tap \"Send me a code\", then enter the code from your")
	fmt.Fprintln(a.out, "     WhatsApp \"Message yourself\" chat")
	fmt.Fprintln(a.out, "\nAny other remote MCP client (ChatGPT connectors, Cursor, n8n...): use the same URL; sign-in is standard OAuth.")
	fmt.Fprintln(a.out, "This computer has to be awake with wassup running. Keep it up with: wassup daemon install")
}

func cmdRemote(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Use WhatsApp from claude.ai on the web and your phone (remote MCP with OAuth)",
		Long: `Serves wassup's MCP tools over HTTPS so claude.ai (web, Desktop, mobile) and other
remote MCP clients can reach your WhatsApp. Each person runs their own wassup;
nothing goes through a shared server.

Signing in proves you own the account: a one-time code arrives in your WhatsApp
"Message yourself" chat. Tokens rotate, can be revoked, and every tool call runs
through your [remote] policy profile with approvals.`,
	}
	var useTS, useCF bool
	var urlFlag, listen, profile string
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Turn on remote MCP and expose it (Tailscale Funnel, Cloudflare, or your own URL)",
		Example: `  wassup remote setup                    # picks Tailscale if it's running, else Cloudflare
  wassup remote setup --tailscale        # stable https://<machine>.<tailnet>.ts.net URL
  wassup remote setup --cloudflare       # quick tunnel; URL changes when the daemon restarts
  wassup remote setup --url https://wa.example.com   # your own reverse proxy to the listen address`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			cfg := app.cfg
			if listen != "" {
				cfg.Remote.Listen = listen
			}
			if profile != "" {
				if _, ok := cfg.Profiles[profile]; !ok {
					return app.fail(api.Errf(api.CodeUsage, "no policy profile %q", profile))
				}
				cfg.Remote.Profile = profile
			}
			mode := ""
			switch {
			case urlFlag != "":
				mode = "url"
			case useTS:
				mode = "tailscale"
			case useCF:
				mode = "cloudflare"
			default:
				if _, err := tailscaleHost(); err == nil {
					mode = "tailscale"
				} else if _, err := exec.LookPath("cloudflared"); err == nil {
					mode = "cloudflare"
				} else {
					return app.fail(api.Errf(api.CodeUsage, "no tunnel available").WithHint("install Tailscale (stable URL) or cloudflared, or pass --url for your own HTTPS proxy"))
				}
			}
			port := cfg.Remote.Listen[strings.LastIndex(cfg.Remote.Listen, ":")+1:]
			switch mode {
			case "url":
				if !strings.HasPrefix(urlFlag, "https://") && !strings.HasPrefix(urlFlag, "http://localhost") {
					return app.fail(api.Errf(api.CodeUsage, "--url must be https://"))
				}
				cfg.Remote.PublicURL, cfg.Remote.Tunnel = strings.TrimRight(urlFlag, "/"), "none"
			case "tailscale":
				host, err := tailscaleHost()
				if err != nil {
					return app.fail(api.Errf(api.CodeUsage, "%v", err))
				}
				out, err := exec.Command("tailscale", "funnel", "--bg", port).CombinedOutput()
				if err != nil {
					fmt.Fprintln(app.errw, strings.TrimSpace(string(out)))
					return app.fail(api.Errf(api.CodeUsage, "tailscale funnel failed").WithHint("Funnel may need enabling for your tailnet; follow the link above, then run this again"))
				}
				cfg.Remote.PublicURL, cfg.Remote.Tunnel = "https://"+host, "tailscale"
			case "cloudflare":
				if _, err := exec.LookPath("cloudflared"); err != nil {
					return app.fail(api.Errf(api.CodeUsage, "cloudflared isn't installed").WithHint("brew install cloudflared"))
				}
				cfg.Remote.PublicURL, cfg.Remote.Tunnel = "", "cloudflare"
			}
			cfg.Remote.Enabled = true
			if err := config.Save(cfg); err != nil {
				return app.fail(err)
			}
			if err := app.restartDaemon(); err != nil {
				return app.fail(err)
			}
			var st map[string]any
			for i := 0; i < 40; i++ {
				if err := app.callQuiet("remote.status", nil, &st); err == nil && st["public_url"] != nil {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			public, _ := st["public_url"].(string)
			if public == "" {
				return app.fail(api.Errf(api.CodeTimeout, "the tunnel didn't report a public URL").WithHint("see: wassup daemon logs"))
			}
			reach := checkReachable(public)
			return app.emit(map[string]any{"mode": mode, "public_url": public, "connector_url": public + "/mcp", "reachable": reach == nil}, func() {
				app.ok("remote MCP is on (%s)", mode)
				if reach != nil {
					fmt.Fprintf(app.out, "%s %s isn't reachable yet (%v). New tunnels can take a minute; check with: wassup remote status\n", app.paint("33", "note:"), public, reach)
				}
				if mode == "cloudflare" {
					fmt.Fprintln(app.out, app.paint("33", "note:")+" Cloudflare quick-tunnel URLs change whenever the daemon restarts; you'd need to re-add the connector. Prefer --tailscale or --url for a stable address.")
				}
				app.printConnectorSteps(public)
			})
		},
	}
	setup.Flags().BoolVar(&useTS, "tailscale", false, "expose with Tailscale Funnel")
	setup.Flags().BoolVar(&useCF, "cloudflare", false, "expose with a Cloudflare quick tunnel")
	setup.Flags().StringVar(&urlFlag, "url", "", "use your own public https URL that proxies to the listen address")
	setup.Flags().StringVar(&listen, "listen", "", "local address (default 127.0.0.1:8790)")
	setup.Flags().StringVar(&profile, "profile", "", "policy profile for remote calls (default agent)")

	status := &cobra.Command{
		Use: "status", Short: "Is remote MCP on, and where?", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			var st map[string]any
			if err := app.call("remote.status", nil, &st); err != nil {
				return err
			}
			var conns []map[string]any
			_ = app.callQuiet("remote.connections", nil, &conns)
			st["connections"] = len(conns)
			if u, _ := st["public_url"].(string); u != "" {
				st["reachable"] = checkReachable(u) == nil
			}
			return app.emit(st, func() {
				app.kv([2]string{"enabled", fmt.Sprint(st["enabled"])}, [2]string{"running", fmt.Sprint(st["running"])},
					[2]string{"connector url", fmt.Sprint(orEmpty(st["connector_url"]))}, [2]string{"reachable", fmt.Sprint(orEmpty(st["reachable"]))},
					[2]string{"tunnel", fmt.Sprint(st["tunnel"])}, [2]string{"listen", fmt.Sprint(st["listen"])},
					[2]string{"profile", fmt.Sprint(st["profile"])}, [2]string{"connected apps", fmt.Sprint(len(conns))})
				if st["enabled"] != true {
					fmt.Fprintln(app.out, "\nTurn it on with: wassup remote setup")
				}
			})
		},
	}
	conns := &cobra.Command{
		Use: "connections", Aliases: []string{"apps", "tokens"}, Short: "Apps connected through remote MCP", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			var list []map[string]any
			if err := app.call("remote.connections", nil, &list); err != nil {
				return err
			}
			return app.emit(list, func() {
				if len(list) == 0 {
					fmt.Fprintln(app.out, "no connected apps")
				}
				tw := table(app.out)
				for _, r := range list {
					fmt.Fprintf(tw, "%v\t%v\tlast used %v\n", r["name"], r["client_id"], orEmpty(r["last_used"]))
				}
				tw.Flush()
			})
		},
	}
	revoke := &cobra.Command{
		Use: "revoke <client-id|all>", Short: "Disconnect an app (it must sign in again)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("remote.revoke", map[string]string{"client": args[0]}, &res); err != nil {
				return err
			}
			return app.emit(res, func() { app.ok("revoked %v tokens", res["revoked_tokens"]) })
		},
	}
	codes := &cobra.Command{
		Use: "codes", Short: "Show sign-in codes waiting to be typed (if WhatsApp couldn't deliver them)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			var list []map[string]any
			if err := app.call("remote.codes", nil, &list); err != nil {
				return err
			}
			return app.emit(list, func() {
				if len(list) == 0 {
					fmt.Fprintln(app.out, "no sign-ins waiting for a code")
				}
				for _, r := range list {
					fmt.Fprintf(app.out, "%v  code %s\n", r["client"], app.paint("1", fmt.Sprint(r["code"])))
				}
			})
		},
	}
	disable := &cobra.Command{
		Use: "disable", Short: "Turn remote MCP off", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			cfg := app.cfg
			was := cfg.Remote.Tunnel
			cfg.Remote.Enabled = false
			if err := config.Save(cfg); err != nil {
				return app.fail(err)
			}
			if was == "tailscale" {
				port := cfg.Remote.Listen[strings.LastIndex(cfg.Remote.Listen, ":")+1:]
				_ = exec.Command("tailscale", "funnel", port, "off").Run()
			}
			if err := app.restartDaemon(); err != nil {
				return app.fail(err)
			}
			return app.emit(map[string]any{"enabled": false}, func() { app.ok("remote MCP is off; connected apps can't reach it anymore") })
		},
	}
	cmd.AddCommand(setup, status, conns, revoke, codes, disable)
	return cmd
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// ---------- connect: one command per client ----------

// wassupPath prefers the PATH entry (survives Homebrew upgrades) over the resolved binary.
func wassupPath() string {
	if p, err := exec.LookPath("wassup"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	exe, _ := os.Executable()
	return exe
}

func claudeDesktopConfig() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Claude", "claude_desktop_config.json")
	}
	return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
}

// mergeMCPJSON adds mcpServers.whatsapp to a JSON config file, keeping everything else and a .bak copy.
func mergeMCPJSON(path, exe string) error {
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s isn't valid JSON: %w", path, err)
		}
		_ = os.WriteFile(path+".bak", b, 0o600)
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["whatsapp"] = map[string]any{"command": exe, "args": []string{"mcp"}}
	doc["mcpServers"] = servers
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

var errNoClaudeCLI = errors.New("claude isn't on PATH")

// addClaudeCode registers wassup with Claude Code for every project.
func addClaudeCode(exe string) (string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", errNoClaudeCLI
	}
	out, err := exec.Command("claude", "mcp", "add", "whatsapp", "--scope", "user", "--", exe, "mcp").CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil && !strings.Contains(msg, "already exists") {
		return "", fmt.Errorf("claude mcp add failed: %s", msg)
	}
	return msg, nil
}

// codexCommand returns the command configured in Codex's [mcp_servers.whatsapp] block, and
// whether the block exists at all.
func codexCommand(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	in, found := false, false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			in = t == "[mcp_servers.whatsapp]"
			found = found || in
			continue
		}
		if in && strings.HasPrefix(t, "command") {
			if k, v, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == "command" {
				v = strings.TrimSpace(v)
				if u, err := strconv.Unquote(v); err == nil {
					return u, true
				}
				return strings.Trim(v, `"'`), true
			}
		}
	}
	return "", found
}

// addCodex adds [mcp_servers.whatsapp] to Codex's config, or points an existing block at exe.
// It returns "added", "updated" or "unchanged".
func addCodex(path, exe string) (string, error) {
	b, _ := os.ReadFile(path)
	cmd, found := codexCommand(path)
	if found && cmd == exe {
		return "unchanged", nil
	}
	if len(b) > 0 {
		_ = os.WriteFile(path+".bak", b, 0o600)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if found {
		lines := strings.Split(string(b), "\n")
		in, replaced := false, false
		for i, line := range lines {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "[") {
				if in && !replaced {
					// Block had no command line: insert one before the next table.
					lines = append(lines[:i], append([]string{fmt.Sprintf("command = %q", exe)}, lines[i:]...)...)
					replaced = true
					break
				}
				in = t == "[mcp_servers.whatsapp]"
				continue
			}
			if in && !replaced {
				if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == "command" {
					lines[i] = fmt.Sprintf("command = %q", exe)
					replaced = true
				}
			}
		}
		if !replaced {
			lines = append(lines, fmt.Sprintf("command = %q", exe))
		}
		return "updated", os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, err = fmt.Fprintf(f, "\n[mcp_servers.whatsapp]\ncommand = %q\nargs = [\"mcp\"]\n", exe)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return "added", err
}

func cmdConnect(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "connect [claude-desktop|claude-code|claude-web|codex|cursor]",
		Short: "Connect wassup to an AI app in one step",
		Long: `Adds wassup as an MCP server to an AI app:

  claude-desktop  edits Claude Desktop's config (restart the app afterwards)
  claude-code     runs: claude mcp add whatsapp --scope user -- wassup mcp
  claude-web      claude.ai on the web and phone (needs: wassup remote setup)
  codex           adds [mcp_servers.whatsapp] to ~/.codex/config.toml
  cursor          adds it to ~/.cursor/mcp.json

With no argument it shows what's installed and connected.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			exe := wassupPath()
			home, _ := os.UserHomeDir()
			if len(args) == 0 {
				var st map[string]any
				_ = app.callQuiet("remote.status", nil, &st)
				remoteOn := st["enabled"] == true
				var rows []map[string]any
				for _, a := range detectApps(exe) {
					rows = append(rows, map[string]any{"target": a.id, "name": a.name, "found": a.found, "connected": a.connected, "command": exe})
					if a.id == "claude-code" {
						rows = append(rows, map[string]any{"target": "claude-web", "name": "Claude on the web and phone", "found": true, "remote_enabled": remoteOn, "connected": remoteOn})
					}
				}
				return app.emit(rows, func() {
					for _, r := range rows {
						state := app.paint("2", "not installed")
						switch {
						case r["target"] == "claude-web" && r["remote_enabled"] == true:
							state = app.paint("32", "remote MCP is on")
						case r["target"] == "claude-web":
							state = app.paint("33", "run wassup remote setup first")
						case r["connected"] == true:
							state = app.paint("32", "connected")
						case r["found"] == true:
							state = app.paint("33", "available, run: wassup connect "+fmt.Sprint(r["target"]))
						}
						fmt.Fprintf(app.out, "  %-15s %s\n", r["target"], state)
					}
				})
			}
			switch strings.ToLower(args[0]) {
			case "claude-desktop", "desktop", "claude":
				path := claudeDesktopConfig()
				if err := mergeMCPJSON(path, exe); err != nil {
					return app.fail(err)
				}
				return app.emit(map[string]string{"config": path}, func() {
					app.ok("added WhatsApp to Claude Desktop (%s)", path)
					fmt.Fprintln(app.out, "Quit and reopen Claude Desktop. Try: \"catch me up on WhatsApp\".")
				})
			case "claude-code", "code":
				msg, err := addClaudeCode(exe)
				if errors.Is(err, errNoClaudeCLI) {
					fmt.Fprintf(app.out, "Claude Code isn't on PATH. Run this once it is:\n  claude mcp add whatsapp --scope user -- %s mcp\n", exe)
					return nil
				}
				if err != nil {
					return app.fail(api.Errf(api.CodeUsage, "%s", err.Error()))
				}
				return app.emit(map[string]string{"output": msg}, func() {
					app.ok("WhatsApp is available in Claude Code (%s)", msg)
					fmt.Fprintln(app.out, "Teach it the CLI too: wassup skill install")
				})
			case "claude-web", "claude.ai", "web", "mobile", "phone":
				var st map[string]any
				if err := app.call("remote.status", nil, &st); err != nil {
					return err
				}
				u, _ := st["public_url"].(string)
				if st["enabled"] != true || u == "" {
					fmt.Fprintln(app.out, "claude.ai reaches wassup over the internet, so turn on remote MCP first:")
					fmt.Fprintln(app.out, "  wassup remote setup")
					return nil
				}
				return app.emit(st, func() { app.printConnectorSteps(u) })
			case "codex":
				path := filepath.Join(home, ".codex", "config.toml")
				state, err := addCodex(path, exe)
				if err != nil {
					return app.fail(err)
				}
				return app.emit(map[string]string{"config": path, "state": state, "command": exe}, func() {
					switch state {
					case "unchanged":
						app.ok("Codex already runs %s", exe)
					case "updated":
						app.ok("pointed Codex's whatsapp server at %s (%s)", exe, path)
					default:
						app.ok("added WhatsApp to Codex (%s)", path)
					}
				})
			case "cursor":
				path := filepath.Join(home, ".cursor", "mcp.json")
				if err := mergeMCPJSON(path, exe); err != nil {
					return app.fail(err)
				}
				return app.emit(map[string]string{"config": path}, func() { app.ok("added WhatsApp to Cursor (%s)", path) })
			}
			return app.fail(api.Errf(api.CodeUsage, "unknown app %q", args[0]).WithHint("claude-desktop, claude-code, claude-web, codex or cursor"))
		},
	}
}

var _ = rpc.Client{}
