package assembly

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"pi-go/internal/agent"
	"pi-go/internal/permission"
	"pi-go/internal/tool"
)

// The side conversation is the kernel's temporary branch with a reader in front
// of it: it never reaches disk, and what it may do is narrower than the main
// conversation.

// SideReadOnly names the calls a side conversation may make without asking
// first. It is an allowlist, not a list of dangerous tools: a tool added later
// has to be classified on purpose instead of quietly escaping the gate.
var SideReadOnly = map[string]bool{
	"read": true,
	"ls":   true,
	"find": true,
	"grep": true,
	// The git tools belong here for the same reason the file readers do: they
	// only read, and the argv whitelist is what makes that true rather than
	// promised.
	"git_status": true,
	"git_diff":   true,
	"git_log":    true,
	"git_show":   true,
}

// The two things the model is told when a call did not run. Both are one line:
// the renderer prints a failure's reason under the call, so a paragraph would
// land in the user's scrollback on every refusal, and the rule it states is
// short enough to be worth reading anyway.
const (
	SideDeclined = "side conversation: the user declined this change. Reads run without asking; changes do not. Do not retry it here."
	SideNoReader = "side conversation: this change needs approval and there is no terminal to ask on, so only reads run here."
)

// AllowedTools is the reader's "yes, and stop asking" for one session: a set of
// tool names, owned by the session they were answered in.
//
// Both halves of that scope are load-bearing. A set of names with no session
// would carry a yes into the next conversation, which is a different set of files
// and often a different project; a session-wide flag with no names would turn one
// answer about `bash` into permission for `write`. So the set is dropped whenever
// the open session changes, and only the tool the question named goes in.
type AllowedTools struct {
	mu      sync.Mutex
	session string
	names   map[string]struct{}
}

// Allows reports whether this tool was already answered "for this session".
func (a *AllowedTools) Allows(session, name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != session {
		// A session that was never written to this list has nothing remembered,
		// whatever the last one had.
		return false
	}
	_, ok := a.names[name]
	return ok
}

// Remember records one standing yes.
//
// The reset happens here rather than in a hook on session switching: a hook is a
// call somebody has to remember to make on every path that opens or resumes a
// session, and the one path that forgot would be a permission that outlived its
// conversation.
func (a *AllowedTools) Remember(session, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != session {
		a.session = session
		a.names = nil
	}
	if a.names == nil {
		a.names = make(map[string]struct{})
	}
	a.names[name] = struct{}{}
}

// ApprovalSubject is the sentence shown when a tool asks. The arguments are part
// of it because two calls to the same tool are not the same question: "bash"
// alone does not say what would run.
func ApprovalSubject(call tool.Call) string {
	subject := call.Name
	if compact := compactArgs(call.Arguments, 100); compact != "" {
		subject += "  " + compact
	}
	return subject
}

func compactArgs(raw json.RawMessage, limit int) string {
	if len(raw) == 0 {
		return ""
	}
	return cut(strings.Join(strings.Fields(string(raw)), " "), limit)
}

// cut shortens text to limit runes, marking the cut with an ellipsis.
func cut(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// Gate is the approval path for one session: the permission ladder, the
// remembered yeses, and the question a front end answers.
//
// It is a value rather than a callback so the three front ends share one
// implementation. A terminal answers the question with a keypress, a window with
// a blocking POST, and a script with "no": all three are the same gate with a
// different Ask.
type Gate struct {
	engine  *permission.Engine
	allowed AllowedTools

	mu sync.Mutex
	// typing says the next turn belongs to a side conversation. It is set by
	// the front end because only it knows which conversation the reader is
	// typing into.
	typing bool
	// ask asks the reader. Nil denies: a caller with no reader cannot approve
	// anything, and guessing yes is the one answer that loses data.
	ask func(Ask) Answer
	// note reports a standing yes, so a permission that no longer draws a box
	// leaves a trace the reader can find.
	note func(string)
	// session is the conversation the questions and the rememberings belong to.
	// It is read through a function because /resume replaces the manager.
	session func() string
}

// NewGate is the gate for a front end that owns the engine itself. /permission
// changes the mode on a live engine, and a test builds one to describe a posture
// without a config file; both need the gate and the ladder to be the same object.
func NewGate(engine *permission.Engine) *Gate { return &Gate{engine: engine} }

// Engine is the permission ladder this gate consults. It is exported because the
// front ends own the mode switch: /permission writes to the same engine.
// A nil gate answers nil rather than panicking: a front end that has no gate at
// all asks the same question as one whose gate has no engine.
func (g *Gate) Engine() *permission.Engine {
	if g == nil {
		return nil
	}
	return g.engine
}

// SetAsk replaces the question path. A nil function means "no reader".
func (g *Gate) SetAsk(ask func(Ask) Answer, note func(string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ask = ask
	g.note = note
}

// SetTyping marks whether the next turn belongs to the side conversation.
func (g *Gate) SetTyping(typing bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.typing = typing
}

// SetSession names the conversation new answers are remembered against.
func (g *Gate) SetSession(session func() string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.session = session
}

// BeforeToolCall is app.Config.BeforeToolCall: the gate that enforces the
// permission ladder in the main conversation and the read-only boundary in the
// side conversation.
func (g *Gate) BeforeToolCall(ctx context.Context, call tool.Call) (*agent.Block, error) {
	g.mu.Lock()
	typing, ask, session := g.typing, g.ask, g.session
	g.mu.Unlock()

	approve := g.approveFunc(ask, session)
	if typing {
		if g.engine != nil {
			if decision := g.engine.Decide(call); decision.Behavior == permission.Deny {
				return &agent.Block{
					Block:  true,
					Reason: "permission denied: " + decision.Reason + ". Do not retry or work around this call.",
				}, nil
			}
		}
		if SideReadOnly[call.Name] {
			return nil, nil
		}
		if approve == nil {
			return &agent.Block{Block: true, Reason: SideNoReader}, nil
		}
		if !approve(call) {
			return &agent.Block{Block: true, Reason: SideDeclined}, nil
		}
		return nil, nil
	}

	if g.engine == nil {
		return nil, nil
	}
	if blocked, reason := g.engine.Gate(ctx, call, approve); blocked {
		return &agent.Block{Block: true, Reason: reason}, nil
	}
	return nil, nil
}

// approveFunc is the gate the questions go through: a remembered answer first,
// then the reader.
func (g *Gate) approveFunc(ask func(Ask) Answer, session func() string) func(tool.Call) bool {
	if ask == nil {
		return nil
	}
	return func(call tool.Call) bool {
		id := ""
		if session != nil {
			id = session()
		}
		if g.allowed.Allows(id, call.Name) {
			return true
		}
		if g.engine != nil && g.engine.AllowedForSession(id, call.Name) {
			return true
		}
		reply := ask(Ask{Subject: ApprovalSubject(call), Tool: call.Name})
		if reply.Allowed && reply.Always {
			g.allowed.Remember(id, call.Name)
			if g.engine != nil {
				g.engine.AllowForSession(id, call.Name)
			}
			g.mu.Lock()
			note := g.note
			g.mu.Unlock()
			if note != nil {
				note(call.Name + ": allowed for the rest of this session")
			}
		}
		return reply.Allowed
	}
}
