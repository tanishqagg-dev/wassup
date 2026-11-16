package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/daemon"
	"github.com/tanishqagg-dev/wassup/internal/mcpserver"
	"github.com/tanishqagg-dev/wassup/internal/rpc"
	"github.com/tanishqagg-dev/wassup/skills"
)

func summarizeEvent(ev map[string]any) string {
	data, _ := ev["data"].(map[string]any)
	switch ev["type"] {
	case "message":
		who := fmt.Sprint(data["sender_name"])
		if data["from_me"] == true {
			who = "me"
		}
		text := fmt.Sprint(data["text"])
		if t := fmt.Sprint(data["type"]); t != "text" {
			text = "[" + t + "] " + text
		}
		return fmt.Sprintf("%s in %v: %s  (%v)", who, data["chat_name"], trunc(text, 120), data["id"])
	case "reaction":
		return fmt.Sprintf("%v reacted %v to %v", data["sender_name"], data["emoji"], data["id"])
	case "receipt":
		return fmt.Sprintf("%v %v by %v", data["ids"], data["status"], data["by_name"])
	case "typing":
		return fmt.Sprintf("%v is %v", data["name"], data["state"])
	case "presence":
		return fmt.Sprintf("%v online=%v", data["name"], data["online"])
	case "call":
		return fmt.Sprintf("%v call from %v: %v", data["media"], data["name"], data["status"])
	case "connection":
		return fmt.Sprintf("%v %v", data["state"], data["error"])
	}
	b, _ := json.Marshal(ev["data"])
	return trunc(string(b), 160)
}

func cmdEvents(app *App) *cobra.Command {
	var types, chats []string
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Stream live events (messages, reactions, receipts, calls...) until Ctrl-C",
		Long: `Prints one event per line. With --json each line is a JSON object:
{"type":"message","time":"...","chat":"...","data":{...}}

Types: message, message.edited, message.revoked, message.pinned, reaction, receipt,
typing, presence, poll.vote, call, group.update, group.joined, picture, contact,
history_sync, offline_sync.completed, media.downloaded, connection, blocklist.`,
		Example: "  wassup events --json\n  wassup events --type message,reaction --chat \"Team\"\n  wassup events --json | jq -c 'select(.type==\"message\")'",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			onEvent := func(raw json.RawMessage) error {
				if app.JSON {
					_, err := fmt.Fprintln(app.out, string(raw))
					return err
				}
				var ev map[string]any
				if json.Unmarshal(raw, &ev) != nil {
					return nil
				}
				t, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(ev["time"]))
				fmt.Fprintf(app.out, "%s %-16s %s\n", app.paint("2", t.Local().Format("15:04:05")), app.paint("36", fmt.Sprint(ev["type"])), summarizeEvent(ev))
				return nil
			}
			return app.callStream("events.subscribe", api.EventsParams{Types: types, Chats: chats}, nil, onEvent, 0)
		},
	}
	cmd.Flags().StringSliceVarP(&types, "type", "t", nil, "only these event types (repeatable or comma-separated)")
	cmd.Flags().StringSliceVar(&chats, "chat", nil, "only events from these chats")
	return cmd
}

func cmdMCP(app *App) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run as an MCP server on stdio (for Claude, Codex, Cursor...)",
		Long: `Serves WhatsApp as Model Context Protocol tools over stdin/stdout.
Tool calls use the policy profile set in [mcp] profile (default "agent").

It exposes about 30 core tools (catch_up, read_chat, send_message, view_media,
schedule_message...) plus whatsapp_call, which reaches every method, along with
resources (wassup://chats, wassup://chat/{jid}) and prompts. Sends that need
approval show an Approve prompt in clients that support it, otherwise a prompt
arrives in your WhatsApp "Message yourself" chat. --all-tools adds a tool per feature.

Set it up with:  wassup mcp config`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := app.Profile
			if profile == "" {
				profile = app.cfg.MCP.Profile
			}
			acct := app.acct
			backend := mcpserver.RPCBackend{
				Client: &rpc.Client{Socket: acct.Socket},
				Ensure: func() error { return daemon.EnsureRunning(acct, 15*time.Second) },
			}
			ctx, cancel := app.ctx(0)
			defer cancel()
			err := mcpserver.RunStdio(ctx, mcpserver.Options{
				Backend: backend, Source: "mcp", Profile: profile, AllTools: all,
				Fence: app.cfg.MCP.Fence, ApprovalWait: app.cfg.MCP.ApprovalWait.Duration, WhatsAppApprovals: app.cfg.MCP.WhatsAppApprovals,
				TrustClientApproval: app.cfg.MCP.TrustClientApproval,
			})
			if err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "wassup mcp:", err)
				return exitError{1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all-tools", false, "register a tool for every feature")
	cfgCmd := &cobra.Command{
		Use:   "config [claude|codex|cursor|json]",
		Short: "Print the snippet that adds wassup to an MCP client",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, _ := os.Executable()
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			target := "all"
			if len(args) == 1 {
				target = args[0]
			}
			jsonCfg := fmt.Sprintf(`{
  "mcpServers": {
    "whatsapp": { "command": %q, "args": ["mcp"] }
  }
}`, exe)
			snippets := map[string]string{
				"claude": fmt.Sprintf("claude mcp add whatsapp --scope user -- %s mcp", exe),
				"codex":  fmt.Sprintf("# ~/.codex/config.toml\n[mcp_servers.whatsapp]\ncommand = %q\nargs = [\"mcp\"]", exe),
				"cursor": "# ~/.cursor/mcp.json\n" + jsonCfg,
				"json":   jsonCfg,
			}
			if app.JSON {
				return app.emit(snippets, nil)
			}
			if s, ok := snippets[target]; ok {
				fmt.Fprintln(app.out, s)
				return nil
			}
			for _, k := range []string{"claude", "codex", "cursor"} {
				fmt.Fprintf(app.out, "%s\n%s\n\n", app.paint("1", k+":"), snippets[k])
			}
			fmt.Fprintln(app.out, "Other clients: use the JSON form with `wassup mcp config json`.")
			return nil
		},
	}
	cmd.AddCommand(cfgCmd)
	return cmd
}

func cmdGuide(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "guide",
		Short: "Print the guide for AI agents using wassup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body := skills.WhatsApp
			if i := strings.Index(body, "\n---\n"); strings.HasPrefix(body, "---") && i > 0 {
				body = strings.TrimLeft(body[i+5:], "\n")
			}
			if app.JSON {
				return app.emit(map[string]string{"guide": body}, nil)
			}
			fmt.Fprint(app.out, body)
			return nil
		},
	}
}

func cmdSkill(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "skill", Short: "Install the wassup skill so coding agents know how to use it"}
	var dir string
	var targets []string
	install := &cobra.Command{
		Use:   "install",
		Short: "Write SKILL.md for Claude Code, Codex or a custom folder",
		Example: "  wassup skill install                # ~/.claude/skills/wassup\n" +
			"  wassup skill install --for codex\n  wassup skill install --dir ./skills/wassup",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			var dirs []string
			if dir != "" {
				dirs = append(dirs, dir)
			}
			for _, t := range targets {
				switch t {
				case "claude":
					dirs = append(dirs, filepath.Join(home, ".claude", "skills", "wassup"))
				case "codex":
					dirs = append(dirs, filepath.Join(home, ".codex", "skills", "wassup"))
				case "agents":
					dirs = append(dirs, filepath.Join(home, ".agents", "skills", "wassup"))
				default:
					return app.fail(api.Errf(api.CodeUsage, "unknown target %q (claude, codex, agents)", t))
				}
			}
			if len(dirs) == 0 {
				dirs = append(dirs, filepath.Join(home, ".claude", "skills", "wassup"))
			}
			var written []string
			for _, d := range dirs {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return app.fail(err)
				}
				p := filepath.Join(d, "SKILL.md")
				if err := os.WriteFile(p, []byte(skills.WhatsApp), 0o644); err != nil {
					return app.fail(err)
				}
				written = append(written, p)
			}
			return app.emit(map[string]any{"written": written}, func() {
				for _, w := range written {
					app.ok("wrote %s", w)
				}
			})
		},
	}
	install.Flags().StringVar(&dir, "dir", "", "write into this folder")
	install.Flags().StringSliceVar(&targets, "for", nil, "claude, codex, agents (repeatable)")
	show := &cobra.Command{
		Use: "show", Short: "Print SKILL.md", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { fmt.Fprint(app.out, skills.WhatsApp); return nil },
	}
	cmd.AddCommand(install, show)
	return cmd
}

type flagDoc struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Usage     string `json:"usage"`
}

type cmdDoc struct {
	Command string    `json:"command"`
	Use     string    `json:"use"`
	Short   string    `json:"short"`
	Aliases []string  `json:"aliases,omitempty"`
	Flags   []flagDoc `json:"flags,omitempty"`
}

func walk(c *cobra.Command, out *[]cmdDoc) {
	if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
		return
	}
	if c.Runnable() {
		d := cmdDoc{Command: c.CommandPath(), Use: c.UseLine(), Short: c.Short, Aliases: c.Aliases}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			d.Flags = append(d.Flags, flagDoc{Name: f.Name, Shorthand: f.Shorthand, Type: f.Value.Type(), Default: f.DefValue, Usage: f.Usage})
		})
		*out = append(*out, d)
	}
	for _, sub := range c.Commands() {
		walk(sub, out)
	}
}

func cmdSchema(app *App) *cobra.Command {
	var cliOnly, methodsOnly bool
	cmd := &cobra.Command{
		Use:   "schema [method]",
		Short: "Machine-readable description of every command and API method",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			methods, err := mcpserver.Schemas()
			if err != nil {
				return app.fail(err)
			}
			var out any
			switch {
			case len(args) == 1:
				m, ok := methods[args[0]]
				if !ok {
					return app.fail(api.Errf(api.CodeNotFound, "no method %q", args[0]).WithHint("run wassup schema to list them"))
				}
				out = m
			case cliOnly:
				var docs []cmdDoc
				walk(cmd.Root(), &docs)
				out = docs
			case methodsOnly:
				out = methods
			default:
				var docs []cmdDoc
				walk(cmd.Root(), &docs)
				var global []flagDoc
				cmd.Root().PersistentFlags().VisitAll(func(f *pflag.Flag) {
					global = append(global, flagDoc{Name: f.Name, Shorthand: f.Shorthand, Type: f.Value.Type(), Default: f.DefValue, Usage: f.Usage})
				})
				out = map[string]any{"commands": docs, "global_flags": global, "methods": methods}
			}
			enc := json.NewEncoder(app.out)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
	cmd.Flags().BoolVar(&cliOnly, "cli", false, "only CLI commands and flags")
	cmd.Flags().BoolVar(&methodsOnly, "methods", false, "only API methods (as used by MCP and the socket)")
	return cmd
}

func cmdAudit(app *App) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show recent actions taken through wassup (who, what, which policy)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var list []api.AuditEntry
			if err := app.call("audit.list", api.LimitParams{Limit: limit}, &list); err != nil {
				return err
			}
			if list == nil {
				list = []api.AuditEntry{}
			}
			return app.emit(list, func() {
				tw := table(app.out)
				fmt.Fprintln(tw, "TIME\tSOURCE\tPROFILE\tMETHOD\tRESULT\tTARGETS")
				for _, a := range list {
					res := app.paint("32", "ok")
					if a.DryRun {
						res = app.paint("33", "dry-run")
					}
					if !a.OK {
						res = app.paint("31", trunc(a.Error, 40))
					}
					t, _ := time.Parse(time.RFC3339, a.Time)
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", when(t), a.Source, a.Profile, a.Method, res, trunc(a.Targets, 50))
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "how many entries")
	cmd.AddCommand(&cobra.Command{
		Use: "verify", Short: "Check the audit log's hash chain for tampering", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("audit.verify", nil, &res); err != nil {
				return err
			}
			if err := app.emit(res, func() {
				if res["intact"] == true {
					app.ok("audit log intact (%v entries)", res["rows"])
				} else {
					fmt.Fprintf(app.out, "%s the audit log was modified at entry %v\n", app.paint("31", "tampered:"), res["first_bad_row"])
				}
			}); err != nil {
				return err
			}
			if res["intact"] != true {
				return exitError{1}
			}
			return nil
		},
	})
	return cmd
}
