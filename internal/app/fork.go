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

	// ErrInvalidForkEntry reports an entry id that does not name an answer in
	// this session: a branch is cut after a reply, and a reply is the only place
	// a turn can be left without rewriting the question that opened it.
	ErrInvalidForkEntry = errors.New("app: invalid entry id for forking")

	// ErrSessionNotSaved reports a disk-backed session whose file has not been
	// created yet: lazy creation only writes on the first user or assistant
	// message, so a branch cannot point parentSession at a missing file.
	ErrSessionNotSaved = errors.New("app: session has not been saved yet")
)

// ForkPoint identifies one answer a session can fork from, and the turn that
// answer closed.
//
// Turn counts prompts from 1 along the active branch. The window groups its
// transcript by prompt for the same reason, and the number is what lets a button
// drawn under a reply find the entry behind it: the transcript the page holds
// carries no entry ids of its own.
type ForkPoint struct {
	EntryID string
	Turn    int
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

// ForkPoints returns every answer the conversation in view can be branched
// from, in the order it was said.
//
// The answer rather than the prompt is where a branch belongs: a fork keeps
// everything up to and including the reply the reader wants to build on, and
// what they write next is a new instruction rather than a rewritten question.
// That is also why the text is returned — a list of answers has to show them —
// but nothing is handed back as a draft, because the reply itself is not
// something to retype.
//
// One point per turn: the answer that closed it. An assistant message that still
// holds tool calls is skipped, because a branch ending there would carry a call
// with no result, and an earlier answer of the same turn is replaced by the one
// that closed it.
//
// The branch rather than the file: a session can hold entries the current leaf
// does not descend from, and a reply the conversation in view no longer contains
// is not a place it can be branched.
func (a *App) ForkPoints() []ForkPoint {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]ForkPoint, 0)
	turn := 0
	// The projected messages rather than the entries, because that is what the
	// window draws: a compaction and a custom message both arrive as user
	// messages, so counting entries would number the buttons against turns the
	// reader cannot see. The source entry is still what a point names, since
	// that is what a branch is cut at.
	projection, err := a.manager.BuildProjection()
	if err != nil {
		return out
	}
	for _, projected := range projection.Entries {
		entry := projected.Source
		for _, message := range projected.Messages {
			switch message.Role {
			case wire.RoleUser:
				// Only a prompt the window draws takes a number. The window
				// skips a user message with neither text nor an image, and a
				// turn nobody can see must not shift every button after it.
				if promptVisible(message.Content) {
					turn++
				}
			case wire.RoleAssistant:
				if turn == 0 || entry.Type != session.TypeMessage {
					continue
				}
				text := answerText(message.Content)
				if text == "" {
					continue
				}
				point := ForkPoint{EntryID: entry.ID, Turn: turn, Text: text}
				// Two answers in one turn means the first was retried or failed.
				// Keeping both would offer a fork point the conversation has
				// already moved past.
				if n := len(out); n > 0 && out[n-1].Turn == turn {
					out[n-1] = point
					continue
				}
				out = append(out, point)
			}
		}
	}
	return out
}

// ForkAt branches into a new session holding the history up to and including
// entryID, which must name an answer in this session.
//
// Including rather than excluding is the whole point: a fork is taken after the
// reply, so the branch continues from what the agent just said instead of
// re-asking what it answered. Nothing is returned, because the reply that was
// kept is already on screen in the new branch and there is no prompt to hand
// back for editing.
//
// Queues and any open side conversation belong to the abandoned future and are
// discarded.
func (a *App) ForkAt(entryID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		return err
	}

	selected, ok := a.manager.Entry(entryID)
	if !ok || selected.Type != session.TypeMessage {
		return ErrInvalidForkEntry
	}
	var probe struct {
		Role    wire.Role        `json:"role"`
		Content wire.ContentList `json:"content"`
	}
	if err := json.Unmarshal(selected.Message, &probe); err != nil {
		return ErrInvalidForkEntry
	}
	if probe.Role != wire.RoleAssistant || answerText(probe.Content) == "" {
		return ErrInvalidForkEntry
	}

	// The branch records this file as its parent, so the file has to exist: an
	// assistant message implies a flushed session, but a caller can name an
	// answer on a session whose write was lost.
	if err := a.requireSaved(); err != nil {
		return err
	}
	if _, err := a.manager.CreateBranchedSession(entryID); err != nil {
		return err
	}

	a.resetQueuesAndSide()

	projected, err := a.manager.BuildContext()
	if err != nil {
		return err
	}
	a.model = a.baseModel
	a.provider = a.baseProvider
	a.thinking = a.baseThinking
	a.pendingModel = nil
	a.systemPrompt = a.baseSystemPrompt
	a.promptSource = a.basePromptSource
	a.restoreNote = ""
	a.adoptThinkingLevel(a.manager.BuildContextEntries(), projected.ThinkingLevel)
	a.adoptModel(projected.Model, projected.Model != nil)
	a.adoptSystemPrompt(projected.Messages)

	a.ag = a.buildAgent(a.systemPrompt, &a.steering, &a.followUps)
	a.ag.SetMessages(projected.Messages)
	a.messages = append([]wire.Message(nil), projected.Messages...)
	return nil
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

// promptVisible reports whether a user message is drawn as a prompt at all. The
// window drops one with no text and no image, so this has to drop the same ones:
// otherwise the numbers the window counts and the numbers these points carry
// would part company on a conversation that holds an image-only message.
func promptVisible(content wire.ContentList) bool {
	for _, block := range content.Items() {
		switch block.Type {
		case wire.ContentText:
			if block.Text != "" {
				return true
			}
		case wire.ContentImage:
			return true
		}
	}
	// A plain-string content is its own single text block.
	return content.IsPlain() && content.PlainText() != ""
}

// answerText extracts what an answer said, or "" when the message cannot close
// a turn.
//
// Plain strings pass through, and text blocks are joined without a separator the
// way pi's contentText(..., "") does. A message holding a tool call answers ""
// whatever else it contains: keeping the call without its result would leave the
// branch in a state providers reject, and dropping the call would put words in
// the agent's mouth.
func answerText(content wire.ContentList) string {
	if content.IsPlain() {
		return content.PlainText()
	}
	var out strings.Builder
	for _, block := range content.Items() {
		switch block.Type {
		case wire.ContentToolCall:
			return ""
		case wire.ContentText:
			out.WriteString(block.Text)
		}
	}
	return out.String()
}
