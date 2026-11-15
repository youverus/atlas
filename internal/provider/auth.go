package provider

import (
	"context"
)

// WithAPIKey returns p with key applied to every request that does not carry
// its own credential.
//
// The kernel never puts a credential in a request: the agent builds Options
// with only the thinking level, and compaction adds a session id. That is the
// right split for a library, because it keeps secrets out of the transcript and
// out of session files. A binary that reads its key from a config file
// therefore has to attach it at the edge, where the adapter is chosen, and this
// wrapper is that edge.
//
// A request that already sets Options.APIKey wins, so a caller can override one
// call without rebuilding the adapter. An empty key returns p unchanged rather
// than wrapping it, which keeps a local endpoint from carrying a pointless
// layer.
func WithAPIKey(p Provider, key string) Provider {
	if key == "" {
		return p
	}
	return keyed{inner: p, key: key}
}

type keyed struct {
	inner Provider
	key   string
}

func (k keyed) API() string { return k.inner.API() }

func (k keyed) Stream(ctx context.Context, req Request) (Stream, error) {
	if req.Options.APIKey == "" {
		req.Options.APIKey = k.key
	}
	return k.inner.Stream(ctx, req)
}
