package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// streamBuffer is how many events a subscriber may fall behind before its
// oldest pending event is dropped. It is deliberately small: the point of the
// buffer is to absorb a repaint, not to let a frozen window replay a turn.
const streamBuffer = 256

// heartbeatEvery is how often a comment line is written to an idle stream. A
// comment is legal SSE that carries no event, so the page ignores it; its job
// is to fail fast on a window that is gone, since a write to a closed socket is
// the only reliable signal that nobody is reading.
const heartbeatEvery = 15 * time.Second

// streamEvent is one frame waiting to be written. Name becomes the SSE event
// name; an empty name is the default event that EventSource reports as
// "message".
type streamEvent struct {
	Name string
	Data []byte
}

// frame renders the event as one SSE frame. Data is written as a single data
// line: json.Marshal never emits a newline, so the multi-line rule does not
// apply here — and if it ever did, a bare newline would silently split the
// frame into two events.
func (e streamEvent) frame() []byte {
	var buffer bytes.Buffer
	if e.Name != "" {
		buffer.WriteString("event: ")
		buffer.WriteString(e.Name)
		buffer.WriteByte('\n')
	}
	buffer.WriteString("data: ")
	buffer.Write(e.Data)
	buffer.WriteString("\n\n")
	return buffer.Bytes()
}

// subscriber is one open stream. The channel is written by publishers and read
// only by the handler that owns the connection, so a slow reader delays nobody
// else.
type subscriber struct {
	events chan streamEvent
	closed chan struct{}
	once   sync.Once
}

// drop closes the subscriber exactly once. Both the broker and the handler can
// arrive here, in either order.
func (s *subscriber) drop() {
	s.once.Do(func() { close(s.closed) })
}

// broker fans one stream of events out to every open window.
//
// Publishing never blocks: a subscriber whose buffer is full loses its oldest
// event instead of stalling the turn that produced it. The alternative — a
// blocking send — turns one frozen window into a frozen conversation, and the
// event that gets dropped is at worst a repaint the next frame repeats.
type broker struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
}

func newBroker() *broker {
	return &broker{subs: make(map[*subscriber]struct{})}
}

func (b *broker) subscribe() *subscriber {
	sub := &subscriber{
		events: make(chan streamEvent, streamBuffer),
		closed: make(chan struct{}),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		// Close already ran: hand back a subscriber that is born dropped so the
		// handler writes one frame and returns instead of blocking forever.
		sub.drop()
		return sub
	}
	b.subs[sub] = struct{}{}
	return sub
}

func (b *broker) unsubscribe(sub *subscriber) {
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
	sub.drop()
}

func (b *broker) publish(event streamEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		select {
		case sub.events <- event:
		default:
			// Full: discard the oldest and take the newest. Keeping the tail is
			// what makes a recovered window show the current state rather than
			// a replay that ends where the reader already was.
			select {
			case <-sub.events:
			default:
			}
			select {
			case sub.events <- event:
			default:
			}
		}
	}
}

// close drops every subscriber and refuses later ones. Window shutdown goes
// through here, so a stream blocked on write is released by its own handler
// noticing closed.
func (b *broker) close() {
	b.mu.Lock()
	b.closed = true
	subs := make([]*subscriber, 0, len(b.subs))
	for sub := range b.subs {
		subs = append(subs, sub)
	}
	b.subs = make(map[*subscriber]struct{})
	b.mu.Unlock()
	for _, sub := range subs {
		sub.drop()
	}
}

// serveEvents holds one stream open for the life of the window.
//
// No CORS headers are set: the page is served by this server, so a same-origin
// request needs none, and adding them would invite the cross-origin reads the
// host check exists to refuse.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "httpapi: streaming unsupported", http.StatusInternalServerError)
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")

	// retry is the client's reconnect delay after a dropped connection: it is
	// the cheapest way to survive the window being backgrounded.
	fmt.Fprint(w, "retry: 1000\n\n")
	flusher.Flush()

	sub := s.broker.subscribe()
	defer s.broker.unsubscribe(sub)

	if payload, err := json.Marshal(s.facts); err == nil {
		if _, err := w.Write(streamEvent{Name: "hello", Data: payload}.frame()); err != nil {
			return
		}
		flusher.Flush()
	}

	heartbeat := time.NewTicker(heartbeatEvery)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.closed:
			return
		case event := <-sub.events:
			if _, err := w.Write(event.frame()); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			// A comment line: legal, ignored by EventSource, and it fails on a
			// socket whose window has gone away.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
