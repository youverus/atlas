package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MCPFileName is the file under the atlas home that declares MCP servers.
const MCPFileName = "mcp.json"

// mcpProjectDir is the per-project directory, the same one layouts and themes
// use. Keeping the three together is what makes ".atlas" a thing a reader can
// learn once.
const mcpProjectDir = ".atlas"

// MCPServer is one server as the configuration describes it, with ${NAME} already
// expanded. Version numbers and timeouts are still the file's units: the seconds
// here become a duration in cmd/atlas, which is the only place that talks to the
// client package.
type MCPServer struct {
	// Name is the map key, and what the model sees inside every tool name.
	Name string
	// Type is what the file said, empty when it was inferred from the fields.
	Type    string
	Command string
	Args    []string
	Env     map[string]string
	Cwd     string
	URL     string
	Headers map[string]string
	// Auth is the authorization mode. Only "oauth" is defined; empty means the
	// credentials, if any, are in Headers.
	Auth string
	// Scopes are what the authorization asks for. Only meaningful with Auth.
	Scopes []string
	// Timeout is how long one call may stay silent before it is abandoned, in
	// seconds; zero means the client default. It is an idle budget, not a total:
	// a server that reports progress restarts it.
	Timeout int
	// Tools narrows this server's tools to the names it spells here. Empty means
	// every tool the server declares.
	Tools []string
}

// MCPConfig describes the servers configured for a session.
type MCPConfig struct {
	// Servers are the enabled servers, sorted by name.
	Servers []MCPServer
	// Disabled are the configured servers that should stay disconnected, sorted by name.
	Disabled []MCPServer
	// Missing names the variables a ${NAME} referred to that were not set.
	// They are reported rather than expanded to an empty string, because a
	// server that starts with half a credential fails in a way that names
	// nothing.
	Missing []string
	// Global is the path of the global file, whether or not it exists. The
	// caller points a user at it when there is nothing to connect to.
	Global string
	// Project is the path of the project file, empty when there was none.
	Project string
	// Notes is what was left out and why: a plugin that did not load, a plugin
	// server whose name was taken. They are lines rather than errors because
	// each one costs only its own entry — a session with three servers still
	// starts when the fourth is refused.
	Notes []string
}

// mcpFile is the JSON shape of one file. The map key is the server name, which is
// why the name is not a field.
type mcpFile struct {
	Servers map[string]mcpEntry `json:"mcpServers"`
}

// mcpEntry is the JSON shape of one server.
//
// Timeout and Enabled are pointers because "absent" and "zero" mean different
// things: an absent timeout is the client's default, and an absent enabled is
// true. A plain bool would make every config that omits the field look disabled.
type mcpEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Auth    string            `json:"auth"`
	Scopes  []string          `json:"scopes"`
	Tools   []string          `json:"tools"`
	Timeout *int              `json:"timeout"`
	Enabled *bool             `json:"enabled"`
}

// LoadMCP reads the MCP configuration for one session.
//
// Three sources are folded in this order: the global file, the installed
// plugins, and the project file. Explicit user switches then override the
// resulting enabled flags. A missing global file is normal before the feature
// is used. Malformed files, unknown keys and invalid declarations are errors.
func LoadMCP(home, cwd string) (MCPConfig, error) {
	cfg := MCPConfig{Global: filepath.Join(home, MCPFileName)}
	plugins := LoadPlugins(home)
	cfg.Notes = append(cfg.Notes, plugins.Problems...)

	entries, err := readMCPFile(cfg.Global)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return MCPConfig{}, err
	}
	if entries == nil {
		// A missing file and an empty one mean the same thing from here on, and
		// a plugin may be the only thing that has anything to declare.
		entries = make(map[string]mcpEntry, 4)
	}
	foldPlugins(entries, plugins, &cfg.Notes)

	if cwd != "" {
		path := filepath.Join(cwd, mcpProjectDir, MCPFileName)
		overrides, err := readMCPFile(path)
		switch {
		case err == nil:
			cfg.Project = path
			if err := applyMCPOverrides(entries, overrides, path); err != nil {
				return MCPConfig{}, err
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			return MCPConfig{}, err
		}
	}

	settings, err := LoadCapabilities(home)
	if err != nil {
		return MCPConfig{}, err
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	// Duplicate detection is about the tool names, not the map: "my-server" and
	// "my_server" would produce the same mcp__ prefix, and the second one would
	// silently shadow the first.
	canonical := make(map[string]string, len(names))
	for _, name := range names {
		key := strings.ReplaceAll(name, "-", "_")
		if previous, seen := canonical[key]; seen {
			return MCPConfig{}, fmt.Errorf("config: %s: mcpServers: %q and %q differ only in \"-\" and \"_\"",
				cfg.Global, previous, name)
		}
		canonical[key] = name

		entry := entries[name]
		if err := entry.validate(name); err != nil {
			return MCPConfig{}, fmt.Errorf("config: %s: mcpServers.%s: %w", cfg.Global, name, err)
		}
		enabled := entry.Enabled == nil || *entry.Enabled
		if override, ok := settings.MCP[name]; ok {
			enabled = override
		}
		if enabled {
			cfg.Servers = append(cfg.Servers, entry.resolve(name, &cfg.Missing))
			continue
		}
		// Missing variables only affect startup for servers that will connect.
		var missing []string
		cfg.Disabled = append(cfg.Disabled, entry.resolve(name, &missing))
	}

	sort.Strings(cfg.Missing)
	cfg.Missing = compactStrings(cfg.Missing)
	return cfg, nil
}

// readMCPFile decodes one file strictly. Unknown keys are refused with the path
// that contains them, which is the rule config.json and tui.json already follow.
func readMCPFile(path string) (map[string]mcpEntry, error) {
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
	var file mcpFile
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %s: trailing data after the config object", path)
	}
	return file.Servers, nil
}

// foldPlugins adds the servers the installed plugins declare.
//
// The order is the collision rule, and it is the same one skills follow: the
// first source to claim a name keeps it. The user's own mcp.json comes first,
// then the plugins in name order, then the project file — which can still only
// switch a server on or off, including one a plugin declared.
//
// A losing server is skipped with a note rather than refused. The plugin is not
// the file the reader is editing, and one collision between two installs should
// not cost the session every other server on the machine.
func foldPlugins(entries map[string]mcpEntry, plugins PluginConfig, notes *[]string) {
	// The map is keyed the way the tool names are, because "my-server" and
	// "my_server" would produce the same mcp__ prefix.
	origin := make(map[string]claim, len(entries))
	for name := range entries {
		origin[strings.ReplaceAll(name, "-", "_")] = claim{name: name, source: "mcp.json"}
	}
	for _, plugin := range plugins.Plugins {
		for _, name := range plugin.ServerNames() {
			key := strings.ReplaceAll(name, "-", "_")
			if winner, taken := origin[key]; taken {
				reason := fmt.Sprintf("%s already declares that name", winner.source)
				if winner.name != name {
					reason = fmt.Sprintf("%s already declares %q, which differs only in \"-\" and \"_\"", winner.source, winner.name)
				}
				*notes = append(*notes, fmt.Sprintf("plugin: %s: mcpServers.%s: %s, so this server is not added", plugin.Dir, name, reason))
				continue
			}
			entries[name] = plugin.servers[name]
			origin[key] = claim{name: name, source: fmt.Sprintf("the plugin %q", plugin.Name)}
		}
	}
}

// claim is which source holds a server name, for a note about a lost collision.
type claim struct {
	name   string
	source string
}

// applyMCPOverrides folds the project file into the global entries.
//
// A project file may only switch servers on and off. Anything that could add or
// change a command, a URL or a credential would turn a checkout into argv and a
// secret store, and atlas has no notion of a trusted project to gate that on
// (docs/mcp.md section 7).
func applyMCPOverrides(entries map[string]mcpEntry, overrides map[string]mcpEntry, path string) error {
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		override := overrides[name]
		if err := override.projectOnly(); err != nil {
			return fmt.Errorf("config: %s: mcpServers.%s: %w", path, name, err)
		}
		current, ok := entries[name]
		if !ok {
			// Silently ignoring the entry would make "I wrote it down" a puzzle;
			// the typo is the likelier explanation and this message finds it.
			return fmt.Errorf("config: %s: mcpServers.%s: no server of that name in mcp.json or in an installed plugin", path, name)
		}
		current.Enabled = override.Enabled
		entries[name] = current
	}
	return nil
}

// projectOnly refuses every key a project file is not allowed to carry.
func (e mcpEntry) projectOnly() error {
	switch {
	case e.Type != "", e.Command != "", len(e.Args) > 0, len(e.Env) > 0, e.Cwd != "",
		e.URL != "", len(e.Headers) > 0, e.Auth != "", len(e.Scopes) > 0, e.Timeout != nil,
		len(e.Tools) > 0:
		return errors.New("a project file may only set \"enabled\"; command, url, env, headers, auth, scopes, tools, cwd and timeout belong in the global mcp.json")
	}
	return nil
}

// validate checks one entry. Everything is checked here rather than at connect
// time, so a broken file names the line the user wrote instead of a process that
// failed to start.
func (e mcpEntry) validate(name string) error {
	if !validMCPName(name) {
		return errors.New("a server name must be letters, digits, \"-\" and \"_\"")
	}

	kind := e.Type
	switch kind {
	case "":
		switch {
		case e.Command != "" && e.URL != "":
			return errors.New("set either \"command\" or \"url\", not both")
		case e.Command == "" && e.URL == "":
			return errors.New("needs a \"command\" (stdio) or a \"url\" (streamable HTTP)")
		case e.Command != "":
			kind = "stdio"
		default:
			kind = "http"
		}
	case "stdio":
		if e.Command == "" {
			return errors.New("type \"stdio\" needs a \"command\"")
		}
		if e.URL != "" {
			return errors.New("type \"stdio\" cannot have a \"url\"")
		}
	case "http", "streamable-http":
		if e.URL == "" {
			return fmt.Errorf("type %q needs a \"url\"", kind)
		}
		if e.Command != "" {
			return fmt.Errorf("type %q cannot have a \"command\"", kind)
		}
	case "sse":
		// The old HTTP+SSE transport is two endpoints and a different session
		// model. Most servers that used it also serve the streamable endpoint,
		// which is the same URL the rest of this ecosystem calls /mcp.
		return errors.New("\"type\": \"sse\" is the deprecated HTTP+SSE transport; point \"url\" at the streamable HTTP endpoint instead (usually ending in /mcp)")
	default:
		return fmt.Errorf("type %q is not one of: stdio, http, streamable-http", kind)
	}

	switch kind {
	case "stdio":
		if len(e.Headers) > 0 {
			return errors.New("\"headers\" belong to a \"url\" server")
		}
	default:
		if len(e.Args) > 0 || len(e.Env) > 0 || e.Cwd != "" {
			return errors.New("\"args\", \"env\" and \"cwd\" belong to a \"command\" server")
		}
		if err := validateMCPURL(e.URL); err != nil {
			return fmt.Errorf("url: %w", err)
		}
		if err := validateMCPHeaders(e.Headers); err != nil {
			return err
		}
	}

	if err := e.validateAuth(kind); err != nil {
		return err
	}
	if e.Cwd != "" && !filepath.IsAbs(e.Cwd) {
		// A relative directory would resolve against whatever directory atlas
		// was started in, which is a property of the shell and not of the config.
		return errors.New("cwd: must be an absolute path")
	}
	if e.Timeout != nil && *e.Timeout <= 0 {
		return errors.New("timeout: must be a positive number of seconds")
	}
	if err := validateMCPEnv(e.Env); err != nil {
		return err
	}
	return validateMCPTools(e.Tools)
}

// validateMCPTools checks a whitelist. An empty name is the typo a list invites;
// the names themselves are the server's, so nothing else here is knowable before
// the server speaks (mcp.Server.Missing reports the ones it does not have).
func validateMCPTools(tools []string) error {
	if len(tools) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tools))
	for _, name := range tools {
		if name == "" {
			return errors.New("tools: one entry is an empty string")
		}
		if seen[name] {
			return fmt.Errorf("tools: %q is listed twice", name)
		}
		seen[name] = true
	}
	return nil
}

// validateAuth checks the two authorization keys (docs/mcp-oauth.md section 4.1).
//
// Every refusal here is "the file said something that cannot work", which is why
// they belong next to the rest of validate rather than at connect time: an
// authorization that cannot succeed must fail on the line that was written, not
// two commands later.
func (e mcpEntry) validateAuth(kind string) error {
	if e.Auth != "" && e.Auth != "oauth" {
		return fmt.Errorf(`"auth": %q is not a mode atlas has; the only one is "oauth"`, e.Auth)
	}
	if e.Auth == "oauth" {
		if kind == "stdio" {
			return errors.New(`"auth": "oauth" needs a "url": a server started as a process has no HTTP endpoint to authorize against`)
		}
		// Two credentials for one header is a choice this client would have to
		// make on the user's behalf, and either choice can be the wrong one.
		for name := range e.Headers {
			if strings.EqualFold(name, "Authorization") {
				return errors.New(`"auth": "oauth" and an "Authorization" header are two credentials for one server; delete one of them`)
			}
		}
	}
	if len(e.Scopes) > 0 && e.Auth != "oauth" {
		// The fifth refusal, beyond the four the design doc lists: a key that is
		// silently ignored is exactly the failure this file exists to prevent.
		return errors.New(`"scopes" only mean something with "auth": "oauth"`)
	}
	for _, scope := range e.Scopes {
		if scope == "" {
			return errors.New("a scope is empty")
		}
		if strings.ContainsAny(scope, " \t\r\n") {
			return fmt.Errorf("scope %q contains whitespace, which is what separates scopes", scope)
		}
	}
	return nil
}

// validMCPName keeps a name usable as part of a tool name, a header value and a
// line of output. The character set is the one the clients in this ecosystem
// share, which is also what makes an entry portable between them.
func validMCPName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// validateMCPURL accepts an absolute http or https URL. It does not reuse
// validateBaseURL: that one refuses a path, and an MCP endpoint almost always has
// one.
func validateMCPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("must start with http:// or https://")
	}
	if parsed.Host == "" {
		return errors.New("has no host")
	}
	return nil
}

// validateMCPHeaders refuses what a header cannot carry. The check is here rather
// than at request time because a value with a newline is a request smuggling bug
// someone typed, and the error that surfaces from net/http names nothing.
func validateMCPHeaders(headers map[string]string) error {
	for name, value := range headers {
		if strings.TrimSpace(name) == "" {
			return errors.New("a header name is empty")
		}
		if strings.ContainsAny(name, " \t\r\n:") {
			return fmt.Errorf("header name %q contains a character a header cannot carry", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q has a line break in its value", name)
		}
	}
	return nil
}

// validateMCPEnv checks the environment half. An empty name is a typo; an "=" in a
// name is the one character the kernel's environment format cannot express.
func validateMCPEnv(env map[string]string) error {
	for name := range env {
		if name == "" {
			return errors.New("env: a variable name is empty")
		}
		if strings.ContainsAny(name, "=\x00") {
			return fmt.Errorf("env: %q is not a valid variable name", name)
		}
	}
	return nil
}

// resolve turns a validated entry into a server, expanding ${NAME} on the way.
func (e mcpEntry) resolve(name string, missing *[]string) MCPServer {
	server := MCPServer{
		Name:    name,
		Type:    e.Type,
		Command: e.Command,
		Cwd:     e.Cwd,
		URL:     e.URL,
		Auth:    e.Auth,
	}
	if e.Timeout != nil {
		server.Timeout = *e.Timeout
	}
	if len(e.Scopes) > 0 {
		server.Scopes = make([]string, len(e.Scopes))
		copy(server.Scopes, e.Scopes)
	}
	if len(e.Tools) > 0 {
		server.Tools = make([]string, len(e.Tools))
		copy(server.Tools, e.Tools)
	}
	if len(e.Args) > 0 {
		server.Args = make([]string, len(e.Args))
		for index, arg := range e.Args {
			server.Args[index] = expandMCP(arg, missing)
		}
	}
	if len(e.Env) > 0 {
		server.Env = make(map[string]string, len(e.Env))
		for key, value := range e.Env {
			server.Env[key] = expandMCP(value, missing)
		}
	}
	if len(e.Headers) > 0 {
		server.Headers = make(map[string]string, len(e.Headers))
		for key, value := range e.Headers {
			server.Headers[key] = expandMCP(value, missing)
		}
	}
	return server
}

// expandMCP replaces ${NAME} with the value of the variable.
//
// os.ExpandEnv is not used because it turns an unset variable into an empty
// string: the server then authenticates with nothing and fails with a message
// about permissions, while the actual problem is a variable that was never set.
// Leaving the literal in place and reporting the name is the difference between a
// puzzle and an instruction.
func expandMCP(value string, missing *[]string) string {
	if !strings.Contains(value, "${") {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value))
	rest := value
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			builder.WriteString(rest)
			return builder.String()
		}
		end := strings.Index(rest[start:], "}")
		if end < 0 {
			builder.WriteString(rest)
			return builder.String()
		}
		end += start
		builder.WriteString(rest[:start])
		name := rest[start+2 : end]
		switch {
		case name == "":
			builder.WriteString(rest[start : end+1])
		default:
			if replacement, ok := os.LookupEnv(name); ok {
				builder.WriteString(replacement)
			} else {
				builder.WriteString(rest[start : end+1])
				*missing = append(*missing, name)
			}
		}
		rest = rest[end+1:]
	}
}

// compactStrings removes neighbours that repeat, for a slice already sorted.
func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	kept := values[:1]
	for _, value := range values[1:] {
		if value != kept[len(kept)-1] {
			kept = append(kept, value)
		}
	}
	return kept
}
