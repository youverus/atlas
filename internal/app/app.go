// Package app is the orchestration layer: it binds one session to one agent,
// records every message the kernel finishes, and owns the state a session file
// tracks outside the transcript (model, thinking level, name, side
// conversations).
//
// This is the layer a CLI, an RPC server, or a TUI drives. It knows about
// sessions, models, and turns; it knows nothing about transports, rendering, or
// which model catalog exists.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"pi-go/internal/agent"
	"pi-go/internal/compact"
	"pi-go/internal/provider"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

var (
	// ErrBusy reports that a turn is already running on the same conversation.
	ErrBusy = errors.New("app: a turn is already running")
	// ErrSideClosed reports a prompt sent to a side conversation that was
	// already closed.
	ErrSideClosed = errors.New("app: side conversation is closed")
	// ErrArchived reports a write to a session the user archived. The file is
	// still there and still readable; what it lost is the right to grow, until
	// Restore gives it back.
	ErrArchived = errors.New("app: this session is archived; /restore makes it active again, /delete removes it")
	// ErrNotArchived reports a restore or delete of a session that is live.
	ErrNotArchived = errors.New("app: this session is not archived")
	// ErrNothingToArchive reports an archive of a conversation that has no
	// file yet: lazy creation never wrote one, so there is nothing to hide.
	ErrNothingToArchive = errors.New("app: this conversation has no messages, so there is nothing to archive")
)

// PromptSource returns the instructions to send with a request, for the
// directory the conversation is running in.
//
// It is asked once per turn rather than once per session: the files behind
// the instructions belong to the user, who can rewrite them while the session
// runs, and a conversation that answers with the copy it read at startup is
// acting on rules that no longer exist. An error means the instructions could
// not be read; the turn is refused rather than sent without them, because a
// model answering without the instructions it was given is wrong in a way
// nobody can see.
//
// The directory is a parameter because a session carries one: /resume can open
// a conversation recorded in another project, and the instructions that belong
// to it are that project's, not the one this process started in.
type PromptSource func(cwd string) (string, error)

// StaticPrompt returns a source that always answers the same text.
//
// It is the prompt of a session with no files behind it: a test, the offline
// demo, a kernel another front end drives. Its answer cannot depend on the
// directory, which is the truth for a prompt that was handed in as a string.
func StaticPrompt(text string) PromptSource {
	return func(string) (string, error) { return text, nil }
}

// ToolSet answers the tools a conversation in cwd may call.
//
// The argument is the same directory PromptSource is asked about: a session
// records where it belongs, and the file tools are bounded by that workspace
// rather than by the one this process happens to be in. A set that does not
// depend on the directory is what StaticTools returns, which is what every
// front end with a fixed tool list means.
type ToolSet func(cwd string) []tool.Tool

// StaticTools returns a set that answers the same tools everywhere.
//
// It is the set of a kernel that was handed its tools and nothing else: a
// test, the offline demo, an embedding front end. A session from another
// project then runs with tools that were not built for it, which is the honest
// answer for a front end that has no way to build a second set.
func StaticTools(set []tool.Tool) ToolSet {
	return func(string) []tool.Tool { return set }
}

// PromptReload says what a reload found.
type PromptReload struct {
	// Changed is true when the instructions differ from the ones in effect.
	Changed bool
	// Live is false when the session carries its own instructions: a
	// conversation that was started with a prompt recorded in its file keeps
	// them, the same way it keeps the tools it was declared with, and there
	// is nothing to reload.
	Live bool
	// Prompt is the instructions in effect after the reload.
	Prompt string
}

// Resolver turns the provider/model pair a session records into the model
// descriptor and the adapter that streams it.
//
// It exists because a session file stores only a pair of names, while a request
// needs the whole descriptor: the assistant message records Model.API, so a
// model rebuilt from a session must be the same descriptor the provider was
// configured with. The resolver is called when a resumed session's model is
// adopted and on every model switch, never per request.
type Resolver func(providerName, modelID string) (wire.Model, provider.Provider, error)

// ModelOption is one model a front end may switch to: the descriptor plus the
// thinking levels that model can express.
//
// The levels travel with the descriptor because they are per model, and a layer
// that had to look them up somewhere else would be the second place that decides
// what a model can do. An empty Levels means the model cannot think at all,
// which is not the same as a model this list does not know: an unknown model has
// no answer, and a model with no axis answers off.
type ModelOption struct {
	Model  wire.Model
	Levels []string
}

// ModelCycle is what a model cycle reports: the model now in effect and the
// thinking level that came with it.
type ModelCycle struct {
	Model         wire.Model
	ThinkingLevel string
}

// ThinkingFitter rounds a requested level to one the model can send and reports
// why the answer differs from the request.
//
// It is a hook for the same reason Resolver is one: which levels a model offers
// is a fact about the config and the vendor table, and neither belongs in this
// layer. An error means the level cannot be expressed at all — a name outside
// the vocabulary, or a gear sent to a model that cannot think.
type ThinkingFitter func(model wire.Model, level string) (fitted, adjustment string, err error)

// Config describes a conversation. Everything except Model, Provider, and the
// session manager is optional.
type Config struct {
	// Model is the model to use unless the session already records another one.
	Model wire.Model
	// Provider streams Model. It must handle Model.API.
	Provider provider.Provider

	// ThinkingLevel is the initial level. Empty means session.DefaultThinkingLevel.
	ThinkingLevel string

	// SystemPrompt is the prompt sent to the model. It is read again for
	// every turn, so a project file that changed or a skill that was
	// installed takes effect on the next question. Nil means the session has
	// no instructions of its own.
	//
	// A resumed session that stored its own system message overrides it, so
	// a conversation keeps the instructions and tool declarations it was
	// started with.
	SystemPrompt PromptSource
	// Tools are declared on every request and executed by the agent. The set
	// is asked for the session's directory, so a switch to a session from
	// another project brings that project's tools with it.
	Tools ToolSet

	// Resolver rebuilds a model from a name pair. Required to switch models and
	// to adopt the model a resumed session recorded.
	Resolver Resolver

	// Models lists every model a front end may switch to, in the order a cycle
	// walks them. Empty means this process has no model list: listing answers
	// with nothing and a cycle reports that there is nothing to cycle, which is
	// the truth for a kernel that was given one model and no table.
	Models []ModelOption

	// FitThinking rounds a thinking level to one the model in effect can send.
	// Nil means the level is taken as given, which is all a kernel that was not
	// told about model capabilities can honestly do.
	FitThinking ThinkingFitter

	// Compaction configures automatic compaction. Nil means enabled with pi's
	// defaults; a non-nil value is used as given, so Enabled:false turns it off.
	Compaction *compact.Settings

	// GoalBudget caps the tokens one /goal may spend across its turns. Zero
	// means no budget. The value is snapshotted onto the goal when it is set,
	// so a later config change does not rewrite a running goal's cap.
	GoalBudget int

	// ToolExecution defaults to agent.ToolExecutionParallel.
	ToolExecution agent.ToolExecution
	// MaxParallel caps concurrent tool calls. Defaults to the agent default.
	MaxParallel int

	// BeforeToolCall may veto a call; AfterToolCall may rewrite its result.
	BeforeToolCall func(ctx context.Context, call tool.Call) (*agent.Block, error)
	AfterToolCall  func(ctx context.Context, call tool.Call, result *tool.Result)
}

// App binds one session to one agent.
//
// A turn is exclusive: while one runs, model switches, branches, and other turns
// on the same conversation return ErrBusy. Steering is the exception, because a
// message that cannot arrive mid-turn is not steering.
type App struct {
	mu sync.Mutex

	manager  *session.Manager
	resolver Resolver
	// models is the switch list, copied so a caller that keeps editing its slice
	// cannot change what this conversation cycles through.
	models      []ModelOption
	fitThinking ThinkingFitter
	// tools is the set the conversation asks for by directory, not the tools
	// themselves: the answer changes with the session, and a slice captured at
	// startup would describe the directory this process started in forever.
	tools ToolSet

	execution   agent.ToolExecution
	maxParallel int
	before      func(ctx context.Context, call tool.Call) (*agent.Block, error)
	after       func(ctx context.Context, call tool.Call, result *tool.Result)

	compaction   compact.Settings
	goalBudget   int
	model        wire.Model
	provider     provider.Provider
	thinking     string
	systemPrompt string
	// promptSource is where the instructions come from. It is nil when the
	// session carries its own: a conversation that was started with a prompt
	// recorded in its file keeps it, and a source would overwrite it on the
	// next turn.
	promptSource PromptSource
	restoreNote  string

	// baseModel, baseProvider, baseThinking, baseSystemPrompt and
	// basePromptSource are the config's knobs, kept so SwitchSession can
	// reset to them before adopting what the next session recorded. A resume
	// that kept whatever the previous conversation had switched to would leak
	// one session's settings into another, and a session that recorded nothing
	// would then keep them.
	baseModel        wire.Model
	baseProvider     provider.Provider
	baseThinking     string
	baseSystemPrompt string
	basePromptSource PromptSource

	ag *agent.Agent
	// messages mirrors what the session holds, so reads stay off the agent's
	// transcript while a turn is appending to it.
	messages  []wire.Message
	steering  messageQueue
	followUps messageQueue
	busy      bool
	side      *Side
}

// New binds manager to a new agent. The session's recorded model, thinking
// level, and system prompt win over the config, which is what makes resume
// continue the same conversation instead of restarting it with new settings.
func New(cfg Config, manager *session.Manager) (*App, error) {
	if manager == nil {
		return nil, errors.New("app: a session manager is required")
	}
	if cfg.Provider == nil {
		return nil, errors.New("app: Config.Provider is required")
	}
	if cfg.Model.ID == "" {
		return nil, errors.New("app: Config.Model.ID is required")
	}

	a := &App{
		manager:      manager,
		resolver:     cfg.Resolver,
		models:       cloneModels(cfg.Models),
		fitThinking:  cfg.FitThinking,
		tools:        cfg.Tools,
		execution:    cfg.ToolExecution,
		maxParallel:  cfg.MaxParallel,
		before:       cfg.BeforeToolCall,
		after:        cfg.AfterToolCall,
		compaction:   compact.DefaultSettings,
		goalBudget:   cfg.GoalBudget,
		model:        cfg.Model,
		provider:     cfg.Provider,
		thinking:     cfg.ThinkingLevel,
		promptSource: cfg.SystemPrompt,
	}
	if a.thinking == "" {
		a.thinking = session.DefaultThinkingLevel
	}
	if cfg.Compaction != nil {
		a.compaction = *cfg.Compaction
	}
	// The instructions are read once here so a file that cannot be read stops
	// the session before it starts rather than on its first question. The
	// source is asked again for every turn after this.
	if a.promptSource != nil {
		prompt, err := a.promptSource(manager.Cwd())
		if err != nil {
			return nil, err
		}
		a.systemPrompt = prompt
	}
	a.baseModel = cfg.Model
	a.baseProvider = cfg.Provider
	a.baseThinking = a.thinking
	a.baseSystemPrompt = a.systemPrompt
	a.basePromptSource = a.promptSource

	projected, err := manager.BuildContext()
	if err != nil {
		return nil, err
	}
	// A session without messages has nothing to restore, even if it recorded a
	// model change; pi makes the same check.
	hasConversation := len(projected.Messages) > 0
	a.adoptThinkingLevel(manager.BuildContextEntries(), projected.ThinkingLevel)
	a.adoptModel(projected.Model, hasConversation)
	a.adoptSystemPrompt(projected.Messages)

	a.ag = a.buildAgent(a.systemPrompt, &a.steering, &a.followUps)
	a.ag.SetMessages(projected.Messages)
	a.messages = append([]wire.Message(nil), projected.Messages...)
	return a, nil
}

// adoptThinkingLevel prefers a level the active branch recorded. The check for
// the entry itself matters: a branch that recorded "off" must win over a config
// that asks for something else, and Context alone cannot tell "off" from "never
// recorded".
func (a *App) adoptThinkingLevel(branch []session.Entry, recorded string) {
	for _, entry := range branch {
		if entry.Type == session.TypeThinkingLevelChange {
			a.thinking = recorded
			return
		}
	}
}

// adoptModel prefers the model the session recorded, which is what pi restores
// on resume. When the resolver cannot provide it, the configured model stays and
// restoreNote explains why.
func (a *App) adoptModel(recorded *session.ModelRef, hasConversation bool) {
	if recorded == nil || !hasConversation {
		return
	}
	if recorded.Provider == a.model.Provider && recorded.ModelID == a.model.ID {
		return
	}
	name := recorded.Provider + "/" + recorded.ModelID
	if a.resolver == nil {
		a.restoreNote = fmt.Sprintf("kept %s; no resolver is configured to restore %s", a.model.ID, name)
		return
	}
	model, streamer, err := a.resolver(recorded.Provider, recorded.ModelID)
	if err != nil {
		a.restoreNote = fmt.Sprintf("kept %s; restoring %s failed: %v", a.model.ID, name, err)
		return
	}
	a.model = model
	a.provider = streamer
}

// adoptSystemPrompt takes the prompt a session already stored. pi writes its
// system message into the transcript, so a conversation opened in pi resumes in
// Go with the same instructions and the same tool declarations.
//
// The source is dropped rather than shadowed: a source is asked again for
// every turn, and the instructions a conversation was started with are the
// ones it keeps — the same rule the tools follow.
func (a *App) adoptSystemPrompt(messages []wire.Message) {
	if text := storedPrompt(messages); text != "" {
		a.promptSource = nil
		a.systemPrompt = text
	}
}

// storedPrompt is the text of the system message a transcript opens with, or ""
// when the conversation carries no instructions of its own.
//
// It is a function rather than two checks in a row in adoptSystemPrompt because
// SwitchSession has to know the same thing before it touches a single field: a
// session that carries its prompt asks no source at all.
func storedPrompt(messages []wire.Message) string {
	if len(messages) == 0 || messages[0].Role != wire.RoleSystem {
		return ""
	}
	return messages[0].Content.Text()
}

// toolsOf answers the tools of the directory the session is in. A nil set is a
// conversation with no tools, which is what a kernel that was handed none means.
func (a *App) toolsOf() []tool.Tool {
	if a.tools == nil {
		return nil
	}
	return a.tools(a.manager.Cwd())
}

// refreshTools rebinds the agent's registry to the set the session asks for.
//
// It runs before every turn, for the same reason the instructions are read
// again: an MCP server can declare a tool while the conversation runs, and the
// client answers notifications/tools/list_changed by listing the server again
// (docs/mcp.md section 5). A tool that only appeared after a restart is a tool
// this conversation never had.
//
// The list is replaced rather than compared, because a server can re-declare a
// tool under its old name with a new schema, and the schema is what the model
// reads. Callers must hold a.mu.
func (a *App) refreshTools(ag *agent.Agent) {
	if ag == nil {
		return
	}
	ag.Registry().Replace(a.toolsOf()...)
}

// buildAgent creates an agent from the current immutable knobs. Callers must
// hold a.mu, and the returned agent must be replaced rather than mutated when a
// knob changes, because agent.Config is immutable by contract.
func (a *App) buildAgent(prompt string, steering, followUps *messageQueue) *agent.Agent {
	return agent.New(agent.Config{
		Model:               a.model,
		Provider:            a.provider,
		ThinkingLevel:       a.thinking,
		SystemPrompt:        prompt,
		Tools:               a.toolsOf(),
		ToolExecution:       a.execution,
		MaxParallel:         a.maxParallel,
		BeforeToolCall:      a.before,
		AfterToolCall:       a.after,
		GetSteeringMessages: steering.drain,
		GetFollowUpMessages: followUps.drain,
	})
}

// rebuild recreates the agent after a model or thinking level change, keeping
// the transcript it had.
func (a *App) rebuild() {
	messages := a.ag.Messages()
	a.ag = a.buildAgent(a.systemPrompt, &a.steering, &a.followUps)
	a.ag.SetMessages(messages)
}

// refreshPrompt reads the instructions again and rebuilds the agent when they
// changed.
//
// It runs at the start of every turn: the files behind the prompt are the
// user's to rewrite while the session runs, and the copy read at startup goes
// stale the moment they do. The source is asked outside the lock — it reads
// files, and a turn's lock is not the place to spend that time — and the
// result is compared under it, so two turns that race cannot both install a
// prompt.
func (a *App) refreshPrompt() error {
	a.mu.Lock()
	source, cwd := a.promptSource, a.manager.Cwd()
	a.mu.Unlock()
	if source == nil {
		return nil
	}
	prompt, err := source(cwd)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.promptSource == nil {
		// The session took its own instructions while the source was being
		// asked: they win, and the answer that just arrived is not theirs.
		return nil
	}
	if a.manager.Cwd() != cwd {
		// The conversation moved to another directory while the source was
		// being asked. This answer describes a project the session left, and
		// the switch already read the one it went to.
		return nil
	}
	if prompt == a.systemPrompt {
		return nil
	}
	a.systemPrompt = prompt
	a.rebuild()
	return nil
}

// ReloadSystemPrompt reads the instructions again and reports what it found.
//
// The prompt is read for every turn anyway, so this command exists for the
// question the reader actually has: did my file get read, and what is in the
// prompt now. A session that carries its own instructions says so instead of
// pretending to reload something.
func (a *App) ReloadSystemPrompt() (PromptReload, error) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return PromptReload{}, ErrBusy
	}
	source, cwd := a.promptSource, a.manager.Cwd()
	a.mu.Unlock()
	if source == nil {
		return PromptReload{Live: false, Prompt: a.SystemPrompt()}, nil
	}

	prompt, err := source(cwd)
	if err != nil {
		return PromptReload{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.promptSource == nil {
		return PromptReload{Live: false, Prompt: a.systemPrompt}, nil
	}
	if a.manager.Cwd() != cwd {
		// A switch ran while the source was being asked, and it read the
		// instructions of the project it opened. Installing this answer would
		// put the previous project's rules in front of the new one.
		return PromptReload{Live: true, Prompt: a.systemPrompt}, nil
	}
	changed := prompt != a.systemPrompt
	if changed {
		a.systemPrompt = prompt
		a.rebuild()
	}
	return PromptReload{Changed: changed, Live: true, Prompt: a.systemPrompt}, nil
}

// refresh re-derives the transcript and the system prompt from the active
// branch. Model and thinking level stay untouched, matching pi's context refresh
// after navigation.
func (a *App) refresh() error {
	projected, err := a.manager.BuildContext()
	if err != nil {
		return err
	}
	a.adoptSystemPrompt(projected.Messages)
	a.ag = a.buildAgent(a.systemPrompt, &a.steering, &a.followUps)
	a.ag.SetMessages(projected.Messages)
	a.messages = append([]wire.Message(nil), projected.Messages...)
	return nil
}

// Model returns the model in effect.
func (a *App) Model() wire.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// ThinkingLevel returns the level forwarded to the provider with every request.
func (a *App) ThinkingLevel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.thinking
}

// GoalBudget returns the token budget new goals start with. Zero means none.
func (a *App) GoalBudget() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.goalBudget
}

// SystemPrompt returns the prompt in effect.
func (a *App) SystemPrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.systemPrompt
}

// RestoreNote explains why the configured model was kept instead of the one the
// session recorded. It is empty when the model was restored or nothing needed
// restoring.
func (a *App) RestoreNote() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.restoreNote
}

// Messages returns a snapshot of the transcript. It is safe to call while a
// turn is running because it reads the mirror the persistence path maintains.
func (a *App) Messages() []wire.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]wire.Message(nil), a.messages...)
}

// MessageCount reports how many messages the transcript holds.
func (a *App) MessageCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.messages)
}

// ContextUsage reports how many tokens the active branch occupies and how large
// the model's window is, with a window of zero meaning the config did not
// declare one.
//
// It is the same estimate automatic compaction uses, so a footer that says the
// context is full and the code that acts on it cannot disagree. That estimate
// walks the branch, which is why a caller repainting on every keystroke has to
// memoize it (cmd/atlas does).
func (a *App) ContextUsage() (tokens, window int) {
	a.mu.Lock()
	window = a.model.ContextWindow
	path := a.manager.Path("")
	a.mu.Unlock()

	projection, err := a.manager.BuildProjection()
	if err != nil {
		// A branch that cannot be projected has no usage to report; the caller
		// shows what it knows rather than a zero it would have to special-case.
		return 0, window
	}
	return compact.EstimateProjection(projection, path).Tokens, window
}

// Busy reports whether a turn is running on the main conversation.
func (a *App) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

// Session exposes the session this app drives, for callers that render its path
// or name. Changing it directly bypasses the app's bookkeeping.
func (a *App) Session() *session.Manager { return a.manager }

// Name returns the session name, or "" when it was never set.
func (a *App) Name() string { return a.manager.Name() }

// SetName records a new session name.
func (a *App) SetName(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.guardArchived(); err != nil {
		return err
	}
	_, err := a.manager.AppendSessionInfo(name)
	return err
}

// SetModel switches models and records the change. The resolver runs first, so a
// failed lookup leaves the app untouched.
func (a *App) SetModel(providerName, modelID string) (wire.Model, error) {
	if a.resolver == nil {
		return wire.Model{}, errors.New("app: SetModel needs a Resolver")
	}
	model, streamer, err := a.resolver(providerName, modelID)
	if err != nil {
		return wire.Model{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return wire.Model{}, ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		return wire.Model{}, err
	}
	if _, err := a.manager.AppendModelChange(model.Provider, model.ID); err != nil {
		return wire.Model{}, err
	}
	a.model = model
	a.provider = streamer
	a.restoreNote = ""
	a.rebuild()
	return model, nil
}

// SetThinkingLevel switches the thinking level and records the change when it
// differs from the current one, matching pi. An empty level means the default.
func (a *App) SetThinkingLevel(level string) error {
	if level == "" {
		level = session.DefaultThinkingLevel
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if level == a.thinking {
		return nil
	}
	if _, err := a.manager.AppendThinkingLevelChange(level); err != nil {
		return err
	}
	a.thinking = level
	a.rebuild()
	return nil
}

// Models returns the switch list. It is a copy: a front end that renders the
// list must not be able to edit what the kernel cycles through.
func (a *App) Models() []ModelOption {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneModels(a.models)
}

// AvailableThinkingLevels reports the levels the model in effect can send. ok is
// false when this app was given no row for that model: "I was not told" and "it
// cannot think" are different answers, and a client renders them differently.
func (a *App) AvailableThinkingLevels() ([]string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.levelsOf(a.model)
}

// FitThinking rounds level to one the model in effect can send, reporting the
// adjustment so the caller can say it instead of taking an answer it did not ask
// for. Nothing is changed here: the caller decides whether to apply it, which is
// what lets a transport answer "here is what you would get".
func (a *App) FitThinking(level string) (fitted, adjustment string, err error) {
	a.mu.Lock()
	model, fit := a.model, a.fitThinking
	a.mu.Unlock()
	if fit == nil {
		return level, "", nil
	}
	return fit(model, level)
}

// CycleModel switches to the next model in the list, wrapping at the end, and
// carries the thinking level with it the way pi does. ok is false when the list
// holds fewer than two models: there is nothing to cycle, which is a result
// rather than a failure.
//
// The level is fitted to the new model because a switch and a level are one
// setting from the reader's side: reporting "high" for a model that cannot send
// it would describe a request the next turn will not make.
func (a *App) CycleModel() (ModelCycle, bool, error) {
	a.mu.Lock()
	options := cloneModels(a.models)
	current := a.model
	a.mu.Unlock()

	if len(options) < 2 {
		return ModelCycle{}, false, nil
	}
	// A current model that is not in the list starts the walk at the first
	// entry, which is what pi does: a stale session model is not a position on
	// the ring, so the ring is joined where it begins.
	index := 0
	for i, option := range options {
		if option.Model.Provider == current.Provider && option.Model.ID == current.ID {
			index = i
			break
		}
	}
	next := options[(index+1)%len(options)]

	model, err := a.SetModel(next.Model.Provider, next.Model.ID)
	if err != nil {
		return ModelCycle{}, false, err
	}

	a.mu.Lock()
	level := a.thinking
	a.mu.Unlock()
	if levels, known := a.AvailableThinkingLevels(); known && len(levels) == 0 {
		// A model that cannot think has exactly one sendable level, and pi
		// lands there too rather than leaving a gear the request will drop.
		level = session.DefaultThinkingLevel
	} else if fitted, _, fitErr := a.FitThinking(level); fitErr == nil {
		level = fitted
	}
	if err := a.SetThinkingLevel(level); err != nil {
		return ModelCycle{}, false, err
	}
	return ModelCycle{Model: model, ThinkingLevel: level}, true, nil
}

// CycleThinkingLevel advances to the next level the model offers, wrapping at the
// end. ok is false when the model cannot think or the list does not know it:
// pi answers null there instead of cycling through levels nothing would send.
func (a *App) CycleThinkingLevel() (string, bool, error) {
	a.mu.Lock()
	current := a.thinking
	a.mu.Unlock()

	levels, known := a.AvailableThinkingLevels()
	if !known || len(levels) == 0 {
		return "", false, nil
	}
	// A level that is not on the axis starts the walk at its first position,
	// which is pi's rule and the only sensible one for a level the model
	// cannot express.
	next := levels[0]
	for i, level := range levels {
		if level == current {
			next = levels[(i+1)%len(levels)]
			break
		}
	}
	if err := a.SetThinkingLevel(next); err != nil {
		return "", false, err
	}
	return next, true, nil
}

// levelsOf returns the thinking axis for one model and whether the list holds a
// row for it. The caller holds the lock.
func (a *App) levelsOf(model wire.Model) ([]string, bool) {
	for _, option := range a.models {
		if option.Model.Provider == model.Provider && option.Model.ID == model.ID {
			return append([]string(nil), option.Levels...), true
		}
	}
	return nil, false
}

// cloneModels copies the list and the slices inside it: wire.Model and
// ModelOption both carry slices, so a shallow copy would share them.
func cloneModels(options []ModelOption) []ModelOption {
	if options == nil {
		return nil
	}
	out := make([]ModelOption, len(options))
	for i, option := range options {
		model := option.Model
		model.Input = append([]string(nil), option.Model.Input...)
		out[i] = ModelOption{Model: model, Levels: append([]string(nil), option.Levels...)}
	}
	return out
}

// SetSystemPrompt replaces the instructions with a fixed text and rebuilds the
// agent, keeping the transcript.
//
// The source is dropped rather than shadowed: a source is asked again for
// every turn, so a fixed text that lasted one turn would be a method whose
// effect did not match its name. Nothing is recorded — like the system prompt
// at startup, this is a property of the process, not of the conversation, and
// a session reopened later reads the files again.
func (a *App) SetSystemPrompt(prompt string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	a.basePromptSource = nil
	a.promptSource = nil
	a.baseSystemPrompt = prompt
	a.systemPrompt = prompt
	a.rebuild()
	return nil
}

// NewSession abandons the conversation and starts an empty one in the same cwd
// and session directory. The model, thinking level, tools, and system prompt
// stay, because a new conversation is the same assistant with a clean history.
// An in-memory app stays in memory.
//
// A running turn must be stopped by the caller first: resetting the session
// underneath an agent that is still appending would record messages into a
// conversation that no longer exists.
func (a *App) NewSession() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	// An archived conversation is read-only, and abandoning it for a fresh one
	// is exactly the move that leaves it behind half-said. /restore lifts the
	// lock; /delete ends the session on purpose.
	if err := a.guardArchived(); err != nil {
		return err
	}
	// The old session is deliberately not written here. Its file exists only
	// once it holds a user or assistant message, so a conversation the user
	// never talked to leaves nothing behind.
	return a.newSessionLocked()
}

// newSessionLocked resets the manager in place to an empty conversation. The
// caller holds the lock and has already checked busy: Archive and Delete both
// end with a fresh conversation, and they end it the same way /new does.
func (a *App) newSessionLocked() error {
	if _, err := a.manager.NewSession(session.Options{}); err != nil {
		return err
	}
	if a.side != nil {
		a.side.close()
		a.side = nil
	}
	// pi builds a new session object for a new conversation, so queued
	// messages and the queue modes go with the old one.
	a.steering.clear()
	a.followUps.clear()
	a.steering.setMode("")
	a.followUps.setMode("")
	return a.refresh()
}

// Branch moves the active leaf to entryID and rebuilds the transcript from the
// branch that ends there.
func (a *App) Branch(entryID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if err := a.guardArchived(); err != nil {
		return err
	}
	if err := a.manager.Branch(entryID); err != nil {
		return err
	}
	return a.refresh()
}
