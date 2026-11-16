package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/config"
)

// webhookSender POSTs events to configured URLs with retries, off the connection's hot path.
type webhookSender struct {
	e      *Engine
	queue  chan api.Event
	client *http.Client
	once   sync.Once
	done   chan struct{}
}

func newWebhookSender(e *Engine) *webhookSender {
	w := &webhookSender{e: e, queue: make(chan api.Event, 2000), client: &http.Client{Timeout: 10 * time.Second}, done: make(chan struct{})}
	go w.loop()
	return w
}

func (w *webhookSender) enqueue(ev api.Event) {
	if len(w.e.Config().Webhooks) == 0 {
		return
	}
	select {
	case w.queue <- ev:
	default:
		w.e.Log.Warn().Str("type", ev.Type).Msg("webhook queue full; dropping event")
	}
}

func (w *webhookSender) close() { w.once.Do(func() { close(w.done) }) }

func (w *webhookSender) loop() {
	for {
		select {
		case <-w.done:
			return
		case ev := <-w.queue:
			for _, hook := range w.e.Config().Webhooks {
				if w.wants(hook, ev) {
					w.deliver(hook, ev)
				}
			}
		}
	}
}

func (w *webhookSender) wants(h config.Webhook, ev api.Event) bool {
	if h.URL == "" {
		return false
	}
	if len(h.Events) > 0 {
		ok := false
		for _, t := range h.Events {
			if t == ev.Type || t == "*" || (strings.HasSuffix(t, ".*") && strings.HasPrefix(ev.Type, strings.TrimSuffix(t, "*"))) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if ev.Type == "message" && !h.IncludeFromMe {
		if m, ok := ev.Data.(*api.Message); ok && m.FromMe {
			return false
		}
	}
	if len(h.Chats) > 0 {
		j, err := types.ParseJID(ev.Chat)
		if err != nil || !w.e.matchList(context.Background(), h.Chats, j) {
			return false
		}
	}
	return true
}

func (w *webhookSender) deliver(h config.Webhook, ev api.Event) {
	body, err := json.Marshal(map[string]any{"account": w.e.Acct.Name, "event": ev})
	if err != nil {
		return
	}
	backoff := []time.Duration{0, 2 * time.Second, 8 * time.Second, 30 * time.Second}
	for attempt, wait := range backoff {
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-w.done:
				return
			}
		}
		req, _ := http.NewRequest(http.MethodPost, h.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "wassup/"+Version)
		req.Header.Set("X-Wassup-Event", ev.Type)
		if h.Secret != "" {
			mac := hmac.New(sha256.New, []byte(h.Secret))
			mac.Write(body)
			req.Header.Set("X-Wassup-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := w.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return
			}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
				w.e.Log.Warn().Int("status", resp.StatusCode).Str("url", h.URL).Msg("webhook rejected event")
				return
			}
		}
		w.e.Log.Debug().Int("attempt", attempt+1).Str("url", h.URL).Msg("webhook delivery failed, retrying")
	}
	w.e.Log.Warn().Str("url", h.URL).Str("type", ev.Type).Msg("webhook delivery gave up")
}
