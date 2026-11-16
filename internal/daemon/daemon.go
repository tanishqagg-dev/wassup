// Package daemon runs the long-lived process that holds the WhatsApp connection.
// Only one process may use a session at a time, so every CLI command talks to it.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/engine"
	"github.com/tanishqagg-dev/wassup/internal/remote"
	"github.com/tanishqagg-dev/wassup/internal/rpc"
)

// Logger writes JSON logs to the account log file (and pretty logs to stderr in the foreground).
func Logger(acct *config.Account, foreground bool, debug bool) (zerolog.Logger, io.Closer, error) {
	if err := acct.Ensure(); err != nil {
		return zerolog.Nop(), nil, err
	}
	if st, err := os.Stat(acct.Log); err == nil && st.Size() > 20<<20 {
		_ = os.Rename(acct.Log, acct.Log+".1")
	}
	f, err := os.OpenFile(acct.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return zerolog.Nop(), nil, err
	}
	var w io.Writer = f
	if foreground {
		w = zerolog.MultiLevelWriter(f, zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	}
	level := zerolog.InfoLevel
	if debug || os.Getenv("WASSUP_DEBUG") != "" {
		level = zerolog.DebugLevel
	}
	return zerolog.New(w).Level(level).With().Timestamp().Str("account", acct.Name).Logger(), f, nil
}

// Run holds the connection until SIGINT/SIGTERM or `wassup daemon stop`.
func Run(acct *config.Account, foreground, debug bool) error {
	if err := acct.Ensure(); err != nil {
		return err
	}
	unlock, err := lockFile(acct.Lock)
	if err != nil {
		return fmt.Errorf("another wassup daemon is already running for account %q", acct.Name)
	}
	defer unlock()
	log, closer, err := Logger(acct, foreground, debug)
	if err != nil {
		return err
	}
	defer closer.Close()
	_ = os.WriteFile(acct.Pid, []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer os.Remove(acct.Pid)

	e, err := engine.New(acct, log)
	if err != nil {
		log.Error().Err(err).Msg("engine init failed")
		return err
	}
	ln, err := rpc.Listen(acct.Socket)
	if err != nil {
		e.Stop()
		return err
	}
	defer os.Remove(acct.Socket)
	stop := make(chan struct{}, 1)
	srv := &rpc.Server{D: e, Log: log, OnStop: func() { stop <- struct{}{} }}
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Error().Err(err).Msg("socket server stopped")
			stop <- struct{}{}
		}
	}()
	e.Start()
	if e.Config().Remote.Enabled {
		if rs, err := remote.Start(e, log); err != nil {
			log.Error().Err(err).Msg("remote MCP disabled for this run")
		} else {
			remote.SetCurrent(rs)
			defer rs.Close()
		}
	}
	log.Info().Int("pid", os.Getpid()).Str("socket", acct.Socket).Str("version", engine.Version).Msg("daemon started")

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info().Str("signal", s.String()).Msg("shutting down")
	case <-stop:
		log.Info().Msg("stop requested")
	}
	srv.Close()
	done := make(chan struct{})
	go func() { e.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Warn().Msg("engine didn't stop within 10s")
	}
	return nil
}

// Spawn starts a detached daemon process.
func Spawn(acct *config.Account) error {
	if err := acct.Ensure(); err != nil {
		return err
	}
	// With a login service installed, launchd/systemd must own the daemon. A detached copy would
	// hold the lock and leave the service failing and relaunching every few seconds.
	if kickService(acct, false) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(acct.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "daemon", "run", "--account", acct.Name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	cmd.Env = os.Environ()
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// EnsureRunning starts the daemon if needed and waits until the socket answers.
// kickService asks the login service to start the daemon now (restart: replace a running one).
// It reports false when no service is installed or the service manager refused.
func kickService(acct *config.Account, restart bool) bool {
	if !Installed(acct) {
		return false
	}
	switch runtimeOS() {
	case "darwin":
		args := []string{"kickstart"}
		if restart {
			args = append(args, "-k")
		}
		args = append(args, "gui/"+strconv.Itoa(os.Getuid())+"/"+serviceLabel(acct))
		return exec.Command("launchctl", args...).Run() == nil
	case "linux":
		verb := "start"
		if restart {
			verb = "restart"
		}
		return exec.Command("systemctl", "--user", verb, systemdUnit(acct)).Run() == nil
	}
	return false
}

// Restart stops whatever daemon holds the account (service-owned or not) and starts it again,
// through the login service when one is installed.
func Restart(acct *config.Account, timeout time.Duration) error {
	_, _ = Stop(acct, 15*time.Second)
	if !kickService(acct, true) {
		if err := Spawn(acct); err != nil {
			return api.Errf(api.CodeDaemonUnavailable, "couldn't start the daemon: %v", err)
		}
	}
	return waitUp(acct, timeout)
}

func waitUp(acct *config.Account, timeout time.Duration) error {
	c := &rpc.Client{Socket: acct.Socket}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Available() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return api.Errf(api.CodeDaemonUnavailable, "the daemon didn't come up within %s", timeout).WithHint("see: wassup daemon logs")
}

func EnsureRunning(acct *config.Account, timeout time.Duration) error {
	c := &rpc.Client{Socket: acct.Socket}
	if c.Available() {
		return nil
	}
	if err := Spawn(acct); err != nil {
		return api.Errf(api.CodeDaemonUnavailable, "couldn't start the daemon: %v", err)
	}
	return waitUp(acct, timeout)
}

// Stop asks the daemon to exit and waits for it.
func Stop(acct *config.Account, timeout time.Duration) (bool, error) {
	c := &rpc.Client{Socket: acct.Socket}
	if !c.Available() {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Call(ctx, "daemon.stop", nil, api.Meta{Source: "cli"}, nil)
	if err != nil {
		if pid := PID(acct); pid > 0 {
			terminate(pid)
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !c.Available() && PID(acct) == 0 {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid := PID(acct); pid > 0 {
		forceKill(pid)
		time.Sleep(300 * time.Millisecond)
	}
	return true, nil
}

// PID returns the daemon's process ID if it's alive.
func PID(acct *config.Account) int {
	b, err := os.ReadFile(acct.Pid)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	if !alive(pid) {
		return 0
	}
	return pid
}

// ---------- background service ----------

// serviceID names the login service. A non-default WASSUP_HOME gets a short hash suffix, so a
// test or second install never replaces the service that runs your real account.
func serviceID(acct *config.Account) string {
	home, err := filepath.Abs(config.Home())
	if err != nil {
		home = config.Home()
	}
	def := ""
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		def = filepath.Join(dir, ".wassup")
	}
	if filepath.Clean(home) == filepath.Clean(def) {
		return acct.Name
	}
	sum := sha256.Sum256([]byte(filepath.Clean(home)))
	return acct.Name + "-" + hex.EncodeToString(sum[:4])
}

func serviceLabel(acct *config.Account) string { return "dev.wassup." + serviceID(acct) }

func systemdUnit(acct *config.Account) string { return "wassup-" + serviceID(acct) + ".service" }

func launchdPath(acct *config.Account) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel(acct)+".plist")
}

func systemdPath(acct *config.Account) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", systemdUnit(acct))
}

// Install registers a login service that keeps the daemon running and restarts it if it dies.
func Install(acct *config.Account) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	_, _ = Stop(acct, 10*time.Second)
	envLines := ""
	if h := os.Getenv("WASSUP_HOME"); h != "" {
		envLines = "WASSUP_HOME=" + h
	}
	switch runtimeOS() {
	case "darwin":
		path := launchdPath(acct)
		env := ""
		if envLines != "" {
			env = fmt.Sprintf("\t<key>EnvironmentVariables</key>\n\t<dict><key>WASSUP_HOME</key><string>%s</string></dict>\n", xmlEscape(os.Getenv("WASSUP_HOME")))
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>daemon</string><string>run</string><string>--account</string><string>%s</string></array>
%s	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>10</integer>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
	<key>ProcessType</key><string>Background</string>
</dict>
</plist>
`, serviceLabel(acct), xmlEscape(exe), acct.Name, env, xmlEscape(acct.Log), xmlEscape(acct.Log))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
			return "", err
		}
		uid := strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", "gui/"+uid, path).Run()
		if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, path).CombinedOutput(); err != nil {
			return path, fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return path, nil
	case "linux":
		path := systemdPath(acct)
		envLine := ""
		if envLines != "" {
			envLine = "Environment=" + envLines + "\n"
		}
		unit := fmt.Sprintf(`[Unit]
Description=wassup WhatsApp CLI daemon (%s)
After=network-online.target

[Service]
ExecStart=%s daemon run --account %s
%sRestart=always
RestartSec=5

[Install]
WantedBy=default.target
`, acct.Name, exe, acct.Name, envLine)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			return "", err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnit(acct)).CombinedOutput(); err != nil {
			return path, fmt.Errorf("systemctl enable: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return path, nil
	}
	return "", errors.New("background services are supported on macOS (launchd) and Linux (systemd); elsewhere run `wassup daemon run` under your own supervisor")
}

func Uninstall(acct *config.Account) (string, error) {
	switch runtimeOS() {
	case "darwin":
		path := launchdPath(acct)
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), path).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return path, err
		}
		return path, nil
	case "linux":
		path := systemdPath(acct)
		_ = exec.Command("systemctl", "--user", "disable", "--now", systemdUnit(acct)).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return path, err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return path, nil
	}
	return "", errors.New("no service manager support on this OS")
}

// Installed reports whether a login service file exists.
func Installed(acct *config.Account) bool {
	var p string
	switch runtimeOS() {
	case "darwin":
		p = launchdPath(acct)
	case "linux":
		p = systemdPath(acct)
	default:
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// Lock takes the session lock for in-process use (when no daemon runs).
func Lock(path string) (func(), error) { return lockFile(path) }
