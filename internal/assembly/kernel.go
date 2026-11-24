package assembly

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"pi-go/internal/app"
	"pi-go/internal/artifact"
	"pi-go/internal/browser"
	"pi-go/internal/compact"
	"pi-go/internal/config"
	"pi-go/internal/provider"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/wire"
)

// The kit assembled from one config file: an app, the session it writes to, the
// gate its tools pass, and the connections and services that outlive a turn.
//
// The reason this is a package rather than a function in the command is that
// there are two front ends now. A terminal and a window must run the same tools
// under the same permission engine and read the same prompt, or they are two
// products that happen to share a repository.

// SpillDirName is the subdirectory of a session bucket where output too large
// for a message is written. It sits beside the .jsonl files rather than in one
// shared temp directory because a spill belongs to the conversation that asked
// for it: `List` skips directories, so nothing here is mistaken for a session,
// and removing a bucket removes its leftovers.
const SpillDirName = "spill"

// Kernel is one assembled atlas: everything a front end needs to run turns.
type Kernel struct {
	// App is the kernel itself: turns, models, compaction, side conversations.
	App *app.App
	// Manager is the session behind it. It is exported because /resume, /new
	// and /fork replace it, and those are front-end commands.
	Manager *session.Manager
	// Gate is the approval path the app calls before every tool.
	Gate *Gate
	// Config is the file this was built from. Front-end commands read it for
	// the model list, the session directory and the update repository.
	Config *config.Config
	// Selection is the model the kernel started on.
	Selection config.Selection
	// Model is its descriptor.
	Model wire.Model
	// Cwd is the directory tools run in.
	Cwd string
	// Prompts is the live prompt, asked again for every turn.
	Prompts *PromptSource

	mu               sync.Mutex
	options          Options
	defaultSelection config.Selection
	spill            *tool.Spill
	mcp              *MCPServers
	browser          *browser.Service
	artifacts        *artifact.Repo
	tasks            TaskWiring
	subagents        *subagentRunner
	set              app.ToolSet
	// notes are the warnings that belong to the reader rather than to a turn:
	// a missing credential, a sandbox that cannot be applied, a permission file
	// that would not parse. They are returned instead of printed because a
	// window has no stderr, and a warning nobody sees is not a warning.
	notes []string
	// notices are the other half: things that went as asked, with a caveat the
	// reader still wants. A session recorded in a directory that no longer
	// exists runs here instead, and a resumed conversation that was compacted
	// says so. The split from notes is what lets a front end label them
	// differently — a warning is about the configuration, a notice about the
	// conversation.
	notices []string
}

// Notes is what the assembly wants to tell the reader once, as a warning.
func (k *Kernel) Notes() []string { return k.notes }

// Notices is the same, for the adjustments that are not warnings.
func (k *Kernel) Notices() []string { return k.notices }

// MCPServers is the connected servers, for the commands that describe them.
func (k *Kernel) MCPServers() *MCPServers { return k.mcp }

// Tasks is the background-command capability, nil on a platform without one.
func (k *Kernel) Tasks() TaskWiring { return k.tasks }

// SetSubagentObserver receives background task snapshots for the primary and
// live sessions. Calls for different tasks may happen concurrently.
func (k *Kernel) SetSubagentObserver(observer func(wire.SubagentUpdate)) {
	if k != nil && k.subagents != nil {
		k.subagents.setObserver(observer)
	}
}

// StopSubagent cancels an admitted task that belongs to the named session.
func (k *Kernel) StopSubagent(sessionID, taskID string) error {
	if k == nil || k.subagents == nil {
		return errors.New("assembly: background subagents are disabled")
	}
	return k.subagents.stop(sessionID, taskID)
}

// Close releases resources that outlive a turn and cancels owned background work.
func (k *Kernel) Close() {
	if k.subagents != nil {
		k.subagents.close()
	}
	if k.mcp != nil {
		k.mcp.Close()
	}
	if k.browser != nil {
		_ = k.browser.Close()
	}
	if k.tasks.Service != nil {
		_ = k.tasks.Service.Close()
	}
}

// New assembles one kernel from a config file.
//
// The order below is load-bearing twice. The prompt is read before the app is
// built so a file that cannot be read stops the run while nothing has been
// touched yet. And the permission engine is built before the app because the
// app keeps the hook for its lifetime — the mode is read again on every call,
// but the object identity is not.
func New(ctx context.Context, o Options) (*Kernel, []string, error) {
	cwd, err := ResolveDir(o.Workdir)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := LoadConfig(o.ConfigPath)
	if err != nil {
		return nil, nil, err
	}
	if o.ConfigPath != "" {
		if path, err := configFilePath(o.ConfigPath); err == nil {
			if abs, err := filepath.Abs(path); err == nil {
				o.ConfigPath = abs
			}
		}
	}
	selection, err := cfg.Resolve(o.Model)
	if err != nil {
		return nil, nil, err
	}

	var notes []string
	model, adapter, level, adjustment, err := buildConfiguredModel(selection, o.Thinking, cfg.ThinkingLevel)
	if err != nil {
		return nil, nil, err
	}
	if adjustment != "" {
		notes = append(notes, adjustment)
	}
	if notice := CredentialNotice(selection); notice != "" {
		notes = append(notes, notice)
	}
	defaultSelection, _ := cfg.Resolve("")

	dir, err := SessionDir(o, cfg, cwd)
	if err != nil {
		return nil, nil, err
	}
	manager, notice, err := OpenSession(o, dir, cwd, false)
	if err != nil {
		return nil, nil, err
	}
	var notices []string
	if notice != "" {
		notices = append(notices, notice)
	}

	// Output too large for a message is written next to the session that asked
	// for it, so deleting a conversation takes its leftovers with it and the
	// path in the notice says which conversation it came from. The directory is
	// asked for at write time rather than read here, because /resume can move
	// the session while the tool set stays the one built for this workspace.
	spill := tool.NewSpill(func() string { return filepath.Join(manager.Dir(), SpillDirName) })

	// The prompt is a source, not a string: the app asks it again for every
	// turn, so a project file that changed and a skill that was installed take
	// effect on the next question. The first read happens here so a file that
	// cannot be read stops the session before it starts.
	prompts := NewPrompts(o.SystemFile, o.NoContextFiles, o.NoSkills).Source()
	first, err := prompts.Read(cwd)
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, first.Notes...)

	webSet, webNote := WebTools(cfg, spill)
	if webNote != "" {
		notes = append(notes, webNote)
	}
	applied, sandboxWarning := SandboxNote(cfg.Sandbox)
	if sandboxWarning != "" {
		notes = append(notes, sandboxWarning)
	}
	engine, permNote := PermissionFor(cfg, applied, selection, o.PermissionMode)
	if permNote != "" {
		notes = append(notes, permNote)
	}

	var servers *MCPServers
	if !o.NoMCP {
		opened, mcpNotes, mcpErr := NewMCP(ctx, cwd, spill, o.Version)
		if mcpErr != nil {
			return nil, nil, mcpErr
		}
		servers = &opened
		notes = append(notes, mcpNotes...)
	}
	var mcpTools func() []tool.Tool
	if servers != nil {
		mcpTools = servers.Tools
	}

	wiring := TasksFor(manager, spill)
	browserSvc := browser.NewService(browserOptions(cfg))
	artifactRepo := artifact.NewRepo()
	var currentApp *app.App
	fallbackSession := func() (string, string) {
		if currentApp != nil {
			if sess := currentApp.Session(); sess != nil {
				return sess.ID(), sess.File()
			}
		}
		return manager.ID(), manager.File()
	}
	extraSet := buildExtraTools(cwd, webSet, browserSvc, artifactRepo, fallbackSession)

	gate := &Gate{engine: engine, session: manager.ID, questionSession: manager.File}
	gate.SetAsk(o.Ask, nil)

	set := ToolSet(cfg, extraSet, mcpTools, GitTools, spill, wiring)
	var subagents *subagentRunner
	appCfg := app.Config{
		Model:          model,
		Provider:       adapter,
		ThinkingLevel:  level,
		SystemPrompt:   prompts.Prompt,
		Tools:          set,
		Resolver:       ResolverFor(cfg),
		Models:         ModelTable(cfg),
		FitThinking:    ThinkingFitter(cfg),
		BeforeToolCall: gate.BeforeToolCall,
		GoalBudget:     o.goalBudget(cfg),
		// Where a conversation started in another project belongs. The same
		// resolution the startup session used, asked again per directory: the
		// default layout answers a bucket per project, a configured store
		// answers itself.
		SessionDir: func(cwd string) (string, error) { return SessionDir(o, cfg, cwd) },
	}
	if o.EnableSubagents {
		subagents = newSubagentRunner(cfg, gate.Engine())
		appCfg.Subagents = subagents
	}
	application, err := app.New(appCfg, manager)
	if err != nil {
		(&Kernel{mcp: servers, browser: browserSvc, tasks: wiring, subagents: subagents}).Close()
		return nil, nil, err
	}
	currentApp = application
	if note := application.RestoreNote(); note != "" {
		notices = append(notices, note)
	}

	return &Kernel{
		App:              application,
		Manager:          manager,
		Gate:             gate,
		Config:           cfg,
		Selection:        selection,
		Model:            model,
		Cwd:              cwd,
		Prompts:          prompts,
		options:          o,
		defaultSelection: defaultSelection,
		spill:            spill,
		set:              set,
		mcp:              servers,
		browser:          browserSvc,
		artifacts:        artifactRepo,
		tasks:            wiring,
		subagents:        subagents,
		notes:            notes,
		notices:          notices,
	}, notes, nil
}

// LiveSession pairs one independent App instance with its session-scoped Gate.
type LiveSession struct {
	App  *app.App
	Gate *Gate
}

// ConfigPath returns the path to the active configuration file.
func (k *Kernel) ConfigPath() (string, error) {
	if k == nil {
		return "", errors.New("assembly: kernel is not initialized")
	}
	return configFilePath(k.options.ConfigPath)
}

// Reload re-reads the config file (and permission rules) and updates the live
// kernel: model catalog, resolvers, thinking rules, permission options, and
// tools. When config.json's defaultModel changes, the active session switches
// immediately when idle or queues the switch for the next user message while a
// turn is still running.
func (k *Kernel) Reload() error {
	return k.ReloadSessions()
}

// ReloadSessions reloads configuration for the kernel's primary App plus any
// additional live sessions held by a multi-session host.
func (k *Kernel) ReloadSessions(extra ...*LiveSession) error {
	if k == nil || k.App == nil {
		return errors.New("assembly: kernel is not initialized")
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	path, err := configFilePath(k.options.ConfigPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	newDefault, err := cfg.Resolve("")
	if err != nil && k.options.Model != "" {
		newDefault, err = cfg.Resolve(k.options.Model)
	}
	if err != nil {
		return err
	}

	model, adapter, level, _, err := buildConfiguredModel(newDefault, k.options.Thinking, cfg.ThinkingLevel)
	if err != nil {
		return err
	}

	overrideModel := k.defaultModelChanged(cfg, newDefault)
	thinkingChanged := k.Config != nil && k.Config.ThinkingLevel != cfg.ThinkingLevel

	var mcpTools func() []tool.Tool
	if k.mcp != nil {
		mcpTools = k.mcp.Tools
	}
	webSet, _ := WebTools(cfg, k.spill)
	if k.browser != nil {
		k.browser.Configure(browserOptions(cfg))
	}
	extraSet := buildExtraTools(k.Cwd, webSet, k.browser, k.artifacts, func() (string, string) {
		if k.App != nil {
			if sess := k.App.Session(); sess != nil {
				return sess.ID(), sess.File()
			}
		}
		if k.Manager != nil {
			return k.Manager.ID(), k.Manager.File()
		}
		return "", ""
	})
	applied, _ := SandboxNote(cfg.Sandbox)
	newSet := ToolSet(cfg, extraSet, mcpTools, GitTools, k.spill, k.tasks)

	o := k.options
	runtimeCfg := app.RuntimeConfig{
		Model:            model,
		Provider:         adapter,
		ThinkingLevel:    level,
		Tools:            newSet,
		Resolver:         ResolverFor(cfg),
		Models:           ModelTable(cfg),
		FitThinking:      ThinkingFitter(cfg),
		GoalBudget:       o.goalBudget(cfg),
		SessionDir:       func(cwd string) (string, error) { return SessionDir(o, cfg, cwd) },
		OverrideModel:    overrideModel,
		OverrideThinking: thinkingChanged,
	}
	if err := k.App.ReloadConfig(runtimeCfg); err != nil {
		return err
	}
	for _, target := range extra {
		if target == nil || target.App == nil || target.App == k.App {
			continue
		}
		if err := target.App.ReloadConfig(runtimeCfg); err != nil {
			return err
		}
	}

	if engine := k.Gate.Engine(); engine != nil {
		permOpts, _ := permissionOptions(cfg, applied, newDefault, k.options.PermissionMode)
		updateMode := k.options.PermissionMode == "" && configuredPermissionMode(k.Config) != configuredPermissionMode(cfg)
		if err := engine.Reload(permOpts, updateMode); err != nil {
			return err
		}
	}

	k.Config = cfg
	k.Selection = newDefault
	if _, err := cfg.Resolve(""); err == nil {
		k.defaultSelection = newDefault
	}
	k.Model = model
	k.set = newSet
	if k.subagents != nil {
		k.subagents.configure(cfg)
	}
	return nil
}

// ContextBreakdown summarizes the active session's prompt, instruction files,
// skills, and MCP footprint.
type ContextBreakdown struct {
	MCPServers         int
	MCPConfigured      int
	MCPTools           int
	MCPTokens          int
	Skills             int
	SkillsTokens       int
	AgentsMDLoaded     bool
	ContextFiles       []string
	ContextFilesTokens int
	SystemPrompt       string
	SystemPromptTokens int
}

// InspectContext returns loaded MCP, skill, AGENTS.md, and system prompt
// details for cwd, preferring livePrompt when the active session already holds one.
func (k *Kernel) InspectContext(cwd, livePrompt string) ContextBreakdown {
	var out ContextBreakdown
	if k != nil {
		mcpOverhead := k.MCPServers().Overhead()
		out.MCPServers = mcpOverhead.Connected
		out.MCPConfigured = mcpOverhead.Configured
		out.MCPTools = mcpOverhead.Tools
		out.MCPTokens = mcpOverhead.Tokens
	}
	var prompts *PromptSource
	if k != nil {
		prompts = k.Prompts
	}
	if summary, err := prompts.Inspect(cwd); err == nil {
		out.Skills = summary.Skills
		out.SkillsTokens = summary.SkillsTokens
		out.AgentsMDLoaded = summary.AgentsMDLoaded
		out.ContextFiles = append([]string(nil), summary.ContextFiles...)
		out.ContextFilesTokens = summary.ContextFilesTokens
		out.SystemPrompt = summary.Text
		out.SystemPromptTokens = summary.SystemPromptTokens
	}
	if livePrompt != "" {
		out.SystemPrompt = livePrompt
		out.SystemPromptTokens = compact.EstimateTextTokens(livePrompt)
	}
	return out
}

func buildExtraTools(cwd string, webSet []tool.Tool, browserSvc *browser.Service, repo *artifact.Repo, fallback func() (string, string)) []tool.Tool {
	// Register deepseek_web_search and deepseek_web_fetch ahead of browser_* tools
	// so the model prefers lightweight web search/fetch over launching a browser.
	extra := append([]tool.Tool(nil), webSet...)
	extra = append(extra, BrowserTools(browserSvc)...)
	extra = append(extra, ArtifactTools(cwd, repo, fallback)...)
	return extra
}

func browserOptions(cfg *config.Config) browser.Options {
	var opts browser.Options
	if cfg == nil || cfg.Browser == nil {
		return opts
	}
	if cfg.Browser.Executable != "" {
		if expanded, err := config.ExpandHome(cfg.Browser.Executable); err == nil {
			opts.Executable = expanded
		} else {
			opts.Executable = cfg.Browser.Executable
		}
	}
	opts.HeadlessOverride = cfg.Browser.Headless
	return opts
}

// CanSpawnSessions reports whether this Kernel was built with full assembly
// configuration and can instantiate independent concurrent session Apps.
func (k *Kernel) CanSpawnSessions() bool {
	return k != nil && k.Config != nil && k.Prompts != nil
}

// OpenLiveSession opens an existing session file (when path is non-empty) or
// starts a fresh empty session in cwd, returning an independent App and Gate
// that share this Kernel's live config, tools, MCP connections, and permission
// engine so multiple conversations can run concurrently.
func (k *Kernel) OpenLiveSession(path, cwd string) (*LiveSession, error) {
	if k == nil || k.App == nil {
		return nil, errors.New("assembly: kernel is not initialized")
	}
	k.mu.Lock()
	cfg := k.Config
	selection := k.Selection
	o := k.options
	prompts := k.Prompts
	set := k.set
	gate := k.Gate
	defaultCwd := k.Cwd
	k.mu.Unlock()

	var manager *session.Manager
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("app: no session file at %s", path)
			}
			return nil, fmt.Errorf("assembly: open %s: %w", path, err)
		}
		opened, err := session.Open(path, filepath.Dir(path), "")
		if err != nil {
			return nil, err
		}
		if missing, gone := opened.MissingCwd(); gone {
			return nil, fmt.Errorf("app: session %s was recorded in %s, which no longer exists; restore that directory to resume it, or open it with -session and -cwd to run it here", path, missing)
		}
		manager = opened
	} else {
		targetCwd := cwd
		if targetCwd == "" {
			targetCwd = defaultCwd
		}
		resolvedCwd, err := ResolveDir(targetCwd)
		if err != nil {
			return nil, err
		}
		dir, err := SessionDir(o, cfg, resolvedCwd)
		if err != nil {
			return nil, err
		}
		created, err := session.Create(resolvedCwd, dir, session.Options{})
		if err != nil {
			return nil, err
		}
		manager = created
	}

	model, adapter, level, _, err := buildConfiguredModel(selection, o.Thinking, cfg.ThinkingLevel)
	if err != nil {
		return nil, err
	}
	var sessionGate *Gate
	if gate != nil {
		sessionGate = gate.CloneForSession(manager.ID, manager.File)
	}

	appCfg := app.Config{
		Model:         model,
		Provider:      adapter,
		ThinkingLevel: level,
		SystemPrompt:  prompts.Prompt,
		Tools:         set,
		Resolver:      ResolverFor(cfg),
		Models:        ModelTable(cfg),
		FitThinking:   ThinkingFitter(cfg),
		GoalBudget:    o.goalBudget(cfg),
		SessionDir:    func(dir string) (string, error) { return SessionDir(o, cfg, dir) },
	}
	if sessionGate != nil {
		appCfg.BeforeToolCall = sessionGate.BeforeToolCall
	}
	if k.subagents != nil {
		appCfg.Subagents = k.subagents
	}
	application, err := app.New(appCfg, manager)
	if err != nil {
		return nil, err
	}
	return &LiveSession{App: application, Gate: sessionGate}, nil
}

func (k *Kernel) defaultModelChanged(cfg *config.Config, newDefault config.Selection) bool {
	prevDefault := k.defaultSelection
	if prevDefault.Model.ID != "" {
		return prevDefault.ProviderName != newDefault.ProviderName || prevDefault.Model.ID != newDefault.Model.ID
	}
	if k.Config != nil && k.Config.DefaultModel != cfg.DefaultModel {
		return newDefault.ProviderName != k.Selection.ProviderName || newDefault.Model.ID != k.Selection.Model.ID
	}
	return false
}

func buildConfiguredModel(selection config.Selection, overrideThinking, cfgThinking string) (wire.Model, provider.Provider, string, string, error) {
	level := overrideThinking
	if level == "" {
		level = cfgThinking
	}
	level, adjustment, err := FitThinking(selection, level)
	if err != nil {
		return wire.Model{}, nil, "", "", err
	}
	model, adapter, err := Build(selection)
	if err != nil {
		return wire.Model{}, nil, "", "", err
	}
	return model, adapter, level, adjustment, nil
}

func configFilePath(explicit string) (string, error) {
	if explicit != "" {
		return config.ExpandHome(explicit)
	}
	return config.DefaultPath()
}

// LoadConfig reads the config file. On a first run it creates the home directory
// and a template, then fails with what to edit: continuing with a guessed model
// would send the first prompt to an endpoint the user never chose.
//
// An explicit -config never writes. A typed path is the user's, and a typo must
// not leave a template behind to be mistaken for their own file.
func LoadConfig(explicit string) (*config.Config, error) {
	path, err := configFilePath(explicit)
	if err != nil {
		return nil, err
	}
	if explicit != "" {
		return config.Load(path)
	}

	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	written, created, writeErr := config.WriteTemplate(filepath.Dir(path))
	if writeErr != nil {
		return nil, writeErr
	}
	if !created {
		return nil, fmt.Errorf("config: %s was not readable and could not be created", path)
	}
	return nil, fmt.Errorf("config: created %s; add a credential there, then run atlas again", written)
}

// goalBudget is the token budget a configured goal runs under. Zero means the
// config names none, which leaves the budget to the goal command's own default.
func (o Options) goalBudget(cfg *config.Config) int {
	if cfg == nil || cfg.Goal == nil {
		return 0
	}
	return cfg.Goal.TokenBudget
}

// Tools is the tool set for one directory. It is a function of the directory
// because /resume can open a session recorded in another project.
func (k *Kernel) Tools(cwd string) []tool.Tool {
	k.mu.Lock()
	set := k.set
	k.mu.Unlock()
	if set == nil {
		return nil
	}
	return set(cwd)
}
