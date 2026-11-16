<p align="center"><img src="brand/mark.svg" width="88" alt="wassup logo: a speech bubble with two ticks cut out of it, forming a w"></p>

# wassup

WhatsApp from your terminal and from your AI apps. Claude (web, Desktop, phone, Code), Codex, Cursor and scripts can read, search, send and wait for messages on your own account, with your approval where it matters.

```bash
wassup setup                                   # link your phone and connect your AI apps, in plain words
wassup send "Mom" "landed safely"
wassup send Alex "dinner at 8?" --wait 10m     # send, then block until Alex replies
wassup send Team "standup in 5" --in 25m       # schedule it
wassup chats --unread
wassup search invoice --after 30d
wassup transcribe 3EB0C431D5F7A1B2C3D4         # voice note to text, locally
wassup connect claude-desktop                  # WhatsApp tools inside Claude
```

wassup links to your account as a device, the same way WhatsApp Web does. After one scan you don't need the phone app, a browser or WhatsApp Desktop. It's a single Go binary: no Node, Chrome or Python.

## Use it from Claude and other AI apps

| App | How |
|---|---|
| Claude Desktop | `wassup connect claude-desktop`, then restart Claude |
| Claude Code | `wassup connect claude-code` (and `wassup skill install` to teach it the CLI) |
| claude.ai on the web and phone | `wassup remote setup`, then add the printed URL under Settings > Connectors |
| Codex | `wassup connect codex` |
| Cursor | `wassup connect cursor` |
| Anything that speaks MCP | `wassup mcp` (stdio) or the remote URL (HTTP + OAuth) |

What the AI gets:

- **About 30 tools** (about 50 with `--all-tools`, and `whatsapp_call` reaches all 78 methods either way): `catch_up` for an unread digest across chats, `read_chat`, `search_messages`, `send_message`, `wait_for_reply`, `schedule_message`, reactions, edits, groups and more.
- **Eyes and ears.** `view_media` shows photos, stickers and video frames to the model as images, reads documents (PDF text included), and transcribes voice notes.
- **Resources and prompts.** `wassup://chats` and `wassup://chat/{jid}` with live update notifications, plus ready-made prompts: catch me up, draft a reply in my style, summarise a chat, find something.
- **Approval in the chat.** When your policy wants a human OK, Claude shows the exact message with an Approve button and an editable text field. Clients without that UI fall back to a prompt in your own WhatsApp "Message yourself" chat: reply `yes 4821` from your phone. The agent can never approve its own request.

### claude.ai on the web and phone

`wassup remote setup` serves the same tools over HTTPS with a built-in OAuth server and exposes them with Tailscale Funnel (stable `https://<machine>.<tailnet>.ts.net` URL), a Cloudflare tunnel, or your own reverse proxy (`--url`). Signing in sends a one-time code to your WhatsApp "Message yourself" chat, so only the owner of the account can connect. Tokens rotate, a replayed refresh token cuts the connection off, and `wassup remote revoke` disconnects any app.

Every person runs their own wassup. There's no shared server and nobody else's account is involved.

## What it can do

- **Messages:** text, replies with quotes, @mentions (including hidden mention-all), link previews, edits, delete for everyone, forward, pin, star, read receipts, typing indicators, scheduled sends.
- **Media:** images, video, GIFs, voice notes, audio, documents, stickers, view-once. HEIC and PNG become JPEG, any audio becomes an Opus voice note with a real waveform, any image becomes a sticker. Downloads ask the sender's phone to re-upload media that expired on WhatsApp's servers.
- **Voice notes to text:** local whisper.cpp (private, free) or any OpenAI-compatible API. Optional auto-transcription as they arrive.
- **Polls, locations, contact cards.** Poll votes are decrypted and tallied.
- **Groups and communities:** create, rename, describe, photo, settings, members, admins, invite links, join requests, leave, community links.
- **Channels:** follow, read posts, react, mute, create.
- **Status (stories):** post text with a background color, images or video.
- **Chats:** archive, pin, mute, mark unread, disappearing messages, delete, synced to your other devices.
- **Your account:** name, about, photo, every privacy setting, block list.
- **Calls:** wassup can't carry call audio (no open WhatsApp library can), but it logs incoming calls, streams them as events, and can auto-reject with a text reply.
- **Search** over everything it has seen, **history** backfill from your phone, **live events** and **webhooks** with HMAC signatures, **multiple accounts**.

## Install

**Mac app (easiest).** Download `wassup.dmg` from the [releases page](https://github.com/tanishqagg-dev/wassup/releases) once it's listed there (the first notarized build is on its way), drag it to Applications and open it. A setup window links your phone with a QR code, connects Claude, Codex and Cursor with one click each, and asks how careful your AI should be. After that it lives in the menu bar: anything your AI wants to send shows up there and as a notification with Approve and Decline. The command line tool is inside, and the menu has "Install command line tool…".

**Command line:**

```bash
brew install tanishqagg-dev/tap/wassup          # macOS (and Linux Homebrew)
curl -fsSL https://raw.githubusercontent.com/tanishqagg-dev/wassup/main/install.sh | sh
go install github.com/tanishqagg-dev/wassup/cmd/wassup@latest   # Go 1.26+
```

Or download a signed archive from the releases page (see [docs/releasing.md](docs/releasing.md) to verify it).

Optional tools: `ffmpeg` (voice notes from any audio, video, thumbnails), `cwebp` (stickers), `whisper-cpp` (local transcription), `pdftotext` (PDF reading for AI apps), `tailscale` or `cloudflared` (claude.ai on the web). `wassup doctor` lists what's missing.

## Link your account

`wassup setup` walks through linking, the background service, connecting AI apps and the approval level, one question at a time. It's safe to re-run; finished steps are skipped. Or do the steps yourself:

```bash
wassup login                 # QR code: phone > Settings > Linked devices > Link a device
wassup login --phone +14155550123    # or an 8-character pairing code
wassup daemon install        # stay connected across reboots
```

## Everyday use

```bash
wassup chats                          # recent chats with unread counts
wassup messages "Mom" -n 50           # read a conversation
wassup messages --unread "Team"
wassup read "Team"                    # blue ticks
wassup send "+919876543210" "hi"      # any number with country code
wassup send me "note to self"
wassup send "Rahul" -f memo.m4a --voice
wassup send "Mom" "Happy birthday!" --at "tomorrow 9am"
wassup schedule                       # what's queued
wassup poll create "Team" "Lunch?" Pizza Sushi Tacos --multi
wassup group create "Trip 2026" "Alice" "+14155550123"
wassup story post "Shipping today" --bg "#1F2A44"
wassup download <message-id>
wassup drafts                         # anything waiting for your approval
```

Recipients can be a phone number with country code, an exact contact or group name, a JID, or `me`. If a name matches more than one chat, wassup lists the candidates and stops. It never guesses who to message.

Every command has `--help` with examples, and `wassup schema` prints every command, flag and method as JSON.

## For scripts and agents using the CLI

- `--json` everywhere: `{"ok":true,"data":...}` or `{"ok":false,"error":{"code","message","hint","details"}}`.
- Stable exit codes: 0 ok, 2 usage, 3 not logged in, 4 not found, 5 ambiguous name, 6 policy/approval/rate limit, 7 timeout, 8 not connected, 9 WhatsApp rejected it.
- `wassup send X "..." --wait 10m` returns the reply; `wassup wait --chat X --match "yes|no"` blocks for one.
- `--dry-run` describes any action without doing it.
- `wassup skill install` writes a `SKILL.md` that teaches coding agents the rules.

## Safety

Letting an AI message real people deserves care. wassup layers it:

- **Approvals.** A policy profile can require your OK for every message (`approval = "always"`, the default for AI apps), only for new chats and groups, or never. Switch with `wassup approval always|new|never`. Pending actions are listed by `wassup drafts`.
- **Prompt-injection fencing.** Text other people wrote reaches the model inside a tagged block with a random nonce, labelled as data. A message can't fake the end of the block.
- **Allowlists, deny lists, blocked actions, confirmations** for groups, strangers, bulk sends and destructive actions.
- **Rate limits with a warmup.** A freshly linked account starts at 25% of its limits in week one and 50% in week two, then ramps to full.
- **Tamper-evident audit log.** Every action is recorded with the profile that allowed it, in an HMAC hash chain: `wassup audit verify` detects edits.

```toml
[profiles.agent]
mode = "allowlist"
allow = ["+14155550123", "Mom", "*@g.us"]
approval = "always"
deny_actions = ["groups.leave", "chats.delete", "auth.logout"]
rate_per_minute = 12
warmup = true
```

Where the boundaries are:

- **Remote MCP (claude.ai and other HTTPS clients)** is a real boundary. The agent only has the tools, can't pick a profile or approve itself, can only attach files from wassup's media folder, and only first-party clients (claude.ai by default) may relay in-app approvals; every other app approves on your phone.
- **Local MCP (Claude Desktop, Code)** can't approve its own requests either, but the app itself relays your approval answer, so only connect apps you trust (or set `trust_client_approval = false`).
- **A local agent with shell access** runs as you. It could run `wassup drafts approve` or edit the config, so profiles are guard rails against mistakes there, not a sandbox.

## Why it stays connected

- One daemon per account behind a file lock holds the single connection WhatsApp allows; every command and AI app talks to it.
- Automatic reconnects, a watchdog for sockets that die quietly, and self-updating of the WhatsApp Web version when WhatsApp says it's outdated.
- Every message, edit, revoke, reaction, receipt and vote is stored locally as it arrives; offline messages are fetched on reconnect.
- `wassup doctor` names the fix for each problem.

## Configuration

`~/.wassup/config.toml` holds daemon settings, link previews, number verification, call auto-reject, media auto-download, transcription, MCP and remote settings, policy profiles and webhooks. See [docs/configuration.md](docs/configuration.md) or run `wassup config docs`. Changes apply within a second.

## Data and privacy

Everything stays on your machine in `~/.wassup`: session keys, a SQLite store of messages, downloaded media, transcripts. Files are owner-only. Nothing leaves except traffic to WhatsApp, link-preview fetches for URLs you send, an optional transcription API, webhooks you configure, and AI apps you connect. `wassup logout --wipe` unlinks and deletes the local data.

## Is this allowed?

wassup is unofficial. It speaks WhatsApp's multi-device protocol through [whatsmeow](https://github.com/tulir/whatsmeow), the library behind the Beeper and mautrix bridges, and isn't affiliated with WhatsApp or Meta.

WhatsApp bans numbers that behave like spam: bulk messages to people who haven't saved you, many new groups at once, identical messages to many chats. Use it the way you'd use your phone. The rate limits, warmup and `send-many` pacing help, but can't guarantee anything. For heavy automation, use a separate number.

## How it works

```
wassup CLI ───────┐
wassup mcp (stdio)┼── unix socket ──> wassup daemon ──> whatsmeow ──> WhatsApp
                  │                     │
claude.ai ── HTTPS + OAuth ─────────────┤  remote MCP (optional)
                                        ├── session.db  (whatsmeow keys)
                                        ├── wassup.db   (messages, FTS5, drafts, schedule, audit chain)
                                        └── webhooks, approvals, scheduler, auto-reject, transcription
```

The CLI, both MCP transports and the socket call the same method registry, so every feature is available everywhere with the same parameters, errors and policy checks.

## Development

```bash
make build      # bin/wassup
make test       # unit and integration tests (no WhatsApp account needed)
make install    # copies bin/wassup to ~/.local/bin and restarts a running daemon
```

## License

MIT. whatsmeow is MPL-2.0.
