// Package channel connects chat platforms to the kernel: adapters run on one
// side of a normalized event, and the same app.Ask the terminal and the window
// drive runs on the other.
//
// It lives in its own module (pi-go/channel) for the same reason desktop/ does:
// the Feishu adapter needs a third-party SDK, and the kernel module's
// dependency gate keeps internal/ stdlib-only so every offline test means what
// it says. The gateway imports pi-go/internal (assembly, wire, config) — Go's
// internal rule is about the import path prefix, not the directory tree.
//
// One gateway process runs N adapters over one Runner. Every inbound message
// is deduped by its platform id, bound to a session file by its chat, and
// executed serially per chat: two messages from one chat never race two turns.
package channel

import (
	"context"
	"time"
)

// ChatType distinguishes the two conversation shapes a platform offers. It is
// part of the binding key only through Target.Key's adapter and chat ids, so
// it is advisory: callers use it to pick wording, not to route.
type ChatType string

const (
	ChatDM    ChatType = "dm"
	ChatGroup ChatType = "group"
)

// Target identifies one conversation on one platform.
//
// UserID is the platform's own stable id for the human (Feishu open_id, QQ
// OpenID) — never a phone number or QQ number. UserName is what the platform
// displayed, for greetings and logs only.
type Target struct {
	Adapter  string
	ChatID   string
	ThreadID string // Feishu topic; empty outside one
	ChatType ChatType
	UserID   string
	UserName string
}

// Key is the binding key: the session a message lands in is decided by this,
// never by a string an adapter happens to build. A Feishu p2p chat_id is
// already per-user, so adapter + chat + thread covers DM / group / topic
// without a fourth field.
func (t Target) Key() string {
	key := t.Adapter + ":" + t.ChatID
	if t.ThreadID != "" {
		key += ":" + t.ThreadID
	}
	return key
}

// Inbound is one message from a platform, normalized into what a turn needs.
// Text carries the command when it is one; the gateway parses it.
type Inbound struct {
	Target    Target
	MessageID string // platform id: the dedupe key, and Feishu's reply anchor
	Text      string
	At        time.Time
}

// Capabilities declares what a platform does with an outbound answer, so the
// gateway never asks "can this platform edit" with an if over names.
type Capabilities struct {
	MaxRunes int // per-message ceiling; the gateway chunks to it
}

// Adapter is one platform transport. Run blocks for the life of the
// connection — the gateway runs it in its own goroutine — and every method
// must stay safe to call from the gateway's goroutines.
type Adapter interface {
	Name() string
	Caps() Capabilities
	// Run connects and feeds normalized messages to sink until ctx ends.
	Run(ctx context.Context, sink Sink) error
	// Send delivers one piece of text to the chat. replyTo, when non-empty,
	// anchors the answer under the reader's message; only the gateway's first
	// chunk carries it, so a five-chunk answer does not quote five times.
	Send(ctx context.Context, t Target, text string, replyTo string) error
}

// Sink is the gateway side of the contract: the one call an adapter makes
// with an admitted message. Implementations must not block on the turn.
type Sink interface {
	Inbound(msg Inbound)
}
