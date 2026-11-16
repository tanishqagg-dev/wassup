package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const extraSchema = `
CREATE TABLE IF NOT EXISTS drafts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT NOT NULL,
	created INTEGER NOT NULL,
	expires INTEGER NOT NULL,
	source TEXT NOT NULL,
	profile TEXT NOT NULL,
	method TEXT NOT NULL,
	params TEXT NOT NULL,
	summary TEXT NOT NULL,
	preview TEXT NOT NULL,
	targets TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'pending',
	result TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	resolved_at INTEGER NOT NULL DEFAULT 0,
	resolved_by TEXT NOT NULL DEFAULT '',
	notified INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS drafts_status ON drafts(status);

CREATE TABLE IF NOT EXISTS scheduled (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	created INTEGER NOT NULL,
	due INTEGER NOT NULL,
	source TEXT NOT NULL,
	profile TEXT NOT NULL,
	method TEXT NOT NULL,
	params TEXT NOT NULL,
	summary TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	result TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	sent_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS scheduled_due ON scheduled(status, due);

CREATE TABLE IF NOT EXISTS transcripts (
	chat_jid TEXT NOT NULL,
	message_id TEXT NOT NULL,
	text TEXT NOT NULL,
	engine TEXT NOT NULL,
	language TEXT NOT NULL DEFAULT '',
	ts INTEGER NOT NULL,
	PRIMARY KEY (chat_jid, message_id)
);

CREATE TABLE IF NOT EXISTS oauth_clients (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	redirect_uris TEXT NOT NULL,
	created INTEGER NOT NULL,
	kind TEXT NOT NULL DEFAULT 'dcr'
);

CREATE TABLE IF NOT EXISTS oauth_tokens (
	hash TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	client_id TEXT NOT NULL,
	client_name TEXT NOT NULL DEFAULT '',
	family TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL,
	expires INTEGER NOT NULL,
	last_used INTEGER NOT NULL DEFAULT 0,
	used INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0,
	trusted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS oauth_tokens_family ON oauth_tokens(family);

CREATE TABLE IF NOT EXISTS oauth_families (
	family TEXT PRIMARY KEY,
	client_id TEXT NOT NULL,
	created INTEGER NOT NULL,
	revoked INTEGER NOT NULL DEFAULT 0
);
`

func (d *DB) initExtra() error {
	_, err := d.db.Exec(extraSchema)
	return err
}

// ---------- drafts (actions waiting for a human OK) ----------

type Draft struct {
	ID         int64
	Code       string
	Created    time.Time
	Expires    time.Time
	Source     string
	Profile    string
	Method     string
	Params     string
	Summary    string
	Preview    string
	Targets    string
	Reason     string
	Status     string // pending, sent, failed, rejected, expired
	Result     string
	Error      string
	ResolvedAt time.Time
	ResolvedBy string
	Notified   bool
}

const draftCols = `id, code, created, expires, source, profile, method, params, summary, preview, targets, reason, status, result, error, resolved_at, resolved_by, notified`

func scanDraft(row interface{ Scan(...any) error }) (*Draft, error) {
	var dr Draft
	var created, expires, resolved int64
	var notified int
	if err := row.Scan(&dr.ID, &dr.Code, &created, &expires, &dr.Source, &dr.Profile, &dr.Method, &dr.Params, &dr.Summary, &dr.Preview,
		&dr.Targets, &dr.Reason, &dr.Status, &dr.Result, &dr.Error, &resolved, &dr.ResolvedBy, &notified); err != nil {
		return nil, err
	}
	dr.Created, dr.Expires = time.Unix(created, 0), time.Unix(expires, 0)
	if resolved > 0 {
		dr.ResolvedAt = time.Unix(resolved, 0)
	}
	dr.Notified = notified == 1
	return &dr, nil
}

func (d *DB) CreateDraft(ctx context.Context, dr *Draft) error {
	res, err := d.db.ExecContext(ctx, `INSERT INTO drafts (code, created, expires, source, profile, method, params, summary, preview, targets, reason) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		dr.Code, dr.Created.Unix(), dr.Expires.Unix(), dr.Source, dr.Profile, dr.Method, dr.Params, dr.Summary, dr.Preview, dr.Targets, dr.Reason)
	if err != nil {
		return err
	}
	dr.ID, _ = res.LastInsertId()
	dr.Status = "pending"
	return nil
}

func (d *DB) GetDraft(ctx context.Context, id int64) (*Draft, error) {
	dr, err := scanDraft(d.db.QueryRowContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return dr, err
}

// PendingDraftByCode finds the pending draft a WhatsApp reply refers to.
func (d *DB) PendingDraftByCode(ctx context.Context, code string) (*Draft, error) {
	dr, err := scanDraft(d.db.QueryRowContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE code=? AND status='pending' ORDER BY id DESC LIMIT 1`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return dr, err
}

func (d *DB) ListDrafts(ctx context.Context, status string, limit int) ([]*Draft, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + draftCols + ` FROM drafts`
	args := []any{}
	if status != "" && status != "all" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Draft
	for rows.Next() {
		dr, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dr)
	}
	return out, nil
}

// ClaimDraft moves a pending draft to "approved" exactly once; false means someone else got it first or it expired.
func (d *DB) ClaimDraft(ctx context.Context, id int64, by string) (bool, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE drafts SET status='approved', resolved_at=?, resolved_by=? WHERE id=? AND status='pending' AND expires>=?`,
		time.Now().Unix(), by, id, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (d *DB) FinishDraft(ctx context.Context, id int64, status, result, errMsg, by string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE drafts SET status=?, result=?, error=?, resolved_at=?, resolved_by=CASE WHEN ?='' THEN resolved_by ELSE ? END WHERE id=?`,
		status, result, errMsg, time.Now().Unix(), by, by, id)
	return err
}

func (d *DB) RejectDraft(ctx context.Context, id int64, by string) (bool, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE drafts SET status='rejected', resolved_at=?, resolved_by=? WHERE id=? AND status='pending'`, time.Now().Unix(), by, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (d *DB) MarkDraftNotified(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE drafts SET notified=1 WHERE id=?`, id)
	return err
}

// ExpireDrafts marks overdue pending drafts expired and returns them.
func (d *DB) ExpireDrafts(ctx context.Context) ([]*Draft, error) {
	now := time.Now().Unix()
	rows, err := d.db.QueryContext(ctx, `SELECT `+draftCols+` FROM drafts WHERE status='pending' AND expires<?`, now)
	if err != nil {
		return nil, err
	}
	var out []*Draft
	for rows.Next() {
		if dr, err := scanDraft(rows); err == nil {
			out = append(out, dr)
		}
	}
	rows.Close()
	_, err = d.db.ExecContext(ctx, `UPDATE drafts SET status='expired', resolved_at=? WHERE status='pending' AND expires<?`, now, now)
	return out, err
}

// ---------- scheduled sends ----------

type Scheduled struct {
	ID      int64
	Created time.Time
	Due     time.Time
	Source  string
	Profile string
	Method  string
	Params  string
	Summary string
	Status  string // pending, sent, failed, cancelled, missed
	Result  string
	Error   string
	SentAt  time.Time
}

const schedCols = `id, created, due, source, profile, method, params, summary, status, result, error, sent_at`

func scanSched(row interface{ Scan(...any) error }) (*Scheduled, error) {
	var s Scheduled
	var created, due, sent int64
	if err := row.Scan(&s.ID, &created, &due, &s.Source, &s.Profile, &s.Method, &s.Params, &s.Summary, &s.Status, &s.Result, &s.Error, &sent); err != nil {
		return nil, err
	}
	s.Created, s.Due = time.Unix(created, 0), time.Unix(due, 0)
	if sent > 0 {
		s.SentAt = time.Unix(sent, 0)
	}
	return &s, nil
}

func (d *DB) CreateScheduled(ctx context.Context, s *Scheduled) error {
	res, err := d.db.ExecContext(ctx, `INSERT INTO scheduled (created, due, source, profile, method, params, summary) VALUES (?,?,?,?,?,?,?)`,
		time.Now().Unix(), s.Due.Unix(), s.Source, s.Profile, s.Method, s.Params, s.Summary)
	if err != nil {
		return err
	}
	s.ID, _ = res.LastInsertId()
	s.Status = "pending"
	return nil
}

func (d *DB) GetScheduled(ctx context.Context, id int64) (*Scheduled, error) {
	s, err := scanSched(d.db.QueryRowContext(ctx, `SELECT `+schedCols+` FROM scheduled WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (d *DB) ListScheduled(ctx context.Context, status string, limit int) ([]*Scheduled, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + schedCols + ` FROM scheduled`
	args := []any{}
	if status != "" && status != "all" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY due ASC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Scheduled
	for rows.Next() {
		s, err := scanSched(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// ClaimDue atomically takes pending items whose time has come.
func (d *DB) ClaimDue(ctx context.Context, now time.Time) ([]*Scheduled, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+schedCols+` FROM scheduled WHERE status='pending' AND due<=? ORDER BY due LIMIT 20`, now.Unix())
	if err != nil {
		return nil, err
	}
	var due []*Scheduled
	for rows.Next() {
		if s, err := scanSched(rows); err == nil {
			due = append(due, s)
		}
	}
	rows.Close()
	var claimed []*Scheduled
	for _, s := range due {
		res, err := d.db.ExecContext(ctx, `UPDATE scheduled SET status='sending' WHERE id=? AND status='pending'`, s.ID)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 1 {
			claimed = append(claimed, s)
		}
	}
	return claimed, nil
}

func (d *DB) FinishScheduled(ctx context.Context, id int64, status, result, errMsg string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE scheduled SET status=?, result=?, error=?, sent_at=? WHERE id=?`, status, result, errMsg, time.Now().Unix(), id)
	return err
}

func (d *DB) CancelScheduled(ctx context.Context, id int64) (bool, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE scheduled SET status='cancelled' WHERE id=? AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoverSending puts items stuck in "sending" (daemon died mid-send) back to pending.
func (d *DB) RecoverSending(ctx context.Context) {
	_, _ = d.db.ExecContext(ctx, `UPDATE scheduled SET status='pending' WHERE status='sending'`)
}

// ---------- transcripts ----------

func (d *DB) PutTranscript(ctx context.Context, chat, id, text, engine, lang string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO transcripts (chat_jid, message_id, text, engine, language, ts) VALUES (?,?,?,?,?,?)
		ON CONFLICT(chat_jid, message_id) DO UPDATE SET text=excluded.text, engine=excluded.engine, language=excluded.language, ts=excluded.ts`,
		chat, id, text, engine, lang, time.Now().Unix())
	return err
}

func (d *DB) Transcript(ctx context.Context, chat, id string) (text, engine string, ok bool) {
	err := d.db.QueryRowContext(ctx, `SELECT text, engine FROM transcripts WHERE chat_jid=? AND message_id=?`, chat, id).Scan(&text, &engine)
	return text, engine, err == nil
}

// ---------- OAuth (remote MCP) ----------

type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs string // newline-separated
	Created      time.Time
	Kind         string // dcr or cimd
}

func (d *DB) PutOAuthClient(ctx context.Context, c OAuthClient) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO oauth_clients (id, name, redirect_uris, created, kind) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, redirect_uris=excluded.redirect_uris`, c.ID, c.Name, c.RedirectURIs, c.Created.Unix(), c.Kind)
	return err
}

func (d *DB) GetOAuthClient(ctx context.Context, id string) (*OAuthClient, error) {
	var c OAuthClient
	var created int64
	err := d.db.QueryRowContext(ctx, `SELECT id, name, redirect_uris, created, kind FROM oauth_clients WHERE id=?`, id).Scan(&c.ID, &c.Name, &c.RedirectURIs, &created, &c.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	c.Created = time.Unix(created, 0)
	return &c, err
}

type OAuthToken struct {
	Hash       string
	Kind       string // access or refresh
	ClientID   string
	ClientName string
	Family     string
	Scope      string
	Created    time.Time
	Expires    time.Time
	LastUsed   time.Time
	Used       bool
	Revoked    bool // token or its whole connection (family) was revoked
	Trusted    bool // the client may relay the user's approvals (first-party redirect)
}

func (d *DB) PutOAuthToken(ctx context.Context, t OAuthToken) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO oauth_tokens (hash, kind, client_id, client_name, family, scope, created, expires, trusted) VALUES (?,?,?,?,?,?,?,?,?)`,
		t.Hash, t.Kind, t.ClientID, t.ClientName, t.Family, t.Scope, t.Created.Unix(), t.Expires.Unix(), b2i(t.Trusted))
	return err
}

// IssueFamily starts a new connection and stores its first access and refresh tokens atomically.
func (d *DB) IssueFamily(ctx context.Context, access, refresh OAuthToken) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_families (family, client_id, created) VALUES (?,?,?)`, access.Family, access.ClientID, time.Now().Unix()); err != nil {
		return err
	}
	for _, t := range []OAuthToken{access, refresh} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_tokens (hash, kind, client_id, client_name, family, scope, created, expires, trusted) VALUES (?,?,?,?,?,?,?,?,?)`,
			t.Hash, t.Kind, t.ClientID, t.ClientName, t.Family, t.Scope, t.Created.Unix(), t.Expires.Unix(), b2i(t.Trusted)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Rotation outcomes.
var (
	ErrTokenInvalid = errors.New("refresh token invalid, expired or revoked")
	ErrTokenReused  = errors.New("refresh token reuse: connection revoked")
)

// RotateRefresh spends a refresh token and issues its replacements in one transaction. A token that
// was already spent revokes the whole connection, and nothing is ever issued into a revoked one.
func (d *DB) RotateRefresh(ctx context.Context, oldHash, clientID string, access, refresh OAuthToken) (*OAuthToken, error) {
	tx, err := d.db.BeginTx(ctx, nil) // _txlock=immediate: writers are serialized
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var t OAuthToken
	var expires int64
	var used, revoked, famRevoked, trusted int
	err = tx.QueryRowContext(ctx, `SELECT t.client_id, t.client_name, t.family, t.scope, t.expires, t.used, t.revoked, t.trusted, COALESCE(f.revoked, 1)
		FROM oauth_tokens t LEFT JOIN oauth_families f ON f.family = t.family WHERE t.hash=? AND t.kind='refresh'`, oldHash).
		Scan(&t.ClientID, &t.ClientName, &t.Family, &t.Scope, &expires, &used, &revoked, &trusted, &famRevoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenInvalid
	} else if err != nil {
		return nil, err
	}
	if revoked == 1 || famRevoked == 1 || time.Now().Unix() > expires || (clientID != "" && clientID != t.ClientID) {
		return nil, ErrTokenInvalid
	}
	if used == 1 {
		_, _ = tx.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE family=?`, t.Family)
		_, _ = tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked=1 WHERE family=?`, t.Family)
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrTokenReused
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET used=1, last_used=? WHERE hash=?`, time.Now().Unix(), oldHash); err != nil {
		return nil, err
	}
	t.Trusted = trusted == 1
	for _, nt := range []OAuthToken{access, refresh} {
		nt.ClientID, nt.ClientName, nt.Family, nt.Scope, nt.Trusted = t.ClientID, t.ClientName, t.Family, t.Scope, t.Trusted
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_tokens (hash, kind, client_id, client_name, family, scope, created, expires, trusted) VALUES (?,?,?,?,?,?,?,?,?)`,
			nt.Hash, nt.Kind, nt.ClientID, nt.ClientName, nt.Family, nt.Scope, nt.Created.Unix(), nt.Expires.Unix(), b2i(nt.Trusted)); err != nil {
			return nil, err
		}
	}
	return &t, tx.Commit()
}

func (d *DB) GetOAuthToken(ctx context.Context, hash string) (*OAuthToken, error) {
	var t OAuthToken
	var created, expires, last int64
	var used, revoked, trusted int
	err := d.db.QueryRowContext(ctx, `SELECT t.hash, t.kind, t.client_id, t.client_name, t.family, t.scope, t.created, t.expires, t.last_used, t.used,
		CASE WHEN t.revoked=1 OR COALESCE(f.revoked, 1)=1 THEN 1 ELSE 0 END, t.trusted
		FROM oauth_tokens t LEFT JOIN oauth_families f ON f.family = t.family WHERE t.hash=?`, hash).
		Scan(&t.Hash, &t.Kind, &t.ClientID, &t.ClientName, &t.Family, &t.Scope, &created, &expires, &last, &used, &revoked, &trusted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	t.Created, t.Expires = time.Unix(created, 0), time.Unix(expires, 0)
	if last > 0 {
		t.LastUsed = time.Unix(last, 0)
	}
	t.Used, t.Revoked, t.Trusted = used == 1, revoked == 1, trusted == 1
	return &t, nil
}

func (d *DB) TouchOAuthToken(ctx context.Context, hash string) {
	_, _ = d.db.ExecContext(ctx, `UPDATE oauth_tokens SET last_used=? WHERE hash=?`, time.Now().Unix(), hash)
}

func (d *DB) RevokeFamily(ctx context.Context, family string) error {
	if _, err := d.db.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE family=?`, family); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `UPDATE oauth_tokens SET revoked=1 WHERE family=?`, family)
	return err
}

func (d *DB) RevokeClient(ctx context.Context, clientID string) (int64, error) {
	_, _ = d.db.ExecContext(ctx, `UPDATE oauth_families SET revoked=1 WHERE client_id=? OR ?='all'`, clientID, clientID)
	res, err := d.db.ExecContext(ctx, `UPDATE oauth_tokens SET revoked=1 WHERE revoked=0 AND (client_id=? OR ?='all')`, clientID, clientID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ActiveConnections lists token families with a live refresh token: one row per connected app.
type Connection struct {
	ClientID, ClientName, Family string
	Created, LastUsed, Expires   time.Time
}

func (d *DB) ActiveConnections(ctx context.Context) ([]Connection, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT t.client_id, t.client_name, t.family, MIN(t.created), MAX(t.last_used), MAX(t.expires) FROM oauth_tokens t
		JOIN oauth_families f ON f.family = t.family WHERE t.revoked=0 AND f.revoked=0 AND t.expires>? GROUP BY t.family ORDER BY MAX(t.last_used) DESC`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		var c Connection
		var created, last, exp int64
		if err := rows.Scan(&c.ClientID, &c.ClientName, &c.Family, &created, &last, &exp); err != nil {
			return nil, err
		}
		c.Created, c.Expires = time.Unix(created, 0), time.Unix(exp, 0)
		if last > 0 {
			c.LastUsed = time.Unix(last, 0)
		}
		out = append(out, c)
	}
	return out, nil
}

// ExecForTest runs raw SQL; tests use it to simulate tampering.
func (d *DB) ExecForTest(ctx context.Context, q string) error {
	_, err := d.db.ExecContext(ctx, q)
	return err
}
