package permission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"pi-go/internal/tool"
)

// FileName is the rule file inside the atlas home directory.
const FileName = "permission.json"

// SupportedVersion is the only permission.json schema version this package
// reads.
const SupportedVersion = 2

// maxStoreSize bounds permission.json so a malformed path cannot stream an
// unbounded file into memory.
const maxStoreSize = 1 << 20

// Matcher kinds supported in permission.json.
const (
	KindTool    = "tool"
	KindCommand = "command"
)

// Rule is one entry in allow, deny, or ask.
type Rule struct {
	ToolName string  `json:"tool_name"`
	Matcher  Matcher `json:"matcher"`
}

// Matcher narrows a Rule to a command pattern, or leaves it whole-tool when
// Kind is empty or KindTool.
type Matcher struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern,omitempty"`
}

func (m Matcher) wholeTool() bool {
	return m.Kind == "" || m.Kind == KindTool
}

// Store holds the rules from permission.json, or records the error that left
// the store unhealthy.
type Store struct {
	allow  []Rule
	deny   []Rule
	ask    []Rule
	broken error
}

// BrokenStore returns a Store marked unhealthy with err. The ladder fails
// closed on a broken store: default asks on every call, and bypassPermissions
// denies every call.
func BrokenStore(err error) *Store {
	if err == nil {
		err = errors.New("store is unhealthy")
	}
	return &Store{broken: err}
}

// LoadFile reads permission.json at path.
//
// A missing file is not an error: a fresh atlas home has no permission.json and
// runs with an empty rule table. Any other read or parse error returns both a
// BrokenStore (so a caller that hands the store to an Engine still gets
// fail-closed behaviour) and the error itself.
func LoadFile(path string) (*Store, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{}, nil
		}
		err = fmt.Errorf("permission: %s: %w", path, err)
		return BrokenStore(err), err
	}
	if info.Size() > maxStoreSize {
		err := fmt.Errorf("permission: %s is %d bytes, limit is %d", path, info.Size(), maxStoreSize)
		return BrokenStore(err), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		err = fmt.Errorf("permission: %s: %w", path, err)
		return BrokenStore(err), err
	}
	store, err := Parse(data)
	if err != nil {
		err = fmt.Errorf("permission: %s: %w", path, err)
		return BrokenStore(err), err
	}
	return store, nil
}

// rawFile is the on-disk shape of permission.json.
type rawFile struct {
	Version *int            `json:"version"`
	Allow   json.RawMessage `json:"allow"`
	Deny    json.RawMessage `json:"deny"`
	Ask     json.RawMessage `json:"ask"`
}

type rawRule struct {
	ToolName string          `json:"tool_name"`
	Matcher  json.RawMessage `json:"matcher"`
}

type rawMatcher struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
}

// Parse decodes and validates permission.json bytes.
func Parse(data []byte) (*Store, error) {
	var file rawFile
	if err := decodeStrict("permission", data, &file); err != nil {
		return BrokenStore(err), err
	}
	if file.Version == nil {
		err := errors.New("version: required (must be 2)")
		return BrokenStore(err), err
	}
	if *file.Version != SupportedVersion {
		err := fmt.Errorf("version: unsupported version %d (want %d)", *file.Version, SupportedVersion)
		return BrokenStore(err), err
	}
	allow, err := parseRuleList("allow", file.Allow)
	if err != nil {
		return BrokenStore(err), err
	}
	deny, err := parseRuleList("deny", file.Deny)
	if err != nil {
		return BrokenStore(err), err
	}
	ask, err := parseRuleList("ask", file.Ask)
	if err != nil {
		return BrokenStore(err), err
	}
	return &Store{
		allow: allow,
		deny:  deny,
		ask:   ask,
	}, nil
}

func parseRuleList(name string, raw json.RawMessage) ([]Rule, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := decodeStrict(name, raw, &items); err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(items))
	for i, item := range items {
		prefix := fmt.Sprintf("%s[%d]", name, i)
		var r rawRule
		if err := decodeStrict(prefix, item, &r); err != nil {
			return nil, err
		}
		toolName := strings.TrimSpace(r.ToolName)
		if toolName == "" {
			return nil, fmt.Errorf("%s.tool_name: required", prefix)
		}
		matcher, err := parseMatcher(prefix+".matcher", r.Matcher)
		if err != nil {
			return nil, err
		}
		out = append(out, Rule{
			ToolName: toolName,
			Matcher:  matcher,
		})
	}
	return out, nil
}

func parseMatcher(prefix string, raw json.RawMessage) (Matcher, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Matcher{Kind: KindTool}, nil
	}
	var m rawMatcher
	if err := decodeStrict(prefix, trimmed, &m); err != nil {
		return Matcher{}, err
	}
	switch m.Kind {
	case KindTool:
		if strings.TrimSpace(m.Pattern) != "" {
			return Matcher{}, fmt.Errorf("%s.pattern: %s matcher takes no pattern", prefix, KindTool)
		}
		return Matcher{Kind: KindTool}, nil
	case KindCommand:
		if strings.TrimSpace(m.Pattern) == "" {
			return Matcher{}, fmt.Errorf("%s.pattern: required for %s matcher", prefix, KindCommand)
		}
		return Matcher{Kind: KindCommand, Pattern: m.Pattern}, nil
	case "":
		return Matcher{}, fmt.Errorf("%s.kind: required", prefix)
	default:
		return Matcher{}, fmt.Errorf("%s.kind: unknown matcher kind %q (want %q or %q)", prefix, m.Kind, KindTool, KindCommand)
	}
}

func decodeStrict(prefix string, data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%s: trailing data after JSON value", prefix)
	}
	return nil
}

func (s *Store) rulesFor(behavior Behavior) []Rule {
	if s == nil {
		return nil
	}
	switch behavior {
	case Allow:
		return s.allow
	case Deny:
		return s.deny
	case Ask:
		return s.ask
	default:
		return nil
	}
}

func (s *Store) matchWholeTool(toolName string, behavior Behavior) (Rule, bool) {
	for _, rule := range s.rulesFor(behavior) {
		if rule.ToolName == toolName && rule.Matcher.wholeTool() {
			return rule, true
		}
	}
	return Rule{}, false
}

func (s *Store) matchContent(call tool.Call, behavior Behavior) (Rule, bool) {
	rules := s.rulesFor(behavior)
	if len(rules) == 0 {
		return Rule{}, false
	}
	command, ok := extractCommand(call.Arguments)
	if !ok {
		return Rule{}, false
	}
	for _, rule := range rules {
		if rule.ToolName != call.Name || rule.Matcher.wholeTool() {
			continue
		}
		if rule.Matcher.Kind == KindCommand && matchCommand(rule.Matcher.Pattern, command) {
			return rule, true
		}
	}
	return Rule{}, false
}

func extractCommand(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", false
	}
	if args.Command == "" {
		return "", false
	}
	return args.Command, true
}

// matchCommand matches a permission.json command pattern against a shell
// command string.
//
// Two pattern forms are supported, matching MiniMax Code's command matcher:
//   - "<prefix>:*": matches when any subcommand segment equals <prefix> or
//     starts with "<prefix> " (for example "curl:*" matches "curl https://x"
//     and "echo ok && curl -I https://x", but not "curling").
//   - wildcard patterns containing "*" (or literal command strings): matched
//     against each subcommand segment and the full command.
func matchCommand(pattern, command string) bool {
	pattern = strings.TrimSpace(pattern)
	command = strings.TrimSpace(command)
	if pattern == "" || command == "" {
		return false
	}
	segments := commandSegments(command)
	if prefix, ok := strings.CutSuffix(pattern, ":*"); ok {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			return true
		}
		for _, seg := range segments {
			if seg == prefix || strings.HasPrefix(seg, prefix+" ") || strings.HasPrefix(seg, prefix+"\t") {
				return true
			}
		}
		return false
	}
	if strings.Contains(pattern, "*") {
		if wildcardMatch(pattern, command) {
			return true
		}
		for _, seg := range segments {
			if wildcardMatch(pattern, seg) {
				return true
			}
		}
		return false
	}
	for _, seg := range segments {
		if seg == pattern || strings.HasPrefix(seg, pattern+" ") || strings.HasPrefix(seg, pattern+"\t") {
			return true
		}
	}
	return strings.Contains(command, pattern)
}

// commandSegments splits a shell command on top-level operators (;, &&, ||, |,
// newline) outside quotes and strips leading VAR=value assignments from each
// segment. The full command is included first so patterns that span a pipeline
// still match.
func commandSegments(command string) []string {
	out := []string{command}
	var (
		cur    strings.Builder
		quote  byte
		escape bool
	)
	flush := func() {
		seg := stripEnvAssignments(strings.TrimSpace(cur.String()))
		cur.Reset()
		if seg != "" && seg != command {
			out = append(out, seg)
		}
	}
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escape {
			cur.WriteByte(ch)
			escape = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			cur.WriteByte(ch)
			escape = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			cur.WriteByte(ch)
			continue
		}
		if ch == ';' || ch == '\n' {
			flush()
			continue
		}
		if (ch == '&' || ch == '|') && i+1 < len(command) && command[i+1] == ch {
			flush()
			i++
			continue
		}
		if ch == '|' {
			flush()
			continue
		}
		cur.WriteByte(ch)
	}
	flush()
	return out
}

func stripEnvAssignments(segment string) string {
	for {
		segment = strings.TrimLeft(segment, " \t")
		eq := strings.IndexByte(segment, '=')
		if eq <= 0 {
			return segment
		}
		name := segment[:eq]
		if !validEnvName(name) {
			return segment
		}
		rest := segment[eq+1:]
		end := skipEnvValue(rest)
		if end >= len(rest) {
			return segment
		}
		if rest[end] != ' ' && rest[end] != '\t' {
			return segment
		}
		segment = strings.TrimLeft(rest[end:], " \t")
	}
}

func validEnvName(name string) bool {
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if i == 0 {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_') {
				return false
			}
			continue
		}
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return false
		}
	}
	return len(name) > 0
}

func skipEnvValue(s string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ' ' || ch == '\t' {
			return i
		}
	}
	return len(s)
}

func wildcardMatch(pattern, text string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == text
	}
	if !strings.HasPrefix(text, parts[0]) {
		return false
	}
	text = text[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(text, parts[i])
		if idx < 0 {
			return false
		}
		text = text[idx+len(parts[i]):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(text, last)
}
