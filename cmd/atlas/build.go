package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"pi-go/internal/app"
	"pi-go/internal/classify"
	"pi-go/internal/config"
	"pi-go/internal/permission"
	"pi-go/internal/provider"
	"pi-go/internal/provider/anthropic"
	"pi-go/internal/provider/google"
	"pi-go/internal/provider/openai"
	"pi-go/internal/provider/responses"
	"pi-go/internal/sandbox"
	"pi-go/internal/tool"
	"pi-go/internal/tool/shell"
	"pi-go/internal/tool/web"
	"pi-go/internal/tui"
	"pi-go/internal/wire"
)

// build turns one config selection into the descriptor the kernel records and
// the adapter that streams it.
//
// Both halves come from one place on purpose: a session stores provider and
// model names, and a replay rebuilds the descriptor from them. If the initial
// model and the resolved model were built by different code they would drift,
// and a resumed session would send a different request than the one it
// recorded.
func build(selection config.Selection) (wire.Model, provider.Provider, error) {
	adapter, err := adapterOf(selection)
	if err != nil {
		return wire.Model{}, nil, err
	}
	return modelOf(selection), adapter, nil
}

// modelOf converts a declaration into the kernel descriptor.
//
// BaseURL is folded to the effective endpoint: the model override wins, the
// provider root fills the rest. The descriptor is what lands in assistant
// messages, so it records the endpoint that was actually called rather than the
// one that would have been the default.
//
// The numbers come from factsOf, which is what lets a config declare a model by
// id alone: a window it did not write is the vendor's documented window, and a
// window of zero would turn automatic compaction off without saying so.
func modelOf(selection config.Selection) wire.Model {
	declared := selection.Model
	facts := factsOf(selection)
	return wire.Model{
		ID:            declared.ID,
		Name:          facts.name,
		API:           selection.Provider.API,
		Provider:      selection.ProviderName,
		BaseURL:       effectiveBaseURL(selection),
		Reasoning:     declared.Reasoning,
		ContextWindow: facts.contextWindow,
		MaxTokens:     facts.maxTokens,
		Input:         append([]string(nil), facts.input...),
	}
}

func effectiveBaseURL(selection config.Selection) string {
	if selection.Model.BaseURL != "" {
		return selection.Model.BaseURL
	}
	return selection.Provider.BaseURL
}

// adapterOf builds the wire adapter and attaches the credential.
//
// The key is attached here, not in the kernel: the agent builds provider.Options
// with only the thinking level, which keeps a secret out of the transcript and
// out of every session file. Wrapping the adapter is the only place that can
// see both the key and the request.
func adapterOf(selection config.Selection) (provider.Provider, error) {
	baseURL := effectiveBaseURL(selection)
	key := selection.Provider.Key()
	compat := selection.Provider.Compat

	switch selection.Provider.API {
	case openai.APIName:
		adapter := openai.New(openai.Compat{
			BaseURL:                         baseURL,
			MaxTokensField:                  compat.MaxTokensField,
			RequireReasoningContent:         compat.RequireReasoningContent,
			DisableStreamOptions:            compat.DisableStreamOptions,
			TolerateMissingFinishReason:     compat.TolerateMissingFinishReason,
			RequireToolResultName:           compat.RequireToolResultName,
			RequireAssistantAfterToolResult: compat.RequireAssistantAfterToolResult,
			ThinkingDialect:                 compat.ThinkingDialect,
			DisableReasoningEffort:          compat.DisableReasoningEffort,
			ToolChoice:                      compat.ToolChoice,
			SendStore:                       compat.SendStore,
		})
		return provider.WithAPIKey(adapter, key), nil
	case anthropic.APIName:
		adapter := anthropic.New(anthropic.Compat{
			BaseURL:          baseURL,
			Version:          compat.Version,
			ThinkingDialect:  compat.ThinkingDialect,
			AdaptiveThinking: compat.AdaptiveThinking,
			StrictTools:      compat.StrictTools,
			ToolChoice:       compat.ToolChoice,
		})
		return provider.WithAPIKey(adapter, key), nil
	case responses.APIName:
		adapter := responses.New(responses.Compat{
			BaseURL:     baseURL,
			ToolChoice:  compat.ToolChoice,
			ServiceTier: compat.ServiceTier,
			Verbosity:   compat.Verbosity,
			Truncation:  compat.Truncation,
		})
		return provider.WithAPIKey(adapter, key), nil
	case google.APIName:
		adapter := google.New(google.Compat{
			BaseURL:         baseURL,
			ThinkingDialect: compat.ThinkingDialect,
			ToolChoice:      compat.ToolChoice,
		})
		return provider.WithAPIKey(adapter, key), nil
	}
	return nil, fmt.Errorf("provider %q declares api %q; this binary speaks %s, %s, %s and %s",
		selection.ProviderName, selection.Provider.API,
		openai.APIName, anthropic.APIName, responses.APIName, google.APIName)
}

// resolverFor builds the function the kernel calls when a session records a
// model this process did not start with, and on every /model switch.
func resolverFor(cfg *config.Config) app.Resolver {
	return func(providerName, modelID string) (wire.Model, provider.Provider, error) {
		selection, err := cfg.Lookup(providerName, modelID)
		if err != nil {
			return wire.Model{}, nil, err
		}
		return build(selection)
	}
}

// credentialNotice explains a missing credential instead of failing on it: a
// local endpoint needs no key, and the error that arrives from the first
// request would name the wrong problem.
func credentialNotice(selection config.Selection) string {
	if selection.Provider.Key() != "" {
		return ""
	}
	hint := "set apiKey"
	if selection.Provider.APIKeyEnv != "" {
		hint = "export " + selection.Provider.APIKeyEnv + " or set apiKey"
	}
	return fmt.Sprintf("provider %q has no credential; %s under providers.%s", selection.ProviderName, hint, selection.ProviderName)
}

// webTools builds the search and fetch tools from the web block.
//
// A missing credential is a warning, not an error. The two tools are an
// addition to a conversation, not a condition for having one, and a config
// shared between machines where only one of them exports the key should still
// start. Saying it once at startup is the honest form: the tools then do not
// exist for this session, so every later turn agrees with the warning instead
// of discovering it through calls that were never on the tool list.
func webTools(block *config.Web, spill *tool.Spill) ([]tool.Tool, string) {
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

// sandboxNote says whether the configured sandbox is in effect here, and what to
// warn about it.
//
// An unavailable executor is a warning here and an error on the command. Saying
// it once at startup is the honest form, and reporting no sandbox instead would
// turn one that cannot be applied into a silent grant of the full permissions,
// which is the one failure a sandbox must not have.
//
// The confinement itself comes from confine, which toolSet calls again for every
// directory a session may belong to. This function is only about the warning: the
// executor is a fact about this machine, so it does not change with the
// workspace, and it is asked without building a policy that nothing would use.
func sandboxNote(block *config.Sandbox) (bool, string) {
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
		if resolved, err := resolveDir(strings.TrimSpace(raw)); err == nil && resolved != "" {
			out = append(out, resolved)
		}
	}
	return out
}

// permissionFor loads $ATLAS_HOME/permission.json and builds the user-space
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
func permissionFor(cfg *config.Config, sandboxActive bool, session config.Selection, override string) (*permission.Engine, string) {
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
	model, adapter, err := build(selection)
	if err != nil {
		return nil, fmt.Sprintf("permission.classifier cannot be built (%v); auto will ask instead of classifying", err)
	}
	return &classify.Classifier{
		Provider: adapter,
		Model:    model,
		Options:  provider.Options{ThinkingLevel: ""},
	}, ""
}

// writeModelList prints every declared model. The default is marked because
// "which model will atlas use" is the question a list answers.
func writeModelList(out io.Writer, cfg *config.Config) error {
	defaultName := ""
	if selection, err := cfg.Resolve(""); err == nil {
		defaultName = selection.Name()
	}

	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "MODEL\tAPI\tCONTEXT\tMAX TOKENS\tENDPOINT"); err != nil {
		return err
	}
	for _, selection := range cfg.Selections() {
		facts := factsOf(selection)
		name := facts.name
		if selection.Name() == defaultName {
			name += " (default)"
		}
		endpoint := effectiveBaseURL(selection)
		if endpoint == "" {
			endpoint = "(adapter default)"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			name, selection.Provider.API, countOrDash(facts.contextWindow), countOrDash(facts.maxTokens), endpoint); err != nil {
			return err
		}
	}
	return writer.Flush()
}

// modelChoices is writeModelList as rows. It lives beside the table on purpose:
// "which models exist" has to have one answer, and a menu that offered a model
// the printed list did not would be that answer twice.
//
// Each row also carries the thinking levels that model declares, because that
// set is per model: a menu that offered the same seven positions on every row
// would let a reader pick one the request cannot be made in. level is the one in
// effect, which decides where each row's axis opens.
func modelChoices(cfg *config.Config, level string) []tui.Choice {
	defaultName := ""
	if selection, err := cfg.Resolve(""); err == nil {
		defaultName = selection.Name()
	}
	selections := cfg.Selections()
	out := make([]tui.Choice, 0, len(selections))
	for _, selection := range selections {
		facts := factsOf(selection)
		label := facts.name
		if selection.Name() == defaultName {
			label += " (default)"
		}
		note := selection.Provider.API
		if facts.contextWindow > 0 {
			// The budget is the one column of the printed table that changes a
			// decision: two models that look alike otherwise differ in how much
			// context they take.
			note += fmt.Sprintf(" · ctx %d", facts.contextWindow)
		}
		out = append(out, tui.Choice{
			Value: selection.Name(),
			Label: label,
			Note:  note,
			Steps: facts.gears,
			Step:  gearIndex(facts.gears, level),
		})
	}
	return out
}

// countOrDash prints "declared: nothing" as a dash rather than a zero, which
// would read as an actual budget.
func countOrDash(value int) string {
	if value == 0 {
		return "-"
	}
	return fmt.Sprint(value)
}
