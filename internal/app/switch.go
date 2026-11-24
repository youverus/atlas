package app

import (
	"fmt"
	"os"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// SwitchSession continues a different session file in place: the manager is
// replaced, the transcript is re-read from the file, and the knobs a session
// records — model, thinking level, system prompt, name — are adopted from it,
// the same restore a startup resume runs.
//
// The swap is all or nothing. Every fallible step runs before the first field
// is touched, so a switch that fails partway leaves the current conversation
// exactly as it was.
//
// A running turn must be stopped by the caller first, the same contract
// NewSession states; a turn that is still running is rejected rather than
// abandoned, because a caller that wants to switch anyway has an abort to
// call. The steering and follow-up queues go with the old conversation: a
// message typed for one transcript must not arrive in another.
//
// The target session may belong to another project. What it records is the
// directory its tools ran in, and this app asks Config.Tools and
// Config.SystemPrompt for that directory rather than for the one the process
// started in, so the conversation arrives with the workspace and the
// instructions it was recorded under. The recorded directory still has to
// exist: a project that was deleted cannot host the tools, and there is no
// override here — a tool call would otherwise fail on its first path.
func (a *App) SwitchSession(path string) error {
	return a.switchSession(path, true)
}

// SwitchSessionLeavingArchived continues a different session file even when the
// conversation currently in view is archived. A windowed viewer can inspect an
// archived transcript read-only and still navigate away to another conversation
// without restoring or deleting it; the archived file itself stays untouched.
func (a *App) SwitchSessionLeavingArchived(path string) error {
	return a.switchSession(path, false)
}

func (a *App) switchSession(path string, guardArchived bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			// session.Open treats a missing file as a fresh session to create,
			// which is right for -session and wrong here: resuming a path that
			// does not exist would silently abandon the conversation.
			return fmt.Errorf("app: no session file at %s", path)
		}
		return fmt.Errorf("app: open %s: %w", path, err)
	}
	// The session directory stays: it is the store the caller configured, and
	// where a target file happens to live says nothing about where the next
	// session belongs.
	manager, err := session.Open(path, a.manager.Dir(), "")
	if err != nil {
		return err
	}
	// A recorded directory that is gone cannot host the tools. Opening it
	// anyway would put the conversation on screen and then fail inside the
	// first call, far from the reason.
	if missing, gone := manager.MissingCwd(); gone {
		return fmt.Errorf("app: session %s was recorded in %s, which no longer exists; restore that directory to resume it, or open it with -session and -cwd to run it here", path, missing)
	}
	projected, err := manager.BuildContext()
	if err != nil {
		return err
	}
	if guardArchived {
		if err := a.guardArchived(); err != nil {
			return err
		}
	}
	// The instructions of the project being opened, read before anything is
	// swapped: a file that cannot be read refuses the switch instead of
	// leaving half of one behind. A session that carries its own prompt
	// contributes no source and no reading — the recorded text wins, the same
	// rule the tools follow. A process with no source keeps the fixed text it
	// was given, because there is nothing else to ask.
	stored := storedPrompt(projected.Messages)
	prompt, promptSource := stored, a.basePromptSource
	switch {
	case stored != "":
		promptSource = nil
	case promptSource != nil:
		if prompt, err = promptSource(manager.Cwd()); err != nil {
			return err
		}
	default:
		prompt = a.baseSystemPrompt
	}
	// The session being left is deliberately not written here: one the user
	// never talked to has no file, and inventing a header line for it is what
	// filled the session directory with empty sessions after every switch.

	a.manager = manager
	if a.side != nil {
		a.side.close()
		a.side = nil
	}
	a.steering.clear()
	a.followUps.clear()
	a.steering.setMode("")
	a.followUps.setMode("")

	a.model = a.baseModel
	a.provider = a.baseProvider
	a.thinking = a.baseThinking
	a.pendingModel = nil
	a.systemPrompt = prompt
	// The source comes back with the other knobs: dropping it was a fact about
	// the session that was left, not about this one. It is nil again when the
	// session that was opened carries its own instructions.
	a.promptSource = promptSource
	a.restoreNote = ""
	hasConversation := len(projected.Messages) > 0
	a.adoptThinkingLevel(manager.BuildContextEntries(), projected.ThinkingLevel)
	a.adoptModel(projected.Model, hasConversation)

	a.ag = a.buildAgent(a.systemPrompt, &a.steering, &a.followUps)
	a.ag.SetMessages(projected.Messages)
	a.messages = append([]wire.Message(nil), projected.Messages...)
	return nil
}
