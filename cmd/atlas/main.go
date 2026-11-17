// Command atlas is the CLI: one static binary that drives the kernel against
// the models declared in ~/.atlas/config.json.
//
// It is a front end, not the composition root: assembling a Turn lives in
// pi-go/internal/assembly, which is the only package that knows a concrete wire
// adapter. Everything below it takes a provider.Provider, which is what makes
// "choose openai-completions or anthropic-messages" one switch there instead of
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
	"strings"
	"syscall"

	"pi-go/internal/app"
	"pi-go/internal/assembly"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider/faux"
	"pi-go/internal/rpc"
	"pi-go/internal/session"
	"pi-go/internal/tool"
	"pi-go/internal/tool/echo"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// version is the CLI's own version. The window title quotes it, so it lives on
// its own rather than inside the code that draws it.
const version = "0.12.28"

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

// assembly is the same command line seen as the request the kernel takes. Every
// field is a flag read straight through; the session is the one exception,
// because a -resume prefix needs the session list to search and that search
// stays in this package.
func (o options) assembly() assembly.Options {
	return assembly.Options{
		ConfigPath:     o.configPath,
		Model:          o.model,
		Thinking:       o.thinking,
		Workdir:        o.workdir,
		PermissionMode: o.permissionMode,
		SystemFile:     o.systemFile,
		NoContextFiles: o.noContextFiles,
		NoSkills:       o.noSkills,
		SessionPath:    o.sessionPath,
		ContinueLast:   o.continueLast,
		SessionDir:     o.sessionDir,
		Version:        version,
	}
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
	cwd, err := assembly.ResolveDir(opts.workdir)
	if err != nil {
		return err
	}
	if opts.demo {
		return runDemo(ctx, opts, cwd, stdio)
	}

	cfg, err := assembly.LoadConfig(opts.configPath)
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

// runConfigured is the mode that talks to a real endpoint.
func runConfigured(ctx context.Context, opts options, cwd string, cfg *config.Config, stdio streams) error {
	request := opts.assembly()
	// A -resume prefix is resolved here rather than in the assembly: the search
	// reads the session list and can print the candidates it refused, and a
	// window has no menu to read them on.
	if opts.resumePath != "" {
		dir, err := assembly.SessionDir(request, cfg, cwd)
		if err != nil {
			return err
		}
		target, err := resolveResumeTarget(opts.resumePath, dir, cwd)
		if err != nil {
			return err
		}
		request.SessionPath = target
	}

	kernel, notes, err := assembly.New(ctx, request)
	if err != nil {
		return err
	}
	// The connections outlive the turn and die with the process: a stdio server
	// is a child process, and one that is not shut down keeps its pipes and its
	// temp directory.
	defer kernel.Close()

	for _, note := range notes {
		fmt.Fprintln(stdio.err, "warning:", note)
	}
	for _, note := range kernel.Notices() {
		fmt.Fprintln(stdio.err, "note:", note)
	}
	reportSession(kernel.Manager, stdio)

	// The terminal is what knows whether the next turn is typed into the main
	// conversation or into a side one, so it owns that half of the gate.
	side := newSideStateWith(kernel.Gate)
	return serve(ctx, kernel, side, opts, stdio)
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
	request := opts.assembly()
	dir, err := assembly.SessionDir(request, nil, cwd)
	if err != nil {
		return err
	}
	if opts.resumePath != "" {
		target, resolveErr := resolveResumeTarget(opts.resumePath, dir, cwd)
		if resolveErr != nil {
			return resolveErr
		}
		request.SessionPath = target
	}
	manager, note, err := assembly.OpenSession(request, dir, cwd, true)
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
func serve(ctx context.Context, kernel *assembly.Kernel, side *sideState, opts options, stdio streams) error {
	// The kernel carries what this function would otherwise take as six
	// arguments: the app, the file it was built from, the directory it runs in,
	// the live prompt and the connections.
	application, cfg, cwd := kernel.App, kernel.Config, kernel.Cwd
	servers, prompts := kernel.MCPServers(), kernel.Prompts
	// grants names the tool groups the session holds, which the footer shows as
	// the session's permissions.
	grants := toolSummary(kernel.Tools(cwd))
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
	return runTUI(ctx, application, side, mcpState{servers}, modelSet,
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
