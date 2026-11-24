package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Allowlist is the machine-managed half of every adapter's admission set: the
// ids added at runtime through pairing, kept in their own file so that
// approving a pairing never has to rewrite the hand-edited channels.json.
//
// The config's static allowlist and this one are unioned at admission time;
// neither alone is the whole truth. The file is re-read when it changes:
// `atlas-gateway approve` runs in a second process while the gateway keeps
// running, and a pairing that needs a gateway restart is not a pairing.
type Allowlist struct {
	mu    sync.Mutex
	path  string
	users map[string]map[string]struct{} // adapter → set of user ids
	mod   time.Time
}

func OpenAllowlist(path string) (*Allowlist, error) {
	a := &Allowlist{path: path, users: map[string]map[string]struct{}{}}
	if err := a.reload(); err != nil {
		return nil, err
	}
	return a, nil
}

// reload re-reads the file unless it has not changed. Every error except
// not-exist is reported once through the return value; admission never
// crashes over its allowlist — the static half still holds.
func (a *Allowlist) reload() error {
	info, err := os.Stat(a.path)
	if os.IsNotExist(err) {
		a.mod = time.Time{}
		a.users = map[string]map[string]struct{}{}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.ModTime().After(a.mod) {
		return nil
	}
	raw, err := os.ReadFile(a.path)
	if err != nil {
		return err
	}
	users := map[string]map[string]struct{}{}
	if err := json.Unmarshal(raw, &users); err != nil {
		return fmt.Errorf("allowlist %s: %w", a.path, err)
	}
	a.users = users
	a.mod = info.ModTime()
	return nil
}

func (a *Allowlist) Has(adapter, userID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.reload()
	set, ok := a.users[adapter]
	if !ok {
		return false
	}
	_, exists := set[userID]
	return exists
}

// List returns a snapshot copy of all runtime-admitted user IDs per adapter.
func (a *Allowlist) List() map[string][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.reload()
	out := make(map[string][]string, len(a.users))
	for adapter, set := range a.users {
		if len(set) == 0 {
			continue
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out[adapter] = ids
	}
	return out
}

func (a *Allowlist) Add(adapter, userID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.reload()
	set, ok := a.users[adapter]
	if !ok {
		set = map[string]struct{}{}
		a.users[adapter] = set
	}
	set[userID] = struct{}{}
	return a.saveLocked()
}

// Remove deletes userID from the runtime allowlist for adapter.
func (a *Allowlist) Remove(adapter, userID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	_ = a.reload()
	if set, ok := a.users[adapter]; ok {
		delete(set, userID)
		if len(set) == 0 {
			delete(a.users, adapter)
		}
	}
	return a.saveLocked()
}

func (a *Allowlist) saveLocked() error {
	raw, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	if info, err := os.Stat(a.path); err == nil {
		a.mod = info.ModTime()
	}
	return nil
}
