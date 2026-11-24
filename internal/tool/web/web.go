// Package web implements the two tools that reach outside the machine:
// deepseek_web_search and deepseek_web_fetch.
//
// Both are clients of one vendor endpoint rather than a crawler. That is a
// decision, not a shortcut: turning a page into Markdown needs a browser-grade
// parser, the tree is dependency free, and a hand-written tag stripper would
// either lie about the structure or break on the first script tag. The endpoint
// does that part properly, so what is left here is a JSON client and the
// wording the model reads.
//
// The credential is not the model's. A session answering through Anthropic
// still searches through this endpoint, so the key comes from its own config
// block and never from the provider that serves the model.
//
// Both calls are POSTs even though both are reads. The vendor's API says so,
// and a read-only intent is carried by what the methods return, not by the
// verb: neither call can change anything on the far side.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pi-go/internal/tool"
)

// DefaultBaseURL is the vendor's origin. It is an origin and not an endpoint
// because the two paths are appended to it; a configured value that carries a
// path is refused rather than doubled up (see internal/config.Web).
const DefaultBaseURL = "https://api.deepseek.com"

const (
	// searchPath and fetchPath are the two endpoints, appended to the origin.
	searchPath = "/v1/tools/search"
	fetchPath  = "/v1/tools/fetch"

	// maxResponseBytes bounds one reply. Ten results carrying full page bodies
	// is the largest legitimate answer, and this is well above it; the cap
	// exists so a wrong URL cannot stream an unbounded body into memory.
	maxResponseBytes = 8 << 20

	// defaultTimeout covers the whole call including the body. The endpoint
	// fetches the page itself, so it is slower than a chat request and the
	// limit is generous for the same reason.
	defaultTimeout = 60 * time.Second

	// attempts is the total number of tries, including the first one. Two is
	// the whole point of a retry: a burst of 429s or one dropped connection is
	// worth another request, a sustained outage is not.
	attempts = 2

	// defaultRetryDelay is the pause before the second attempt. The vendor's
	// documented limit is per second, so a pause of the same order is enough to
	// step over the window rather than hammer into it.
	defaultRetryDelay = 500 * time.Millisecond
)

// Options configures the tool set.
type Options struct {
	// BaseURL is the origin, with no path. Empty means DefaultBaseURL.
	BaseURL string
	// APIKey is the credential. An empty key yields no tools at all: the two
	// calls cannot succeed without one, and a tool that is guaranteed to fail
	// is worse than a tool the model never sees.
	APIKey string
	// HTTPClient overrides the client. Tests use it to point at a local stub.
	HTTPClient *http.Client
	// RetryDelay overrides the pause between attempts. Zero means the default;
	// tests pass a nanosecond so the retry path is exercised without waiting.
	RetryDelay time.Duration
	// Spill is where a page too long to show in full is written, so the notice
	// can name a path instead of saying the rest is lost. Re-fetching returns
	// the same truncated text, which is why this tool spills and the search one
	// does not: asking again with a smaller count is a real second chance.
	Spill *tool.Spill
}

// Tools returns the two web tools in the order they are registered.
func Tools(options Options) []tool.Tool {
	if strings.TrimSpace(options.APIKey) == "" {
		return nil
	}
	set := &shared{
		origin: strings.TrimSuffix(strings.TrimSpace(options.BaseURL), "/"),
		apiKey: options.APIKey,
		client: options.HTTPClient,
		delay:  options.RetryDelay,
		spill:  options.Spill,
	}
	if set.origin == "" {
		set.origin = DefaultBaseURL
	}
	if set.client == nil {
		set.client = &http.Client{Timeout: defaultTimeout}
	}
	if set.delay == 0 {
		set.delay = defaultRetryDelay
	}
	return []tool.Tool{searchTool{set: set}, fetchTool{set: set}}
}

// shared is the state both tools need: where the endpoint is, what to present,
// and how to wait before a second try.
type shared struct {
	origin string
	apiKey string
	client *http.Client
	delay  time.Duration
	spill  *tool.Spill
}

// do posts one request and decodes the reply into response.
//
// Retrying lives here rather than in each tool because the decision is the
// vendor's, not the caller's: the same table of status codes applies to both
// endpoints, and a tool that retried on its own would eventually disagree.
func (s *shared) do(ctx context.Context, path string, request, response any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("web: encode request: %w", err)
	}
	endpoint := s.origin + path

	var failure error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := sleep(ctx, s.delay); err != nil {
				return err
			}
		}
		failure = s.once(ctx, endpoint, payload, response)
		if failure == nil {
			return nil
		}
		// A cancelled turn must not spend its budget on a second attempt.
		if ctx.Err() != nil || !retryable(failure) {
			return failure
		}
	}
	return failure
}

// once performs a single attempt.
func (s *shared) once(ctx context.Context, endpoint string, payload []byte, response any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.apiKey)
	request.Header.Set("Content-Type", "application/json")

	reply, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer reply.Body.Close()

	body, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("web: read response: %w", err)
	}
	if reply.StatusCode != http.StatusOK {
		return responseError(reply.StatusCode, body)
	}
	if err := json.Unmarshal(body, response); err != nil {
		return fmt.Errorf("web: decode response: %w", err)
	}
	return nil
}

// apiError is a reply that was not 200. It carries the vendor's own message,
// which is the only part of a failed call worth repeating: the request holds
// the credential, so no error ever formats one.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("web: http %d", e.status)
	}
	return fmt.Sprintf("web: http %d: %s", e.status, e.message)
}

// responseError extracts the message and discards the envelope. A reply that is
// not JSON at all keeps a short head of its body, because seeing "Bad Gateway"
// is more useful than seeing nothing.
func responseError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	message := strings.TrimSpace(envelope.Error.Message)
	if message == "" {
		message = clampRunes(strings.TrimSpace(string(body)), 300)
	}
	return &apiError{status: status, message: message}
}

// retryable reports whether another attempt can plausibly differ. The list is
// the vendor's: 408, 429 and 5xx are worth a second try, every other 4xx
// repeats identically and is reported as-is.
func retryable(err error) bool {
	var api *apiError
	if errors.As(err, &api) {
		return api.status == http.StatusRequestTimeout ||
			api.status == http.StatusTooManyRequests ||
			api.status >= 500
	}
	// Everything else is a transport failure. There is no status to inspect,
	// and a dropped connection is exactly what a retry is for.
	var transport *url.Error
	return errors.As(err, &transport)
}

// explain turns a failure into the one line the caller can act on. Each status
// has exactly one thing to do about it, and none of them is "send the same
// request again" — which is why this text replaces the raw message instead of
// being added to it.
func explain(name string, err error) error {
	var api *apiError
	if !errors.As(err, &api) {
		return fmt.Errorf("%s: %w", name, err)
	}
	switch api.status {
	case http.StatusBadRequest:
		return fmt.Errorf("%s: the endpoint rejected the arguments: %s", name, api.message)
	case http.StatusUnauthorized:
		return fmt.Errorf("%s: the credential was rejected; check web.apiKeyEnv or providers.deepseek credentials in config.json", name)
	case http.StatusPaymentRequired:
		return fmt.Errorf("%s: the account has no balance, so search and fetch are unavailable until it is topped up", name)
	case http.StatusForbidden:
		return fmt.Errorf("%s: the page was refused by the endpoint's access policy; try another source", name)
	case http.StatusNotFound:
		return fmt.Errorf("%s: no readable page at that url: the site blocked the request, returned an error, or has no extractable text", name)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: rate limited; wait a moment before asking again", name)
	case http.StatusRequestTimeout:
		return fmt.Errorf("%s: the endpoint timed out", name)
	}
	if api.status >= 500 {
		return fmt.Errorf("%s: the endpoint failed (http %d); it is not a problem with this request", name, api.status)
	}
	return fmt.Errorf("%s: %s", name, api.message)
}

// sleep waits, or gives up when the turn is cancelled.
func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// clampRunes shortens text without splitting a rune.
func clampRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// runeLen counts characters, which is the unit the endpoint's limits are
// written in.
func runeLen(text string) int { return len([]rune(text)) }
