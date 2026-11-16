package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

type fake struct{ cancelled chan struct{} }

func (f *fake) Dispatch(ctx context.Context, req *api.Request, emit func(any) error) (any, error) {
	switch req.Method {
	case "echo":
		var p map[string]any
		_ = json.Unmarshal(req.Params, &p)
		p["source"] = req.Meta.Source
		return p, nil
	case "stream":
		for i := 0; i < 3; i++ {
			_ = emit(map[string]int{"n": i})
		}
		return "done", nil
	case "block":
		<-ctx.Done()
		close(f.cancelled)
		return nil, ctx.Err()
	}
	return nil, api.Errf(api.CodeNotFound, "nope").WithHint("try echo")
}

func TestRoundTrip(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "wassup-rpc-")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{cancelled: make(chan struct{})}
	srv := &Server{D: f, Log: zerolog.Nop()}
	go srv.Serve(ln)
	defer srv.Close()
	c := &Client{Socket: sock}
	if !c.Available() {
		t.Fatal("not available")
	}
	ctx := context.Background()
	raw, err := c.Call(ctx, "echo", map[string]string{"a": "b"}, api.Meta{Source: "cli"}, nil)
	if err != nil || string(raw) != `{"a":"b","source":"cli"}` {
		t.Fatalf("echo: %s %v", raw, err)
	}
	var events int
	raw, err = c.Call(ctx, "stream", nil, api.Meta{}, func(json.RawMessage) error { events++; return nil })
	if err != nil || events != 3 || string(raw) != `"done"` {
		t.Fatalf("stream: %s %d %v", raw, events, err)
	}
	_, err = c.Call(ctx, "missing", nil, api.Meta{}, nil)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeNotFound || ae.Hint != "try echo" {
		t.Fatalf("error passthrough: %v", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, _ = c.Call(cctx, "block", nil, api.Meta{}, nil)
	select {
	case <-f.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("server didn't notice the client went away")
	}
	// A second listener on a live socket must refuse.
	if _, err := Listen(sock); err == nil {
		t.Fatal("expected refusal while another server is listening")
	}
}

type metaSpy struct{ got api.Meta }

func (m *metaSpy) Dispatch(ctx context.Context, req *api.Request, emit func(any) error) (any, error) {
	m.got = req.Meta
	return "ok", nil
}

func TestSocketCantClaimInternalSources(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "wassup-rpc-")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	spy := &metaSpy{}
	srv := &Server{D: spy, Log: zerolog.Nop()}
	go srv.Serve(ln)
	defer srv.Close()
	c := &Client{Socket: sock}
	for _, forged := range []string{"approved", "schedule", "whatsapp", "remote", "remote-approval"} {
		_, _ = c.Call(context.Background(), "x", nil, api.Meta{Source: forged, Yes: true}, nil)
		if spy.got.Source != "rpc" {
			t.Errorf("source %q passed through as %q", forged, spy.got.Source)
		}
	}
	_, _ = c.Call(context.Background(), "x", nil, api.Meta{Source: "mcp", Yes: true}, nil)
	if spy.got.Yes {
		t.Error("stdio MCP source may not pre-approve")
	}
}
