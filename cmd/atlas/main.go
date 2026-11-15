// Command atlas is the CLI: one static binary that drives the kernel against
// the models declared in ~/.atlas/config.json.
//
// It is the composition root, and the only package that knows a concrete wire
// adapter. Everything below it takes a provider.Provider, which is what makes
// "choose openai-completions or anthropic-messages" one switch here instead of
// a conditional in the kernel: tests/app and tests/rpc fail the build if the
// orchestration or the transport ever reaches for an adapter directly.
//
// The modes are the ones a single binary needs: one prompt, JSONL events, a
// terminal session, pi's RPC protocol, and the scripted demo that runs offline.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"pi-go/internal/app"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider/faux"
	"pi-go/internal/rpc"
	"pi-go/internal/session"
	"pi-go/internal/task"
	"pi-go/internal/tool"
	"pi-go/internal/tool/echo"
	"pi-go/internal/tool/fs"
	"pi-go/internal/tool/git"
	"pi-go/internal/tool/shell"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// version is the CLI's own version. The window title quotes it, so it lives on
// its own rather than inside the code that draws it.
const version = "0.12.23"

// spillDirName is the subdirectory of a session bucket where output too large
// for a message is written. It sits beside the .jsonl files rather than in one
// shared temp directory because a spill belongs to the conversation that asked
// for it: `List` skips directories, so nothing here is mistaken for a session,
// and removing a bucket removes its leftovers.
const spillDirName = "spill"

// demoPrompt is what -demo runs when no prompt is given, so the smoke test is
// one flag and no argument.
const demoPrompt = "hello from atlas"

// demoSystemPrompt asks for exactly one tool call, which is what makes the
// demo exercise the loop instead of just the response path.
const demoSystemPrompt = "You are a demo agent. Call the echo tool once, then answer."

const usage = `atlas drives a model declared in the config file as a coding agent.

usage:
  atlas [flags] [prompt]        run one turn
  atlas                         interactive session on a terminal
  atlas -tui                    interactive session on the alternate screen
  echo "..." | atlas            read the prompt from stdin
  atlas -rpc                    serve pi's JSONL RPC protocol on stdin/stdout
  atlas -demo [prompt]          run the scripted offline demo
  atlas -list-models            print the configured models
  atlas -resume <id|path>       continue a recorded session

flags:
`

// options is one parsed command line. Everything that touches the network or
// the terminal happens below run, so a test can drive the whole binary with a
// struct, a buffer, and an httptest server.
type options struct {
	configPath   string
	prompt       string
	continueLast bool
	sessionPath  string
	resumePath   string
	sessionDir   string
	model        string
	thinking     string
	systemFile   string
	workdir      string

	// permissionMode overrides permission.mode from config.json for this run.
	// It exists for headless callers: a script has no /permission command and no
	// confirmation prompt, so the posture has to be a flag or not choosable.
	permissionMode string

	// noContextFiles turns off reading AGENTS.md and its siblings. Off by
	// default because the file exists to be read; the flag is the escape for a
	// caller that wants the built-in prompt exactly, such as a benchmark.
	noContextFiles bool

	// noSkills turns off the skills directory. It is the same escape as
	// noContextFiles, and the pair is what makes "nothing was added to the
	// prompt" a testable claim rather than a promise.
	noSkills bool

	asJSON     bool
	rpcMode    bool
	demo       bool
	listModels bool

	// tui asks for the full-screen session. A terminal gets it anyway, so the
	// flag is the explicit form of the intent — and the only way to ask for it
	// from a script, where the answer is an error rather than a screen the
	// caller cannot render.
	tui bool
}

// streams is the process surface: the three file handles and whether the
// session may draw itself on the terminal. Passing them keeps the full-screen
// session and the stdin-prompt path testable.
type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer

	// terminal reports that both ends are a terminal: the session draws on the
	// alternate screen, which needs somewhere to write as well as somewhere to
	// read. stdin a terminal and stdout a pipe is a caller capturing the
	// transcript, and it gets text rather than escape sequences.
	terminal bool

	// color enables ANSI styling: a banner and a box border are decoration, and
	// decoration in a pipe is noise in someone's log file.
	color bool
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	// SIGTERM ends the process. SIGINT is deliberately not registered here: a
	// turn installs its own handler (see ask), so Ctrl-C aborts the turn and
	// leaves the session standing. A session-wide handler would make the only
	// gesture for "stop talking" also the gesture for "forget everything".
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	stdio := streams{
		in:       os.Stdin,
		out:      os.Stdout,
		err:      os.Stderr,
		terminal: isTerminal(os.Stdin) && isTerminal(os.Stdout),
		color:    supportsColor(os.Stdout),
	}
	if err := run(ctx, opts, stdio); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("atlas", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), usage)
		flags.PrintDefaults()
	}

	flags.StringVar(&opts.configPath, "config", "", "config file (default ~/.atlas/config.json)")
	flags.StringVar(&opts.prompt, "prompt", "", "prompt to send; positional arguments work too")
	flags.BoolVar(&opts.continueLast, "continue", false, "continue the most recent session for this directory")
	flags.StringVar(&opts.sessionPath, "session", "", "open the session file at this path")
	flags.StringVar(&opts.resumePath, "resume", "", "continue this session: a path or an id prefix")
	flags.StringVar(&opts.sessionDir, "session-dir", "", "store sessions under this directory")
	flags.StringVar(&opts.model, "model", "", "model to use: \"provider/model\" or a unique model id")
	flags.StringVar(&opts.thinking, "thinking", "", "thinking level: off, low, medium, high, xhigh, max, ultra")
	flags.StringVar(&opts.permissionMode, "permission", "", "permission mode: default, auto, bypassPermissions, off")
	flags.StringVar(&opts.systemFile, "system-prompt-file", "", "file to read the system prompt from")
	flags.BoolVar(&opts.noContextFiles, "no-context-files", false, "do not read AGENTS.md or CLAUDE.md into the system prompt")
	flags.BoolVar(&opts.noSkills, "no-skills", false, "do not list the skills in the system prompt")
	flags.StringVar(&opts.workdir, "cwd", "", "working directory for tools and sessions (default the current one)")
	flags.BoolVar(&opts.asJSON, "json", false, "write agent events as JSONL")
	flags.BoolVar(&opts.rpcMode, "rpc", false, "serve the JSONL RPC protocol on stdin and stdout")
	flags.BoolVar(&opts.demo, "demo", false, "run the scripted demo: no config, no network, no api key")
	flags.BoolVar(&opts.listModels, "list-models", false, "print the configured models and exit")
	flags.BoolVar(&opts.tui, "tui", false, "draw the session full-screen (what a terminal gets anyway)")

	if err := flags.Parse(args); err != nil {
		return options{}, err
	}

	if positional := strings.Join(flags.Args(), " "); positional != "" {
		if opts.prompt != "" {
			return options{}, errors.New("a prompt was given twice: drop either -prompt or the argument")
		}
		opts.prompt = positional
	}

	switch {
	case opts.sessionPath != "" && opts.continueLast:
		return options{}, errors.New("-session opens one file and -continue picks the newest; choose one")
	case opts.resumePath != "" && (opts.sessionPath != "" || opts.continueLast):
		return options{}, errors.New("-resume names a session, and -session and -continue name another; choose one")
	case opts.rpcMode && opts.prompt != "":
		return options{}, errors.New("-rpc reads commands from stdin; drop the prompt")
	case opts.permissionMode != "" && !permission.ValidMode(opts.permissionMode):
		return options{}, fmt.Errorf("-permission %q is not one of: %s", opts.permissionMode, strings.Join(config.PermissionModes, ", "))
	case opts.demo && (opts.configPath != "" || opts.model != "" || opts.thinking != "" || opts.listModels):
		// Half of a demo is worse than none: the flags would be ignored
		// silently, and "why is it answering from faux" costs a debugging
		// session.
		return options{}, errors.New("-demo runs the scripted offline provider and ignores -config, -model, -thinking and -list-models")
	}
	return opts, nil
}

func run(ctx context.Context, opts options, stdio streams) error {
	cwd, err := resolveDir(opts.workdir)
	if err != nil {
		return err
	}
	if opts.demo {
		return runDemo(ctx, opts, cwd, stdio)
	}

	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		return err
	}
	if opts.listModels {
		return writeModelList(stdio.out, cfg)
	}
	if cfg.Update != nil {
		// One assignment per process, before any interface starts: the release
		// check and /update read the same repository, and a config edited mid-
		// session takes effect next time atlas opens.
		setUpdateRepo(cfg.Update.Repo)
	}
	return runConfigured(ctx, opts, cwd, cfg, stdio)
}

// loadConfig reads the config file. On a first run it creates the home
// directory and a template, then fails with what to edit: continuing with a
// guessed model would send the first prompt to an endpoint the user never chose.
//
// An explicit -config never writes. A typed path is the user's, and a typo must
// not leave a template behind to be mistaken for their own file.
func loadConfig(explicit string) (*config.Config, error) {
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

// runConfigured is the mode that talks to a real endpoint.
func runConfigured(ctx context.Context, opts options, cwd string, cfg *config.Config, stdio streams) error {
	selection, err := cfg.Resolve(opts.model)
	if err != nil {
		return err
	}
	level := thinkingLevel(opts, cfg)
	level, adjustment, err := fitThinking(selection, level)
	if err != nil {
		return err
	}
	if adjustment != "" {
		fmt.Fprintln(stdio.err, "warning:", adjustment)
	}
	model, adapter, err := build(selection)
	if err != nil {
		return err
	}
	if notice := credentialNotice(selection); notice != "" {
		fmt.Fprintln(stdio.err, "warning:", notice)
	}

	dir, err := sessionDir(opts, cfg, cwd)
	if err != nil {
		return err
	}
	manager, note, err := openSession(opts, dir, cwd, false)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(stdio.err, "note:", note)
	}
	reportSession(manager, stdio)
	// Output too large for a message is written next to the session that asked
	// for it, so deleting a conversation takes its leftovers with it and the
	// path in the notice says which conversation it came from. The directory is
	// asked for at write time rather than read here, because /resume can move
	// the session while the tool set stays the one built for this workspace.
	spill := tool.NewSpill(func() string { return filepath.Join(manager.Dir(), spillDirName) })

	// The prompt is a source, not a string: the app asks it again for every
	// turn, so a project file that changed and a skill that was installed
	// take effect on the next question. The first read happens here so a file
	// that cannot be read stops the session before it starts.
	prompts := opts.prompts()
	source := prompts.source()
	if _, err := source.read(cwd); err != nil {
		return err
	}
	for _, note := range source.last().Notes {
		fmt.Fprintln(stdio.err, "warning:", note)
	}

	// The git tools are per directory like the file tools: the repository they
	// read is the one the session is in, and /resume can move that. Building them
	// here is also what decides once whether this machine has git at all — a
	// machine without it registers none of them.
	gitTools := func(cwd string) []tool.Tool { return git.Tools(git.Options{Cwd: cwd}) }

	webSet, webNote := webTools(cfg.Web, spill)
	if webNote != "" {
		fmt.Fprintln(stdio.err, "warning:", webNote)
	}
	applied, sandboxNote := sandboxNote(cfg.Sandbox)
	if sandboxNote != "" {
		fmt.Fprintln(stdio.err, "warning:", sandboxNote)
	}
	permEngine, permNote := permissionFor(cfg, applied, selection, opts.permissionMode)
	if permNote != "" {
		fmt.Fprintln(stdio.err, "warning:", permNote)
	}
	mcpServers, mcpNotes, err := mcpFor(ctx, cwd, spill)
	if err != nil {
		return err
	}
	// The connections outlive the turn and die with the process, so the defer is
	// the whole lifetime rule: a stdio server is a child process, and one that is
	// not shut down keeps its pipes and its temp directory.
	defer mcpServers.close()
	for _, note := range mcpNotes {
		fmt.Fprintln(stdio.err, "warning:", note)
	}
	// The set is a function of the directory, not the list built for this one:
	// /resume can open a conversation recorded in another project, and the file
	// tools that come with it are bounded by that project's workspace.
	wiring := tasksFor(manager, spill)
	if wiring.service != nil {
		// Leaving a background command behind would be a process nobody can
		// list and nobody can stop, so the end of this process is the end of
		// its tasks. The defer sits here because serve owns the whole session.
		defer func() { _ = wiring.service.Close() }()
	}
	set := toolSet(cfg, webSet, mcpServers.tools, gitTools, spill, wiring)
	// The gate is built before the app because the app keeps the hook for its
	// lifetime.
	side := newSideStateWith(permEngine)
	application, err := app.New(app.Config{
		Model:          model,
		Provider:       adapter,
		ThinkingLevel:  level,
		SystemPrompt:   source.prompt,
		Tools:          set,
		Resolver:       resolverFor(cfg),
		Models:         modelTable(cfg),
		FitThinking:    thinkingFitter(cfg),
		BeforeToolCall: side.beforeToolCall,
		GoalBudget:     goalBudget(cfg),
	}, manager)
	if err != nil {
		return err
	}
	if note := application.RestoreNote(); note != "" {
		fmt.Fprintln(stdio.err, "note:", note)
	}
	return serve(ctx, application, side, mcpServers, opts, cfg, cwd, toolSummary(set(cwd)), source, stdio)
}

// runDemo keeps the offline path: a scripted provider, the echo tool, and an
// in-memory session unless a session flag says otherwise. It exists so the loop
// can be exercised without a config, a key, or a network.
func runDemo(ctx context.Context, opts options, cwd string, stdio streams) error {
	prompt := opts.prompt
	if prompt == "" {
		prompt = demoPrompt
	}

	// The demo has no config, so the session directory comes from the flags or
	// the default.
	dir, err := sessionDir(opts, nil, cwd)
	if err != nil {
		return err
	}
	manager, note, err := openSession(opts, dir, cwd, true)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(stdio.err, "note:", note)
	}
	reportSession(manager, stdio)

	scripted := faux.New(
		faux.Response{ToolCalls: []wire.Content{
			wire.ToolCallContent("call_1", "echo", toolArgs(prompt)),
		}},
		faux.Response{Text: "demo complete"},
	)
	application, err := app.New(app.Config{
		Model:        wire.Model{ID: "faux-1", Name: "Faux", API: "faux", Provider: "faux"},
		Provider:     scripted,
		SystemPrompt: app.StaticPrompt(demoSystemPrompt),
		Tools:        app.StaticTools([]tool.Tool{echo.New()}),
	}, manager)
	if err != nil {
		return err
	}

	if opts.rpcMode {
		return rpc.New(application).Serve(ctx, stdio.in, stdio.out)
	}
	return askLine(ctx, application, prompt, newRenderer(stdio.out, stdio.err, opts.asJSON))
}

// serve picks between the ways a configured app is driven. A terminal gets the
// full-screen session; a pipe gets one prompt and a transcript. grants names the
// tool groups the session holds, which the footer shows as the session's
// permissions.
func serve(ctx context.Context, application *app.App, side *sideState, servers mcpState, opts options, cfg *config.Config, cwd, grants string, prompts *promptSource, stdio streams) error {
	if opts.rpcMode {
		return rpc.New(application).Serve(ctx, stdio.in, stdio.out)
	}

	render := newRenderer(stdio.out, stdio.err, opts.asJSON)
	if opts.prompt != "" {
		return askLine(ctx, application, opts.prompt, render)
	}
	if opts.asJSON && stdio.terminal {
		// The full-screen session would draw over the event stream; -json is
		// for a caller that reads one JSON object per line.
		return errors.New("no prompt: -json writes one event per line, so it needs a prompt or a piped stdin")
	}
	if opts.tui && !stdio.terminal {
		// A script has nowhere to draw a screen, and the caller would only find
		// out from an error message wrapped in escape sequences.
		return errors.New("-tui draws on the terminal, and this one is not a terminal")
	}
	if !stdio.terminal {
		prompt, err := io.ReadAll(stdio.in)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(prompt)) == "" {
			return errors.New("no prompt: pass one as an argument, on stdin, or run on a terminal for a session")
		}
		return askLine(ctx, application, string(prompt), render)
	}

	switchModel := func(spec string) (wire.Model, string, string, error) {
		selection, level, adjustment, err := resolveSpec(cfg, spec)
		if err != nil {
			return wire.Model{}, "", "", err
		}
		current := application.Model()
		if selection.ProviderName == current.Provider && selection.Model.ID == current.ID {
			// Choosing the model that is already in use must not write a
			// model_change: asking for the list and typing the name would add
			// an entry per look.
			return current, level, adjustment, nil
		}
		model, err := application.SetModel(selection.ProviderName, selection.Model.ID)
		if err != nil {
			return wire.Model{}, "", "", err
		}
		return model, level, adjustment, nil
	}

	modelSet := models{
		switchModel: switchModel,
		list: func(out io.Writer) error {
			return writeModelList(out, cfg)
		},
		choices: func() []tui.Choice {
			return modelChoices(cfg, application.ThinkingLevel())
		},
	}
	// The session reads the terminal itself, from here on. Building anything
	// else that reads it first would leave two readers taking each other's
	// keystrokes.
	return runTUI(ctx, application, side, servers, modelSet,
		newSessionPicker(application, allHome(opts, cfg)), prompts,
		newStatusBar(application, side, cwd, grants), cfg, cwd, stdio)
}

// ask runs one turn on the main conversation. The text is handed over as it is:
// this is the path a generated prompt takes — /init's and /skill's — and those
// are text this program wrote, so an "@" inside one is prose rather than a file
// to attach.
func ask(ctx context.Context, application *app.App, prompt string, render *renderer) error {
	return askMessage(ctx, application, wire.UserMessage(prompt), render)
}

// askLine runs one turn on a line the reader typed. A line is the only place a
// reference can come from, so this is where "@path" becomes content.
func askLine(ctx context.Context, application *app.App, line string, render *renderer) error {
	message, err := attachMessage(line, attachDir(application))
	if err != nil {
		return err
	}
	return askMessage(ctx, application, message, render)
}

func askMessage(ctx context.Context, application *app.App, message wire.Message, render *renderer) error {
	return runTurn(ctx, render, func(turnCtx context.Context, emit func(wire.AgentEvent) error) (app.Result, error) {
		return application.Ask(turnCtx, message, emit)
	})
}

// runTurn runs one turn and reports the failure the kernel recorded. A cancelled
// turn is not an error: it is how Ctrl-C is supposed to end.
//
// The interrupt handler lives for exactly this turn. Registering it on the
// process context instead would cancel every future prompt too, so one Ctrl-C
// in the middle of a long answer would end the session and lose the history
// that made the answer worth interrupting.
//
// The two conversations take the same path because they differ in nothing else:
// same events, same summary, same way of ending.
func runTurn(ctx context.Context, render *renderer, run func(context.Context, func(wire.AgentEvent) error) (app.Result, error)) error {
	turnCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	timer := newTurnTimer()
	result, err := run(turnCtx, func(event wire.AgentEvent) error {
		timer.observe(event)
		return render.event(event)
	})
	if err != nil {
		if turnCtx.Err() != nil {
			// Ctrl-C: the kernel reports the cancellation as an error but still
			// returns what it produced, so the turn ends quietly and the
			// session keeps going.
			render.summary(result, timer.cost())
			return nil
		}
		return err
	}
	render.summary(result, timer.cost())
	if result.StopReason != wire.StopError {
		return nil
	}
	if result.ErrorMessage != "" {
		return errors.New(result.ErrorMessage)
	}
	return errors.New("the turn failed without a reason")
}

// openSession opens the session the flags select, or starts one. keepMemory
// makes a fresh session in-memory, which is what a demo run needs so a smoke
// test leaves no files behind. The note reports an adjustment the caller should
// hand to the user, such as a recorded directory that had to be replaced.
func openSession(opts options, dir, cwd string, keepMemory bool) (*session.Manager, string, error) {
	switch {
	case opts.sessionPath != "":
		path, err := resolveDir(opts.sessionPath)
		if err != nil {
			return nil, "", err
		}
		return openRecorded(path, dir, cwd, opts.workdir != "")
	case opts.resumePath != "":
		path, err := resolveResumeTarget(opts.resumePath, dir, cwd)
		if err != nil {
			return nil, "", err
		}
		return openRecorded(path, dir, cwd, opts.workdir != "")
	case opts.continueLast:
		manager, err := session.ContinueRecent(cwd, dir)
		return manager, "", err
	}
	// A session flag asks for a file, and then the demo must write one: without
	// a flag the demo stays in memory so a smoke test leaves nothing behind.
	if keepMemory && opts.sessionDir == "" {
		manager, err := session.InMemory(cwd, session.Options{})
		return manager, "", err
	}
	manager, err := session.Create(cwd, dir, session.Options{})
	return manager, "", err
}

// reportSession says where the session file is, and whether it opened read-only.
// An archived file is a legitimate thing to open by path, and the lock is not
// something the first prompt should be the first to discover.
func reportSession(manager *session.Manager, stdio streams) {
	if manager.Persisted() {
		fmt.Fprintf(stdio.err, "session: %s\n", manager.File())
	}
	if session.Archived(manager.File()) {
		fmt.Fprintln(stdio.err, archivedLocked)
	}
}

// openRecorded opens a session file named by -session or -resume. The cwd
// override stays empty unless the recorded directory is gone: an opened session
// keeps the directory it was recorded in, and rewriting it would make the header
// disagree with the messages.
//
// A deleted directory is a startup failure, not a surprise inside the first
// tool call. -cwd is the explicit way to say "run it here instead"; without it
// the run stops while no tool has touched anything yet.
func openRecorded(path, dir, cwd string, override bool) (*session.Manager, string, error) {
	manager, err := session.Open(path, dir, "")
	if err != nil {
		return nil, "", err
	}
	missing, gone := manager.MissingCwd()
	if !gone {
		return manager, "", nil
	}
	if !override {
		return nil, "", fmt.Errorf("session %s was recorded in %s, which no longer exists; cd %s to run it there, or pass -cwd %s to run it in the current directory %s", path, missing, missing, cwd, cwd)
	}
	reopened, err := session.Open(path, dir, cwd)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("session %s was recorded in %s, which no longer exists; running it in %s (-cwd)", path, missing, cwd)
	return reopened, note, nil
}

// sessionDir resolves where sessions live: the flag, then the config, then the
// pi-style default under the atlas home, which is
// <home>/sessions/<encoded cwd>.
func sessionDir(opts options, cfg *config.Config, cwd string) (string, error) {
	raw := opts.sessionDir
	if raw == "" && cfg != nil {
		raw = cfg.SessionDir
	}
	if raw == "" {
		home, err := config.DefaultHome()
		if err != nil {
			return "", err
		}
		return session.DefaultDir(cwd, home), nil
	}
	return resolveDir(raw)
}

func thinkingLevel(opts options, cfg *config.Config) string {
	if opts.thinking != "" {
		return opts.thinking
	}
	return cfg.ThinkingLevel
}

// taskWiring is the background-command capability, or nothing.
//
// It is a struct rather than two parameters because the two halves are never
// supplied apart: a service with no tools records commands nothing can list, and
// tools with no service list an empty file forever. Both are nil on a platform
// without a detached runner, which is the signal the set builder reads.
type taskWiring struct {
	service *task.Service
	tools   []tool.Tool
}

// toolSet is the built-in set as the app asks for it: the file tools, bash,
// whatever the web block contributed, and the MCP servers this process
// connected, for one directory.
//
// The file and shell tools are always present, and what narrows them is not the
// list but the arguments: the file tools are bounded by the workspace they were
// given, and bash is bounded by the sandbox when the config asks for one. The web
// tools join the list as they are — they were built once, from the config, and do
// not belong to a directory.
//
// The task service is the one place where a capability is conditional. It is nil
// on a platform with no detached runner, and nil is what removes
// run_in_background from the bash schema and task_query from the tool list: an
// argument that can only fail is worse than an argument that is not offered.
//
// The MCP tools are the one part that is asked for rather than captured: a server
// can declare a tool while the session runs, so mcpTools is a function the app
// calls again before every turn (docs/mcp.md section 5).
//
// It is a function of cwd rather than a slice because a session records the
// directory its tools ran in, and /resume can open one from another project.
// The sandbox is rebuilt with it: a confinement whose writable directory was
// the one this process started in would refuse every write in the project the
// session actually belongs to.
func toolSet(cfg *config.Config, webSet []tool.Tool, mcpTools func() []tool.Tool, gitTools func(cwd string) []tool.Tool, spill *tool.Spill, wiring taskWiring) app.ToolSet {
	var block *config.Sandbox
	if cfg != nil {
		block = cfg.Sandbox
	}
	return func(cwd string) []tool.Tool {
		set := fs.Tools(fs.Options{Cwd: cwd})
		options := shell.Options{Cwd: cwd, Sandbox: narrow(confine(block, cwd)), Spill: spill}
		if wiring.service != nil {
			options.Background = wiring.service
		}
		set = append(set, shell.Tools(options)...)
		set = append(set, gitTools(cwd)...)
		set = append(set, wiring.tools...)
		set = append(set, webSet...)
		if mcpTools == nil {
			return set
		}
		return append(set, mcpTools()...)
	}
}

// resolveDir expands "~" and makes a path absolute. An empty path means the
// process working directory.
func resolveDir(path string) (string, error) {
	expanded, err := config.ExpandHome(path)
	if err != nil {
		return "", err
	}
	if expanded == "" {
		return os.Getwd()
	}
	return filepath.Abs(expanded)
}

// supportsColor reports whether the terminal takes ANSI styling. NO_COLOR and
// TERM=dumb are the two conventions that say no.
func supportsColor(file *os.File) bool {
	if !isTerminal(file) {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return os.Getenv("TERM") != "dumb"
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// toolArgs encodes the demo tool call. The echo tool takes one text field.
func toolArgs(text string) json.RawMessage {
	encoded, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil
	}
	return encoded
}
