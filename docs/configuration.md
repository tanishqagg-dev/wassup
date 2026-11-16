# Configuration

wassup reads `~/.wassup/config.toml` (override with `WASSUP_HOME` or `WASSUP_CONFIG`). The file is created with every option spelled out the first time you run any command. Edit it with `wassup config edit`; the daemon reloads it within a second, no restart needed.

## Top level

| key | default | what it does |
|---|---|---|
| `default_account` | `"default"` | Account used when `--account` isn't given. Each account is a folder under `~/.wassup/accounts/`. |

## `[daemon]`

| key | default | what it does |
|---|---|---|
| `auto_start` | `true` | Start the background daemon on the first command. Off means you run `wassup daemon start` yourself (or every command connects in-process). |
| `connect_timeout` | `"20s"` | How long a command waits for the WhatsApp connection before failing with `not_connected`. |
| `device_name` | `"wassup CLI"` | Name shown on the phone under Linked devices. Applies at the next `wassup login`. |
| `auto_update_version` | `true` | If WhatsApp says the client is outdated, fetch the current WhatsApp Web version and reconnect. |
| `full_history` | `false` | Ask the phone for full history at link time instead of the recent months. Applies at the next `wassup login`. Large accounts take a while. |

## `[send]`

| key | default | what it does |
|---|---|---|
| `link_previews` | `true` | Fetch a title and thumbnail for the first URL in a text message. |
| `verify_numbers` | `true` | Before sending to a raw phone number, check it's on WhatsApp and use the canonical ID WhatsApp returns. |
| `default_country_code` | `""` | Digits to prepend to numbers typed without a country code, for example `"91"`. Empty means numbers must include it. |
| `simulate_typing` | `false` | Show "typing..." briefly before every text send. |

## `[calls]`

wassup can't place or answer calls, but it sees incoming ones.

| key | default | what it does |
|---|---|---|
| `auto_reject` | `false` | Decline incoming calls automatically. |
| `reject_message` | `""` | After an auto-reject, text this to 1:1 callers. Empty sends nothing. |
| `except` | `[]` | Callers never auto-rejected: phones, JIDs or contact names. |

## `[media]`

| key | default | what it does |
|---|---|---|
| `auto_download` | `[]` | Message types to download as they arrive: `image`, `video`, `audio`, `voice`, `document`, `sticker`, or `"*"`. Files land in `~/.wassup/accounts/<name>/media/`. |
| `max_auto_download_mb` | `25` | Skip auto-downloads larger than this. |

## `[mcp]`

| key | default | what it does |
|---|---|---|
| `profile` | `"agent"` | Policy profile applied to every MCP tool call over stdio. |
| `fence` | `true` | Wrap text written by other people in a nonce-tagged block labelled as data (prompt-injection defence). |
| `approval_wait` | `"2m"` | How long a tool call waits for your approval before answering "still pending" (the draft stays valid). |
| `whatsapp_approvals` | `true` | When the AI app can't show an approval prompt, send it to your "Message yourself" chat; reply `yes <code>` or `no <code>`. |
| `trust_client_approval` | `true` | Let local MCP apps (Claude Desktop, Claude Code...) collect your approval in their own UI. The app relays your answer, so a malicious app could fake it; set `false` to always approve on your phone. |

## `[transcribe]`

| key | default | what it does |
|---|---|---|
| `engine` | `"auto"` | `auto` (whisper.cpp if installed, else the API if a key is set), `whisper.cpp`, `api` or `off`. |
| `model` | `"base"` | whisper.cpp model: `tiny`, `base`, `small`, `medium`, `large-v3-turbo`. Downloaded once to `~/.wassup/models`. Use `small` or larger for Hindi and other non-English speech. |
| `language` | `"auto"` | Language code like `en` or `hi`, or `auto`. |
| `auto` | `false` | Transcribe incoming voice notes as they arrive (emits `message.transcribed`). |
| `api_url` | OpenAI | Any OpenAI-compatible `/audio/transcriptions` endpoint (OpenAI, Groq, a local server). |
| `api_model` | `"whisper-1"` | Model name sent to the API. |
| `api_key_env` | `"OPENAI_API_KEY"` | Environment variable holding the API key (set it where the daemon runs). |

## `[remote]`: claude.ai on the web and phone

Set up with `wassup remote setup`; edit by hand only if you know the pieces.

| key | default | what it does |
|---|---|---|
| `enabled` | `false` | Serve MCP over HTTP with OAuth inside the daemon. |
| `listen` | `"127.0.0.1:8790"` | Local address. Keep it on localhost and expose it with a tunnel or proxy. |
| `public_url` | `""` | The `https://` origin clients use. Empty with `tunnel = "cloudflare"` means the tunnel reports it. |
| `tunnel` | `"none"` | `tailscale`, `cloudflare` (quick tunnel, URL changes per run) or `none`. |
| `profile` | `"agent"` | Policy profile for remote tool calls. |
| `token_ttl` | `"1h"` | Access token lifetime. |
| `refresh_ttl` | `"60d"` | Refresh token lifetime. Refresh tokens rotate on every use; a replayed one revokes the connection. |
| `trusted_redirect_hosts` | `["claude.ai", "claude.com"]` | Remote apps whose in-app approval prompts are trusted, because their tokens can only be delivered to that host. Every other remote app approves through your WhatsApp. |

Remote apps can only attach files from wassup's media folder and can't choose where downloads are written, so a prompt-injected agent can't send or overwrite local files.

## `[profiles.<name>]`: guard rails

Every mutating action (send, react, edit, delete, group changes, profile changes...) passes through a policy profile. The CLI uses `default` unless you pass `--profile` or set `WASSUP_PROFILE`. MCP uses `[mcp] profile`. You can define as many profiles as you like, for example a strict one for an unattended bot.

These are guard rails against mistakes, not a security sandbox: anything that can run `wassup` can also pass `--profile`.

| key | what it does |
|---|---|
| `mode` | `"open"` allows any chat. `"allowlist"` only allows chats listed in `allow`. `"read_only"` blocks every mutating action. |
| `allow` | Chats permitted in allowlist mode. Entries can be `+phone`, a JID, an exact contact or group name, or a glob over JIDs like `"*@g.us"` (all groups). Manage with `wassup allow add/rm/list`. |
| `deny` | Chats that are always blocked, in any mode. Manage with `wassup deny add/rm/list`. |
| `deny_actions` | Method names or globs to block, for example `["groups.*", "chats.delete", "auth.logout"]`. See `wassup schema --methods` for names. |
| `confirm_groups` | Sending into a group needs `--yes` (or your approval for AI apps). |
| `confirm_new_chats` | Messaging a number you have no history with needs confirmation. |
| `confirm_destructive` | Deleting messages or chats, leaving groups, removing members, resetting invite links and logging out need confirmation. |
| `confirm_bulk` | A single command touching more than this many chats needs confirmation. `0` disables. |
| `dry_run` | Every mutating call only reports what it would do. |
| `rate_per_minute`, `rate_per_hour` | Cap on outgoing messages for this profile. `0` = unlimited. Over the limit you get `rate_limited` with `retry_after_seconds`. |
| `rate_per_chat_per_minute` | Cap on messages to a single chat. |
| `min_interval` | Minimum gap between sends, for example `"2s"`. wassup waits instead of failing. |
| `approval` | `"always"`: every send waits for your OK. `"new_chats_and_groups"`: only sends to someone new or a group wait. `"none"`: no approval step. For the profile AI apps use, `wassup approval always\|new\|never` sets it (and the remote profile too). |
| `approval_timeout` | How long a waiting request stays approvable, for example `"30m"`. After that it expires and nothing is sent. |
| `warmup` | On a freshly linked account, run at 25% of the rate limits in week one and 50% in week two. |

Defaults: `default` is open with 30/min and 300/hour and no approval step. `agent` (used by AI apps) needs your approval for every message, asks before destructive actions or touching more than 5 chats, blocks `auth.logout`, allows 12/min and 120/hour, and spaces sends 1.5s apart. Both warm up on a new link.

Example: let an agent talk only to two people and one group, never delete anything:

```toml
[profiles.agent]
mode = "allowlist"
allow = ["+14155550123", "Mom", "120363025246125244@g.us"]
deny_actions = ["messages.delete", "chats.delete", "groups.leave", "auth.logout"]
confirm_groups = true
rate_per_minute = 6
```

## `[[webhooks]]`

POST every matching event to a URL. Bodies are `{"account": "...", "event": {...}}`. Failed deliveries retry after 2s, 8s and 30s.

| key | what it does |
|---|---|
| `url` | Where to POST. |
| `events` | Event types to send, e.g. `["message", "reaction", "call"]`. Empty = all. `"message.*"` matches `message.edited` etc. |
| `chats` | Only events from these chats (same formats as `allow`). Empty = all. |
| `secret` | If set, `X-Wassup-Signature: sha256=<hex HMAC of the body>` is added. |
| `include_from_me` | Also send messages you sent. |

```toml
[[webhooks]]
url = "http://localhost:8080/wassup"
events = ["message"]
secret = "change-me"
```

## Environment variables

| variable | same as |
|---|---|
| `WASSUP_HOME` | data and config folder (default `~/.wassup`) |
| `WASSUP_CONFIG` | config file path |
| `WASSUP_ACCOUNT` | `--account` |
| `WASSUP_PROFILE` | `--profile` |
| `WASSUP_JSON=1` | `--json` |
| `WASSUP_YES=1` | `--yes` |
| `WASSUP_DRY_RUN=1` | `--dry-run` |
| `WASSUP_NO_DAEMON=1` | `--no-daemon` |
| `WASSUP_DEBUG=1` | verbose daemon logs |
| `NO_COLOR` | disable colors |
