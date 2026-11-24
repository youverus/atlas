package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"pi-go/internal/config"
	"pi-go/internal/httpapi"
)

// defaultDesktopPort is the port the desktop shell tries first. A well-known
// port is what lets the browser extension find the shell without a discovery
// dance; the web build's dev proxy points here too, so development and the
// installed app agree.
const defaultDesktopPort = 7788

// extensionState is what the desktop shell persists for the browser extension:
// the bearer token its options page can fall back to, and the allowlist of
// extension origins the reader approved through the pairing card. Both are
// stable across restarts so a configured extension does not go stale on every
// launch.
type extensionState struct {
	Token     string   `json:"token"`
	Allowlist []string `json:"allowlist,omitempty"`
}

// loadExtensionState reads ~/.atlas/extension.json, creating it with a fresh
// token on first use. An unreadable file is replaced rather than fatal: the
// cost is one re-pairing, not a shell that refuses to start.
func loadExtensionState() (*extensionState, string) {
	path := extensionStatePath()
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			var state extensionState
			if json.Unmarshal(raw, &state) == nil && state.Token != "" {
				return &state, path
			}
		}
	}
	issued := make([]byte, 24)
	if _, err := rand.Read(issued); err != nil {
		return &extensionState{}, path
	}
	state := &extensionState{Token: hex.EncodeToString(issued)}
	if path != "" {
		if err := writeExtensionState(path, state); err != nil {
			fmt.Fprintf(os.Stderr, "atlas-desktop: extension state: %v\n", err)
		}
	}
	return state, path
}

// extensionStatePath returns the file the extension state persists in, or ""
// when there is no Atlas home to keep it in.
func extensionStatePath() string {
	home, err := config.DefaultHome()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "extension.json")
}

// writeExtensionState persists the state atomically, tmp plus rename, so a
// crash never leaves a half-written credential behind.
func writeExtensionState(path string, state *extensionState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// extensionAuthorizer implements httpapi.ExtensionApprovals: the loaded state
// answers the guard's per-request check, and an approval lands in the durable
// file so the next launch starts with the same extensions admitted. Approved
// origins are full Origins (chrome-extension://<id>) — the browser stamps them
// itself, so the id is as good as the token at naming one installed extension.
type extensionAuthorizer struct {
	mu    sync.Mutex
	state *extensionState
	path  string
}

func newExtensionAuthorizer(state *extensionState, path string) *extensionAuthorizer {
	return &extensionAuthorizer{state: state, path: path}
}

func (a *extensionAuthorizer) Approved(origin string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, known := range a.state.Allowlist {
		if known == origin {
			return true
		}
	}
	return false
}

func (a *extensionAuthorizer) Approve(origin string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, known := range a.state.Allowlist {
		if known == origin {
			return nil
		}
	}
	a.state.Allowlist = append(a.state.Allowlist, origin)
	sort.Strings(a.state.Allowlist)
	// 无家可写时批准仍是会话内的：扩展当次可用，重启后需重新配对。
	if a.path == "" {
		return nil
	}
	return writeExtensionState(a.path, a.state)
}

// bindServer listens on the well-known desktop port, falling back to an
// ephemeral port when something else owns it. ATLAS_DESKTOP_PORT overrides the
// well-known one, and an invalid or busy override falls through the same way,
// because a shell that refuses to start over a port is worse than one the
// extension has to be pointed at again.
func bindServer(server *httpapi.Server) error {
	port := defaultDesktopPort
	if raw := strings.TrimSpace(os.Getenv("ATLAS_DESKTOP_PORT")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			port = parsed
		}
	}
	if _, err := server.ListenOn(fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
		return nil
	}
	_, err := server.Listen()
	return err
}
