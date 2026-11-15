package main

// The side conversation's gate asks before a call that would change something,
// and the question has three answers: no, yes this once, and yes for the rest of
// this session. The third one is what makes a gate livable — the eighth copy of
// the same write should not need the eighth identical question — and it is
// deliberately the narrowest standing permission that still helps.

import (
	"sync"

	"pi-go/internal/tool"
	"pi-go/internal/tui"
)

// allowedTools is the reader's "yes, and stop asking" for one session: a set of
// tool names, owned by the session they were answered in.
//
// Both halves of that scope are load-bearing. A set of names with no session
// would carry a yes into the next conversation, which is a different set of files
// and often a different project; a session-wide flag with no names would turn one
// answer about `bash` into permission for `write`. So the set is dropped whenever
// the open session changes, and only the tool the question named goes in.
type allowedTools struct {
	mu      sync.Mutex
	session string
	names   map[string]struct{}
}

// allows reports whether this tool was already answered "for this session".
func (a *allowedTools) allows(session, name string) bool {
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

// remember records one standing yes.
//
// The reset happens here rather than in a hook on session switching: a hook is a
// call somebody has to remember to make on every path that opens or resumes a
// session, and the one path that forgot would be a permission that outlived its
// conversation.
func (a *allowedTools) remember(session, name string) {
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

// approveTool is the gate the side conversation asks through: a remembered
// answer first, then the reader. The note is how the standing answer becomes
// visible — it is the one trace of a permission that no longer draws a box, and a
// reader who wants it gone needs to be able to see that it exists.
func approveTool(state *sideState, session string, ask func(tui.AskRequest) tui.AskAnswer, note func(string)) func(tool.Call) bool {
	return func(call tool.Call) bool {
		if state.allowed.allows(session, call.Name) {
			return true
		}
		if state.engine != nil && state.engine.AllowedForSession(session, call.Name) {
			return true
		}
		reply := ask(tui.AskRequest{Subject: approvalSubject(call), Tool: call.Name})
		if reply.Allowed && reply.Always {
			state.allowed.remember(session, call.Name)
			if state.engine != nil {
				state.engine.AllowForSession(session, call.Name)
			}
			note(call.Name + ": allowed for the rest of this session")
		}
		return reply.Allowed
	}
}
