// Package permission decides whether a tool call runs, asks the user first, or
// is refused outright.
//
// It is the user-space half of the boundary whose kernel half lives in
// internal/sandbox. The sandbox confines what a child process tree can touch
// once bash starts; this package decides whether bash, write, edit, or any
// other tool is called in the first place. go/docs/permissions.md records the
// reference ladder and the parts that were deliberately left out.
//
// This package is a leaf: it depends on internal/tool and the standard library
// only.
package permission

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"pi-go/internal/tool"
)

// Mode is the user-facing permission posture.
//
// Four modes, and they answer two different questions. ModeDefault and ModeAuto
// ask; the difference is who answers. ModeBypass and ModeOff stop asking; the
// difference is what else stops happening. The reference implementation calls
// them Ask, Auto, Full access and Off.
//
// ModeAuto does not change the ladder: rules, checkers and deny still decide
// first, and only an ask that no rule produced is handed to a Classifier. That
// is why the ladder below has no auto branch.
const (
	// ModeDefault asks when a rule or a checker says so, and a human answers.
	ModeDefault = "default"
	// ModeAuto asks the configured classifier instead of a human, for the asks
	// no rule produced. A classifier failure asks the human.
	ModeAuto = "auto"
	// ModeBypass waives the soft per-call review while keeping explicit deny
	// rules, explicit ask rules, and bypass-immune checks in force.
	ModeBypass = "bypassPermissions"
	// ModeOff disables the gate: no rule, no checker, no ask.
	ModeOff = "off"
)

// ValidMode reports whether mode is a supported setting. An empty string means
// ModeDefault.
func ValidMode(mode string) bool {
	switch mode {
	case "", ModeDefault, ModeAuto, ModeBypass, ModeOff:
		return true
	}
	return false
}

// askPolicy is what a Mode translates into inside the ladder. Keeping the
// translation separate from the mode vocabulary means adding a mode later does
// not rewrite the ladder itself.
type askPolicy string

const (
	policyOnRequest askPolicy = "on-request"
	policyNever     askPolicy = "never"
	// policyOff skips the ladder entirely. It is not "never ask" — that one
	// still runs every deny rule and every checker.
	policyOff askPolicy = "off"
)

func policyFor(mode string) askPolicy {
	switch mode {
	case ModeBypass:
		return policyNever
	case ModeOff:
		return policyOff
	}
	return policyOnRequest
}

// ClassifyRequest is what a Classifier is asked about. It carries the tool name,
// its arguments, and the ladder's reason for asking, and nothing else: the
// conversation is the model's own writing and must not be able to vote on
// whether the model may act.
type ClassifyRequest struct {
	Tool   string
	Args   string
	Reason string
}

// Verdict is a classifier's answer. Only Allow and Deny are meaningful; anything
// else leaves the question with the human.
type Verdict struct {
	Behavior Behavior
	Reason   string
}

// Classifier answers an ask the ladder could not settle by rule. It is a local
// decision made by a model the user configured: no request leaves for a service
// the user did not name.
type Classifier interface {
	Classify(ctx context.Context, request ClassifyRequest) (Verdict, error)
}

// Behavior is the outcome of a rule, a checker, or the ladder as a whole.
type Behavior string

const (
	Undecided Behavior = ""
	Allow     Behavior = "allow"
	Ask       Behavior = "ask"
	Deny      Behavior = "deny"
)

// Decision is what the ladder concludes for one tool call.
type Decision struct {
	Behavior Behavior
	Reason   string

	// Classifiable marks an Ask that came from no rule: the tool has a checker
	// and it stayed undecided, or it failed. Only these are handed to a
	// Classifier in ModeAuto. An ask produced by a rule, by a bypass-immune
	// check, or by an unhealthy store is the user's own word and stays with the
	// human.
	Classifiable bool
}

// CheckResult is what a per-tool Checker returns.
//
// BypassImmune marks an Ask that even ModeBypass must not wave through, such as
// writing to a sensitive credential path outside the workspace.
type CheckResult struct {
	Behavior     Behavior
	Reason       string
	BypassImmune bool
}

// Checker inspects a tool call for a specific tool name. Returning Undecided
// means the tool has no objection of its own and leaves the outcome to the mode
// and rule store at the bottom of the ladder.
type Checker func(call tool.Call) (CheckResult, error)

// Options configures an Engine.
type Options struct {
	// Mode is one of the four Mode values; empty means ModeDefault.
	Mode string

	// Store holds the rules loaded from permission.json. Nil means an empty,
	// healthy rule store.
	Store *Store

	// SandboxEnabled reports whether bash runs inside the kernel sandbox.
	SandboxEnabled bool

	// AutoAllowBashIfSandboxed skips whole-tool bash ask rules (and the
	// default bash checker prompt) when SandboxEnabled is also true. Content
	// ask rules and deny rules are never skipped.
	AutoAllowBashIfSandboxed bool

	// Classifier answers the rule-free asks of ModeAuto. Nil in ModeAuto means
	// those asks go to the human, which is the fail-closed reading of "the
	// classifier could not be built".
	Classifier Classifier

	// Checkers maps a tool name to its checker. Nil uses DefaultCheckers(); an
	// explicit map (including an empty one) is used as given so tests can
	// exercise the bottom of the ladder directly.
	Checkers map[string]Checker
}

// Engine evaluates tool calls against the permission ladder and remembers
// session-scoped approvals in memory.
type Engine struct {
	opts       Options
	store      *Store
	checkers   map[string]Checker
	classifier Classifier

	mu      sync.Mutex
	mode    string
	session string
	allowed map[string]struct{}
}

// New builds an Engine from opts.
func New(opts Options) *Engine {
	checkers := opts.Checkers
	if checkers == nil {
		checkers = DefaultCheckers()
	}
	return &Engine{
		opts:       opts,
		store:      opts.Store,
		checkers:   checkers,
		classifier: opts.Classifier,
		mode:       opts.Mode,
	}
}

// Mode reports the posture in force right now.
func (e *Engine) Mode() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mode
}

// SetMode changes the posture for the rest of the process.
//
// It is the runtime half of the four modes: the ladder reads the mode on every
// call, so a switch takes effect on the next tool call rather than on the next
// session. An unknown name is refused instead of being stored, because a typo
// that silently became "ask" would look like the switch did nothing.
func (e *Engine) SetMode(mode string) error {
	if !ValidMode(mode) {
		return fmt.Errorf("permission: %q is not one of default, auto, bypassPermissions, off", mode)
	}
	mode = normalizeMode(mode)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = mode
	return nil
}

// Reload updates the rule store, sandbox flags, classifier, and optionally the
// active mode while preserving session-scoped tool approvals.
func (e *Engine) Reload(opts Options, updateMode bool) error {
	if updateMode && !ValidMode(opts.Mode) {
		return fmt.Errorf("permission: %q is not one of default, auto, bypassPermissions, off", opts.Mode)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store = opts.Store
	e.opts.SandboxEnabled = opts.SandboxEnabled
	e.opts.AutoAllowBashIfSandboxed = opts.AutoAllowBashIfSandboxed
	e.classifier = opts.Classifier
	if updateMode {
		e.mode = normalizeMode(opts.Mode)
	}
	return nil
}

func normalizeMode(mode string) string {
	if mode == "" {
		return ModeDefault
	}
	return mode
}

func (e *Engine) snapshot() (mode string, store *Store, sandboxEnabled, autoAllow bool, classifier Classifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mode, e.store, e.opts.SandboxEnabled, e.opts.AutoAllowBashIfSandboxed, e.classifier
}

// DefaultCheckers returns the built-in checker table.
//
// Read-only tools (read, ls, find, grep, echo, deepseek_web_search, deepseek_web_fetch) have no
// checker, so the ladder's final step allows them without asking. Mutating and
// execution tools (bash, write, edit) register a checker that returns
// Undecided, which makes the ladder ask in ModeDefault and allow in ModeBypass
// unless a rule says otherwise.
func DefaultCheckers() map[string]Checker {
	undecided := func(tool.Call) (CheckResult, error) {
		return CheckResult{Behavior: Undecided}, nil
	}
	return map[string]Checker{
		"bash":             undecided,
		"write":            undecided,
		"edit":             undecided,
		"browser_click":    undecided,
		"browser_click_at": undecided,
		"browser_type":     undecided,
		"browser_press":    undecided,
		"browser_scroll":   undecided,
	}
}

// AllowForSession records in memory that toolName was approved for the rest of
// sessionID. Switching to a different session ID discards the previous
// session's set automatically.
func (e *Engine) AllowForSession(sessionID, toolName string) {
	if toolName == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != sessionID {
		e.session = sessionID
		e.allowed = nil
	}
	if e.allowed == nil {
		e.allowed = make(map[string]struct{})
	}
	e.allowed[toolName] = struct{}{}
}

// AllowedForSession reports whether toolName has a standing in-memory approval
// for sessionID.
func (e *Engine) AllowedForSession(sessionID, toolName string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != sessionID {
		return false
	}
	_, ok := e.allowed[toolName]
	return ok
}

func (e *Engine) sessionAllows(toolName string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.allowed[toolName]
	return ok
}

func (e *Engine) checkerFor(toolName string) (Checker, bool) {
	if checker, ok := e.checkers[toolName]; ok {
		return checker, true
	}
	// Remote MCP tools can mutate external state; when using the default
	// checker table, treat mcp__<server>__<tool> as having an undecided
	// checker so ModeDefault asks unless a rule allows it.
	if e.opts.Checkers == nil && strings.HasPrefix(toolName, "mcp__") {
		return func(tool.Call) (CheckResult, error) {
			return CheckResult{Behavior: Undecided}, nil
		}, true
	}
	return nil, false
}

// Decide runs the permission ladder for call.
//
// The order is the design:
//  0. ModeOff allows everything: no rule, no checker, no ask.
//  1. Unhealthy rule store fails closed (deny in bypass, ask in default).
//  2. Deny rules (whole-tool or content-specific) win over everything.
//  3. Tool checker deny.
//  4. Whole-tool ask rule, skipped for bash when both SandboxEnabled and
//     AutoAllowBashIfSandboxed are true.
//  5. Tool checker error falls back to ask.
//  6. Bypass-immune checker ask.
//  7. Content-specific ask rule (never skipped by sandbox or bypass).
//  8. Ordinary checker ask when not in bypassPermissions.
//  9. bypassPermissions allows anything that survived the ask steps above.
//  10. Allow rules (whole-tool, content-specific, or session memory) and
//     checker allow.
//  11. A tool with a checker that stayed undecided asks (unless bash is
//     auto-allowed by an active sandbox); a tool with no checker is allowed.
func (e *Engine) Decide(call tool.Call) Decision {
	mode, store, sandboxEnabled, autoAllowBash, _ := e.snapshot()
	policy := policyFor(mode)

	if policy == policyOff {
		return Decision{
			Behavior: Allow,
			Reason:   "permission checks are off",
		}
	}

	if store != nil && store.broken != nil {
		if policy == policyNever {
			return Decision{
				Behavior: Deny,
				Reason:   fmt.Sprintf("permission rule store is unhealthy (%v); bypassPermissions refuses to run without rules", store.broken),
			}
		}
		return Decision{
			Behavior: Ask,
			Reason:   fmt.Sprintf("permission rule store is unhealthy (%v); approval is required", store.broken),
		}
	}

	if rule, ok := store.matchWholeTool(call.Name, Deny); ok {
		return Decision{
			Behavior: Deny,
			Reason:   fmt.Sprintf("denied by rule for %s", rule.ToolName),
		}
	}
	if rule, ok := store.matchContent(call, Deny); ok {
		return Decision{
			Behavior: Deny,
			Reason:   fmt.Sprintf("denied by %s rule %q for %s", rule.Matcher.Kind, rule.Matcher.Pattern, rule.ToolName),
		}
	}

	checker, hasChecker := e.checkerFor(call.Name)
	var (
		check    CheckResult
		checkErr error
	)
	if hasChecker && checker != nil {
		check, checkErr = checker(call)
	}
	if checkErr == nil && check.Behavior == Deny {
		reason := check.Reason
		if reason == "" {
			reason = fmt.Sprintf("denied by %s checker", call.Name)
		}
		return Decision{Behavior: Deny, Reason: reason}
	}

	bashAutoAllowed := call.Name == "bash" && sandboxEnabled && autoAllowBash
	if rule, ok := store.matchWholeTool(call.Name, Ask); ok && !bashAutoAllowed {
		return Decision{
			Behavior: Ask,
			Reason:   fmt.Sprintf("approval required by rule for %s", rule.ToolName),
		}
	}

	if checkErr != nil {
		return Decision{
			Behavior: Ask,
			Reason:   fmt.Sprintf("tool permission checker failed (%v); approval is required", checkErr),
		}
	}

	if check.Behavior == Ask && check.BypassImmune {
		reason := check.Reason
		if reason == "" {
			reason = fmt.Sprintf("approval required for %s", call.Name)
		}
		return Decision{Behavior: Ask, Reason: reason}
	}

	if rule, ok := store.matchContent(call, Ask); ok {
		return Decision{
			Behavior: Ask,
			Reason:   fmt.Sprintf("approval required by %s rule %q for %s", rule.Matcher.Kind, rule.Matcher.Pattern, rule.ToolName),
		}
	}

	if check.Behavior == Ask && policy != policyNever {
		reason := check.Reason
		if reason == "" {
			reason = fmt.Sprintf("approval required for %s", call.Name)
		}
		return Decision{Behavior: Ask, Reason: reason, Classifiable: true}
	}

	if policy == policyNever {
		return Decision{
			Behavior: Allow,
			Reason:   "allowed under bypassPermissions",
		}
	}

	if rule, ok := store.matchWholeTool(call.Name, Allow); ok {
		return Decision{
			Behavior: Allow,
			Reason:   fmt.Sprintf("allowed by rule for %s", rule.ToolName),
		}
	}
	if rule, ok := store.matchContent(call, Allow); ok {
		return Decision{
			Behavior: Allow,
			Reason:   fmt.Sprintf("allowed by %s rule %q for %s", rule.Matcher.Kind, rule.Matcher.Pattern, rule.ToolName),
		}
	}
	if e.sessionAllows(call.Name) {
		return Decision{
			Behavior: Allow,
			Reason:   fmt.Sprintf("allowed for this session: %s", call.Name),
		}
	}
	if check.Behavior == Allow {
		reason := check.Reason
		if reason == "" {
			reason = fmt.Sprintf("allowed by %s checker", call.Name)
		}
		return Decision{Behavior: Allow, Reason: reason}
	}

	if hasChecker {
		if bashAutoAllowed {
			return Decision{
				Behavior: Allow,
				Reason:   "bash ask bypassed because sandbox is active and autoAllowBashIfSandboxed is enabled",
			}
		}
		return Decision{
			Behavior:     Ask,
			Reason:       fmt.Sprintf("no permission rule matched %s; approval is required", call.Name),
			Classifiable: true,
		}
	}
	return Decision{
		Behavior: Allow,
		Reason:   fmt.Sprintf("no checker registered for %s", call.Name),
	}
}

// Gate evaluates call and, when the ladder says Ask, resolves that ask.
//
// Resolution has two possible readers, in this order. In ModeAuto an ask that no
// rule produced goes to the Classifier first; allow runs the call, deny refuses
// it, and anything else — including a classifier that failed or timed out —
// falls through to approve. That order is the fail-closed one: the classifier can
// only ever move an ask to allow or to deny, never past a rule, and a broken
// classifier costs a prompt rather than a silent approval.
//
// It returns blocked=true with an actionable message for the model when the
// call must not execute. The message distinguishes a rule denial, a classifier
// denial, a missing terminal, and a user refusal from a tool execution failure so
// the model stops instead of trying to work around the refusal.
func (e *Engine) Gate(ctx context.Context, call tool.Call, approve func(tool.Call) bool) (bool, string) {
	decision := e.Decide(call)
	switch decision.Behavior {
	case Allow:
		return false, ""
	case Deny:
		return true, fmt.Sprintf("permission denied: %s. Do not retry or work around this call.", decision.Reason)
	}

	if verdict, ok := e.classify(ctx, call, decision); ok {
		switch verdict.Behavior {
		case Allow:
			return false, ""
		case Deny:
			reason := verdict.Reason
			if reason == "" {
				reason = decision.Reason
			}
			return true, fmt.Sprintf("permission denied by the auto classifier: %s (%s). Do not retry or work around this call unless the user says otherwise.", reason, decision.Reason)
		}
	}

	if approve == nil {
		return true, fmt.Sprintf("permission: %s requires user approval (%s) and there is no terminal to ask on. Ask the user before retrying.", call.Name, decision.Reason)
	}
	if !approve(call) {
		return true, fmt.Sprintf("permission: the user declined %s (%s). This is a user decision, not a tool failure; do not retry it without asking.", call.Name, decision.Reason)
	}
	return false, ""
}

// classify asks the classifier about an ask that no rule produced. The second
// result is false when there is nothing to ask, when the mode is not ModeAuto, or
// when the classifier could not answer — every one of which leaves the ask with
// the human.
func (e *Engine) classify(ctx context.Context, call tool.Call, decision Decision) (Verdict, bool) {
	mode, _, _, _, classifier := e.snapshot()
	if classifier == nil || mode != ModeAuto || !decision.Classifiable {
		return Verdict{}, false
	}
	verdict, err := classifier.Classify(ctx, ClassifyRequest{
		Tool:   call.Name,
		Args:   string(call.Arguments),
		Reason: decision.Reason,
	})
	if err != nil {
		return Verdict{}, false
	}
	if verdict.Behavior != Allow && verdict.Behavior != Deny {
		return Verdict{}, false
	}
	return verdict, true
}
