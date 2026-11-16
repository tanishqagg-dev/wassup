// Package engine owns the WhatsApp connection, the local store and every operation the CLI exposes.
package engine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
	"github.com/tanishqagg-dev/wassup/internal/store"
)

// Version is set at build time.
var Version = "dev"

const (
	StateLoggedOut    = "logged_out"
	StatePairing      = "pairing"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateReconnecting = "reconnecting"
	StateBanned       = "temporarily_banned"
	StateOutdated     = "client_outdated"
	StateReplaced     = "replaced_by_other_session"
	StateStopped      = "stopped"
)

type Engine struct {
	Acct *config.Account
	Log  zerolog.Logger

	cfgMu      sync.RWMutex
	cfg        *config.Config
	cfgMtime   time.Time
	cfgChecked time.Time

	container *sqlstore.Container
	clientMu  sync.RWMutex
	replaceMu sync.Mutex
	client    *whatsmeow.Client
	handlerID uint32

	DB  *store.DB
	Bus *Bus

	stateMu     sync.RWMutex
	state       string
	lastErr     string
	connectedAt time.Time
	started     time.Time
	syncNote    string

	loginMu sync.Mutex

	waitMu        sync.Mutex
	historyWaits  map[string][]chan int
	mediaRetries  map[string]chan *events.MediaRetry
	sendMu        sync.Mutex
	groupsFetched time.Time

	hooks *webhookSender
	ctx   context.Context
	stop  context.CancelFunc
	wg    sync.WaitGroup
}

// New opens the stores and builds a client, but does not connect.
func New(acct *config.Account, log zerolog.Logger) (*Engine, error) {
	if err := acct.Ensure(); err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	e := &Engine{
		Acct:         acct,
		Log:          log,
		cfg:          cfg,
		Bus:          NewBus(),
		state:        StateLoggedOut,
		started:      time.Now(),
		historyWaits: map[string][]chan int{},
		mediaRetries: map[string]chan *events.MediaRetry{},
	}
	if st, err := os.Stat(config.Path()); err == nil {
		e.cfgMtime = st.ModTime()
	}
	e.ctx, e.stop = context.WithCancel(context.Background())

	sessDB, err := sql.Open("sqlite", store.DSN(acct.SessionDB))
	if err != nil {
		return nil, err
	}
	sessDB.SetMaxOpenConns(4)
	e.container = sqlstore.NewWithDB(sessDB, "sqlite3", waLog.Zerolog(log.With().Str("mod", "store").Logger().Level(zerolog.WarnLevel)))
	if err := e.container.Upgrade(e.ctx); err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	e.DB, err = store.Open(acct.AppDB)
	if err != nil {
		return nil, err
	}
	key, err := loadOrCreateKey(filepath.Join(acct.Dir, "audit.key"))
	if err != nil {
		return nil, fmt.Errorf("audit key: %w", err)
	}
	e.DB.SetAuditKey(key)
	e.applyDeviceProps()
	device, err := e.container.GetFirstDevice(e.ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	e.restoreWAVersion()
	e.setClient(device)
	e.hooks = newWebhookSender(e)
	return e, nil
}

func (e *Engine) applyDeviceProps() {
	cfg := e.Config()
	wastore.DeviceProps.Os = new(cfg.Daemon.DeviceName)
	wastore.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	if cfg.Daemon.FullHistory {
		wastore.DeviceProps.RequireFullSync = new(true)
	}
}

// setClient swaps in a client for device. The old client is torn down outside clientMu:
// whatsmeow holds its handler lock while running our callbacks, and those callbacks take
// clientMu, so removing handlers under clientMu could deadlock.
func (e *Engine) setClient(device *wastore.Device) {
	e.replaceMu.Lock()
	defer e.replaceMu.Unlock()
	cl := whatsmeow.NewClient(device, waLog.Zerolog(e.Log.With().Str("mod", "wassup").Logger()))
	cl.EnableAutoReconnect = true
	cl.InitialAutoReconnect = true
	cl.AutomaticMessageRerequestFromPhone = true
	cl.BackgroundEventCtx = e.ctx
	cl.AutoReconnectHook = func(err error) bool {
		e.setState(StateReconnecting, err.Error())
		return true
	}
	newID := cl.AddEventHandler(func(evt any) {
		// Ignore late events from a client that has been replaced.
		if e.Client() == cl {
			e.handleEvent(evt)
		}
	})
	e.clientMu.Lock()
	old, oldID := e.client, e.handlerID
	e.client, e.handlerID = cl, newID
	e.clientMu.Unlock()
	if old != nil {
		// Async: this may be reached from inside one of old's own event callbacks.
		go func() {
			old.RemoveEventHandler(oldID)
			old.Disconnect()
		}()
	}
}

// Client returns the current whatsmeow client.
func (e *Engine) Client() *whatsmeow.Client {
	e.clientMu.RLock()
	defer e.clientMu.RUnlock()
	return e.client
}

// Config returns the live config, re-reading config.toml at most once a second when it changed.
func (e *Engine) Config() *config.Config {
	e.cfgMu.RLock()
	cfg, checked := e.cfg, e.cfgChecked
	e.cfgMu.RUnlock()
	if time.Since(checked) < time.Second {
		return cfg
	}
	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	e.cfgChecked = time.Now()
	st, err := os.Stat(config.Path())
	if err != nil || !st.ModTime().After(e.cfgMtime) {
		return e.cfg
	}
	fresh, err := config.Load()
	if err != nil {
		e.Log.Warn().Err(err).Msg("config reload failed; keeping previous config")
		return e.cfg
	}
	e.cfg, e.cfgMtime = fresh, st.ModTime()
	e.Log.Info().Msg("config reloaded")
	return e.cfg
}

func (e *Engine) setState(state, errMsg string) {
	e.stateMu.Lock()
	changed := e.state != state
	e.state = state
	if errMsg != "" || state == StateConnected {
		e.lastErr = errMsg
	}
	if state == StateConnected {
		e.connectedAt = time.Now()
	}
	e.stateMu.Unlock()
	if changed {
		e.Log.Info().Str("state", state).Str("error", errMsg).Msg("connection state")
		e.publish(api.Event{Type: "connection", Data: map[string]any{"state": state, "error": errMsg}})
	}
}

func (e *Engine) State() (string, string) {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()
	return e.state, e.lastErr
}

// Start connects if a session exists and keeps the connection healthy until Stop.
func (e *Engine) Start() {
	cl := e.Client()
	if cl.Store.ID != nil {
		e.connect()
	}
	e.wg.Add(3)
	go e.watchdog()
	go e.draftSweeper()
	go e.scheduler()
}

// loadOrCreateKey keeps a private 32-byte key next to the account data.
func loadOrCreateKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (e *Engine) connect() {
	cl := e.Client()
	e.setState(StateConnecting, "")
	if err := cl.ConnectContext(e.ctx); err != nil && err != whatsmeow.ErrAlreadyConnected {
		e.setState(StateReconnecting, err.Error())
	}
}

// watchdog reconnects if the socket has been down for a while and whatsmeow's own retry gave up.
func (e *Engine) watchdog() {
	defer e.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	var downSince time.Time
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		}
		cl := e.Client()
		state, _ := e.State()
		if cl.Store.ID == nil || state == StateBanned || state == StateReplaced || state == StateLoggedOut || state == StatePairing {
			downSince = time.Time{}
			continue
		}
		if cl.IsConnected() {
			downSince = time.Time{}
			continue
		}
		if downSince.IsZero() {
			downSince = time.Now()
			continue
		}
		if time.Since(downSince) > 90*time.Second {
			e.Log.Warn().Msg("watchdog: connection down for 90s, reconnecting")
			cl.Disconnect()
			e.connect()
			downSince = time.Now()
		}
	}
}

// Stop disconnects and closes the stores.
func (e *Engine) Stop() {
	e.setState(StateStopped, "")
	e.stop()
	if cl := e.Client(); cl != nil {
		cl.Disconnect()
	}
	e.hooks.close()
	e.wg.Wait()
	_ = e.DB.Close()
	_ = e.container.Close()
}

// WaitConnected blocks until the socket is up and logged in, or returns a structured error.
func (e *Engine) WaitConnected(ctx context.Context) error {
	cl := e.Client()
	if cl.Store.ID == nil {
		return api.Errf(api.CodeNotLoggedIn, "this account isn't linked yet").WithHint("run: wassup login")
	}
	timeout := e.Config().Daemon.ConnectTimeout.Duration
	deadline := time.Now().Add(timeout)
	for {
		if cl.IsConnected() && cl.IsLoggedIn() {
			return nil
		}
		state, lastErr := e.State()
		switch state {
		case StateBanned:
			return api.Errf(api.CodeWhatsApp, "WhatsApp has temporarily banned this number: %s", lastErr)
		case StateOutdated:
			return api.Errf(api.CodeWhatsApp, "WhatsApp rejected this client version").WithHint("update wassup to the latest release")
		case StateReplaced:
			return api.Errf(api.CodeNotConnected, "another program connected with this same session and replaced us").WithHint("stop the other copy, then run: wassup daemon restart")
		case StateLoggedOut:
			return api.Errf(api.CodeNotLoggedIn, "this device was unlinked from the phone").WithHint("run: wassup login")
		}
		if time.Now().After(deadline) {
			msg := "couldn't connect to WhatsApp within " + timeout.String()
			if lastErr != "" {
				msg += ": " + lastErr
			}
			return api.Errf(api.CodeNotConnected, "%s", msg).WithHint("check the network; wassup doctor shows details")
		}
		select {
		case <-ctx.Done():
			return api.Errf(api.CodeTimeout, "cancelled while waiting for the connection")
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// OwnJID is the logged-in phone-number JID without device suffix.
func (e *Engine) OwnJID() types.JID {
	cl := e.Client()
	if cl.Store.ID == nil {
		return types.EmptyJID
	}
	return cl.Store.ID.ToNonAD()
}

func (e *Engine) restoreWAVersion() {
	raw := e.DB.Get(e.ctx, "wa_version")
	if raw == "" {
		return
	}
	var v wastore.WAVersionContainer
	if err := json.Unmarshal([]byte(raw), &v); err == nil && v[0] > 0 {
		cur := wastore.GetWAVersion()
		if v.LessThan(cur) {
			return
		}
		wastore.SetWAVersion(v)
	}
}

// refreshWAVersion asks web.whatsapp.com for the current version. Used when the server says we're outdated.
func (e *Engine) refreshWAVersion() bool {
	ctx, cancel := context.WithTimeout(e.ctx, 20*time.Second)
	defer cancel()
	v, err := whatsmeow.GetLatestVersion(ctx, nil)
	if err != nil {
		e.Log.Warn().Err(err).Msg("couldn't fetch latest WhatsApp Web version")
		return false
	}
	cur := wastore.GetWAVersion()
	if !cur.LessThan(*v) {
		return false
	}
	wastore.SetWAVersion(*v)
	if b, err := json.Marshal(v); err == nil {
		_ = e.DB.Set(e.ctx, "wa_version", string(b))
	}
	e.Log.Info().Str("version", v.String()).Msg("updated WhatsApp Web version")
	return true
}

// ---------- event bus ----------

type Bus struct {
	mu   sync.Mutex
	subs map[int]chan api.Event
	next int
}

func NewBus() *Bus { return &Bus{subs: map[int]chan api.Event{}} }

func (b *Bus) Subscribe(buf int) (int, <-chan api.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	ch := make(chan api.Event, buf)
	b.subs[b.next] = ch
	return b.next, ch
}

func (b *Bus) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		close(ch)
		delete(b.subs, id)
	}
}

func (b *Bus) Publish(ev api.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // slow subscriber; drop rather than stall the connection
		}
	}
}

func (e *Engine) publish(ev api.Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	e.Bus.Publish(ev)
	e.hooks.enqueue(ev)
}
