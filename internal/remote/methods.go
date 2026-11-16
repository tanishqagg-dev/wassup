package remote

import (
	"sync/atomic"
	"time"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/engine"
)

var current atomic.Pointer[Server]

// SetCurrent records the running server so CLI methods can report on it.
func SetCurrent(s *Server) { current.Store(s) }

type RevokeParams struct {
	Client string `json:"client" jsonschema:"Client ID from wassup remote connections, or 'all'"`
}

func init() {
	engine.RegisterMethod(&engine.Method{Name: "remote.status", Summary: "Remote MCP server state and public URL", Params: api.Empty{}, H: func(c *engine.Call) (any, error) {
		cfg := c.E.Config().Remote
		res := map[string]any{"enabled": cfg.Enabled, "listen": cfg.Listen, "tunnel": cfg.Tunnel, "profile": cfg.Profile, "running": false}
		if s := current.Load(); s != nil {
			res["running"] = true
			if u := s.PublicURL(); u != "" {
				res["public_url"] = u
				res["connector_url"] = u + "/mcp"
			}
		} else if cfg.PublicURL != "" {
			res["public_url"] = cfg.PublicURL
			res["connector_url"] = cfg.PublicURL + "/mcp"
		}
		return res, nil
	}})
	engine.RegisterMethod(&engine.Method{Name: "remote.codes", Summary: "Sign-in codes waiting to be entered (local use only)", Params: api.Empty{}, H: func(c *engine.Call) (any, error) {
		if c.Req.Meta.Source == "mcp" || c.Req.Meta.Source == "remote" {
			return nil, api.Errf(api.CodePolicyDenied, "sign-in codes are only shown in the local terminal")
		}
		s := current.Load()
		if s == nil {
			return []map[string]any{}, nil
		}
		out := s.PendingCodes()
		if out == nil {
			out = []map[string]any{}
		}
		return out, nil
	}})
	engine.RegisterMethod(&engine.Method{Name: "remote.connections", Summary: "Apps connected through remote MCP", Params: api.Empty{}, H: func(c *engine.Call) (any, error) {
		rows, err := c.E.DB.ActiveConnections(c.Ctx)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, r := range rows {
			item := map[string]any{"client_id": r.ClientID, "name": r.ClientName, "connected": r.Created.Format(time.RFC3339), "expires": r.Expires.Format(time.RFC3339)}
			if !r.LastUsed.IsZero() {
				item["last_used"] = r.LastUsed.Format(time.RFC3339)
			}
			out = append(out, item)
		}
		return out, nil
	}})
	engine.RegisterMethod(&engine.Method{Name: "remote.revoke", Summary: "Disconnect a remote app (or all of them)", Params: RevokeParams{}, Mutating: true, H: func(c *engine.Call) (any, error) {
		var p RevokeParams
		if err := c.Bind(&p); err != nil {
			return nil, err
		}
		if c.Req.Meta.Source == "mcp" || c.Req.Meta.Source == "remote" {
			return nil, api.Errf(api.CodePolicyDenied, "revoke connections from the local terminal")
		}
		n, err := c.E.DB.RevokeClient(c.Ctx, p.Client)
		return map[string]any{"revoked_tokens": n}, err
	}})
}
