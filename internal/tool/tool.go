// Package tool defines the execution boundary: what a tool receives from the
// model and what it returns. Tools know nothing about providers or sessions.
package tool

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"pi-go/internal/wire"
)

// Call is one tool invocation requested by the model.
type Call struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Partial is a streaming update emitted while a tool runs.
type Partial struct {
	Content []wire.Content
	Details json.RawMessage
}

// Result is the final outcome of a tool call. IsError tells the model the call
// failed without aborting the run; returning an error from Execute does the
// same after the agent converts it.
type Result struct {
	Content           []wire.Content
	Details           json.RawMessage
	StructuredContent json.RawMessage
	Usage             *wire.Usage
	IsError           bool
	Terminate         bool
}

// Tool is an executable function exposed to the model.
type Tool interface {
	// Name is the identifier the model uses.
	Name() string
	// Label is a short human-readable name for UI output.
	Label() string
	// Description is shown to the model.
	Description() string
	// Parameters is a JSON Schema object. It is declared once and reused.
	Parameters() json.RawMessage
	// Execute runs the call. It should honor ctx and may stream partial
	// results through onUpdate, which is never nil.
	Execute(ctx context.Context, call Call, onUpdate func(Partial)) (Result, error)
}

// Background describes a command that outlives the tool call that started it.
//
// The split it encodes is the whole reason this type is here rather than in a
// task package: the tool knows how to start a process and the recorder knows
// how to keep a record, and neither has to import the other. A tool that was
// handed no Recorder has no background capability at all, and says so by not
// declaring the parameter (the shell tool does exactly that).
type Background struct {
	// Command is what the model asked for, verbatim.
	Command string
	// Description is a one-line summary for listings.
	Description string
	// Cwd is where it runs.
	Cwd string
	// ToolCallID is the call that started it.
	ToolCallID string
}

// BackgroundExit is how a detached command ended.
type BackgroundExit struct {
	// Code is the exit status, or 128+signal when it was killed.
	Code int
	// Aborted is set when the group was killed on purpose.
	Aborted bool
	// TimedOut is set when the command's own timeout fired.
	TimedOut bool
	// Err is a failure to run at all, as opposed to a non-zero exit.
	Err error
	// StartedAt and EndedAt bracket the run, so the record can report how long
	// it took without a second clock.
	StartedAt time.Time
	EndedAt   time.Time
}

// Recorder keeps the record of a background command.
//
// Register creates the row and returns the file its output goes to; the tool
// writes the bytes because it already owns the pipe. Started exists because the
// row cannot say "running" before there is a process to be running, and Settled
// is called once from whichever goroutine reaps it.
type Recorder interface {
	Register(spec Background) (id string, output *os.File, err error)
	Started(id string, pgid int)
	Settled(id string, exit BackgroundExit)
}

// Declaration converts a tool into the wire form sent to the model.
func Declaration(t Tool) wire.Tool {
	return wire.Tool{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters:  t.Parameters(),
	}
}

// SessionContext identifies the active conversation for session-scoped tools.
type SessionContext struct {
	ID   string
	File string
	Cwd  string
}

type sessionContextKey struct{}

// WithSession attaches session metadata to ctx so session-scoped tools can
// locate their per-conversation state even when a tool set is shared across
// multiple live sessions in the same workspace.
func WithSession(ctx context.Context, session SessionContext) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, session)
}

// SessionFromContext extracts the SessionContext attached by WithSession.
func SessionFromContext(ctx context.Context) (SessionContext, bool) {
	if ctx == nil {
		return SessionContext{}, false
	}
	session, ok := ctx.Value(sessionContextKey{}).(SessionContext)
	if !ok || (session.ID == "" && session.File == "" && session.Cwd == "") {
		return SessionContext{}, false
	}
	return session, true
}
