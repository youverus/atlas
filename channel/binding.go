package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Binding is one chat's standing connection to a session file.
//
// CWD is the working directory tools run in for this chat — the thing a
// project IS, so it rides the binding rather than the process. Session is the
// session file the binding continues; empty means "start a new one", which is
// also what it goes back to after /new.
type Binding struct {
	Session string `json:"session,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Name    string `json:"name,omitempty"` // display name, first-seen chat title
	Created string `json:"created,omitempty"`
}

// BindingStore is the exported alias of the chat-to-session store for channel managers.
type BindingStore = bindingStore

// bindingStore is the chat → session map, persisted as one JSON file. Every
// mutation saves: the store is small (one record per chat), and a binding that
// vanishes on a crash would make the next message start over.
type bindingStore struct {
	mu         sync.Mutex
	path       string
	defaultCWD string
	m          map[string]Binding
}

func OpenBindings(path, defaultCWD string) (*bindingStore, error) {
	s := &bindingStore{path: path, defaultCWD: defaultCWD, m: map[string]Binding{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.m); err != nil {
		return nil, fmt.Errorf("bindings %s: %w", path, err)
	}
	return s, nil
}

func (s *bindingStore) get(key string) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	return b, ok
}

// ensure returns the binding for key, creating it — with the default working
// directory and a first-seen timestamp — when absent. name is what the chat
// was called when we met it.
func (s *bindingStore) ensure(key, name string) Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.m[key]; ok {
		return b
	}
	b := Binding{CWD: s.defaultCWD, Name: name, Created: time.Now().Format(time.RFC3339)}
	s.m[key] = b
	if err := s.saveLocked(); err != nil {
		// The turn still runs; the binding is re-created next message with the
		// same defaults. Losing it costs continuity, not correctness.
		fmt.Fprintf(os.Stderr, "channel: save bindings: %v\n", err)
	}
	return b
}

func (s *bindingStore) mutate(key string, f func(Binding) Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := f(s.m[key])
	s.m[key] = b
	if err := s.saveLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "channel: save bindings: %v\n", err)
	}
}

func (s *bindingStore) setSession(key, file string) {
	s.mutate(key, func(b Binding) Binding { b.Session = file; return b })
}

func (s *bindingStore) setCWD(key, cwd string) {
	s.mutate(key, func(b Binding) Binding { b.CWD = cwd; return b })
}

// saveLocked writes the file atomically: a crash between truncate and write
// must not leave the reader half a JSON file.
// SetDefaultCWD updates the working directory used when new chats bind.
func (s *bindingStore) SetDefaultCWD(cwd string) {
	s.mu.Lock()
	s.defaultCWD = cwd
	s.mu.Unlock()
}

// List returns a snapshot of all persisted chat bindings.
func (s *bindingStore) List() map[string]Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Binding, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

// Delete removes a chat binding from disk so its next message starts fresh.
func (s *bindingStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return s.saveLocked()
}

func (s *bindingStore) saveLocked() error {
	raw, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
