package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// deduper remembers the message ids a platform already delivered. Platforms
// redeliver on reconnect and retry; answering twice is worse than answering
// once, so the check runs before anything else and the id is recorded even
// when the turn later fails — a failed turn is not an invitation to replay.
type deduper struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string // FIFO ring; oldest id is evicted first
	cap   int
	path  string // persisted on Close; empty disables it
}

func OpenDedupe(path string, cap int) (*deduper, error) {
	d := &deduper{seen: map[string]struct{}{}, cap: cap, path: path}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("dedupe %s: %w", path, err)
	}
	// A persisted list can exceed the cap (the cap changed, or a previous run
	// was larger): keep the newest, which is the tail.
	if len(ids) > cap {
		ids = ids[len(ids)-cap:]
	}
	for _, id := range ids {
		d.seen[id] = struct{}{}
		d.order = append(d.order, id)
	}
	return d, nil
}

// first records id and reports whether it had never been seen. The composite
// id the caller builds (adapter + ":" + platform id) keeps two platforms'
// identical-looking ids apart.
func (d *deduper) first(id string) bool {
	if id == "" {
		return true // a platform that sends no ids cannot be deduped
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[id]; ok {
		return false
	}
	d.seen[id] = struct{}{}
	d.order = append(d.order, id)
	if len(d.order) > d.cap {
		old := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, old)
	}
	return true
}

// Close persists the ring. Best effort: losing it means the next run may
// answer one redelivered message twice, which is recoverable; failing the
// shutdown over it is not a trade worth making.
func (d *deduper) Close() error {
	if d.path == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	raw, err := json.MarshalIndent(d.order, "", "")
	if err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.path)
}
