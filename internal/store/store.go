// Package store keeps the local copy of chats, messages, reactions, calls and the audit log.
// It is separate from whatsmeow's session database so it can be wiped without unlinking.
package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DSN builds a modernc SQLite connection string with the pragmas both databases need.
func DSN(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

type DB struct {
	db       *sql.DB
	auditMu  sync.Mutex
	auditKey []byte
}

const schema = `
CREATE TABLE IF NOT EXISTS chats (
	jid TEXT PRIMARY KEY,
	kind TEXT NOT NULL DEFAULT 'dm',
	name TEXT NOT NULL DEFAULT '',
	last_message_at INTEGER NOT NULL DEFAULT 0,
	last_message_id TEXT NOT NULL DEFAULT '',
	last_preview TEXT NOT NULL DEFAULT '',
	unread_count INTEGER NOT NULL DEFAULT 0,
	last_read_at INTEGER NOT NULL DEFAULT 0,
	archived INTEGER NOT NULL DEFAULT 0,
	pinned INTEGER NOT NULL DEFAULT 0,
	muted_until INTEGER NOT NULL DEFAULT 0,
	marked_unread INTEGER NOT NULL DEFAULT 0,
	ephemeral_seconds INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS chats_last ON chats(last_message_at);

CREATE TABLE IF NOT EXISTS messages (
	chat_jid TEXT NOT NULL,
	id TEXT NOT NULL,
	sender_jid TEXT NOT NULL DEFAULT '',
	sender_name TEXT NOT NULL DEFAULT '',
	sender_raw TEXT NOT NULL DEFAULT '',
	from_me INTEGER NOT NULL DEFAULT 0,
	ts INTEGER NOT NULL,
	type TEXT NOT NULL DEFAULT 'text',
	text TEXT NOT NULL DEFAULT '',
	media_mime TEXT NOT NULL DEFAULT '',
	media_name TEXT NOT NULL DEFAULT '',
	media_size INTEGER NOT NULL DEFAULT 0,
	media_path TEXT NOT NULL DEFAULT '',
	quoted_id TEXT NOT NULL DEFAULT '',
	mentions TEXT NOT NULL DEFAULT '',
	extra TEXT NOT NULL DEFAULT '',
	forwarded INTEGER NOT NULL DEFAULT 0,
	view_once INTEGER NOT NULL DEFAULT 0,
	edited INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0,
	starred INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT '',
	raw BLOB,
	PRIMARY KEY (chat_jid, id)
);
CREATE INDEX IF NOT EXISTS messages_chat_ts ON messages(chat_jid, ts);
CREATE INDEX IF NOT EXISTS messages_id ON messages(id);
CREATE INDEX IF NOT EXISTS messages_ts ON messages(ts);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(text, content='messages', content_rowid='rowid', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE OF text ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
	INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;

CREATE TABLE IF NOT EXISTS reactions (
	chat_jid TEXT NOT NULL,
	message_id TEXT NOT NULL,
	sender_jid TEXT NOT NULL,
	emoji TEXT NOT NULL,
	ts INTEGER NOT NULL,
	PRIMARY KEY (chat_jid, message_id, sender_jid)
);

CREATE TABLE IF NOT EXISTS poll_votes (
	chat_jid TEXT NOT NULL,
	message_id TEXT NOT NULL,
	voter_jid TEXT NOT NULL,
	options TEXT NOT NULL,
	ts INTEGER NOT NULL,
	PRIMARY KEY (chat_jid, message_id, voter_jid)
);

CREATE TABLE IF NOT EXISTS calls (
	id TEXT PRIMARY KEY,
	from_jid TEXT NOT NULL,
	group_jid TEXT NOT NULL DEFAULT '',
	media TEXT NOT NULL DEFAULT '',
	ts INTEGER NOT NULL,
	status TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts INTEGER NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	profile TEXT NOT NULL DEFAULT '',
	method TEXT NOT NULL,
	targets TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '',
	ok INTEGER NOT NULL,
	dry_run INTEGER NOT NULL DEFAULT 0,
	sends INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	hash TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts);

CREATE TABLE IF NOT EXISTS pending_edits (
	chat_jid TEXT NOT NULL,
	message_id TEXT NOT NULL,
	text TEXT NOT NULL,
	ts INTEGER NOT NULL,
	PRIMARY KEY (chat_jid, message_id)
);

CREATE TABLE IF NOT EXISTS kv (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init local store: %w", err)
	}
	d := &DB{db: db}
	if err := d.initExtra(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init local store: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error { return d.db.Close() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- messages ----------

type Message struct {
	Chat       string
	ID         string
	Sender     string
	SenderName string
	SenderRaw  string // sender exactly as WhatsApp addressed it (may be a LID); used to build message keys
	FromMe     bool
	Time       time.Time
	Type       string
	Text       string
	MediaMime  string
	MediaName  string
	MediaSize  int64
	MediaPath  string
	QuotedID   string
	Mentions   string // comma-separated JIDs
	Extra      string // JSON with type-specific fields
	Forwarded  bool
	ViewOnce   bool
	Edited     bool
	Revoked    bool
	Starred    bool
	Status     string
	Raw        []byte
}

const msgCols = `chat_jid, id, sender_jid, sender_name, sender_raw, from_me, ts, type, text, media_mime, media_name, media_size, media_path, quoted_id, mentions, extra, forwarded, view_once, edited, revoked, starred, status, raw`

func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	var ts int64
	var fromMe, fwd, vo, edited, revoked, starred int
	err := row.Scan(&m.Chat, &m.ID, &m.Sender, &m.SenderName, &m.SenderRaw, &fromMe, &ts, &m.Type, &m.Text, &m.MediaMime, &m.MediaName,
		&m.MediaSize, &m.MediaPath, &m.QuotedID, &m.Mentions, &m.Extra, &fwd, &vo, &edited, &revoked, &starred, &m.Status, &m.Raw)
	if err != nil {
		return nil, err
	}
	m.Time = time.Unix(ts, 0)
	m.FromMe, m.Forwarded, m.ViewOnce, m.Edited, m.Revoked, m.Starred = fromMe == 1, fwd == 1, vo == 1, edited == 1, revoked == 1, starred == 1
	return &m, nil
}

// UpsertMessage inserts or refreshes a message. It reports whether the row is new.
// Local-only state (media path, starred, delivery status, edits, revokes) is never overwritten by an older copy.
func (d *DB) UpsertMessage(ctx context.Context, m *Message) (bool, error) {
	var exists int
	_ = d.db.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE chat_jid=? AND id=?`, m.Chat, m.ID).Scan(&exists)
	_, err := d.db.ExecContext(ctx, `INSERT INTO messages (`+msgCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(chat_jid, id) DO UPDATE SET
			sender_jid=CASE WHEN excluded.sender_jid != '' THEN excluded.sender_jid ELSE sender_jid END,
			sender_name=CASE WHEN excluded.sender_name != '' THEN excluded.sender_name ELSE sender_name END,
			sender_raw=CASE WHEN sender_raw != '' THEN sender_raw ELSE excluded.sender_raw END,
			type=CASE WHEN edited=1 OR revoked=1 THEN type ELSE excluded.type END,
			text=CASE WHEN edited=1 OR revoked=1 THEN text ELSE excluded.text END,
			media_mime=excluded.media_mime, media_name=excluded.media_name, media_size=excluded.media_size,
			quoted_id=excluded.quoted_id, mentions=excluded.mentions, extra=excluded.extra,
			forwarded=excluded.forwarded, view_once=excluded.view_once,
			status=CASE WHEN status='' THEN excluded.status ELSE status END,
			raw=CASE WHEN revoked=1 OR edited=1 THEN raw ELSE excluded.raw END`,
		m.Chat, m.ID, m.Sender, m.SenderName, m.SenderRaw, b2i(m.FromMe), m.Time.Unix(), m.Type, m.Text, m.MediaMime, m.MediaName,
		m.MediaSize, m.MediaPath, m.QuotedID, m.Mentions, m.Extra, b2i(m.Forwarded), b2i(m.ViewOnce), b2i(m.Edited),
		b2i(m.Revoked), b2i(m.Starred), m.Status, m.Raw)
	return exists == 0, err
}

// GetMessage finds a message. chat may be empty, in which case the ID must be unique.
func (d *DB) GetMessage(ctx context.Context, chat, id string) (*Message, error) {
	if chat != "" {
		m, err := scanMessage(d.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE chat_jid=? AND id=?`, chat, id))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return m, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE id=? LIMIT 5`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, m)
	}
	if len(found) == 0 {
		return nil, nil
	}
	if len(found) > 1 {
		return nil, ErrAmbiguousID
	}
	return found[0], nil
}

var ErrAmbiguousID = errors.New("message ID exists in more than one chat")

type MessageQuery struct {
	Chat     string
	Sender   string
	FromMe   *bool
	Type     string
	Before   time.Time
	After    time.Time
	Limit    int
	Desc     bool
	FTS      string // full-text query, already escaped
	Like     string // fallback substring search
	NoStatus bool   // hide status@broadcast
}

func (d *DB) ListMessages(ctx context.Context, q MessageQuery) ([]*Message, error) {
	var where []string
	var args []any
	from := "messages"
	if q.FTS != "" {
		from = "messages JOIN messages_fts ON messages_fts.rowid = messages.rowid"
		where = append(where, "messages_fts MATCH ?")
		args = append(args, q.FTS)
	}
	if q.Like != "" {
		where = append(where, "messages.text LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(q.Like)+"%")
	}
	if q.Chat != "" {
		where = append(where, "messages.chat_jid = ?")
		args = append(args, q.Chat)
	} else if q.NoStatus {
		where = append(where, "messages.chat_jid != 'status@broadcast'")
	}
	if q.Sender != "" {
		where = append(where, "messages.sender_jid = ?")
		args = append(args, q.Sender)
	}
	if q.FromMe != nil {
		where = append(where, "messages.from_me = ?")
		args = append(args, b2i(*q.FromMe))
	}
	if q.Type != "" {
		where = append(where, "messages.type = ?")
		args = append(args, q.Type)
	}
	if !q.Before.IsZero() {
		where = append(where, "messages.ts < ?")
		args = append(args, q.Before.Unix())
	}
	if !q.After.IsZero() {
		where = append(where, "messages.ts > ?")
		args = append(args, q.After.Unix())
	}
	sqlStr := "SELECT " + prefixCols("messages.", msgCols) + " FROM " + from
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 30
	}
	// Fetch newest N, then flip to ascending unless the caller asked for descending.
	sqlStr += fmt.Sprintf(" ORDER BY messages.ts DESC, messages.rowid DESC LIMIT %d", limit)
	rows, err := d.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if !q.Desc {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, rows.Err()
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (d *DB) MarkRevoked(ctx context.Context, chat, id string) (bool, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE messages SET revoked=1, text='', raw=NULL WHERE chat_jid=? AND id=?`, chat, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (d *DB) EditMessage(ctx context.Context, chat, id, text string, raw []byte) (bool, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE messages SET edited=1, text=?, raw=COALESCE(?, raw) WHERE chat_jid=? AND id=?`, text, raw, chat, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (d *DB) DeleteMessage(ctx context.Context, chat, id string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM messages WHERE chat_jid=? AND id=?`, chat, id)
	return err
}

var statusRank = map[string]int{"": 0, "pending": 1, "sent": 2, "delivered": 3, "read": 4, "played": 5}

// SetStatus upgrades delivery status (never downgrades read to delivered).
func (d *DB) SetStatus(ctx context.Context, chat string, ids []string, status string) error {
	rank := statusRank[status]
	for _, id := range ids {
		var cur string
		err := d.db.QueryRowContext(ctx, `SELECT status FROM messages WHERE chat_jid=? AND id=?`, chat, id).Scan(&cur)
		if err != nil {
			continue
		}
		if statusRank[cur] < rank {
			if _, err := d.db.ExecContext(ctx, `UPDATE messages SET status=? WHERE chat_jid=? AND id=?`, status, chat, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *DB) SetMediaPath(ctx context.Context, chat, id, path string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE messages SET media_path=? WHERE chat_jid=? AND id=?`, path, chat, id)
	return err
}

func (d *DB) SetRaw(ctx context.Context, chat, id string, raw []byte) error {
	_, err := d.db.ExecContext(ctx, `UPDATE messages SET raw=? WHERE chat_jid=? AND id=?`, raw, chat, id)
	return err
}

func (d *DB) SetStarred(ctx context.Context, chat, id string, starred bool) error {
	_, err := d.db.ExecContext(ctx, `UPDATE messages SET starred=? WHERE chat_jid=? AND id=?`, b2i(starred), chat, id)
	return err
}

// OldestMessage returns the earliest stored message in a chat, used as the anchor for history requests.
func (d *DB) OldestMessage(ctx context.Context, chat string) (*Message, error) {
	m, err := scanMessage(d.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages WHERE chat_jid=? ORDER BY ts ASC LIMIT 1`, chat))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

func (d *DB) CountMessages(ctx context.Context, chat string) int {
	var n int
	if chat == "" {
		_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&n)
	} else {
		_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE chat_jid=?`, chat).Scan(&n)
	}
	return n
}

// UnreadIncoming lists incoming messages newer than the chat's last read mark.
func (d *DB) UnreadIncoming(ctx context.Context, chat string, limit int) ([]*Message, error) {
	var lastRead int64
	_ = d.db.QueryRowContext(ctx, `SELECT last_read_at FROM chats WHERE jid=?`, chat).Scan(&lastRead)
	rows, err := d.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages WHERE chat_jid=? AND from_me=0 AND ts>=? ORDER BY ts DESC LIMIT ?`, chat, lastRead, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// ---------- reactions & polls ----------

type Reaction struct {
	Chat, MessageID, Sender, Emoji string
	Time                           time.Time
}

func (d *DB) PutReaction(ctx context.Context, r Reaction) error {
	if r.Emoji == "" {
		_, err := d.db.ExecContext(ctx, `DELETE FROM reactions WHERE chat_jid=? AND message_id=? AND sender_jid=?`, r.Chat, r.MessageID, r.Sender)
		return err
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO reactions (chat_jid, message_id, sender_jid, emoji, ts) VALUES (?,?,?,?,?)
		ON CONFLICT(chat_jid, message_id, sender_jid) DO UPDATE SET emoji=excluded.emoji, ts=excluded.ts WHERE excluded.ts >= reactions.ts`,
		r.Chat, r.MessageID, r.Sender, r.Emoji, r.Time.Unix())
	return err
}

func (d *DB) Reactions(ctx context.Context, chat string, ids []string) (map[string][]Reaction, error) {
	out := map[string][]Reaction{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{chat}
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT message_id, sender_jid, emoji, ts FROM reactions WHERE chat_jid=? AND message_id IN (`+strings.Join(ph, ",")+`) ORDER BY ts`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Reaction
		var ts int64
		if err := rows.Scan(&r.MessageID, &r.Sender, &r.Emoji, &ts); err != nil {
			return nil, err
		}
		r.Chat, r.Time = chat, time.Unix(ts, 0)
		out[r.MessageID] = append(out[r.MessageID], r)
	}
	return out, nil
}

type PollVote struct {
	Chat, MessageID, Voter string
	Options                []string
	Time                   time.Time
}

func (d *DB) PutPollVote(ctx context.Context, v PollVote) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO poll_votes (chat_jid, message_id, voter_jid, options, ts) VALUES (?,?,?,?,?)
		ON CONFLICT(chat_jid, message_id, voter_jid) DO UPDATE SET options=excluded.options, ts=excluded.ts WHERE excluded.ts >= poll_votes.ts`,
		v.Chat, v.MessageID, v.Voter, strings.Join(v.Options, "\x1f"), v.Time.Unix())
	return err
}

func (d *DB) PollVotes(ctx context.Context, chat, id string) ([]PollVote, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT voter_jid, options, ts FROM poll_votes WHERE chat_jid=? AND message_id=?`, chat, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PollVote
	for rows.Next() {
		var v PollVote
		var opts string
		var ts int64
		if err := rows.Scan(&v.Voter, &opts, &ts); err != nil {
			return nil, err
		}
		if opts != "" {
			v.Options = strings.Split(opts, "\x1f")
		}
		v.Chat, v.MessageID, v.Time = chat, id, time.Unix(ts, 0)
		out = append(out, v)
	}
	return out, nil
}

// ---------- chats ----------

type Chat struct {
	JID           string
	Kind          string
	Name          string
	LastMessageAt time.Time
	LastMessageID string
	LastPreview   string
	Unread        int
	LastReadAt    time.Time
	Archived      bool
	Pinned        bool
	MutedUntil    int64
	MarkedUnread  bool
	Ephemeral     int
}

const chatCols = `jid, kind, name, last_message_at, last_message_id, last_preview, unread_count, last_read_at, archived, pinned, muted_until, marked_unread, ephemeral_seconds`

func scanChat(row interface{ Scan(...any) error }) (*Chat, error) {
	var c Chat
	var last, lastRead int64
	var arch, pin, mu int
	if err := row.Scan(&c.JID, &c.Kind, &c.Name, &last, &c.LastMessageID, &c.LastPreview, &c.Unread, &lastRead, &arch, &pin, &c.MutedUntil, &mu, &c.Ephemeral); err != nil {
		return nil, err
	}
	if last > 0 {
		c.LastMessageAt = time.Unix(last, 0)
	}
	if lastRead > 0 {
		c.LastReadAt = time.Unix(lastRead, 0)
	}
	c.Archived, c.Pinned, c.MarkedUnread = arch == 1, pin == 1, mu == 1
	return &c, nil
}

func (d *DB) EnsureChat(ctx context.Context, jid, kind string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO chats (jid, kind, updated_at) VALUES (?,?,?) ON CONFLICT(jid) DO NOTHING`, jid, kind, time.Now().Unix())
	return err
}

// TouchChat records a new message in a chat. Older messages (history sync) never move the last-message pointer back.
func (d *DB) TouchChat(ctx context.Context, jid, kind string, ts time.Time, msgID, preview string, incomingUnread bool) error {
	if err := d.EnsureChat(ctx, jid, kind); err != nil {
		return err
	}
	inc := 0
	if incomingUnread {
		inc = 1
	}
	_, err := d.db.ExecContext(ctx, `UPDATE chats SET
		last_message_id = CASE WHEN ? >= last_message_at THEN ? ELSE last_message_id END,
		last_preview = CASE WHEN ? >= last_message_at THEN ? ELSE last_preview END,
		last_message_at = MAX(last_message_at, ?),
		unread_count = unread_count + ?,
		updated_at = ?
		WHERE jid = ?`, ts.Unix(), msgID, ts.Unix(), preview, ts.Unix(), inc, time.Now().Unix(), jid)
	return err
}

func (d *DB) SetChatName(ctx context.Context, jid, kind, name string) error {
	if name == "" {
		return d.EnsureChat(ctx, jid, kind)
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO chats (jid, kind, name, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(jid) DO UPDATE SET name=excluded.name, kind=excluded.kind, updated_at=excluded.updated_at`, jid, kind, name, time.Now().Unix())
	return err
}

type ChatFlags struct {
	Archived     *bool
	Pinned       *bool
	MutedUntil   *int64
	MarkedUnread *bool
	Unread       *int
	LastReadAt   *time.Time
	Ephemeral    *int
}

func (d *DB) SetChatFlags(ctx context.Context, jid, kind string, f ChatFlags) error {
	if err := d.EnsureChat(ctx, jid, kind); err != nil {
		return err
	}
	var sets []string
	var args []any
	if f.Archived != nil {
		sets, args = append(sets, "archived=?"), append(args, b2i(*f.Archived))
	}
	if f.Pinned != nil {
		sets, args = append(sets, "pinned=?"), append(args, b2i(*f.Pinned))
	}
	if f.MutedUntil != nil {
		sets, args = append(sets, "muted_until=?"), append(args, *f.MutedUntil)
	}
	if f.MarkedUnread != nil {
		sets, args = append(sets, "marked_unread=?"), append(args, b2i(*f.MarkedUnread))
	}
	if f.Unread != nil {
		sets, args = append(sets, "unread_count=?"), append(args, *f.Unread)
	}
	if f.LastReadAt != nil {
		sets, args = append(sets, "last_read_at=MAX(last_read_at, ?)"), append(args, f.LastReadAt.Unix())
	}
	if f.Ephemeral != nil {
		sets, args = append(sets, "ephemeral_seconds=?"), append(args, *f.Ephemeral)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, jid)
	_, err := d.db.ExecContext(ctx, `UPDATE chats SET `+strings.Join(sets, ", ")+` WHERE jid=?`, args...)
	return err
}

func (d *DB) GetChat(ctx context.Context, jid string) (*Chat, error) {
	c, err := scanChat(d.db.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE jid=?`, jid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

type ChatQuery struct {
	Kind     string
	Unread   bool
	Archived string // exclude, include, only
	Limit    int
}

func (d *DB) ListChats(ctx context.Context, q ChatQuery) ([]*Chat, error) {
	where := []string{"jid != 'status@broadcast'"}
	var args []any
	if q.Kind != "" {
		where, args = append(where, "kind=?"), append(args, q.Kind)
	}
	if q.Unread {
		where = append(where, "(unread_count > 0 OR marked_unread = 1)")
	}
	switch q.Archived {
	case "only":
		where = append(where, "archived=1")
	case "include":
	default:
		where = append(where, "archived=0")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `SELECT `+chatCols+` FROM chats WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY pinned DESC, last_message_at DESC LIMIT %d`, limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AllChatNames is used by recipient resolution.
func (d *DB) AllChatNames(ctx context.Context) (map[string]Chat, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+chatCols+` FROM chats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out[c.JID] = *c
	}
	return out, nil
}

func (d *DB) DeleteChat(ctx context.Context, jid string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM messages WHERE chat_jid=?`, jid); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, `DELETE FROM reactions WHERE chat_jid=?`, jid)
	_, _ = d.db.ExecContext(ctx, `DELETE FROM poll_votes WHERE chat_jid=?`, jid)
	_, err := d.db.ExecContext(ctx, `DELETE FROM chats WHERE jid=?`, jid)
	return err
}

func (d *DB) ClearChat(ctx context.Context, jid string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM messages WHERE chat_jid=?`, jid)
	return err
}

func (d *DB) CountChats(ctx context.Context) int {
	var n int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats`).Scan(&n)
	return n
}

// ---------- calls ----------

type Call struct {
	ID, From, Group, Media, Status string
	Time                           time.Time
}

func (d *DB) PutCall(ctx context.Context, c Call) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO calls (id, from_jid, group_jid, media, ts, status) VALUES (?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status,
			media=CASE WHEN excluded.media != '' THEN excluded.media ELSE media END`,
		c.ID, c.From, c.Group, c.Media, c.Time.Unix(), c.Status)
	return err
}

func (d *DB) GetCall(ctx context.Context, id string) (*Call, error) {
	var c Call
	var ts int64
	err := d.db.QueryRowContext(ctx, `SELECT id, from_jid, group_jid, media, ts, status FROM calls WHERE id=?`, id).Scan(&c.ID, &c.From, &c.Group, &c.Media, &ts, &c.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	c.Time = time.Unix(ts, 0)
	return &c, nil
}

func (d *DB) ListCalls(ctx context.Context, limit int) ([]Call, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, from_jid, group_jid, media, ts, status FROM calls ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Call
	for rows.Next() {
		var c Call
		var ts int64
		if err := rows.Scan(&c.ID, &c.From, &c.Group, &c.Media, &ts, &c.Status); err != nil {
			return nil, err
		}
		c.Time = time.Unix(ts, 0)
		out = append(out, c)
	}
	return out, nil
}

// ---------- audit & rate limits ----------

type Audit struct {
	Time    time.Time
	Source  string
	Profile string
	Method  string
	Targets string
	Params  string
	OK      bool
	DryRun  bool
	Sends   int
	Error   string
}

// SetAuditKey enables the tamper-evident hash chain: every row's hash covers the previous row's.
func (d *DB) SetAuditKey(key []byte) { d.auditKey = key }

// auditDigest signs an unambiguous JSON encoding of the row, chained to the previous hash.
func auditDigest(key []byte, prev string, ts int64, a Audit) string {
	b, _ := json.Marshal([]any{prev, ts, a.Source, a.Profile, a.Method, a.Targets, a.Params, a.OK, a.DryRun, a.Sends, a.Error})
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

func (d *DB) AddAudit(ctx context.Context, a Audit) error {
	d.auditMu.Lock()
	defer d.auditMu.Unlock()
	ts := a.Time.UnixMilli()
	hash := ""
	if d.auditKey != nil {
		var prev string
		_ = d.db.QueryRowContext(ctx, `SELECT hash FROM audit ORDER BY id DESC LIMIT 1`).Scan(&prev)
		hash = auditDigest(d.auditKey, prev, ts, a)
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO audit (ts, source, profile, method, targets, params, ok, dry_run, sends, error, hash) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		ts, a.Source, a.Profile, a.Method, a.Targets, a.Params, b2i(a.OK), b2i(a.DryRun), a.Sends, a.Error, hash)
	return err
}

// VerifyAudit recomputes the chain. It returns the number of rows checked and the ID of the
// first row whose hash doesn't match (0 if the log is intact).
func (d *DB) VerifyAudit(ctx context.Context) (int, int64, error) {
	if d.auditKey == nil {
		return 0, 0, errors.New("audit key not loaded")
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, ts, source, profile, method, targets, params, ok, dry_run, sends, error, hash FROM audit ORDER BY id`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	prev, n := "", 0
	for rows.Next() {
		var id, ts int64
		var a Audit
		var ok, dry int
		var hash string
		if err := rows.Scan(&id, &ts, &a.Source, &a.Profile, &a.Method, &a.Targets, &a.Params, &ok, &dry, &a.Sends, &a.Error, &hash); err != nil {
			return n, 0, err
		}
		a.OK, a.DryRun = ok == 1, dry == 1
		n++
		if want := auditDigest(d.auditKey, prev, ts, a); !hmac.Equal([]byte(want), []byte(hash)) {
			return n, id, nil
		}
		prev = hash
	}
	return n, 0, rows.Err()
}

func (d *DB) ListAudit(ctx context.Context, limit int) ([]Audit, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `SELECT ts, source, profile, method, targets, ok, dry_run, sends, error FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audit
	for rows.Next() {
		var a Audit
		var ts int64
		var ok, dry int
		if err := rows.Scan(&ts, &a.Source, &a.Profile, &a.Method, &a.Targets, &ok, &dry, &a.Sends, &a.Error); err != nil {
			return nil, err
		}
		a.Time, a.OK, a.DryRun = time.UnixMilli(ts), ok == 1, dry == 1
		out = append(out, a)
	}
	return out, nil
}

// CountSends sums successful outgoing messages since a time. target narrows to one chat ("" = all).
func (d *DB) CountSends(ctx context.Context, profile string, since time.Time, target string) int {
	var n sql.NullInt64
	if target == "" {
		_ = d.db.QueryRowContext(ctx, `SELECT SUM(sends) FROM audit WHERE ok=1 AND dry_run=0 AND profile=? AND ts>=?`, profile, since.UnixMilli()).Scan(&n)
	} else {
		_ = d.db.QueryRowContext(ctx, `SELECT SUM(sends) FROM audit WHERE ok=1 AND dry_run=0 AND profile=? AND ts>=? AND targets=?`, profile, since.UnixMilli(), target).Scan(&n)
	}
	return int(n.Int64)
}

// OldestSendSince returns the time of the oldest counted send in the window, to compute retry-after.
func (d *DB) OldestSendSince(ctx context.Context, profile string, since time.Time) time.Time {
	var ts sql.NullInt64
	_ = d.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM audit WHERE ok=1 AND dry_run=0 AND sends>0 AND profile=? AND ts>=?`, profile, since.UnixMilli()).Scan(&ts)
	if !ts.Valid {
		return time.Time{}
	}
	return time.UnixMilli(ts.Int64)
}

func (d *DB) LastSend(ctx context.Context, profile string) time.Time {
	var ts sql.NullInt64
	_ = d.db.QueryRowContext(ctx, `SELECT MAX(ts) FROM audit WHERE ok=1 AND dry_run=0 AND sends>0 AND profile=?`, profile).Scan(&ts)
	if !ts.Valid {
		return time.Time{}
	}
	return time.UnixMilli(ts.Int64)
}

// ---------- edits that arrive before their original ----------

// PutPendingEdit remembers an edit whose original message hasn't been stored yet (newest wins).
func (d *DB) PutPendingEdit(ctx context.Context, chat, id, text string, ts time.Time) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO pending_edits (chat_jid, message_id, text, ts) VALUES (?,?,?,?)
		ON CONFLICT(chat_jid, message_id) DO UPDATE SET text=excluded.text, ts=excluded.ts WHERE excluded.ts >= pending_edits.ts`,
		chat, id, text, ts.Unix())
	return err
}

// TakePendingEdit returns and clears a pending edit for a message, if any.
func (d *DB) TakePendingEdit(ctx context.Context, chat, id string) (string, bool) {
	var text string
	if err := d.db.QueryRowContext(ctx, `SELECT text FROM pending_edits WHERE chat_jid=? AND message_id=?`, chat, id).Scan(&text); err != nil {
		return "", false
	}
	_, _ = d.db.ExecContext(ctx, `DELETE FROM pending_edits WHERE chat_jid=? AND message_id=?`, chat, id)
	return text, true
}

// ---------- kv ----------

func (d *DB) Get(ctx context.Context, key string) string {
	var v string
	_ = d.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	return v
}

func (d *DB) Set(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO kv (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// FTSQuery turns free text into a safe FTS5 query: each bare word becomes a quoted prefix token,
// quoted phrases stay phrases, and a trailing * keeps prefix matching.
func FTSQuery(q string) string {
	var parts []string
	q = strings.TrimSpace(q)
	for len(q) > 0 {
		if q[0] == '"' {
			end := strings.IndexByte(q[1:], '"')
			if end < 0 {
				q = q[1:]
				continue
			}
			phrase := strings.TrimSpace(q[1 : end+1])
			if phrase != "" {
				parts = append(parts, `"`+strings.ReplaceAll(phrase, `"`, `""`)+`"`)
			}
			q = strings.TrimSpace(q[end+2:])
			continue
		}
		sp := strings.IndexAny(q, " \t\n")
		word := q
		if sp >= 0 {
			word, q = q[:sp], strings.TrimSpace(q[sp:])
		} else {
			q = ""
		}
		prefix := strings.HasSuffix(word, "*")
		word = strings.Trim(word, `*"`)
		if word == "" {
			continue
		}
		tok := `"` + strings.ReplaceAll(word, `"`, `""`) + `"`
		if prefix {
			tok += "*"
		}
		parts = append(parts, tok)
	}
	return strings.Join(parts, " ")
}
