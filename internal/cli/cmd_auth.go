package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
	"rsc.io/qr"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/daemon"
	"github.com/tanishqagg-dev/wassup/internal/engine"
	"github.com/tanishqagg-dev/wassup/internal/rpc"
)

func cmdLogin(app *App) *cobra.Command {
	var phone, qrPNG string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Link this CLI to your phone (QR code or pairing code)",
		Long: `Link wassup as a device on your WhatsApp account, the same way WhatsApp Web links.

QR code (default): open WhatsApp on your phone > Settings > Linked devices >
Link a device, and scan the code printed here.

Pairing code: run wassup login --phone +<country code><number>, then on the phone
choose Link a device > "Link with phone number instead" and type the 8-character code.

After linking, the phone isn't needed day to day. Your recent history syncs in the background.`,
		Example: "  wassup login\n  wassup login --phone +919876543210\n  wassup login --qr-png /tmp/wassup-qr.png --json   # for agents that show images",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := app.runLogin(phone, qrPNG)
			if err != nil {
				return err
			}
			return app.emit(res, func() {
				fmt.Fprintf(app.out, "\n%s linked as %v (%v)\n", app.paint("32", "ok"), res["phone"], res["push_name"])
				fmt.Fprintln(app.out, "Your chat history is syncing in the background. Try: wassup chats")
				if !daemon.Installed(app.acct) {
					fmt.Fprintln(app.out, "Keep it connected after reboots with: wassup daemon install")
				}
			})
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "link with a pairing code for this number instead of a QR code")
	cmd.Flags().StringVar(&qrPNG, "qr-png", "", "also write each QR code to this PNG file")
	return cmd
}

// runLogin streams auth.login: QR codes or a pairing code, then the linked account.
func (app *App) runLogin(phone, qrPNG string) (map[string]any, error) {
	tty := app.color
	onEvent := func(raw json.RawMessage) error {
		var ev struct {
			Type      string `json:"type"`
			Code      string `json:"code"`
			ExpiresIn int    `json:"expires_in"`
			Phone     string `json:"phone"`
		}
		_ = json.Unmarshal(raw, &ev)
		if qrPNG != "" && ev.Type == "qr" {
			if c, err := qr.Encode(ev.Code, qr.L); err == nil {
				c.Scale = 8
				_ = os.WriteFile(qrPNG, c.PNG(), 0o600)
			}
		}
		if app.JSON {
			if qrPNG != "" && ev.Type == "qr" {
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				m["png"] = qrPNG
				raw, _ = json.Marshal(m)
			}
			fmt.Fprintln(app.out, string(raw))
			return nil
		}
		switch ev.Type {
		case "qr":
			if tty {
				fmt.Fprint(app.out, "\033[H\033[2J")
			}
			fmt.Fprintln(app.out, "Scan with WhatsApp on your phone: Settings > Linked devices > Link a device")
			qrterminal.GenerateWithConfig(ev.Code, qrterminal.Config{
				Level: qr.L, Writer: app.out, HalfBlocks: true, QuietZone: 2,
				BlackChar: qrterminal.BLACK_BLACK, WhiteBlackChar: qrterminal.WHITE_BLACK,
				WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE,
			})
			fmt.Fprintf(app.out, "Code refreshes in %ds. Prefer typing a code? Run: wassup login --phone +<number>\n", ev.ExpiresIn)
			if qrPNG != "" {
				fmt.Fprintf(app.out, "Also saved as %s\n", qrPNG)
			}
		case "pair_code":
			fmt.Fprintf(app.out, "\nOn the phone for %s:\n  WhatsApp > Settings > Linked devices > Link a device > Link with phone number instead\n\n  Enter this code:  %s\n\nWaiting for you to confirm on the phone...\n", ev.Phone, app.paint("1", ev.Code))
		}
		return nil
	}
	var res map[string]any
	if err := app.callStream("auth.login", api.LoginParams{Phone: phone}, &res, onEvent, 0); err != nil {
		return nil, err
	}
	return res, nil
}

func cmdLogout(app *App) *cobra.Command {
	var wipe bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Unlink this device from your phone",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var res map[string]any
			if err := app.call("auth.logout", nil, &res); err != nil {
				return err
			}
			if wipe {
				_, _ = daemon.Stop(app.acct, 10*time.Second)
				if err := os.RemoveAll(app.acct.Dir); err != nil {
					return app.fail(err)
				}
				res["wiped"] = app.acct.Dir
			}
			return app.emit(res, func() {
				app.ok("unlinked %v", res["logged_out"])
				if wipe {
					fmt.Fprintf(app.out, "deleted local data in %s\n", app.acct.Dir)
				} else {
					fmt.Fprintln(app.out, "local message history was kept; add --wipe to delete it")
				}
			})
		},
	}
	cmd.Flags().BoolVar(&wipe, "wipe", false, "also delete the local message store and media for this account")
	return cmd
}

func (a *App) printStatus(st api.Status) {
	state := st.State
	switch {
	case st.Connected:
		state = a.paint("32", state)
	case !st.LoggedIn:
		state = a.paint("33", state)
	default:
		state = a.paint("31", state)
	}
	rows := [][2]string{
		{"account", st.Account},
		{"state", state},
		{"phone", st.Phone},
		{"name", st.PushName},
		{"jid", st.JID},
		{"lid", st.LID},
		{"connected since", whenp(st.ConnectedAt)},
		{"last error", st.LastError},
		{"history", st.HistorySync},
		{"stored", fmt.Sprintf("%d messages in %d chats", st.Messages, st.Chats)},
		{"daemon pid", fmt.Sprint(st.DaemonPID)},
		{"wassup version", st.Version},
		{"whatsapp web version", st.WAVersion},
	}
	a.kv(rows...)
	if !st.LoggedIn {
		fmt.Fprintln(a.out, "\nNot linked yet. Run: wassup login")
	}
}

func cmdStatus(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Aliases: []string{"whoami"},
		Short:   "Show whether you're linked and connected",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var st api.Status
			if err := app.call("auth.status", nil, &st); err != nil {
				return err
			}
			return app.emit(st, func() { app.printStatus(st) })
		},
	}
}

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail"`
}

func cmdDoctor(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the setup and explain anything that's wrong",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var checks []check
			add := func(name, status, detail string) { checks = append(checks, check{name, status, detail}) }
			add("config", "ok", config.Path())
			if err := app.acct.Ensure(); err != nil {
				add("data folder", "fail", err.Error())
			} else {
				add("data folder", "ok", app.acct.Dir)
			}
			client := &rpc.Client{Socket: app.acct.Socket}
			running := client.Available()
			var st api.Status
			var stErr error
			if running || app.cfg.Daemon.AutoStart {
				stErr = app.callQuiet("auth.status", nil, &st)
			}
			switch {
			case stErr != nil:
				add("daemon", "fail", api.AsError(stErr).Error())
			case !running && !app.cfg.Daemon.AutoStart:
				add("daemon", "warn", "not running and auto_start is off; start it with wassup daemon start")
			default:
				add("daemon", "ok", fmt.Sprintf("pid %d, socket %s", st.DaemonPID, app.acct.Socket))
			}
			if daemon.Installed(app.acct) {
				add("background service", "ok", "starts at login and restarts if it crashes")
			} else {
				add("background service", "warn", "not installed; messages that arrive while the daemon is stopped are fetched when it restarts. Run: wassup daemon install")
			}
			if stErr == nil {
				switch {
				case !st.LoggedIn:
					add("whatsapp link", "fail", "not linked. Run: wassup login")
				case st.Connected:
					add("whatsapp link", "ok", fmt.Sprintf("connected as %s (%s)", st.Phone, st.PushName))
				default:
					add("whatsapp link", "fail", fmt.Sprintf("linked but %s: %s", st.State, st.LastError))
				}
				add("local store", "ok", fmt.Sprintf("%d messages, %d chats", st.Messages, st.Chats))
			}
			for _, tool := range []struct{ name, why string }{
				{"ffmpeg", "voice notes from any audio, video conversion, GIFs, thumbnails"},
				{"ffprobe", "durations for audio and video"},
				{"cwebp", "stickers from PNG/JPEG"},
			} {
				if _, err := exec.LookPath(tool.name); err == nil {
					add(tool.name, "ok", "found")
				} else {
					add(tool.name, "warn", "missing (optional): "+tool.why)
				}
			}
			pol, name := app.cfg.Policy(app.Profile)
			add("policy", "ok", fmt.Sprintf("profile %q: mode %s, %d/min, %d/hour", name, pol.Mode, pol.RatePerMinute, pol.RatePerHour))
			worst := "ok"
			for _, c := range checks {
				if c.Status == "fail" {
					worst = "fail"
				} else if c.Status == "warn" && worst == "ok" {
					worst = "warn"
				}
			}
			res := map[string]any{"overall": worst, "checks": checks, "version": engine.Version}
			if stErr == nil {
				res["status"] = st
			}
			if err := app.emit(res, func() {
				for _, c := range checks {
					color := map[string]string{"ok": "32", "warn": "33", "fail": "31"}[c.Status]
					fmt.Fprintf(app.out, "%s %-20s %s\n", app.paint(color, fmt.Sprintf("%-4s", c.Status)), c.Name, c.Detail)
				}
			}); err != nil {
				return err
			}
			if worst == "fail" {
				return exitError{1}
			}
			return nil
		},
	}
}

// callQuiet is call without printing errors (for doctor).
func (a *App) callQuiet(method string, params any, out any) error {
	c, err := a.connect()
	if err != nil {
		return err
	}
	ctx, cancel := a.ctx(30 * time.Second)
	defer cancel()
	raw, err := c.Call(ctx, method, params, nil)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

var _ = strings.TrimSpace
