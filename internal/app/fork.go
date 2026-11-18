package app

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"pi-go/internal/session"
	"pi-go/internal/wire"
)

var (
	// ErrNothingToFork reports that the session holds nothing to branch from. It is
	// a state, not a defect: the branch builder answers a missing leaf with a
	// not-found carrying the id it was given, which for an empty session is the
	// empty string and reads like a bug in the caller.
	ErrNothingToFork = errors.New("app: nothing to fork (session is empty)")

	// ErrInvalidForkEntry reports an entry id that does not name a user message
	// in this session.
	ErrInvalidForkEntry = errors.New("app: invalid entry id for forking")

	// ErrSessionNotSaved reports a disk-backed session whose file has not been
	// created yet: lazy creation only writes on the first user or assistant
	// message, so a branch cannot point parentSession at a missing file.
	ErrSessionNotSaved = errors.New("app: session has not been saved yet")
)

// ForkMessage identifies one user message that a session can fork from.
type ForkMessage struct {
	EntryID string
	Text    string
}

// ForkSession continues the conversation in a new session and leaves the
// current one on disk untouched.
//
// The fork holds a copy of the active branch rather than a pointer into the
// original: the two conversations have to be able to diverge, and sharing
// entries would make the first write to either file a decision about the other.
// The header records the original file as the parent, so "where did this come
// from" stays answerable after the fork.
//
// An in-memory conversation stays in memory: it gets a new id and the same
// entries, and nothing is written.
func (a *App) ForkSession() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return "", ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		return "", err
	}
	if a.manager.EntryCount() == 0 {
		// Forking costs a new file and a new id, so doing it before the first
		// message would only rename the empty session the user is looking at.
		return "", ErrNothingToFork
	}
	if _, err := a.manager.CreateBranchedSession(""); err != nil {
		return "", err
	}
	// The transcript is the branch that was just copied, so the rebuild is a
	// no-op the first time; it runs anyway because the manager is now a
	// different session and the app must not hold state from the old one.
	if err := a.refresh(); err != nil {
		return "", err
	}
	return a.manager.ID(), nil
}

// ForkMessages returns every user message on the active branch with non-empty
// text, in the order it was said, so a caller can choose a point to fork before.
//
// The branch rather than the file: a session can hold entries the current leaf
// does not descend from, and a message the conversation in view no longer
// contains is not a place it can be branched.
func (a *App) ForkMessages() []ForkMessage {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]ForkMessage, 0)
	for _, entry := range a.manager.Path("") {
		if entry.Type != session.TypeMessage {
			continue
		}
		var probe struct {
			Role    wire.Role        `json:"role"`
			Content wire.ContentList `json:"content"`
		}
		if err := json.Unmarshal(entry.Message, &probe); err != nil {
			continue
		}
		if probe.Role != wire.RoleUser {
			continue
		}
		if text := forkMessageText(probe.Content); text != "" {
			out = append(out, ForkMessage{EntryID: entry.ID, Text: text})
		}
	}
	return out
}

// ForkAt branches into a new session containing the history strictly before
// entryID, which must identify a user message entry in this session, and returns
// that message's text so the caller can prefill it for editing.
//
// When entryID has no parent (it is the first entry in the session), the new
// session starts empty with parentSession pointing at the original file; lazy
// creation keeps it off disk until a new message arrives. Queues and any open
// side conversation belong to the abandoned future and are discarded.
func (a *App) ForkAt(entryID string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return "", ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		return "", err
	}

	selected, ok := a.manager.Entry(entryID)
	if !ok || selected.Type != session.TypeMessage {
		return "", ErrInvalidForkEntry
	}
	var probe struct {
		Role    wire.Role        `json:"role"`
		Content wire.ContentList `json:"content"`
	}
	if err := json.Unmarshal(selected.Message, &probe); err != nil || probe.Role != wire.RoleUser {
		return "", ErrInvalidForkEntry
	}
	selectedText := forkMessageText(probe.Content)

	targetLeafID := selected.ParentID
	if targetLeafID != nil {
		if _, exists := a.manager.Entry(*targetLeafID); !exists {
			return "", ErrInvalidForkEntry
		}
		if err := a.requireSaved(); err != nil {
			return "", err
		}
		if _, err := a.manager.CreateBranchedSession(*targetLeafID); err != nil {
			return "", err
		}
	} else {
		previousFile := ""
		if a.manager.Persisted() {
			previousFile = a.manager.File()
		}
		if _, err := a.manager.NewSession(session.Options{ParentSession: previousFile}); err != nil {
			return "", err
		}
	}

	a.resetQueuesAndSide()

	projected, err := a.manager.BuildContext()
	if err != nil {
		return "", err
	}
	a.model = a.baseModel
	a.provider = a.baseProvider
	a.thinking = a.baseThinking
	a.systemPrompt = a.baseSystemPrompt
	a.promptSource = a.basePromptSource
	a.restoreNote = ""
	a.adoptThinkingLevel(a.manager.BuildContextEntries(), projected.ThinkingLevel)
	a.adoptModel(projected.Model, projected.Model != nil)
	a.adoptSystemPrompt(projected.Messages)

	a.ag = a.buildAgent(a.systemPrompt, &a.steering, &a.followUps)
	a.ag.SetMessages(projected.Messages)
	a.messages = append([]wire.Message(nil), projected.Messages...)
	return selectedText, nil
}

// CloneSession duplicates the active branch at the current leaf into a new
// session and returns its id. Unlike ForkSession, it rejects an empty leaf
// ("no current entry selected") and an unflushed disk-backed session whose file
// does not exist yet, matching pi's clone command.
func (a *App) CloneSession() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return "", ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		return "", err
	}
	leaf := a.manager.LeafID()
	if leaf == nil {
		return "", ErrNothingToFork
	}
	if err := a.requireSaved(); err != nil {
		return "", err
	}
	if _, err := a.manager.CreateBranchedSession(*leaf); err != nil {
		return "", err
	}
	a.resetQueuesAndSide()
	if err := a.refresh(); err != nil {
		return "", err
	}
	return a.manager.ID(), nil
}

func (a *App) requireSaved() error {
	if !a.manager.Persisted() || a.manager.File() == "" {
		return nil
	}
	if _, err := os.Stat(a.manager.File()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrSessionNotSaved
		}
		return err
	}
	return nil
}

func (a *App) resetQueuesAndSide() {
	if a.side != nil {
		a.side.close()
		a.side = nil
	}
	a.steering.clear()
	a.followUps.clear()
	a.steering.setMode("")
	a.followUps.setMode("")
}

// forkMessageText extracts user message text the way pi's contentText(..., "")
// does: plain strings as-is, and text blocks joined without a separator.
func forkMessageText(content wire.ContentList) string {
	if content.IsPlain() {
		return content.PlainText()
	}
	var out strings.Builder
	for _, block := range content.Items() {
		if block.Type == wire.ContentText {
			out.WriteString(block.Text)
		}
	}
	return out.String()
}
