package assembly

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pi-go/internal/app"
	"pi-go/internal/classify"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider"
	"pi-go/internal/sandbox"
	"pi-go/internal/task"
	"pi-go/internal/tool"
	"pi-go/internal/tool/fs"
	"pi-go/internal/tool/git"
	"pi-go/internal/tool/shell"
	"pi-go/internal/tool/web"
)

// WebTools builds the search and fetch tools from the web block.
//
// A missing credential is a warning, not an error. The two tools are an addition
// to a conversation, not a condition for having one, and a config shared between
// machines where only one of them exports the key should still start. Saying it
// once at startup is the honest form: the tools then do not exist for this
// session, so every later turn agrees with the warning instead of discovering it
// through calls that were never on the tool list.
func WebTools(block *config.Web, spill *tool.Spill) ([]tool.Tool, string) {
	if block == nil {
		return nil, ""
	}
	key := block.Key()
	if key == "" {
		hint := "set apiKey"
		if block.APIKeyEnv != "" {
			hint = "export " + block.APIKeyEnv + " or set apiKey"
		}
		return nil, fmt.Sprintf("web tools are off: web has no credential; %s", hint)
	}
	return web.Tools(web.Options{BaseURL: block.BaseURL, APIKey: key, Spill: spill}), ""
}

// SandboxNote says whether the configured sandbox is in effect here, and what to
// warn about it.
//
// An unavailable executor is a warning here and an error on the command. Saying
// it once at startup is the honest form, and reporting no sandbox instead would
// turn one that cannot be applied into a silent grant of the full permissions,
// which is the one failure a sandbox must not have.
//
// The confinement itself comes from confine, which the tool set calls again for
// every directory a session may belong to. This function is only about the
// warning: the executor is a fact about this machine, so it does not change with
// the workspace, and it is asked without building a policy that nothing would
// use.
func SandboxNote(block *config.Sandbox) (bool, string) {
	if block == nil {
		return false, ""
	}
	if err := sandbox.Available(); err != nil {
		return false, fmt.Sprintf("%v; bash will fail rather than run unconfined", err)
	}
	return true, ""
}

// confine is the confinement the bash tool runs commands under for one
// workspace, or nil when the config asks for none.
//
// Two directories are writable, not one. The workspace is the boundary; the
// temporary directory is there because a compiler, a test runner and half the
// standard tools need somewhere to write, and a sandbox where `go build` fails on
// its own work directory teaches the reader to turn the sandbox off. Everything
// else, including the user's home, is refused by the default-deny the profile
// starts from.
//
// It takes the workspace as an argument because a session carries one: the
// writable root has to be the directory the tools run in, not the one this
// process started in, or every write in the project the session belongs to would
// be refused.
func confine(block *config.Sandbox, cwd string) *sandbox.Sandbox {
	if block == nil {
		return nil
	}
	return sandbox.New(sandbox.Policy{
		Writable: []string{cwd, os.TempDir()},
		DenyRead: denyReadFor(block),
		Network:  block.AllowsNetwork(),
	})
}

// narrow hands a built confinement to the interface the shell tool takes. It
// exists because a nil *sandbox.Sandbox stored in that interface is not nil: the
// tool would believe it was confined and call Wrap on nothing, so "no sandbox"
// has to be the untyped nil the tool tests for.
func narrow(box *sandbox.Sandbox) shell.Sandbox {
	if box == nil {
		return nil
	}
	return box
}

// defaultCredentialDirs are the directories under the user's home that a
// confined bash command must not read even though the rest of the machine is
// readable.
var defaultCredentialDirs = []string{
	".ssh",
	".aws",
	".kube",
	".gnupg",
}

func denyReadFor(block *config.Sandbox) []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, name := range defaultCredentialDirs {
			out = append(out, filepath.Join(home, name))
		}
	}
	if block == nil {
		return out
	}
	for _, raw := range block.DenyRead {
		if resolved, err := ResolveDir(strings.TrimSpace(raw)); err == nil && resolved != "" {
			out = append(out, resolved)
		}
	}
	return out
}

// PermissionFor loads $ATLAS_HOME/permission.json and builds the user-space
// permission engine.
//
// When config.json has no permission block, the fallback mode is
// bypassPermissions so existing setups keep an unprompted main conversation
// while still enforcing any deny or ask rules written in permission.json. A
// broken permission.json produces a warning here and a fail-closed store inside
// the engine.
//
// session is the model the conversation started on. It is what "auto" classifies
// with when the permission block names no classifier of its own, so the default
// judge is a model the user already chose rather than one invented here.
//
// override is -permission: a nonempty value replaces the configured mode for this
// run. The classifier is still built from config.json, because "which mode" and
// "which model judges" are different questions and only the first one is per run.
func PermissionFor(cfg *config.Config, sandboxActive bool, session config.Selection, override string) (*permission.Engine, string) {
	var (
		store *permission.Store
		note  string
	)
	if home, err := config.DefaultHome(); err == nil && home != "" {
		loaded, loadErr := permission.LoadFile(filepath.Join(home, permission.FileName))
		store = loaded
		if loadErr != nil {
			note = loadErr.Error()
		}
	}
	mode := permission.ModeBypass
	autoAllow := false
	if cfg != nil && cfg.Permission != nil {
		mode = cfg.Permission.EffectiveMode()
		autoAllow = cfg.Permission.AutoAllowBashIfSandboxed
	}
	if override != "" {
		mode = override
	}
	classifier, classifyNote := classifierFor(cfg, mode, session)
	if classifyNote != "" {
		if note != "" {
			note += "; "
		}
		note += classifyNote
	}
	return permission.New(permission.Options{
		Mode:                     mode,
		Store:                    store,
		SandboxEnabled:           sandboxActive,
		AutoAllowBashIfSandboxed: autoAllow,
		Classifier:               classifier,
	}), note
}

// classifierFor builds the model that answers "auto" asks.
//
// The resolution order is config.json's own order: permission.classifier when it
// names a model, otherwise the model this run started on. Both are in the config
// the user wrote, which is the point — §8.1 of permissions.md asked which model
// should judge the one that proposes the call, and the answer here is "the one it
// was told to use, named separately so it can be a different one".
//
// Two cases return no classifier and are not errors. Any mode but auto never asks
// the classifier at all. And a model that cannot be built — an unknown name, a
// missing credential, an API this binary does not speak — leaves the engine with
// no classifier, which makes every auto ask go to the human. That is the
// fail-closed reading: a broken judge costs a prompt, never a silent approval.
//
// A config with no permission block still gets a classifier: -permission auto is
// the user asking for auto, and "no key written" is the same empty value that
// falls back to the starting model. Returning nil there would turn the flag into
// a switch that quietly does nothing, which is the failure mode this package
// spends its comments avoiding.
func classifierFor(cfg *config.Config, mode string, session config.Selection) (permission.Classifier, string) {
	if cfg == nil || mode != permission.ModeAuto {
		return nil, ""
	}
	selection := session
	if cfg.Permission != nil {
		if name := cfg.Permission.Classifier; name != "" {
			resolved, err := cfg.Resolve(name)
			if err != nil {
				return nil, fmt.Sprintf("permission.classifier %q cannot be used (%v); auto will ask instead of classifying", name, err)
			}
			selection = resolved
		}
	}
	model, adapter, err := Build(selection)
	if err != nil {
		return nil, fmt.Sprintf("permission.classifier cannot be built (%v); auto will ask instead of classifying", err)
	}
	return &classify.Classifier{
		Provider: adapter,
		Model:    model,
		Options:  provider.Options{ThinkingLevel: ""},
	}, ""
}

// TaskWiring is the background-command capability, or nothing.
//
// It is a struct rather than two parameters because the two halves are never
// supplied apart: a service with no tools records commands nothing can list, and
// tools with no service list an empty file forever. Both are nil on a platform
// without a detached runner, which is the signal the set builder reads.
type TaskWiring struct {
	Service *task.Service
	Tools   []tool.Tool
}

// GitTools are the read-only git tools for one directory. They are per directory
// like the file tools: the repository they read is the one the session is in, and
// /resume can move that. Building them is also what decides once whether this
// machine has git at all — a machine without it registers none of them.
func GitTools(cwd string) []tool.Tool {
	return git.Tools(git.Options{Cwd: cwd})
}

// ToolSet is the built-in set as the app asks for it: the file tools, bash,
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
func ToolSet(cfg *config.Config, webSet []tool.Tool, mcpTools func() []tool.Tool, gitTools func(cwd string) []tool.Tool, spill *tool.Spill, wiring TaskWiring) app.ToolSet {
	var block *config.Sandbox
	if cfg != nil {
		block = cfg.Sandbox
	}
	return func(cwd string) []tool.Tool {
		set := fs.Tools(fs.Options{Cwd: cwd})
		options := shell.Options{Cwd: cwd, Sandbox: narrow(confine(block, cwd)), Spill: spill}
		if wiring.Service != nil {
			options.Background = wiring.Service
		}
		set = append(set, shell.Tools(options)...)
		set = append(set, gitTools(cwd)...)
		set = append(set, wiring.Tools...)
		set = append(set, webSet...)
		if mcpTools == nil {
			return set
		}
		return append(set, mcpTools()...)
	}
}
