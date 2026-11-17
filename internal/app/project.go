package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"pi-go/internal/session"
)

// SessionDir names the store a directory's conversations live in.
//
// The window asks this for every project it draws: a project is a directory,
// and its conversations are the files in that directory's store. A kernel built
// without the policy cannot answer, and says so instead of pointing somewhere
// plausible — the default layout and a store configured by path are both legal
// shapes, and guessing between them would list one project's conversations
// under another.
func (a *App) SessionDir(cwd string) (string, error) {
	a.mu.Lock()
	policy := a.sessionDir
	a.mu.Unlock()
	return sessionDirFor(policy, cwd)
}

// NewSessionIn abandons the conversation and starts an empty one in another
// directory.
//
// This is the move a project list makes when the reader asks for a new
// conversation under a folder they picked: the tools, the instructions and the
// session file all belong to that directory rather than to the one this process
// started in, so the manager is rebuilt for it — the same rebuild a switch to
// another project's conversation performs, with nothing to restore.
//
// The old conversation is deliberately not written here, for the reason
// NewSession gives: a conversation nobody talked to leaves no file behind.
func (a *App) NewSessionIn(cwd string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		return err
	}
	trimmed := cwd
	if trimmed == "" {
		return errors.New("app: no directory to start the conversation in")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return fmt.Errorf("app: resolve %s: %w", cwd, err)
	}
	// A directory that is gone cannot host the tools: starting anyway would put
	// an empty conversation on screen and fail inside the first call, far from
	// the reason.
	info, err := os.Stat(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("app: %s does not exist", absolute)
		}
		return fmt.Errorf("app: stat %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("app: %s is not a directory", absolute)
	}

	dir, err := sessionDirFor(a.sessionDir, absolute)
	if err != nil {
		return err
	}
	manager, err := session.Create(absolute, dir, session.Options{})
	if err != nil {
		return err
	}
	// The instructions of the directory being opened, read before anything is
	// swapped: a file that cannot be read refuses the move instead of leaving
	// half of one behind, the same rule a switch follows.
	prompt, source := a.baseSystemPrompt, a.basePromptSource
	if source != nil {
		if prompt, err = source(manager.Cwd()); err != nil {
			return err
		}
	}

	a.manager = manager
	if a.side != nil {
		a.side.close()
		a.side = nil
	}
	// The knobs go back to the config's: the conversation that was left may
	// have switched models, and a new conversation in a new project starts on
	// what this process was told to use.
	a.steering.clear()
	a.followUps.clear()
	a.steering.setMode("")
	a.followUps.setMode("")
	a.model = a.baseModel
	a.provider = a.baseProvider
	a.thinking = a.baseThinking
	a.systemPrompt = prompt
	a.promptSource = source
	a.restoreNote = ""
	a.ag = a.buildAgent(a.systemPrompt, &a.steering, &a.followUps)
	a.ag.SetMessages(nil)
	a.messages = nil
	return nil
}

// sessionDirFor asks the policy, and refuses when there is none.
func sessionDirFor(policy SessionDirFunc, cwd string) (string, error) {
	if policy == nil {
		return "", errors.New("app: this kernel was built without a session directory policy")
	}
	return policy(cwd)
}
