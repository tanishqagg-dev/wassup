// Package rpc carries requests between the CLI (or MCP server) and the daemon over a Unix socket.
// Each connection carries one request: a JSON line in, zero or more event lines out, then a final line.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/tanishqagg-dev/wassup/internal/api"
)

const maxLine = 64 << 20

// Dispatcher runs one request. engine.Engine satisfies it.
type Dispatcher interface {
	Dispatch(ctx context.Context, req *api.Request, emit func(any) error) (any, error)
}

type Server struct {
	D       Dispatcher
	Log     zerolog.Logger
	OnStop  func()
	ln      net.Listener
	wg      sync.WaitGroup
	closing atomic.Bool
}

// Listen opens the socket, clearing a stale file left by a crashed daemon.
func Listen(path string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			c.Close()
			return nil, fmt.Errorf("another daemon is already listening on %s", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

func (s *Server) Serve(ln net.Listener) error {
	s.ln = ln
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.closing.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *Server) Close() {
	s.closing.Store(true)
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := readLine(br)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	var req api.Request
	var mu sync.Mutex
	write := func(f api.Frame) error {
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, err = conn.Write(append(b, '\n'))
		return err
	}
	if err := json.Unmarshal(line, &req); err != nil {
		_ = write(api.Frame{Final: true, Error: api.Errf(api.CodeUsage, "bad request: %v", err)})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The client half-closes nothing; any read result means it went away.
	go func() {
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
		cancel()
	}()

	// Internal sources (approved, schedule, whatsapp, remote...) only originate inside the daemon.
	switch req.Meta.Source {
	case "cli", "mcp", "mcp-approval", "rpc":
	default:
		req.Meta.Source = "rpc"
	}
	if req.Meta.Source == "mcp" && req.Meta.Yes {
		req.Meta.Yes = false // the stdio MCP server never pre-approves; approval goes through drafts
	}
	if req.Method == "daemon.stop" {
		_ = write(api.Frame{ID: req.ID, Final: true, Result: json.RawMessage(`{"stopping":true}`)})
		if s.OnStop != nil {
			go s.OnStop()
		}
		return
	}
	emit := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return write(api.Frame{ID: req.ID, Event: b})
	}
	start := time.Now()
	res, derr := s.D.Dispatch(ctx, &req, emit)
	f := api.Frame{ID: req.ID, Final: true}
	if derr != nil {
		f.Error = api.AsError(derr)
	} else if res != nil {
		b, err := json.Marshal(res)
		if err != nil {
			f.Error = api.Errf(api.CodeInternal, "encode result: %v", err)
		} else {
			f.Result = b
		}
	}
	ev := s.Log.Debug()
	if derr != nil {
		ev = s.Log.Info().Str("error", derr.Error())
	}
	ev.Str("method", req.Method).Str("source", req.Meta.Source).Dur("took", time.Since(start)).Msg("request")
	_ = write(f)
}

func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, errors.New("request too large")
		}
		if !isPrefix {
			return buf, nil
		}
	}
}

// Client calls the daemon.
type Client struct {
	Socket string
	nextID atomic.Int64
}

// Available reports whether a daemon answers on the socket.
func (c *Client) Available() bool {
	conn, err := net.DialTimeout("unix", c.Socket, 700*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Call sends one request. onEvent receives stream events; it may be nil.
func (c *Client) Call(ctx context.Context, method string, params any, meta api.Meta, onEvent func(json.RawMessage) error) (json.RawMessage, error) {
	conn, err := net.DialTimeout("unix", c.Socket, 3*time.Second)
	if err != nil {
		return nil, api.Errf(api.CodeDaemonUnavailable, "the wassup daemon isn't running").WithHint("start it with: wassup daemon start")
	}
	defer conn.Close()
	var raw json.RawMessage
	if params != nil {
		if raw, err = json.Marshal(params); err != nil {
			return nil, err
		}
	}
	req := api.Request{ID: c.nextID.Add(1), Method: method, Params: raw, Meta: meta}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return nil, api.Errf(api.CodeDaemonUnavailable, "lost the daemon connection: %v", err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	br := bufio.NewReaderSize(conn, 64<<10)
	for {
		line, err := readLine(br)
		if err != nil {
			if ctx.Err() != nil {
				return nil, api.Errf(api.CodeTimeout, "cancelled")
			}
			return nil, api.Errf(api.CodeDaemonUnavailable, "the daemon closed the connection: %v", err).WithHint("check wassup daemon logs")
		}
		var f api.Frame
		if err := json.Unmarshal(line, &f); err != nil {
			return nil, api.Errf(api.CodeInternal, "bad frame from daemon: %v", err)
		}
		if f.Final {
			if f.Error != nil {
				return nil, f.Error
			}
			return f.Result, nil
		}
		if onEvent != nil && len(f.Event) > 0 {
			if err := onEvent(f.Event); err != nil {
				return nil, err
			}
		}
	}
}
