package remote

import (
	"bufio"
	"context"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

var trycloudflare = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// tunnel keeps a Cloudflare quick tunnel running and reports its (changing) URL.
type tunnel struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func startCloudflare(s *Server, listen string) *tunnel {
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnel{cancel: cancel}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		backoff := 2 * time.Second
		for ctx.Err() == nil {
			cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "--no-autoupdate", "--url", "http://"+listen)
			stderr, err := cmd.StderrPipe()
			if err != nil {
				s.Log.Error().Err(err).Msg("cloudflared pipe")
				return
			}
			if err := cmd.Start(); err != nil {
				s.Log.Error().Err(err).Msg("couldn't start cloudflared (brew install cloudflared)")
				return
			}
			sc := bufio.NewScanner(stderr)
			for sc.Scan() {
				if u := trycloudflare.FindString(sc.Text()); u != "" && u != s.PublicURL() {
					s.setPublic(u)
				}
			}
			_ = cmd.Wait()
			if ctx.Err() != nil {
				return
			}
			s.Log.Warn().Dur("retry_in", backoff).Msg("cloudflared exited; restarting")
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < time.Minute {
				backoff *= 2
			}
		}
	}()
	return t
}

func (t *tunnel) stop() {
	t.cancel()
	t.wg.Wait()
}
