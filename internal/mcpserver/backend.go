package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/tanishqagg-dev/wassup/internal/api"
	"github.com/tanishqagg-dev/wassup/internal/engine"
	"github.com/tanishqagg-dev/wassup/internal/rpc"
)

func marshalParams(params any) (json.RawMessage, error) {
	switch p := params.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return p, nil
	default:
		return json.Marshal(p)
	}
}

// EngineBackend calls the engine directly (the remote server runs inside the daemon).
type EngineBackend struct{ E *engine.Engine }

func (b EngineBackend) Call(ctx context.Context, method string, params any, meta api.Meta) (json.RawMessage, error) {
	return b.Stream(ctx, method, params, meta, nil)
}

func (b EngineBackend) Stream(ctx context.Context, method string, params any, meta api.Meta, onEvent func(json.RawMessage) error) (json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	emit := func(v any) error {
		if onEvent == nil {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return onEvent(b)
	}
	res, err := b.E.Dispatch(ctx, &api.Request{Method: method, Params: raw, Meta: meta}, emit)
	if err != nil || res == nil {
		return nil, err
	}
	return json.Marshal(res)
}

// RPCBackend calls the daemon over its socket; Ensure (optional) starts it on demand.
type RPCBackend struct {
	Client *rpc.Client
	Ensure func() error
}

func (b RPCBackend) Call(ctx context.Context, method string, params any, meta api.Meta) (json.RawMessage, error) {
	return b.Stream(ctx, method, params, meta, nil)
}

func (b RPCBackend) Stream(ctx context.Context, method string, params any, meta api.Meta, onEvent func(json.RawMessage) error) (json.RawMessage, error) {
	if b.Ensure != nil && !b.Client.Available() {
		if err := b.Ensure(); err != nil {
			return nil, err
		}
	}
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	var p any
	if len(raw) > 0 {
		p = raw
	}
	return b.Client.Call(ctx, method, p, meta, onEvent)
}
