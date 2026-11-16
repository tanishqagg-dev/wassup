package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/tanishqagg-dev/wassup/docs"
	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/daemon"
	"github.com/tanishqagg-dev/wassup/internal/engine"
	"github.com/tanishqagg-dev/wassup/internal/rpc"
)

func cmdDaemon(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Control the background process that holds the WhatsApp connection",
		Long: `WhatsApp allows one connection per linked device, so wassup keeps it in a small
daemon. Commands start it on demand. Install it as a login service to stay
connected across reboots and receive messages, webhooks and auto-replies 24/7.`,
	}
	var foreground, debugLog bool
	runCmd := &cobra.Command{
		Use: "run", Short: "Run the daemon in this terminal", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			fg := foreground || term.IsTerminal(int(os.Stderr.Fd()))
			if err := daemon.Run(app.acct, fg, debugLog); err != nil {
				return app.fail(api.Errf(api.CodeDaemonUnavailable, "%v", err))
			}
			return nil
		},
	}
	runCmd.Flags().BoolVar(&foreground, "foreground", false, "also log to stderr")
	runCmd.Flags().BoolVar(&debugLog, "debug", false, "verbose logs")
	start := &cobra.Command{
		Use: "start", Short: "Start the daemon in the background", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := daemon.EnsureRunning(app.acct, 15*time.Second); err != nil {
				return app.fail(err)
			}
			return app.emit(map[string]any{"running": true, "pid": daemon.PID(app.acct)}, func() { app.ok("daemon running (pid %d)", daemon.PID(app.acct)) })
		},
	}
	stop := &cobra.Command{
		Use: "stop", Short: "Stop the daemon", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			was, err := daemon.Stop(app.acct, 15*time.Second)
			if err != nil {
				return app.fail(err)
			}
			msg := "daemon stopped"
			if !was {
				msg = "daemon wasn't running"
			}
			if daemon.Installed(app.acct) && was {
				msg += " (the login service will start it again; use wassup daemon uninstall to stop it for good)"
			}
			return app.emit(map[string]any{"stopped": was}, func() { app.ok("%s", msg) })
		},
	}
	restart := &cobra.Command{
		Use: "restart", Short: "Restart the daemon", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if err := daemon.Restart(app.acct, 20*time.Second); err != nil {
				return app.fail(err)
			}
			return app.emit(map[string]any{"running": true, "pid": daemon.PID(app.acct)}, func() { app.ok("daemon restarted (pid %d)", daemon.PID(app.acct)) })
		},
	}
	status := &cobra.Command{
		Use: "status", Short: "Is the daemon running?", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			client := &rpc.Client{Socket: app.acct.Socket}
			res := map[string]any{"running": client.Available(), "pid": daemon.PID(app.acct), "service_installed": daemon.Installed(app.acct), "socket": app.acct.Socket, "log": app.acct.Log}
			return app.emit(res, func() {
				app.kv([2]string{"running", yesno(res["running"].(bool))}, [2]string{"pid", fmt.Sprint(res["pid"])},
					[2]string{"login service", yesno(res["service_installed"].(bool))}, [2]string{"socket", app.acct.Socket}, [2]string{"log", app.acct.Log})
			})
		},
	}
	var lines int
	var follow bool
	logs := &cobra.Command{
		Use: "logs", Short: "Show daemon logs", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error { return app.tailLogs(lines, follow) },
	}
	logs.Flags().IntVarP(&lines, "lines", "n", 50, "how many lines")
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	install := &cobra.Command{
		Use: "install", Short: "Run the daemon at login and restart it if it crashes (launchd/systemd)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			path, err := daemon.Install(app.acct)
			if err != nil {
				return app.fail(err)
			}
			client := &rpc.Client{Socket: app.acct.Socket}
			for i := 0; i < 50 && !client.Available(); i++ {
				time.Sleep(200 * time.Millisecond)
			}
			return app.emit(map[string]any{"installed": path, "running": client.Available()}, func() {
				app.ok("installed %s", path)
				if client.Available() {
					fmt.Fprintln(app.out, "daemon is running and will start at every login")
				}
			})
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: "Remove the login service", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			path, err := daemon.Uninstall(app.acct)
			if err != nil {
				return app.fail(err)
			}
			return app.emit(map[string]any{"removed": path}, func() { app.ok("removed %s", path) })
		},
	}
	cmd.AddCommand(runCmd, start, stop, restart, status, logs, install, uninstall)
	return cmd
}

func (a *App) printLogLine(line string) {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		fmt.Fprintln(a.out, line)
		return
	}
	ts, _ := time.Parse(time.RFC3339, fmt.Sprint(m["time"]))
	level := fmt.Sprint(m["level"])
	msg := fmt.Sprint(m["message"])
	var extra []string
	for k, v := range m {
		switch k {
		case "time", "level", "message", "account":
			continue
		}
		extra = append(extra, fmt.Sprintf("%s=%v", k, v))
	}
	color := map[string]string{"error": "31", "warn": "33", "info": "32", "debug": "2"}[level]
	fmt.Fprintf(a.out, "%s %s %s %s\n", a.paint("2", ts.Local().Format("Jan 2 15:04:05")), a.paint(color, fmt.Sprintf("%-5s", level)), msg, a.paint("2", strings.Join(extra, " ")))
}

func (a *App) tailLogs(n int, follow bool) error {
	f, err := os.Open(a.acct.Log)
	if err != nil {
		return a.fail(api.Errf(api.CodeNotFound, "no log yet at %s", a.acct.Log))
	}
	defer f.Close()
	st, _ := f.Stat()
	start := st.Size() - int64(n)*400
	if start < 0 {
		start = 0
	}
	_, _ = f.Seek(start, io.SeekStart)
	var buf []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		buf = append(buf, sc.Text())
	}
	if start > 0 && len(buf) > 0 {
		buf = buf[1:]
	}
	if len(buf) > n {
		buf = buf[len(buf)-n:]
	}
	for _, l := range buf {
		if a.JSON {
			fmt.Fprintln(a.out, l)
		} else {
			a.printLogLine(l)
		}
	}
	if !follow {
		return nil
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			time.Sleep(300 * time.Millisecond)
			continue
		} else if err != nil {
			return nil
		}
		line = strings.TrimRight(line, "\n")
		if a.JSON {
			fmt.Fprintln(a.out, line)
		} else {
			a.printLogLine(line)
		}
	}
}

func cmdConfig(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "View and edit config.toml (policy, rate limits, calls, webhooks...)"}
	path := &cobra.Command{
		Use: "path", Short: "Print the config file location", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return app.emit(map[string]string{"path": config.Path(), "home": config.Home()}, func() { fmt.Fprintln(app.out, config.Path()) })
		},
	}
	show := &cobra.Command{
		Use: "show", Short: "Print the effective config", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if app.JSON {
				return app.emit(app.cfg, nil)
			}
			return toml.NewEncoder(app.out).Encode(app.cfg)
		},
	}
	edit := &cobra.Command{
		Use: "edit", Short: "Open config.toml in $EDITOR", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if _, err := os.Stat(config.Path()); err != nil {
				_ = config.Save(app.cfg)
			}
			editor := os.Getenv("VISUAL")
			if editor == "" {
				editor = os.Getenv("EDITOR")
			}
			if editor == "" {
				editor = "vi"
				if runtime.GOOS == "windows" {
					editor = "notepad"
				}
			}
			parts := strings.Fields(editor)
			ed := exec.Command(parts[0], append(parts[1:], config.Path())...)
			ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := ed.Run(); err != nil {
				return app.fail(err)
			}
			if _, err := config.Load(); err != nil {
				return app.fail(api.Errf(api.CodeUsage, "the config has an error: %v", err).WithHint("run wassup config edit again to fix it"))
			}
			app.ok("saved; the daemon picks up changes within a second")
			return nil
		},
	}
	docsCmd := &cobra.Command{
		Use: "docs", Short: "Explain every config option", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error { fmt.Fprint(app.out, docs.Configuration); return nil },
	}
	reset := &cobra.Command{
		Use: "reset", Short: "Rewrite config.toml with defaults (keeps a .bak copy)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if b, err := os.ReadFile(config.Path()); err == nil {
				_ = os.WriteFile(config.Path()+".bak", b, 0o600)
			}
			if err := config.Save(config.Default()); err != nil {
				return app.fail(err)
			}
			return app.emit(map[string]string{"path": config.Path()}, func() { app.ok("defaults written to %s (old file at .bak)", config.Path()) })
		},
	}
	cmd.AddCommand(path, show, edit, docsCmd, reset)
	return cmd
}

// cmdAllow manages allow and deny lists inside config.toml.
func cmdAllow(app *App, which string) *cobra.Command {
	short := "Manage the chats agents may message (allowlist)"
	if which == "deny" {
		short = "Manage chats that may never be messaged (denylist)"
	}
	cmd := &cobra.Command{Use: which, Short: short}
	var profile string
	var enforce bool
	list := func(p *config.Policy) *[]string {
		if which == "deny" {
			return &p.Deny
		}
		return &p.Allow
	}
	getProfile := func() (*config.Policy, string) {
		name := profile
		if name == "" {
			name = app.Profile
		}
		if name == "" {
			name = "default"
		}
		if app.cfg.Profiles[name] == nil {
			app.cfg.Profiles[name] = &config.Policy{Mode: "open"}
		}
		return app.cfg.Profiles[name], name
	}
	add := &cobra.Command{
		Use: "add <chat...>", Short: "Add chats (names are saved as JIDs when they resolve)", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			pol, name := getProfile()
			l := list(pol)
			var added []string
			for _, q := range args {
				entry := q
				if !strings.ContainsAny(q, "*?[") {
					var r api.Resolved
					if err := app.callQuiet("resolve", api.ResolveParams{Query: q}, &r); err == nil && r.JID != "" {
						entry = r.JID
					}
				}
				dup := false
				for _, e := range *l {
					if e == entry {
						dup = true
					}
				}
				if !dup {
					*l = append(*l, entry)
					added = append(added, entry)
				}
			}
			if enforce && which == "allow" {
				pol.Mode = "allowlist"
			}
			if err := config.Save(app.cfg); err != nil {
				return app.fail(err)
			}
			return app.emit(map[string]any{"profile": name, "added": added, "list": *l, "mode": pol.Mode}, func() {
				app.ok("added %s to the %s list of profile %q", strings.Join(added, ", "), which, name)
				if which == "allow" && pol.Mode != "allowlist" {
					fmt.Fprintf(app.out, "note: profile %q is in %q mode, so the allowlist isn't enforced yet. Add --enforce to switch it on.\n", name, pol.Mode)
				}
			})
		},
	}
	add.Flags().BoolVar(&enforce, "enforce", false, "also switch the profile to allowlist mode")
	rm := &cobra.Command{
		Use: "rm <chat...>", Aliases: []string{"remove"}, Short: "Remove chats", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			pol, name := getProfile()
			l := list(pol)
			drop := map[string]bool{}
			for _, q := range args {
				drop[q] = true
				var r api.Resolved
				if err := app.callQuiet("resolve", api.ResolveParams{Query: q}, &r); err == nil && r.JID != "" {
					drop[r.JID] = true
				}
			}
			var kept []string
			for _, e := range *l {
				if !drop[e] {
					kept = append(kept, e)
				}
			}
			if kept == nil {
				kept = []string{}
			}
			*l = kept
			if err := config.Save(app.cfg); err != nil {
				return app.fail(err)
			}
			return app.emit(map[string]any{"profile": name, "list": kept}, func() { app.ok("%s list of %q: %s", which, name, strings.Join(kept, ", ")) })
		},
	}
	ls := &cobra.Command{
		Use: "list", Short: "Show the list", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			pol, name := getProfile()
			l := *list(pol)
			return app.emit(map[string]any{"profile": name, "mode": pol.Mode, "list": l}, func() {
				fmt.Fprintf(app.out, "profile %q (mode %s):\n", name, pol.Mode)
				if len(l) == 0 {
					fmt.Fprintln(app.out, "  (empty)")
				}
				for _, e := range l {
					fmt.Fprintln(app.out, "  "+e)
				}
			})
		},
	}
	for _, c := range []*cobra.Command{add, rm, ls} {
		c.Flags().StringVar(&profile, "profile", "", "policy profile to edit (default: --profile or \"default\")")
	}
	cmd.AddCommand(add, rm, ls)
	return cmd
}

func cmdAccounts(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "accounts", Short: "List linked accounts (use --account NAME to pick one)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			type row struct {
				Name    string `json:"name"`
				Default bool   `json:"default"`
				Running bool   `json:"daemon_running"`
				Dir     string `json:"dir"`
			}
			var rows []row
			for _, n := range config.ListAccounts() {
				acct, err := config.AccountPaths(n)
				if err != nil {
					continue
				}
				rows = append(rows, row{Name: n, Default: n == app.cfg.DefaultAccount, Running: (&rpc.Client{Socket: acct.Socket}).Available(), Dir: acct.Dir})
			}
			if rows == nil {
				rows = []row{}
			}
			return app.emit(rows, func() {
				if len(rows) == 0 {
					fmt.Fprintln(app.out, "no accounts yet; run wassup login")
				}
				for _, r := range rows {
					mark := " "
					if r.Default {
						mark = "*"
					}
					fmt.Fprintf(app.out, "%s %-16s daemon %s\n", mark, r.Name, map[bool]string{true: "running", false: "stopped"}[r.Running])
				}
			})
		},
	}
}

func cmdVersion(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print version information", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wm := "unknown"
			if bi, ok := debug.ReadBuildInfo(); ok {
				for _, d := range bi.Deps {
					if d.Path == "go.mau.fi/whatsmeow" {
						wm = d.Version
					}
				}
			}
			res := map[string]string{"version": engine.Version, "whatsmeow": wm, "go": runtime.Version(), "os": runtime.GOOS + "/" + runtime.GOARCH}
			return app.emit(res, func() {
				fmt.Fprintf(app.out, "wassup %s (whatsmeow %s, %s, %s)\n", res["version"], wm, res["go"], res["os"])
			})
		},
	}
}
