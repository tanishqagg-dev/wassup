// Package config holds file locations and the user-editable config.toml.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration that reads and writes as "30s", "5m", "7d".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(FormatDuration(d.Duration)), nil }

// ParseDuration accepts Go durations plus "d" (days) and "w" (weeks) suffixes.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	mult := time.Duration(0)
	switch {
	case strings.HasSuffix(s, "d"):
		mult = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		mult = 7 * 24 * time.Hour
	}
	if mult != 0 {
		var n float64
		if _, err := fmt.Sscanf(s[:len(s)-1], "%g", &n); err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n * float64(mult)), nil
	}
	return time.ParseDuration(s)
}

// FormatDuration prints whole days as "7d" and everything else Go-style.
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return d.String()
}

// Policy is a named set of guard rails. Every send, edit and admin action passes through one.
type Policy struct {
	// Mode is "open" (anything allowed), "allowlist" (only chats in Allow) or "read_only".
	Mode string `toml:"mode"`
	// Allow lists chats that may be targeted when Mode is "allowlist". Entries can be phone
	// numbers, JIDs, exact contact or group names, or globs like "*@g.us".
	Allow []string `toml:"allow"`
	// Deny always blocks these chats, in any mode.
	Deny []string `toml:"deny"`
	// DenyActions blocks API methods by name or glob, for example "groups.*" or "chats.delete".
	DenyActions []string `toml:"deny_actions"`
	// ConfirmGroups requires --yes (or confirmed=true over MCP) to send into a group.
	ConfirmGroups bool `toml:"confirm_groups"`
	// ConfirmNewChats requires confirmation to message a chat with no local history.
	ConfirmNewChats bool `toml:"confirm_new_chats"`
	// ConfirmDestructive requires confirmation for deletes, leaving groups, removing people and logout.
	ConfirmDestructive bool `toml:"confirm_destructive"`
	// ConfirmBulk requires confirmation when one command targets more than this many chats. 0 = never.
	ConfirmBulk int `toml:"confirm_bulk"`
	// DryRun makes every mutating call report what it would do without doing it.
	DryRun bool `toml:"dry_run"`
	// RatePerMinute and RatePerHour cap outgoing messages. 0 = unlimited.
	RatePerMinute int `toml:"rate_per_minute"`
	RatePerHour   int `toml:"rate_per_hour"`
	// RatePerChatPerMinute caps messages to a single chat. 0 = unlimited.
	RatePerChatPerMinute int `toml:"rate_per_chat_per_minute"`
	// MinInterval spaces consecutive sends apart by waiting, not failing.
	MinInterval Duration `toml:"min_interval"`
	// Approval holds messages for a human OK before they go out: "none", "new_chats_and_groups"
	// or "always". Agents get an in-chat prompt (or a WhatsApp "Message yourself" prompt); the CLI
	// uses --yes. Confirm_* rules above also route through approval for non-CLI callers.
	Approval string `toml:"approval"`
	// ApprovalTimeout is how long a pending approval stays valid.
	ApprovalTimeout Duration `toml:"approval_timeout"`
	// Warmup lowers rate limits on a freshly linked account: 25% in week one, 50% in week two.
	Warmup bool `toml:"warmup"`
}

type DaemonConfig struct {
	// AutoStart launches the background daemon on first use.
	AutoStart bool `toml:"auto_start"`
	// ConnectTimeout is how long a command waits for the WhatsApp connection.
	ConnectTimeout Duration `toml:"connect_timeout"`
	// DeviceName is shown under Linked devices on the phone.
	DeviceName string `toml:"device_name"`
	// AutoUpdateVersion fetches the current WhatsApp Web version if the server says ours is outdated.
	AutoUpdateVersion bool `toml:"auto_update_version"`
	// FullHistory asks the phone for your complete history at link time instead of the recent months.
	// Only affects the next `wassup login`.
	FullHistory bool `toml:"full_history"`
}

type SendConfig struct {
	// LinkPreviews fetches a title and thumbnail for the first URL in a text message.
	LinkPreviews bool `toml:"link_previews"`
	// VerifyNumbers checks that a raw phone number is on WhatsApp before sending.
	VerifyNumbers bool `toml:"verify_numbers"`
	// DefaultCountryCode is prepended to numbers typed without one, e.g. "91" or "1". Empty = always require it.
	DefaultCountryCode string `toml:"default_country_code"`
	// SimulateTyping shows "typing..." for a moment before each text send.
	SimulateTyping bool `toml:"simulate_typing"`
}

type CallsConfig struct {
	// AutoReject declines incoming calls automatically.
	AutoReject bool `toml:"auto_reject"`
	// RejectMessage is sent as a text to 1:1 callers after an auto-reject. Empty = no message.
	RejectMessage string `toml:"reject_message"`
	// Except lists callers that are never auto-rejected (phones, JIDs or contact names).
	Except []string `toml:"except"`
}

type MediaConfig struct {
	// AutoDownload lists message types to download as they arrive: image, video, audio, voice, document, sticker.
	AutoDownload []string `toml:"auto_download"`
	// MaxAutoDownloadMB skips auto-downloads above this size.
	MaxAutoDownloadMB int `toml:"max_auto_download_mb"`
}

type Webhook struct {
	URL string `toml:"url"`
	// Events filters by event type, e.g. ["message", "reaction", "call"]. Empty = all.
	Events []string `toml:"events"`
	// Chats filters by chat (same formats as policy lists). Empty = all.
	Chats []string `toml:"chats"`
	// Secret signs each body with HMAC-SHA256 in the X-Wassup-Signature header.
	Secret string `toml:"secret"`
	// IncludeFromMe also forwards messages you send.
	IncludeFromMe bool `toml:"include_from_me"`
}

type MCPConfig struct {
	// Profile is the policy profile applied to MCP tool calls.
	Profile string `toml:"profile"`
	// Fence wraps text written by other people in tagged blocks so the model treats it as data,
	// not instructions (prompt-injection defence). Strongly recommended.
	Fence bool `toml:"fence"`
	// ApprovalWait is how long a tool call waits for you to approve before returning "pending".
	ApprovalWait Duration `toml:"approval_wait"`
	// WhatsAppApprovals sends approval prompts to your "Message yourself" chat when the MCP client
	// can't show one; reply "yes <code>" or "no <code>" from your phone.
	WhatsAppApprovals bool `toml:"whatsapp_approvals"`
	// TrustClientApproval lets local MCP apps (Claude Desktop, Claude Code...) collect your approval
	// in their own UI. The app relays your answer, so only trust apps you installed yourself.
	TrustClientApproval bool `toml:"trust_client_approval"`
}

type TranscribeConfig struct {
	// Engine is "auto" (whisper.cpp if installed, else the API if a key is set), "whisper.cpp", "api" or "off".
	Engine string `toml:"engine"`
	// Model is the whisper.cpp model: tiny, base, small, medium, large-v3-turbo. Downloaded on first use.
	Model string `toml:"model"`
	// Language is a code like "en" or "hi", or "auto".
	Language string `toml:"language"`
	// Auto transcribes incoming voice notes as they arrive.
	Auto bool `toml:"auto"`
	// APIURL is an OpenAI-compatible /audio/transcriptions endpoint (OpenAI, Groq, a local server...).
	APIURL string `toml:"api_url"`
	// APIModel is the model name sent to the API.
	APIModel string `toml:"api_model"`
	// APIKeyEnv names the environment variable holding the API key.
	APIKeyEnv string `toml:"api_key_env"`
}

type RemoteConfig struct {
	// Enabled serves MCP over HTTPS-capable HTTP with OAuth, for claude.ai (web, phone, Desktop)
	// and other remote MCP clients. Set up with: wassup remote setup
	Enabled bool `toml:"enabled"`
	// Listen is the local address. Keep it on 127.0.0.1 and expose it through a tunnel.
	Listen string `toml:"listen"`
	// PublicURL is the https:// address clients use (Tailscale Funnel, Cloudflare, your proxy).
	PublicURL string `toml:"public_url"`
	// Tunnel is "tailscale", "cloudflare" (quick tunnel, URL changes per run), or "none".
	Tunnel string `toml:"tunnel"`
	// Profile is the policy profile for remote tool calls.
	Profile string `toml:"profile"`
	// TokenTTL is the access token lifetime; refresh tokens last RefreshTTL and rotate on use.
	TokenTTL   Duration `toml:"token_ttl"`
	RefreshTTL Duration `toml:"refresh_ttl"`
	// TrustedRedirectHosts are client hosts whose in-app approval prompts are trusted, because their
	// tokens can only ever be delivered to that host. Other remote apps get the WhatsApp approval prompt.
	TrustedRedirectHosts []string `toml:"trusted_redirect_hosts"`
}

type Config struct {
	DefaultAccount string             `toml:"default_account"`
	Daemon         DaemonConfig       `toml:"daemon"`
	Send           SendConfig         `toml:"send"`
	Calls          CallsConfig        `toml:"calls"`
	Media          MediaConfig        `toml:"media"`
	MCP            MCPConfig          `toml:"mcp"`
	Transcribe     TranscribeConfig   `toml:"transcribe"`
	Remote         RemoteConfig       `toml:"remote"`
	Profiles       map[string]*Policy `toml:"profiles"`
	Webhooks       []Webhook          `toml:"webhooks"`
}

// Default returns the config written on first run. Every key is spelled out so users can see the knobs.
func Default() *Config {
	return &Config{
		DefaultAccount: "default",
		Daemon: DaemonConfig{
			AutoStart:         true,
			ConnectTimeout:    Duration{20 * time.Second},
			DeviceName:        "wassup CLI",
			AutoUpdateVersion: true,
		},
		Send: SendConfig{
			LinkPreviews:  true,
			VerifyNumbers: true,
		},
		Calls: CallsConfig{
			AutoReject:    false,
			RejectMessage: "",
			Except:        []string{},
		},
		Media: MediaConfig{
			AutoDownload:      []string{},
			MaxAutoDownloadMB: 25,
		},
		MCP: MCPConfig{Profile: "agent", Fence: true, ApprovalWait: Duration{2 * time.Minute}, WhatsAppApprovals: true, TrustClientApproval: true},
		Transcribe: TranscribeConfig{
			Engine: "auto", Model: "base", Language: "auto",
			APIURL: "https://api.openai.com/v1/audio/transcriptions", APIModel: "whisper-1", APIKeyEnv: "OPENAI_API_KEY",
		},
		Remote: RemoteConfig{
			Listen: "127.0.0.1:8790", Tunnel: "none", Profile: "agent",
			TokenTTL: Duration{time.Hour}, RefreshTTL: Duration{60 * 24 * time.Hour},
			TrustedRedirectHosts: []string{"claude.ai", "claude.com"},
		},
		Profiles: map[string]*Policy{
			"default": {
				Mode:                 "open",
				Allow:                []string{},
				Deny:                 []string{},
				DenyActions:          []string{},
				ConfirmBulk:          20,
				RatePerMinute:        30,
				RatePerHour:          300,
				RatePerChatPerMinute: 15,
				Approval:             "none",
				ApprovalTimeout:      Duration{30 * time.Minute},
				Warmup:               true,
			},
			"agent": {
				Mode:                 "open",
				Allow:                []string{},
				Deny:                 []string{},
				DenyActions:          []string{"auth.logout"},
				ConfirmGroups:        false,
				ConfirmNewChats:      false,
				ConfirmDestructive:   true,
				ConfirmBulk:          5,
				RatePerMinute:        12,
				RatePerHour:          120,
				RatePerChatPerMinute: 6,
				MinInterval:          Duration{1500 * time.Millisecond},
				Approval:             "always",
				ApprovalTimeout:      Duration{30 * time.Minute},
				Warmup:               true,
			},
		},
		Webhooks: []Webhook{},
	}
}

// Home is the root folder for config, sessions and data. Override with WASSUP_HOME.
func Home() string {
	if h := os.Getenv("WASSUP_HOME"); h != "" {
		return h
	}
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, ".wassup")
}

// Path is the config file location. Override with WASSUP_CONFIG.
func Path() string {
	if p := os.Getenv("WASSUP_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Home(), "config.toml")
}

// Load reads config.toml, creating it with defaults if it doesn't exist yet.
func Load() (*Config, error) {
	path := Path()
	cfg := Default()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if werr := Save(cfg); werr != nil {
			// Not fatal: the defaults still work for this run.
			return cfg, nil
		}
		return cfg, nil
	} else if err != nil {
		return nil, err
	}
	fresh := Default()
	fresh.Profiles = map[string]*Policy{}
	if _, err := toml.Decode(string(data), fresh); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(fresh.Profiles) == 0 {
		fresh.Profiles = Default().Profiles
	}
	if fresh.Profiles["default"] == nil {
		fresh.Profiles["default"] = Default().Profiles["default"]
	}
	if fresh.DefaultAccount == "" {
		fresh.DefaultAccount = "default"
	}
	if fresh.MCP.Profile == "" {
		fresh.MCP.Profile = "agent"
	}
	if fresh.Daemon.ConnectTimeout.Duration == 0 {
		fresh.Daemon.ConnectTimeout.Duration = 20 * time.Second
	}
	if fresh.MCP.ApprovalWait.Duration == 0 {
		fresh.MCP.ApprovalWait.Duration = 2 * time.Minute
	}
	if fresh.Remote.Listen == "" {
		fresh.Remote.Listen = "127.0.0.1:8790"
	}
	if fresh.Remote.Profile == "" {
		fresh.Remote.Profile = "agent"
	}
	if fresh.Remote.TokenTTL.Duration == 0 {
		fresh.Remote.TokenTTL.Duration = time.Hour
	}
	if fresh.Remote.RefreshTTL.Duration == 0 {
		fresh.Remote.RefreshTTL.Duration = 60 * 24 * time.Hour
	}
	if fresh.Transcribe.Engine == "" {
		fresh.Transcribe = Default().Transcribe
	}
	for _, p := range fresh.Profiles {
		if p != nil && p.ApprovalTimeout.Duration == 0 {
			p.ApprovalTimeout.Duration = 30 * time.Minute
		}
	}
	if fresh.Daemon.DeviceName == "" {
		fresh.Daemon.DeviceName = "wassup CLI"
	}
	return fresh, nil
}

const header = `# wassup config. Docs: wassup config docs, or docs/configuration.md in the repo.
# Changes apply to the running daemon within a second; no restart needed.

`

// Save writes the config atomically.
func Save(cfg *Config) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PolicyStrict returns the named profile. An empty name means "default"; any other unknown
// name is an error, so a typo never silently drops the restrictions someone asked for.
func (c *Config) PolicyStrict(name string) (*Policy, string, error) {
	if name == "" {
		p, n := c.Policy("")
		return p, n, nil
	}
	if p, ok := c.Profiles[name]; ok && p != nil {
		return p, name, nil
	}
	var names []string
	for n := range c.Profiles {
		names = append(names, n)
	}
	return nil, name, fmt.Errorf("no policy profile named %q (defined: %s)", name, strings.Join(names, ", "))
}

// Policy returns the named profile, falling back to "default". Use for display only.
func (c *Config) Policy(name string) (*Policy, string) {
	if name == "" {
		name = "default"
	}
	if p, ok := c.Profiles[name]; ok && p != nil {
		return p, name
	}
	return c.Profiles["default"], "default"
}

var accountRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Account holds every file path for one linked WhatsApp account.
type Account struct {
	Name      string
	Dir       string
	SessionDB string
	AppDB     string
	Socket    string
	Lock      string
	Log       string
	Pid       string
	MediaDir  string
}

// ValidAccountName reports whether name can be used as an account folder.
func ValidAccountName(name string) bool { return accountRe.MatchString(name) }

// AccountPaths builds the paths for an account without touching the disk.
func AccountPaths(name string) (*Account, error) {
	if name == "" {
		name = "default"
	}
	if !ValidAccountName(name) {
		return nil, fmt.Errorf("invalid account name %q (use letters, digits, - and _)", name)
	}
	dir := filepath.Join(Home(), "accounts", name)
	sock := filepath.Join(dir, "wassup.sock")
	// Unix socket paths are limited to ~104 bytes on macOS.
	if len(sock) > 100 {
		sum := sha256.Sum256([]byte(dir))
		sock = filepath.Join(os.TempDir(), "wassup-"+hex.EncodeToString(sum[:6])+".sock")
	}
	return &Account{
		Name:      name,
		Dir:       dir,
		SessionDB: filepath.Join(dir, "session.db"),
		AppDB:     filepath.Join(dir, "wassup.db"),
		Socket:    sock,
		Lock:      filepath.Join(dir, "daemon.lock"),
		Log:       filepath.Join(dir, "daemon.log"),
		Pid:       filepath.Join(dir, "daemon.pid"),
		MediaDir:  filepath.Join(dir, "media"),
	}, nil
}

// Ensure creates the account folder with private permissions.
func (a *Account) Ensure() error {
	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		return err
	}
	return os.MkdirAll(a.MediaDir, 0o700)
}

// ListAccounts returns account folder names that exist on disk.
func ListAccounts() []string {
	entries, err := os.ReadDir(filepath.Join(Home(), "accounts"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && ValidAccountName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}
