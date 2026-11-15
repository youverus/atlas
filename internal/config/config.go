// Package config reads the settings that tell atlas which models exist, which
// endpoint serves each one, and which credential to present. It also reads the
// file that says what the interface looks like — the name of that file comes
// from config.json, its contents do not.
//
// The file is JSON because the whole tree is dependency free and a hand-written
// parser is the only alternative. Two decisions follow from that:
//
//   - Unknown fields are rejected. A misspelled "bseUrl" would otherwise send
//     the request to the vendor default with the wrong key, and the error would
//     name neither the field nor the file.
//   - Nothing is inferred beyond the file. A provider without models, a model
//     without an id, and a default model that names nothing are all errors, not
//     silent fallbacks.
//
// This package is pure: no network, no hidden state, and the only write it ever
// performs is the first-run template in home.go.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
)

// The two thinking dialects the google adapter knows, spelled the way the
// adapter names its fields. They live here as well because a config value is
// validated before any adapter sees it.
const (
	dialectThinkingLevel  = "level"
	dialectThinkingBudget = "budget"
)

// FileName is the config file inside the atlas home directory.
const FileName = "config.json"

// ExampleName is the annotated sample written next to FileName on a first run.
// It is never read by this package; it exists so the format is discoverable
// without the documentation.
const ExampleName = "config.example.json"

// TUIFileName is the interface file the template names. It is only a default
// spelling: "tui" in config.json is a path, and nothing here assumes it.
const TUIFileName = "tui.json"

// maxSize bounds the file. A config this large is a mistake, and refusing early
// keeps a decode from streaming an unbounded file into memory.
const maxSize = 1 << 20

// LevelOff is the switch that turns thinking off. It is not a gear: a model
// offers it whether or not it names it, because "send nothing" is always
// expressible.
const LevelOff = "off"

// ThinkingLevels are the levels the protocol layer accepts: off, which is a
// switch rather than an effort, plus six effort gears. Every adapter maps this
// one vocabulary onto what its endpoint can express, so the name is never
// clamped here — a vendor with fewer gears clamps in its own adapter. The copy
// in internal/rpc validates client requests; keeping both spelled the same way
// is what stops a config value from reaching a request that the RPC layer would
// have rejected.
var ThinkingLevels = []string{LevelOff, "low", "medium", "high", "xhigh", "max", "ultra"}

// Config is the whole file.
type Config struct {
	// DefaultModel selects the model used when neither a flag nor the session
	// asks for one. It is "provider/model", or a bare model id that exactly one
	// provider declares.
	DefaultModel string `json:"defaultModel"`

	// ThinkingLevel is the initial level. Empty means the kernel default.
	ThinkingLevel string `json:"thinkingLevel"`

	// SessionDir stores session files. Empty means the atlas default; "~" is
	// expanded.
	SessionDir string `json:"sessionDir"`

	// TUI names the interface's own file. It is a path rather than a block in
	// this one because the appearance is edited far more often than the models
	// are, and because a mistyped color should not be able to break the file
	// that says which endpoint answers. A relative name is relative to this
	// file, not to the working directory.
	//
	// It is a pointer because "no appearance file" and "a name that happens to
	// be empty" are two different statements: omitting the key means the
	// built-in look, while an empty value is a path someone deleted and has to
	// be reported. A plain string cannot tell them apart, and the silent
	// version of that is a reader editing a name that nothing ever reads.
	TUI *string `json:"tui"`

	// Providers maps a provider name to one endpoint and the models it serves.
	// The name is what a session file records, so it must stay stable.
	Providers map[string]Provider `json:"providers"`

	// Web configures the search and fetch tools. It is a block of its own
	// rather than a field of a provider because the credential is not the
	// model's: a session answered by Anthropic still searches through one
	// search endpoint, and tying the key to whoever happens to be answering
	// would mean the tools disappear when the model is switched.
	//
	// A pointer because "no web tools" and "web tools with nothing configured"
	// are different statements. Omitting the block is the first; an empty block
	// is a half-finished edit, and it is reported rather than guessed at.
	Web *Web `json:"web"`

	// Sandbox confines the processes the bash tool starts. A pointer because
	// "no sandbox" and "a sandbox with nothing set" are different statements:
	// omitting the block runs commands with the process's own permissions,
	// which is what every version before this one did, while an empty block is
	// a reader who turned it on and expects confinement.
	Sandbox *Sandbox `json:"sandbox"`

	// Permission configures the user-space approval gate. Omitting the block
	// keeps bypassPermissions as the fallback posture while still enforcing any
	// rules in permission.json; writing the block turns on "default" unless
	// Mode names "bypassPermissions".
	Permission *Permission `json:"permission"`

	// Goal configures /goal workflows. A pointer because "no goal settings"
	// and "an empty goal block" are different statements: omitting the block
	// runs goals with no budget, while an empty block is a reader who turned
	// it on and expects the defaults.
	Goal *Goal `json:"goal"`

	// UI is the contents of the file TUI names, filled by Load. It carries no
	// JSON tags of its own: config.json cannot set these fields, so a reader
	// looking for a color or a gap has exactly one place to look.
	UI UI `json:"-"`
}

// UI is the interactive interface, as tui.json spells it.
//
// It is a file of its own because it is the one part of the settings a request
// never depends on: a reader tuning the interface should not have to scroll
// past providers, and a broken color must not be able to change which model
// answers. The split costs one indirection — config.json names this file — and
// buys the separation, which is what makes an appearance mistake a one-line
// fix instead of a hunt.
type UI struct {
	// Theme names the palette the interface draws with. Empty and "auto" both
	// mean "pick by the terminal's background". The name is looked up when the
	// interface starts rather than here, because the themes live in files this
	// package does not read: a name that resolves to nothing is reported by
	// whoever owns the lookup, with the list of themes it found.
	Theme string `json:"theme"`

	// Colors overrides individual palette tokens on top of the theme. The
	// theme stays the whole answer to "what does atlas look like"; this is
	// for the two or three tokens a reader wants different, so changing an
	// accent does not mean maintaining a palette file.
	//
	// The names and the hex values are checked where the theme is, when the
	// interface starts, for the same reason Theme is: the vocabulary belongs
	// to internal/theme, and this package depends on nothing.
	Colors map[string]string `json:"colors"`

	// Layout is embedded rather than named "layout": the file writes blocks
	// and roles beside theme and colors, while the Go side keeps one value for
	// "the geometry as the file spells it" — the type the merge in cmd/atlas
	// walks through.
	Layout
}

// validate checks the appearance settings that need no outside vocabulary: the
// theme is a name here and a lookup later, so only its shape is checked, while
// the geometry is nothing but numbers and can be checked whole.
func (u UI) validate() error {
	if u.Theme != "" && (u.Theme == "." || u.Theme == ".." || strings.ContainsAny(u.Theme, `/\`)) {
		return fmt.Errorf("theme %q is a name, not a path; a theme file is found by name in the themes directories", u.Theme)
	}
	return u.Layout.validate()
}

// Layout is the interface's geometry as the file spells it, named by the block
// it belongs to.
//
// The names are blocks because that is what a reader is looking at when they
// decide a number is wrong: "the input area is too tall" and "there is no air
// under the answer" are two different problems, and a flat list of ten knobs
// made the reader work out which one they had before they could change it.
//
// Every field is still a pointer, for the reason it always was: zero is a real
// setting for most of these — no gap above the prompt, no reasoning shown —
// while an absent field has to mean "whatever the interface defaults to", and a
// plain int cannot tell "gapAbove": 0 from no gapAbove at all.
type Layout struct {
	// Blocks are the fixed regions of the screen; Roles are the kinds of block
	// drawn inside the transcript. They are separate because a region takes
	// rows away from the screen while a role only decides how one piece of
	// prose looks inside the rows the region already has.
	Blocks Blocks `json:"blocks"`
	Roles  Roles  `json:"roles"`
}

// Blocks are the regions of the screen, in the order they stack.
//
// A region is a piece of real estate: it has a height, a width, and the blank
// rows around it. The transcript is the one region with no number of its own —
// it gets what the others leave — which is why its keys are about the blocks
// inside it rather than about its own extent.
type Blocks struct {
	Transcript TranscriptBlock `json:"transcript"`
	Composer   ComposerBlock   `json:"composer"`
	Palette    PaletteBlock    `json:"palette"`
	Status     StatusBlock     `json:"status"`
	Menu       MenuBlock       `json:"menu"`
	Approval   ApprovalBlock   `json:"approval"`
	Welcome    WelcomeBlock    `json:"welcome"`
}

// TranscriptBlock is the conversation area.
type TranscriptBlock struct {
	// Gap is the blank rows between two transcript blocks that are separated
	// at all. Zero makes a transcript one solid column; the separation itself
	// is not a setting, because it is what tells prose from a tool call.
	// Default 1.
	Gap *int `json:"gap"`

	// Frames boxes a stretch of reasoning and a stretch of calls, and titles
	// each box with what the stretch was. Off draws the conversation the way
	// 0.4.17 did, one line per block.
	//
	// It is a block setting rather than a theme one for the reason the
	// composer's frame is: a rule above and a rule below are two rows taken
	// from the conversation, so the amount of history on screen depends on it.
	// Default true.
	Frames *bool `json:"frames"`
}

// ComposerBlock is the input area under the conversation.
type ComposerBlock struct {
	// GapAbove is the blank rows between the conversation and the input area.
	// Default 1.
	GapAbove *int `json:"gapAbove"`

	// Rows bounds how tall the input line may grow. Past it the transcript
	// disappears while the reader types. Default 8.
	Rows *int `json:"rows"`

	// Frame draws the input area inside a rounded box on the raised
	// background, which is what separates the prompt from the conversation
	// without a gap. Default true.
	//
	// It is a block setting rather than a theme one because the border is
	// geometry: it costs two rows and four cells, so the transcript's height
	// and the width the prompt wraps at both follow from it.
	Frame *bool `json:"frame"`
}

// PaletteBlock is the command palette that opens above the prompt.
type PaletteBlock struct {
	// Rows bounds the palette. Zero hides it. Default 5.
	Rows *int `json:"rows"`
}

// StatusBlock is the bar under the prompt.
type StatusBlock struct {
	// GapAbove is the blank rows between the input area and the status bar.
	// Default 0, which is where the bar has always sat.
	GapAbove *int `json:"gapAbove"`
}

// MenuBlock is the box a pick menu draws in.
type MenuBlock struct {
	// Rows bounds the list inside the box, which scrolls past it. Default 10.
	Rows *int `json:"rows"`

	// Width bounds the box, borders included. Default 72.
	Width *int `json:"width"`
}

// ApprovalBlock is the box an approval question draws in.
type ApprovalBlock struct {
	// Width bounds the box, borders included. Default 64, narrower than a menu
	// because a question is one sentence and a list carries three columns.
	Width *int `json:"width"`
}

// WelcomeBlock is the page an empty conversation shows.
type WelcomeBlock struct {
	// On draws it. Off leaves that screen blank, which is what a reader who
	// wants the prompt at the top of an empty terminal asks for. Default true.
	On *bool `json:"on"`
}

// Roles are the kinds of block drawn inside the transcript.
type Roles struct {
	Thinking ThinkingRole `json:"thinking"`
}

// ThinkingRole is the reasoning block.
type ThinkingRole struct {
	// Rows is how much reasoning is shown: the tail, never the whole stream.
	// Zero leaves the header and drops the body. Default 8.
	Rows *int `json:"rows"`
}

// The plain types are the blocks without their UnmarshalJSON method. A block
// cannot be decoded into itself from its own UnmarshalJSON — the method would
// be found again and the decode would recurse until the stack ends — so each
// method decodes the alias and converts back. Named after the type they shadow
// because that is the whole of their meaning.
type (
	plainTranscript TranscriptBlock
	plainComposer   ComposerBlock
	plainPalette    PaletteBlock
	plainStatus     StatusBlock
	plainMenu       MenuBlock
	plainApproval   ApprovalBlock
	plainWelcome    WelcomeBlock
	plainThinking   ThinkingRole
)

// decodeBlock decodes one block of the geometry and names it in any error.
//
// encoding/json reports an unknown field by its bare name, so a typo inside
// the composer arrives as `unknown field "gap"` with nothing saying which
// block it was in — and "rows" is a key of four of them. Prefixing the path
// here is what keeps the error pointing at the place to edit, which is the
// whole reason the file is grouped by block in the first place.
//
// Strictness is repeated per block rather than inherited: once a block decodes
// itself, the outer decoder no longer looks inside it.
func decodeBlock[T any](name string, data []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

func (b *TranscriptBlock) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainTranscript]("blocks.transcript", data)
	*b = TranscriptBlock(value)
	return err
}

func (b *ComposerBlock) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainComposer]("blocks.composer", data)
	*b = ComposerBlock(value)
	return err
}

func (b *PaletteBlock) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainPalette]("blocks.palette", data)
	*b = PaletteBlock(value)
	return err
}

func (b *StatusBlock) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainStatus]("blocks.status", data)
	*b = StatusBlock(value)
	return err
}

func (b *MenuBlock) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainMenu]("blocks.menu", data)
	*b = MenuBlock(value)
	return err
}

func (b *ApprovalBlock) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainApproval]("blocks.approval", data)
	*b = ApprovalBlock(value)
	return err
}

func (b *WelcomeBlock) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainWelcome]("blocks.welcome", data)
	*b = WelcomeBlock(value)
	return err
}

func (r *ThinkingRole) UnmarshalJSON(data []byte) error {
	value, err := decodeBlock[plainThinking]("roles.thinking", data)
	*r = ThinkingRole(value)
	return err
}

// Web is the search and fetch endpoint the two web tools call.
//
// It is not a provider because nothing about it is model-shaped: it has no
// models, no adapter, and no context window, and the only thing it shares with
// a provider is that both need a credential. Keeping it separate is what lets
// the tools outlive a model switch — the key that reads a page has nothing to
// do with the key that answers the prompt.
type Web struct {
	// BaseURL is the origin, with no path. Empty means the built-in vendor
	// origin. A value carrying /v1 is refused rather than tolerated: the two
	// tool paths already start there, and doubling the prefix would fail with
	// a 404 that names nothing.
	BaseURL string `json:"baseUrl"`

	// APIKey is the credential itself. Prefer APIKeyEnv for the same reason a
	// provider does: a config file is a worse place for a secret.
	APIKey string `json:"apiKey"`

	// APIKeyEnv names an environment variable that overrides APIKey when it is
	// set and non-empty.
	APIKeyEnv string `json:"apiKeyEnv"`
}

// Key returns the credential to present. An environment variable named by
// APIKeyEnv wins over the literal APIKey, so a shell can swap credentials
// without editing a file that may be shared.
func (w Web) Key() string { return envKey(w.APIKey, w.APIKeyEnv) }

func (w Web) validate() error {
	if err := validateBaseURL(w.BaseURL); err != nil {
		return fmt.Errorf("baseUrl: %w", err)
	}
	if w.BaseURL == "" {
		return nil
	}
	parsed, err := url.Parse(w.BaseURL)
	if err != nil {
		return fmt.Errorf("baseUrl: %w", err)
	}
	if path := strings.Trim(parsed.Path, "/"); path != "" {
		return fmt.Errorf("baseUrl %q must be an origin with no path, because the tool endpoints are /v1/tools/search and /v1/tools/fetch", w.BaseURL)
	}
	return nil
}

// Sandbox confines the process tree the bash tool starts.
//
// It is not a permission system. atlas has no approval step for bash, so this
// block is the only thing between a command the model wrote and the rest of the
// machine, and it is enforced by the kernel — which is what makes it hold for
// programs that never heard of atlas. What it does not cover is atlas's own write
// and edit tools, which run inside this process and are bounded by the tool's
// path rules instead.
type Sandbox struct {
	// Network allows a confined command to reach the network. A pointer because
	// the useful default is "yes": a build that cannot fetch a module fails in a
	// way that reads as a broken toolchain, and the network is not what this
	// version of the sandbox is about. `"network": false` is the deliberate
	// offline mode.
	Network *bool `json:"network"`

	// DenyRead names extra paths a confined command may not read, on top of the
	// default credential directories (~/.ssh, ~/.aws, ~/.kube, ~/.gnupg). "~" is
	// expanded against the user's home directory.
	DenyRead []string `json:"denyRead"`
}

// AllowsNetwork reports the network setting, defaulting to allowed.
func (s Sandbox) AllowsNetwork() bool { return s.Network == nil || *s.Network }

func (s Sandbox) validate() error {
	for i, path := range s.DenyRead {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("denyRead[%d]: path is empty", i)
		}
	}
	return nil
}

// Permission configures the user-space approval gate that runs before a tool
// call executes.
type Permission struct {
	// Mode is one of the four postures. "default" asks on mutating tools and a
	// human answers; "auto" answers those rule-free asks with Classifier;
	// "bypassPermissions" waves the fallback ask through while keeping explicit
	// deny and ask rules in force; "off" disables the gate entirely. Empty means
	// "default".
	Mode string `json:"mode"`

	// AutoAllowBashIfSandboxed skips whole-tool bash ask prompts when the
	// kernel sandbox is active. Content-specific ask rules and deny rules remain
	// in force.
	AutoAllowBashIfSandboxed bool `json:"autoAllowBashIfSandboxed"`

	// Classifier names the model that answers rule-free asks in "auto",
	// written as "provider/model". Empty uses the model this run started on,
	// so the classifier is never a service the user did not already configure.
	//
	// It is a separate key on purpose: approving a tool call is a different job
	// from doing the work, and a user may want a smaller or a stricter model for
	// it. Leaving it empty is the convenience default, not the recommendation.
	Classifier string `json:"classifier"`
}

// PermissionModes are the four postures, in the order the interface lists them:
// the two that ask first, then the two that do not.
var PermissionModes = []string{"default", "auto", "bypassPermissions", "off"}

// EffectiveMode returns Mode, defaulting to "default" when the permission block
// is present with an empty mode.
func (p Permission) EffectiveMode() string {
	if p.Mode == "" {
		return "default"
	}
	return p.Mode
}

func (p Permission) validate() error {
	if p.Mode != "" {
		known := false
		for _, mode := range PermissionModes {
			if p.Mode == mode {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("mode %q is not one of: %s", p.Mode, strings.Join(PermissionModes, ", "))
		}
	}
	if p.Classifier != "" {
		if !strings.Contains(p.Classifier, "/") {
			return fmt.Errorf("classifier %q must be written as provider/model", p.Classifier)
		}
	}
	return nil
}

// Goal configures the /goal workflow.
type Goal struct {
	// TokenBudget caps the tokens one goal may spend across its turns. 0 means
	// no budget: the goal runs until it finishes or is paused.
	TokenBudget int `json:"tokenBudget"`
}

func (g Goal) validate() error {
	if g.TokenBudget < 0 {
		return fmt.Errorf("tokenBudget %d is negative; 0 means no budget", g.TokenBudget)
	}
	return nil
}

// Provider is one endpoint and its models. The name it is keyed by is the
// provider half of "provider/model".
type Provider struct {
	// API selects the wire adapter: "openai-completions", "anthropic-messages",
	// or "openai-responses".
	API string `json:"api"`

	// BaseURL is the endpoint root. Empty uses the adapter default, which is
	// the vendor's own API.
	BaseURL string `json:"baseUrl"`

	// APIKey is the credential itself. Prefer APIKeyEnv: a config file is a
	// worse place for a secret than the environment.
	APIKey string `json:"apiKey"`

	// APIKeyEnv names an environment variable that overrides APIKey when it is
	// set and non-empty.
	APIKeyEnv string `json:"apiKeyEnv"`

	// Models declares what this endpoint serves. At least one is required: a
	// provider that serves nothing is a typo, not a configuration.
	Models []Model `json:"models"`

	// Compat carries the per-endpoint protocol switches. The zero value is the
	// right default for the vendor's own API; everything else exists because
	// some compatible server rejects or requires the opposite.
	Compat Compat `json:"compat"`
}

// Compat holds the adapter workarounds. One struct serves both adapters
// because a field each adapter does not read is ignored, and splitting it
// would make a config that switches api keep the wrong half.
type Compat struct {
	// MaxTokensField is "max_tokens" or "max_completion_tokens". Empty picks by
	// host, because api.openai.com rejects max_tokens on reasoning models while
	// most compatible servers never learned max_completion_tokens.
	MaxTokensField string `json:"maxTokensField"`

	// RequireReasoningContent always writes reasoning_content on assistant
	// messages. DeepSeek needs it once a conversation contains reasoning turns.
	RequireReasoningContent bool `json:"requireReasoningContent"`

	// DisableStreamOptions stops sending stream_options.include_usage, which
	// some gateways reject instead of ignoring.
	DisableStreamOptions bool `json:"disableStreamOptions"`

	// TolerateMissingFinishReason accepts a stream that closes without a
	// finish_reason. Off by default: a truncated stream must fail loudly.
	TolerateMissingFinishReason bool `json:"tolerateMissingFinishReason"`

	// RequireToolResultName writes name on tool messages, which a few vendors
	// demand.
	RequireToolResultName bool `json:"requireToolResultName"`

	// RequireAssistantAfterToolResult inserts a synthetic assistant turn after
	// tool results, for endpoints that refuse a user message there.
	RequireAssistantAfterToolResult bool `json:"requireAssistantAfterToolResult"`

	// Version is the anthropic-version header. Empty uses the adapter default.
	Version string `json:"version"`

	// ThinkingDialect selects a vendor that reshaped the thinking fields.
	// Empty keeps each adapter's own vocabulary, which is what the vendor
	// defaults are written against; "deepseek" switches both adapters to the
	// fields DeepSeek documents, because the vendor served both wire formats
	// with its own thinking dials instead of the upstream ones.
	ThinkingDialect string `json:"thinkingDialect"`

	// DisableReasoningEffort stops the openai adapter from sending
	// reasoning_effort, which a gateway in front of a reasoning model may
	// reject instead of ignoring.
	DisableReasoningEffort bool `json:"disableReasoningEffort"`

	// SendStore writes "store": false on a chat completions request. Off by
	// default because a proxy may refuse the field; the responses adapter
	// always sends it, since that API requires an answer.
	SendStore bool `json:"sendStore"`

	// ToolChoice is "auto", "none" or "required". Each adapter translates it
	// into its own spelling; empty sends nothing and leaves the vendor default.
	ToolChoice string `json:"toolChoice"`

	// AdaptiveThinking sends the newer thinking form on the anthropic adapter:
	// the model decides how much to think and the level becomes an effort
	// instead of a token budget.
	AdaptiveThinking bool `json:"adaptiveThinking"`

	// StrictTools lets a tool that declares itself strict travel with the
	// provider's schema validation. Opt in: the endpoint has to support
	// constrained decoding, and a rejection costs the whole request.
	StrictTools bool `json:"strictTools"`

	// ServiceTier selects the serving queue on the responses adapter: "auto",
	// "default", "flex" or "priority".
	ServiceTier string `json:"serviceTier"`

	// Verbosity is how much prose a responses answer carries: "low", "medium"
	// or "high".
	Verbosity string `json:"verbosity"`

	// Truncation is "auto" or "disabled": whether a responses request may drop
	// the head of a conversation that outgrew the window.
	Truncation string `json:"truncation"`
}

// Model is one model declaration.
type Model struct {
	// ID is the name sent to the endpoint, for example "deepseek-chat".
	ID string `json:"id"`

	// Name is the display name. Empty falls back to ID.
	Name string `json:"name"`

	// BaseURL overrides the provider endpoint for this model, which some
	// gateways need when one vendor serves model families under different
	// paths.
	BaseURL string `json:"baseUrl"`

	// ContextWindow is the input budget in tokens. Zero disables automatic
	// compaction, because a threshold cannot be computed without it.
	ContextWindow int `json:"contextWindow"`

	// MaxTokens is the output limit. Zero lets the adapter decide.
	MaxTokens int `json:"maxTokens"`

	// Reasoning marks a model that can produce thinking blocks and accepts a
	// thinking budget.
	Reasoning bool `json:"reasoning"`

	// ThinkingLevels narrows the gears this model offers. Empty means every
	// level in ThinkingLevels. Naming them is what keeps the /model axis honest
	// for a vendor that collapses several names onto one effort: a gear that
	// would send the same request as its neighbour is not offered.
	ThinkingLevels []string `json:"thinkingLevels"`

	// Input lists the accepted modalities: "text" and "image". Empty means
	// undeclared, and an adapter then assumes images can travel rather than
	// dropping them silently.
	Input []string `json:"input"`
}

// Gears returns the thinking levels this model offers, in canonical order: off
// plus whichever gears it declares. A reasoning model that declares nothing
// offers every level; a model that cannot think has no axis at all.
func (m Model) Gears() []string {
	if !m.Reasoning {
		return nil
	}
	declared := make(map[string]bool, len(m.ThinkingLevels))
	for _, level := range m.ThinkingLevels {
		declared[level] = true
	}
	gears := make([]string, 0, len(ThinkingLevels))
	for _, level := range ThinkingLevels {
		if len(declared) == 0 || level == LevelOff || declared[level] {
			gears = append(gears, level)
		}
	}
	return gears
}

// AllowsGear reports whether the model accepts level. Off is always accepted:
// turning thinking off is a switch, not a gear, and a model that cannot think
// still has to be told so.
func (m Model) AllowsGear(level string) bool {
	if level == LevelOff {
		return true
	}
	for _, gear := range m.Gears() {
		if gear == level {
			return true
		}
	}
	return false
}

// Load reads and validates one config file.
//
// A missing file is reported as the operating system's error, so a caller can
// test it with errors.Is(err, os.ErrNotExist); the first-run flow depends on
// that.
func Load(path string) (*Config, error) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("config: %s: %d bytes exceeds the %d byte limit", path, info.Size(), maxSize)
	}

	decoder := json.NewDecoder(io.LimitReader(handle, maxSize))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, movedHint(err))
	}
	// A second JSON value in the file means the user pasted two configs or left
	// a stray brace; either way the first object is not what they meant.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %s: trailing data after the config object", path)
	}
	// The appearance file is read here rather than by whoever needs it, so a
	// caller cannot end up holding a Config with the theme of one file and the
	// geometry of another. Its own errors name that file.
	if cfg.TUI != nil {
		tui, err := loadTUI(*cfg.TUI, path)
		if err != nil {
			return nil, err
		}
		cfg.UI = tui
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return &cfg, nil
}

// movedHint answers the one question a reader upgrading from 0.4.10 asks: the
// appearance keys used to live here, and "unknown field" alone does not say
// where they went.
func movedHint(err error) error {
	text := err.Error()
	if strings.Contains(text, `unknown field "theme"`) || strings.Contains(text, `unknown field "ui"`) {
		return fmt.Errorf("%w (theme, colors, blocks and roles live in the file that \"tui\" names)", err)
	}
	return err
}

// Validate checks everything a request would depend on. Provider names are
// visited in sorted order so the same broken file always reports the same
// first problem.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("no config")
	}
	if len(c.Providers) == 0 {
		return errors.New("providers is empty: declare at least one provider and model")
	}
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		// A name goes into the session file and into "provider/model", so it
		// has to survive that round trip.
		if strings.TrimSpace(name) == "" {
			return errors.New("a provider name is empty")
		}
		if strings.Contains(name, "/") {
			return fmt.Errorf("provider name %q contains \"/\", which would make \"provider/model\" ambiguous", name)
		}
		if err := c.Providers[name].validate(); err != nil {
			return fmt.Errorf("providers.%s: %w", name, err)
		}
	}
	if c.ThinkingLevel != "" && !ValidThinkingLevel(c.ThinkingLevel) {
		return fmt.Errorf("thinkingLevel %q is not one of: %s", c.ThinkingLevel, strings.Join(ThinkingLevels, ", "))
	}
	if c.Web != nil {
		if err := c.Web.validate(); err != nil {
			return fmt.Errorf("web: %w", err)
		}
	}
	if c.Sandbox != nil {
		if err := c.Sandbox.validate(); err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
	}
	if c.Permission != nil {
		if err := c.Permission.validate(); err != nil {
			return fmt.Errorf("permission: %w", err)
		}
	}
	if c.Goal != nil {
		if err := c.Goal.validate(); err != nil {
			return fmt.Errorf("goal: %w", err)
		}
	}
	// The appearance is checked as one thing: the theme's shape and the
	// geometry's ranges. Colors are not checked here, because what a token is
	// called belongs to internal/theme; they fail when the interface starts,
	// which is also before the first frame is drawn.
	if err := c.UI.validate(); err != nil {
		return err
	}
	if c.DefaultModel != "" {
		selection, err := c.Resolve(c.DefaultModel)
		if err != nil {
			return fmt.Errorf("defaultModel: %w", err)
		}
		// Only a model that names its gears constrains the default: an
		// undeclared model takes every level, so there is nothing to check.
		if len(selection.Model.ThinkingLevels) > 0 && c.ThinkingLevel != "" && !selection.Model.AllowsGear(c.ThinkingLevel) {
			return fmt.Errorf("thinkingLevel %q is not offered by %s (offers: %s)", c.ThinkingLevel, selection.Name(), strings.Join(selection.Model.Gears(), ", "))
		}
	}
	return nil
}

// validate is the range check on the geometry.
//
// The ranges are taste, not arithmetic: internal/tui clamps whatever it is
// handed, so a number outside these cannot draw a broken frame — it just draws
// one nobody meant. Each is a count of rows or cells, so the low end is where
// the element stops being usable and the high end is where it stops being part
// of the interface and starts covering it. Reporting them here rather than
// there is what lets the error name the path the reader wrote, which since
// 0.4.13 is the block the number belongs to: blocks.composer.gapAbove, not a
// bare "composerGap" that could have been anywhere.
func (l Layout) validate() error {
	numbers := []struct {
		field     string
		value     *int
		low, high int
	}{
		{"blocks.transcript.gap", l.Blocks.Transcript.Gap, 0, 4},
		{"blocks.composer.gapAbove", l.Blocks.Composer.GapAbove, 0, 8},
		{"blocks.composer.rows", l.Blocks.Composer.Rows, 1, 24},
		{"blocks.palette.rows", l.Blocks.Palette.Rows, 0, 20},
		{"blocks.status.gapAbove", l.Blocks.Status.GapAbove, 0, 4},
		{"blocks.menu.rows", l.Blocks.Menu.Rows, 1, 40},
		{"blocks.menu.width", l.Blocks.Menu.Width, 20, 200},
		{"blocks.approval.width", l.Blocks.Approval.Width, 20, 200},
		{"roles.thinking.rows", l.Roles.Thinking.Rows, 0, 40},
	}
	for _, row := range numbers {
		if row.value == nil {
			continue
		}
		if value := *row.value; value < row.low || value > row.high {
			return fmt.Errorf("%s: %d is outside %d–%d", row.field, value, row.low, row.high)
		}
	}
	return nil
}

func (p Provider) validate() error {
	if p.API == "" {
		return errors.New(`api is required: "openai-completions", "anthropic-messages", "openai-responses" or "google-generative-ai"`)
	}
	if err := validateBaseURL(p.BaseURL); err != nil {
		return fmt.Errorf("baseUrl: %w", err)
	}
	switch p.Compat.MaxTokensField {
	case "", "max_tokens", "max_completion_tokens":
	default:
		return fmt.Errorf("compat.maxTokensField %q is not \"max_tokens\" or \"max_completion_tokens\"", p.Compat.MaxTokensField)
	}
	switch p.Compat.ThinkingDialect {
	case "", "deepseek", dialectThinkingLevel, dialectThinkingBudget:
	default:
		return fmt.Errorf("compat.thinkingDialect %q is not one of: %s", p.Compat.ThinkingDialect,
			strings.Join([]string{"deepseek", dialectThinkingLevel, dialectThinkingBudget}, ", "))
	}
	for _, row := range []struct {
		name   string
		value  string
		values []string
	}{
		{"compat.toolChoice", p.Compat.ToolChoice, []string{"auto", "none", "required"}},
		{"compat.serviceTier", p.Compat.ServiceTier, []string{"auto", "default", "flex", "priority"}},
		{"compat.verbosity", p.Compat.Verbosity, []string{"low", "medium", "high"}},
		{"compat.truncation", p.Compat.Truncation, []string{"auto", "disabled"}},
	} {
		if row.value == "" {
			continue
		}
		if !slices.Contains(row.values, row.value) {
			return fmt.Errorf("%s %q is not one of: %s", row.name, row.value, strings.Join(row.values, ", "))
		}
	}
	if len(p.Models) == 0 {
		return errors.New("models is empty: declare at least one model")
	}
	seen := make(map[string]bool, len(p.Models))
	for index, model := range p.Models {
		if model.ID == "" {
			return fmt.Errorf("models[%d]: id is required", index)
		}
		if seen[model.ID] {
			return fmt.Errorf("models[%d]: duplicate id %q", index, model.ID)
		}
		seen[model.ID] = true
		if err := validateBaseURL(model.BaseURL); err != nil {
			return fmt.Errorf("models[%d] (%s): baseUrl: %w", index, model.ID, err)
		}
		for _, input := range model.Input {
			if input != "text" && input != "image" {
				return fmt.Errorf("models[%d] (%s): input %q is not \"text\" or \"image\"", index, model.ID, input)
			}
		}
		if err := model.validateGears(); err != nil {
			return fmt.Errorf("models[%d] (%s): %w", index, model.ID, err)
		}
	}
	return nil
}

// validateGears rejects a level list that would make the /model axis lie: an
// unknown name, a repeat, the off switch, or any list on a model that cannot
// think at all.
func (m Model) validateGears() error {
	if len(m.ThinkingLevels) == 0 {
		return nil
	}
	if !m.Reasoning {
		return errors.New(`thinkingLevels is set but reasoning is false: a model that cannot think has no levels to offer`)
	}
	seen := make(map[string]bool, len(m.ThinkingLevels))
	for _, level := range m.ThinkingLevels {
		if level == LevelOff {
			// Off is the absence of thinking, which every model can be told,
			// so declaring it here would suggest a choice that is not one.
			return fmt.Errorf("thinkingLevels: %q is always offered and is not a level to declare", LevelOff)
		}
		if !ValidThinkingLevel(level) {
			return fmt.Errorf("thinkingLevels: %q is not one of: %s", level, strings.Join(gearNames(), ", "))
		}
		if seen[level] {
			return fmt.Errorf("thinkingLevels: duplicate %q", level)
		}
		seen[level] = true
	}
	return nil
}

// gearNames lists the effort gears without the off switch, which is what a
// declaration is allowed to name.
func gearNames() []string {
	gears := make([]string, 0, len(ThinkingLevels))
	for _, level := range ThinkingLevels {
		if level != LevelOff {
			gears = append(gears, level)
		}
	}
	return gears
}

func validateBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%q must start with http:// or https://", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	return nil
}

// ValidThinkingLevel reports whether level is one of ThinkingLevels. It is
// exported because a command line that writes a level into a model spec needs
// the same vocabulary the config and the RPC layer validate against.
func ValidThinkingLevel(level string) bool {
	for _, candidate := range ThinkingLevels {
		if candidate == level {
			return true
		}
	}
	return false
}

// Key returns the credential to present.
func (p Provider) Key() string { return envKey(p.APIKey, p.APIKeyEnv) }

// envKey is the one rule for every credential in this file: an environment
// variable named explicitly wins over a literal, so a shell can swap secrets
// without editing a file that may be shared. It is shared by providers and by
// the web endpoint because two spellings of "which key do I use" is exactly the
// kind of difference nobody discovers until a request fails.
func envKey(literal, env string) string {
	if env != "" {
		if value := os.Getenv(env); value != "" {
			return value
		}
	}
	return literal
}
