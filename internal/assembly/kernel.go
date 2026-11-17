package assembly

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"pi-go/internal/app"
	"pi-go/internal/config"
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

	mcp   *MCPServers
	tasks TaskWiring
	set   app.ToolSet
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

// Close releases what outlives a turn: the MCP connections and the task service.
// A stdio server is a child process, and one that is not shut down keeps its
// pipes and its temp directory; a detached command left behind is a process
// nobody can list and nobody can stop.
func (k *Kernel) Close() {
	if k.mcp != nil {
		k.mcp.Close()
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
	selection, err := cfg.Resolve(o.Model)
	if err != nil {
		return nil, nil, err
	}

	var notes []string
	level := o.Thinking
	if level == "" {
		level = cfg.ThinkingLevel
	}
	level, adjustment, err := FitThinking(selection, level)
	if err != nil {
		return nil, nil, err
	}
	if adjustment != "" {
		notes = append(notes, adjustment)
	}
	model, adapter, err := Build(selection)
	if err != nil {
		return nil, nil, err
	}
	if notice := CredentialNotice(selection); notice != "" {
		notes = append(notes, notice)
	}

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

	webSet, webNote := WebTools(cfg.Web, spill)
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

	gate := &Gate{engine: engine, session: manager.ID}
	gate.SetAsk(o.Ask, nil)

	set := ToolSet(cfg, webSet, mcpTools, GitTools, spill, wiring)
	application, err := app.New(app.Config{
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
	}, manager)
	if err != nil {
		if servers != nil {
			servers.Close()
		}
		return nil, nil, err
	}
	if note := application.RestoreNote(); note != "" {
		notices = append(notices, note)
	}

	return &Kernel{
		App:       application,
		Manager:   manager,
		Gate:      gate,
		Config:    cfg,
		Selection: selection,
		Model:     model,
		Cwd:       cwd,
		Prompts:   prompts,
		set:       set,
		mcp:       servers,
		tasks:     wiring,
		notes:     notes,
		notices:   notices,
	}, notes, nil
}

// LoadConfig reads the config file. On a first run it creates the home directory
// and a template, then fails with what to edit: continuing with a guessed model
// would send the first prompt to an endpoint the user never chose.
//
// An explicit -config never writes. A typed path is the user's, and a typo must
// not leave a template behind to be mistaken for their own file.
func LoadConfig(explicit string) (*config.Config, error) {
	if explicit != "" {
		path, err := config.ExpandHome(explicit)
		if err != nil {
			return nil, err
		}
		return config.Load(path)
	}

	path, err := config.DefaultPath()
	if err != nil {
		return nil, err
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
	if k.set == nil {
		return nil
	}
	return k.set(cwd)
}
