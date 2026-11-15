package provider

import (
	"context"
	"io"
	"testing"

	"pi-go/internal/wire"
)

// recorder is the smallest Provider that can answer a question about the
// request it was handed: what Options arrived.
type recorder struct {
	api      string
	requests []Request
}

func (r *recorder) API() string { return r.api }

func (r *recorder) Stream(_ context.Context, req Request) (Stream, error) {
	r.requests = append(r.requests, req)
	return emptyStream{}, nil
}

type emptyStream struct{}

func (emptyStream) Recv() (wire.AssistantEvent, error) { return wire.AssistantEvent{}, io.EOF }
func (emptyStream) Close() error                       { return nil }

func TestWithAPIKeyFillsAnEmptyOption(t *testing.T) {
	inner := &recorder{api: "test"}
	wrapped := WithAPIKey(inner, "secret")
	if wrapped.API() != "test" {
		t.Fatalf("API() = %q, want the wrapped value", wrapped.API())
	}
	if _, err := wrapped.Stream(context.Background(), Request{}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := inner.requests[0].Options.APIKey; got != "secret" {
		t.Fatalf("APIKey = %q, want the wrapper key", got)
	}
}

func TestWithAPIKeyKeepsAnExplicitOption(t *testing.T) {
	// A per-call credential must win: that is what lets one adapter serve two
	// accounts without being rebuilt.
	inner := &recorder{api: "test"}
	wrapped := WithAPIKey(inner, "wrapper")
	req := Request{Options: Options{APIKey: "explicit"}}
	if _, err := wrapped.Stream(context.Background(), req); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := inner.requests[0].Options.APIKey; got != "explicit" {
		t.Fatalf("APIKey = %q, want the explicit value", got)
	}
}

func TestWithAPIKeyCarriesOtherOptions(t *testing.T) {
	inner := &recorder{api: "test"}
	wrapped := WithAPIKey(inner, "secret")
	limit := 128
	req := Request{Options: Options{MaxTokens: &limit, ThinkingLevel: "high", SessionID: "abc"}}
	if _, err := wrapped.Stream(context.Background(), req); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got := inner.requests[0].Options
	if got.MaxTokens == nil || *got.MaxTokens != 128 || got.ThinkingLevel != "high" || got.SessionID != "abc" {
		t.Fatalf("options = %+v, want the request options preserved", got)
	}
	if got.APIKey != "secret" {
		t.Fatalf("APIKey = %q", got.APIKey)
	}
}

func TestWithAPIKeyEmptyReturnsTheProvider(t *testing.T) {
	// A local endpoint needs no credential, and wrapping it anyway would make
	// every call pay for a layer that changes nothing.
	inner := &recorder{api: "test"}
	if wrapped := WithAPIKey(inner, ""); wrapped != Provider(inner) {
		t.Fatalf("WithAPIKey returned %T, want the provider itself", wrapped)
	}
}
