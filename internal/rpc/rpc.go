// Package rpc speaks pi's JSONL RPC protocol: commands arrive as JSON lines,
// responses and agent events leave as JSON lines.
//
// The package is a transport, not a policy layer. Every command maps to one
// internal/app call, and a command the Go kernel cannot serve answers with
// success:false instead of pretending it worked, because a client that edits a
// session must not be told a lie about durable state.
//
// One goroutine reads and answers commands in order, and a prompt runs its turn
// on a second goroutine so steering, aborting, and state queries stay
// responsive while the model streams. Events are written through one mutex, so
// a record is never split and the order a client sees is the order the kernel
// produced.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"pi-go/internal/app"
	"pi-go/internal/session"
	"pi-go/internal/wire"
)

// errAlreadyProcessing is pi's wording for a prompt that arrives mid-run
// without a streaming behavior. Matching it verbatim keeps a client that
// switches between pi and this kernel from special-casing the message.
const errAlreadyProcessing = "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message."

// thinkingLevels is atlas's own vocabulary, not pi's MODEL_THINKING_LEVELS:
// off plus six effort gears. pi spends minimal on the low end; atlas keeps
// ultra and lets each adapter map the gears a vendor cannot express.
//
// These are the names the protocol accepts, and the default answer for a model
// the kernel holds no row for: "nothing told me" is the widest truthful answer.
// Which subset a given model can send is narrower and lives in the model list,
// not here.
var thinkingLevels = []string{"off", "low", "medium", "high", "xhigh", "max", "ultra"}

// unsupported lists commands that exist in pi's protocol but have no
// implementation behind them here. Answering "unknown command" would hide the
// difference between a typo and a gap, so they get their own error.
var unsupported = map[string]bool{
	"set_auto_retry": true,
	"abort_retry":    true,
	"bash":           true,
	"abort_bash":     true,
	"export_html":    true,
}

// Server drives one app over the JSONL protocol.
type Server struct {
	app *app.App
	out *writer

	mu     sync.Mutex
	active *turn
	// compacting is set while the kernel reports a compaction in flight. The
	// command loop is blocked by a manual compaction, but an automatic one runs
	// inside a prompt goroutine, so state queries do arrive while it works.
	compacting bool
}

// turn is the state a running prompt owns: a cancellation handle and a signal
// that its goroutine finished writing.
type turn struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a server that drives application. The server takes no ownership
// of it; the caller closes sessions and providers.
func New(application *app.App) *Server {
	return &Server{app: application}
}

// Serve reads commands from in, writes responses and events to out, and returns
// when in ends or ctx is cancelled. A running turn is cancelled and awaited
// before it returns, so no goroutine writes to out afterwards.
//
// Serve reads in on its own goroutine because a blocked read cannot observe ctx.
// When it returns early the reader stays blocked until the caller closes in, so
// whoever owns in should close it.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = newWriter(out)
	lines := make(chan []byte)
	readErr := make(chan error, 1)

	go func() {
		reader := newLineReader(in)
		for {
			line, err := reader.next()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			s.abort()
			return ctx.Err()
		case err := <-readErr:
			stopErr := s.stop()
			if errors.Is(err, io.EOF) {
				return stopErr
			}
			if stopErr != nil {
				return stopErr
			}
			return err
		case line := <-lines:
			if err := s.handleLine(ctx, line); err != nil {
				s.abort()
				return err
			}
			if err := s.out.failed(); err != nil {
				s.abort()
				return err
			}
		}
	}
}

// stop cancels a running turn and reports a sticky write failure, which is what
// a client that closed stdout leaves behind.
func (s *Server) stop() error {
	s.abort()
	return s.out.failed()
}

// handleLine decodes and dispatches one record.
func (s *Server) handleLine(ctx context.Context, line []byte) error {
	var cmd command
	if err := json.Unmarshal(line, &cmd); err != nil {
		return s.failure(nil, "parse", "Failed to parse command: "+err.Error())
	}
	return s.handle(ctx, cmd)
}

// handle answers one command. A nil error means the answer was written; an
// error means the stream is unusable.
func (s *Server) handle(ctx context.Context, cmd command) error {
	switch cmd.Type {
	case "prompt":
		return s.prompt(ctx, cmd)
	case "steer":
		s.app.Steer(promptMessage(cmd))
		return s.success(cmd.ID, cmd.Type, dispositionResult{Disposition: dispositionQueued})
	case "follow_up":
		s.app.FollowUp(promptMessage(cmd))
		return s.success(cmd.ID, cmd.Type, dispositionResult{Disposition: dispositionQueued})
	case "abort":
		s.abort()
		return s.success(cmd.ID, cmd.Type, nil)
	case "clear_queue":
		steering, followUp := s.app.ClearQueue()
		return s.success(cmd.ID, cmd.Type, clearQueueResult{
			Steering: messageTexts(steering),
			FollowUp: messageTexts(followUp),
		})
	case "new_session":
		return s.newSession(cmd)
	case "switch_session":
		return s.switchSession(cmd)
	case "fork":
		return s.fork(cmd)
	case "clone":
		return s.clone(cmd)
	case "get_fork_messages":
		return s.getForkMessages(cmd)
	case "get_session_stats":
		return s.getSessionStats(cmd)
	case "get_state":
		return s.success(cmd.ID, cmd.Type, s.state())
	case "set_model":
		return s.setModel(cmd)
	case "cycle_model":
		return s.cycleModel(cmd)
	case "get_available_models":
		// An empty list is an empty array, not null: a client renders it.
		models := make([]wire.Model, 0)
		for _, option := range s.app.Models() {
			models = append(models, option.Model)
		}
		return s.success(cmd.ID, cmd.Type, availableModelsResult{Models: models})
	case "compact":
		return s.compact(ctx, cmd)
	case "set_auto_compaction":
		if cmd.Enabled == nil {
			return s.failure(cmd.ID, cmd.Type, "enabled is required")
		}
		s.app.SetAutoCompaction(*cmd.Enabled)
		return s.success(cmd.ID, cmd.Type, nil)
	case "set_thinking_level":
		if !validThinkingLevel(cmd.Level) {
			return s.failure(cmd.ID, cmd.Type, "Unknown thinking level: "+cmd.Level)
		}
		// The level is fitted to the model in effect, but the fitted value is
		// not echoed: pi's response carries no data, so a client that wants to
		// know what was applied reads it back from get_state. Fitting stays an
		// error when the model cannot express the level at all; rounding a gear
		// onto a model that cannot think would report a request nobody sends.
		fitted, _, err := s.app.FitThinking(cmd.Level)
		if err != nil {
			return s.failure(cmd.ID, cmd.Type, err.Error())
		}
		if err := s.app.SetThinkingLevel(fitted); err != nil {
			return s.failure(cmd.ID, cmd.Type, err.Error())
		}
		return s.success(cmd.ID, cmd.Type, nil)
	case "get_available_thinking_levels":
		levels, known := s.app.AvailableThinkingLevels()
		switch {
		case !known:
			levels = append([]string(nil), thinkingLevels...)
		case len(levels) == 0:
			// A model that cannot think has exactly one sendable level: the
			// switch, not the default gear a thinking model starts on.
			levels = []string{"off"}
		}
		return s.success(cmd.ID, cmd.Type, thinkingLevelsResult{Levels: levels})
	case "cycle_thinking_level":
		level, cycled, err := s.app.CycleThinkingLevel()
		if err != nil {
			return s.failure(cmd.ID, cmd.Type, err.Error())
		}
		if !cycled {
			// pi answers null when there is nothing to cycle, and a client's
			// type is "result | null", so the key stays with a null value.
			return s.success(cmd.ID, cmd.Type, (*thinkingCycleResult)(nil))
		}
		return s.success(cmd.ID, cmd.Type, thinkingCycleResult{Level: level})
	case "set_steering_mode":
		mode, err := app.ParseQueueMode(cmd.Mode)
		if err != nil {
			return s.failure(cmd.ID, cmd.Type, err.Error())
		}
		s.app.SetSteeringMode(mode)
		return s.success(cmd.ID, cmd.Type, nil)
	case "set_follow_up_mode":
		mode, err := app.ParseQueueMode(cmd.Mode)
		if err != nil {
			return s.failure(cmd.ID, cmd.Type, err.Error())
		}
		s.app.SetFollowUpMode(mode)
		return s.success(cmd.ID, cmd.Type, nil)
	case "get_messages":
		// An empty transcript is an empty array, not null: a client iterates
		// the value without checking it first.
		messages := s.app.Messages()
		if messages == nil {
			messages = []wire.Message{}
		}
		return s.success(cmd.ID, cmd.Type, messagesResult{Messages: messages})
	case "get_last_assistant_text":
		return s.success(cmd.ID, cmd.Type, lastAssistantText{Text: lastAssistant(s.app.Messages())})
	case "get_entries":
		return s.getEntries(cmd)
	case "get_tree":
		return s.success(cmd.ID, cmd.Type, treeResult{
			Tree:   jsonTree(s.app.Session().Tree()),
			LeafID: s.app.Session().LeafID(),
		})
	case "set_session_name":
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			return s.failure(cmd.ID, cmd.Type, "Session name cannot be empty")
		}
		if err := s.app.SetName(name); err != nil {
			return s.failure(cmd.ID, cmd.Type, err.Error())
		}
		return s.success(cmd.ID, cmd.Type, nil)
	case "get_commands":
		return s.success(cmd.ID, cmd.Type, commandsResult{Commands: []any{}})
	case "extension_ui_response":
		// The kernel has no extension UI, so the answer names no request and
		// there is nothing to resolve. pi ignores unknown ids the same way.
		return nil
	}

	if unsupported[cmd.Type] {
		return s.failure(cmd.ID, cmd.Type, cmd.Type+" is not implemented by this kernel")
	}
	return s.failure(cmd.ID, cmd.Type, "Unknown command: "+cmd.Type)
}

// prompt starts a turn, or queues the message when one is already running.
//
// The response is written before the first event, matching pi: the caller
// learns whether the prompt was accepted without waiting for the model, and a
// failure before that point is reported as a failed command instead of as an
// assistant message.
func (s *Server) prompt(ctx context.Context, cmd command) error {
	message := promptMessage(cmd)

	if s.isStreaming() {
		switch cmd.StreamingBehavior {
		case "steer":
			s.app.Steer(message)
		case "followUp":
			s.app.FollowUp(message)
		case "":
			return s.failure(cmd.ID, cmd.Type, errAlreadyProcessing)
		default:
			return s.failure(cmd.ID, cmd.Type, "Unknown streamingBehavior: "+cmd.StreamingBehavior)
		}
		return s.success(cmd.ID, cmd.Type, dispositionResult{Disposition: dispositionQueued})
	}

	turnCtx, cancel := context.WithCancel(ctx)
	current := &turn{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.active = current
	s.mu.Unlock()

	if err := s.success(cmd.ID, cmd.Type, dispositionResult{Disposition: dispositionStarted}); err != nil {
		cancel()
		close(current.done)
		s.mu.Lock()
		s.active = nil
		s.mu.Unlock()
		return err
	}

	go func() {
		// finish runs before done closes, so a waiter that wakes up never sees
		// a finished turn as still running.
		defer close(current.done)
		defer s.finish(current)
		// A failure after the response was sent cannot be reported as a failed
		// command anymore. The kernel already turned it into events: a provider
		// error is an assistant message with stopReason "error", a cancellation
		// is an aborted one.
		_, _ = s.app.Ask(turnCtx, message, s.emit)
	}()
	return nil
}

// newSession abandons the conversation and starts an empty one.
//
// pi reports cancelled when something declined the switch; nothing can here, so
// the field is always false and a still small response keeps clients that
// always read it working.
// compact summarizes the conversation and answers with the recorded checkpoint.
//
// A running turn is aborted first, as pi does: a summary of a context that is
// still growing would describe a conversation that no longer exists by the time
// it is written. The response waits for the summary, while compaction_start and
// compaction_end stream in the meantime.
func (s *Server) compact(ctx context.Context, cmd command) error {
	s.abort()
	result, err := s.app.Compact(ctx, cmd.CustomInstructions, s.emit)
	if err != nil {
		return s.failure(cmd.ID, cmd.Type, err.Error())
	}
	return s.success(cmd.ID, cmd.Type, result)
}

func (s *Server) newSession(cmd command) error {
	s.abort()
	if err := s.app.NewSession(); err != nil {
		return s.failure(cmd.ID, cmd.Type, err.Error())
	}
	return s.success(cmd.ID, cmd.Type, newSessionResult{Cancelled: false})
}

// switchSession continues a different session file, the way /resume does. A
// running turn is aborted first, as new_session does: a transcript that is
// still growing must not be swapped underneath.
func (s *Server) switchSession(cmd command) error {
	s.abort()
	if cmd.SessionPath == "" {
		return s.failure(cmd.ID, cmd.Type, "sessionPath is required")
	}
	if err := s.app.SwitchSession(cmd.SessionPath); err != nil {
		return s.failure(cmd.ID, cmd.Type, err.Error())
	}
	return s.success(cmd.ID, cmd.Type, newSessionResult{Cancelled: false})
}

// fork branches before a user message entry into a new session and returns the
// selected message text. Like new_session and switch_session, a running turn is
// aborted and awaited first so it cannot append to the forked session.
func (s *Server) fork(cmd command) error {
	s.abort()
	text, err := s.app.ForkAt(cmd.EntryID)
	if err != nil {
		return s.failure(cmd.ID, cmd.Type, forkErrorMessage(err))
	}
	return s.success(cmd.ID, cmd.Type, forkResult{Text: text, Cancelled: false})
}

// clone duplicates the active branch at the current leaf into a new session.
func (s *Server) clone(cmd command) error {
	s.abort()
	if _, err := s.app.CloneSession(); err != nil {
		return s.failure(cmd.ID, cmd.Type, forkErrorMessage(err))
	}
	return s.success(cmd.ID, cmd.Type, cloneResult{Cancelled: false})
}

func (s *Server) getForkMessages(cmd command) error {
	raw := s.app.ForkMessages()
	messages := make([]forkMessageItem, 0, len(raw))
	for _, item := range raw {
		messages = append(messages, forkMessageItem{EntryID: item.EntryID, Text: item.Text})
	}
	return s.success(cmd.ID, cmd.Type, forkMessagesResult{Messages: messages})
}

func (s *Server) getSessionStats(cmd command) error {
	stats, err := s.app.SessionStats()
	if err != nil {
		return s.failure(cmd.ID, cmd.Type, err.Error())
	}
	out := sessionStatsResult{
		SessionFile:       stats.SessionFile,
		SessionID:         stats.SessionID,
		UserMessages:      stats.UserMessages,
		AssistantMessages: stats.AssistantMessages,
		ToolCalls:         stats.ToolCalls,
		ToolResults:       stats.ToolResults,
		TotalMessages:     stats.TotalMessages,
		Tokens: tokenStats{
			Input:      stats.Tokens.Input,
			Output:     stats.Tokens.Output,
			CacheRead:  stats.Tokens.CacheRead,
			CacheWrite: stats.Tokens.CacheWrite,
			Total:      stats.Tokens.Total,
		},
		Cost: stats.Cost,
	}
	if stats.ContextUsage != nil {
		out.ContextUsage = &contextUsageStats{
			Tokens:        stats.ContextUsage.Tokens,
			ContextWindow: stats.ContextUsage.ContextWindow,
			Percent:       stats.ContextUsage.Percent,
		}
	}
	return s.success(cmd.ID, cmd.Type, out)
}

func forkErrorMessage(err error) string {
	switch {
	case errors.Is(err, app.ErrInvalidForkEntry):
		return "Invalid entry ID for forking"
	case errors.Is(err, app.ErrNothingToFork):
		return "Cannot clone session: no current entry selected"
	case errors.Is(err, app.ErrSessionNotSaved):
		return "This session has not been saved yet. Send a message before cloning or forking it."
	default:
		return err.Error()
	}
}

func (s *Server) setModel(cmd command) error {
	model, err := s.app.SetModel(cmd.Provider, cmd.ModelID)
	if err != nil {
		// A model the resolver cannot provide and a run that forbids switching
		// are different failures, and the second one is reported as itself.
		if errors.Is(err, app.ErrBusy) {
			return s.failure(cmd.ID, cmd.Type, err.Error())
		}
		return s.failure(cmd.ID, cmd.Type, fmt.Sprintf("Model not found: %s/%s", cmd.Provider, cmd.ModelID))
	}
	return s.success(cmd.ID, cmd.Type, model)
}

// cycleModel switches to the next model in the kernel's list and reports the
// thinking level that came with it, which is the pair a client renders.
//
// A list too short to cycle is a result rather than a failure, and pi sends data
// null for it: the model was not found, and it was not meant to be.
func (s *Server) cycleModel(cmd command) error {
	cycle, cycled, err := s.app.CycleModel()
	if err != nil {
		return s.failure(cmd.ID, cmd.Type, err.Error())
	}
	if !cycled {
		// The key stays with a null value because a client's type is
		// "result | null", not "result | missing".
		return s.success(cmd.ID, cmd.Type, (*modelCycleResult)(nil))
	}
	return s.success(cmd.ID, cmd.Type, modelCycleResult{
		Model:         cycle.Model,
		ThinkingLevel: cycle.ThinkingLevel,
		IsScoped:      false,
	})
}

func (s *Server) getEntries(cmd command) error {
	entries := s.app.Session().Entries()
	if cmd.Since != nil {
		index := -1
		for i, entry := range entries {
			if entry.ID == *cmd.Since {
				index = i
				break
			}
		}
		if index < 0 {
			return s.failure(cmd.ID, cmd.Type, "Entry not found: "+*cmd.Since)
		}
		entries = entries[index+1:]
	}
	if entries == nil {
		entries = []session.Entry{}
	}
	return s.success(cmd.ID, cmd.Type, entriesResult{Entries: entries, LeafID: s.app.Session().LeafID()})
}

// state reports what a client needs to render a session without asking twice.
func (s *Server) state() sessionState {
	manager := s.app.Session()
	return sessionState{
		Model:                 s.app.Model(),
		ThinkingLevel:         s.app.ThinkingLevel(),
		IsStreaming:           s.isStreaming(),
		IsCompacting:          s.isCompacting(),
		SteeringMode:          string(s.app.SteeringMode()),
		FollowUpMode:          string(s.app.FollowUpMode()),
		SessionFile:           manager.File(),
		SessionID:             manager.ID(),
		SessionName:           manager.Name(),
		AutoCompactionEnabled: s.app.CompactionSettings().Enabled,
		MessageCount:          s.app.MessageCount(),
		PendingMessageCount:   s.app.PendingCount(),
	}
}

// emit writes one kernel event and tracks whether a compaction is in flight, so
// a state query answered during an automatic compaction reports the truth.
func (s *Server) emit(event wire.AgentEvent) error {
	switch event.Type {
	case wire.AgentEventCompactionStart:
		s.setCompacting(true)
	case wire.AgentEventCompactionEnd:
		s.setCompacting(false)
	}
	value, err := encodeEvent(event)
	if err != nil {
		return err
	}
	return s.out.write(value)
}

func (s *Server) success(id *string, name string, data any) error {
	return s.out.write(response{ID: id, Type: "response", Command: name, Success: true, Data: data})
}

func (s *Server) failure(id *string, name, message string) error {
	return s.out.write(response{ID: id, Type: "response", Command: name, Success: false, Error: message})
}

// isStreaming reports whether a turn is running. The flag lives here rather
// than in the app because the transport decides when a prompt is accepted.
func (s *Server) isStreaming() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
}

// setCompacting records that a compaction is in flight.
func (s *Server) setCompacting(compacting bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compacting = compacting
}

// isCompacting reports whether the kernel reported a compaction that has not
// finished.
func (s *Server) isCompacting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compacting
}

// abort cancels a running turn and waits for it to finish writing.
func (s *Server) abort() {
	s.mu.Lock()
	current := s.active
	s.mu.Unlock()
	if current == nil {
		return
	}
	current.cancel()
	<-current.done
}

// finish clears the active turn if it is still the one that ended.
func (s *Server) finish(current *turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == current {
		s.active = nil
	}
}

// promptMessage turns a command's text and images into a user message. Images
// keep their order after the text, which is how pi lays out a prompt with
// attachments.
func promptMessage(cmd command) wire.Message {
	if len(cmd.Images) == 0 {
		return wire.UserMessage(cmd.Message)
	}
	blocks := make([]wire.Content, 0, len(cmd.Images)+1)
	blocks = append(blocks, wire.TextContent(cmd.Message))
	blocks = append(blocks, cmd.Images...)
	return wire.UserMessageBlocks(blocks...)
}

// messageTexts collects the text of queued messages, which is all a client
// needs to show what clear_queue discarded.
func messageTexts(messages []wire.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		texts = append(texts, message.Text())
	}
	return texts
}

// jsonTree shapes session tree nodes for the protocol.
func jsonTree(nodes []*session.TreeNode) []jsonTreeNode {
	out := make([]jsonTreeNode, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, jsonTreeNode{
			Entry:          node.Entry,
			Children:       jsonTree(node.Children),
			Label:          node.Label,
			LabelTimestamp: node.LabelTimestamp,
		})
	}
	return out
}

// lastAssistant returns the trimmed text of the last assistant message that has
// any, skipping aborted messages that never produced content.
func lastAssistant(messages []wire.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != wire.RoleAssistant {
			continue
		}
		if message.StopReason == wire.StopAborted && message.Content.Len() == 0 {
			continue
		}
		var text strings.Builder
		for _, block := range message.Content.Items() {
			if block.Type == wire.ContentText {
				text.WriteString(block.Text)
			}
		}
		if trimmed := strings.TrimSpace(text.String()); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func validThinkingLevel(level string) bool {
	for _, candidate := range thinkingLevels {
		if candidate == level {
			return true
		}
	}
	return false
}
