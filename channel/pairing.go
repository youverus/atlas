package channel

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Pairing is how a stranger becomes a user without anyone hand-copying an id:
// the chat issues a short code, the operator runs `atlas-gateway approve <code>`
// on the machine the gateway runs on, and the code is exchanged for a user id
// in the runtime allowlist.
//
// PairingCodeTTL bounds a code's life: a code that lingers is an invitation
// someone forgot they printed.
const PairingCodeTTL = 30 * time.Minute

type pairingEntry struct {
	Adapter string `json:"adapter"`
	UserID  string `json:"user_id"`
	ChatID  string `json:"chat_id"`
	Created string `json:"created"`
}

type Pairing struct {
	mu      sync.Mutex
	path    string
	pending map[string]pairingEntry // code → entry
	mod     time.Time
}

func OpenPairing(path string) (*Pairing, error) {
	p := &Pairing{path: path, pending: map[string]pairingEntry{}}
	if err := p.reload(); err != nil {
		return nil, err
	}
	p.pruneLocked()
	return p, nil
}

func (p *Pairing) reload() error {
	info, err := os.Stat(p.path)
	if os.IsNotExist(err) {
		p.mod = time.Time{}
		p.pending = map[string]pairingEntry{}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.ModTime().After(p.mod) {
		return nil
	}
	raw, err := os.ReadFile(p.path)
	if err != nil {
		return err
	}
	pending := map[string]pairingEntry{}
	if err := json.Unmarshal(raw, &pending); err != nil {
		return fmt.Errorf("pairing %s: %w", p.path, err)
	}
	p.pending = pending
	p.mod = info.ModTime()
	return nil
}

// NewCode issues a pairing code for this user. Codes are six digits — short
// enough to type from a phone into a terminal, long enough that a half hour
// of guessing is not a strategy.
func (p *Pairing) NewCode(adapter, userID, chatID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.reload()
	p.pruneLocked()
	// Re-issue the same code for the same user mid-window: two messages in a
	// row from an unpaired user should not mean two codes to approve.
	for code, entry := range p.pending {
		if entry.Adapter == adapter && entry.UserID == userID {
			return code, nil
		}
	}
	var code string
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return "", err
		}
		code = fmt.Sprintf("%06d", n.Int64())
		if _, taken := p.pending[code]; !taken {
			break
		}
	}
	p.pending[code] = pairingEntry{
		Adapter: adapter,
		UserID:  userID,
		ChatID:  chatID,
		Created: time.Now().Format(time.RFC3339),
	}
	if err := p.saveLocked(); err != nil {
		return "", err
	}
	return code, nil
}

// PendingCode describes one unexpired pairing request awaiting operator approval.
type PendingCode struct {
	Code    string `json:"code"`
	Adapter string `json:"adapter"`
	UserID  string `json:"userId"`
	ChatID  string `json:"chatId"`
	Created string `json:"created"`
}

// List returns active unexpired pairing codes ordered newest first.
func (p *Pairing) List() []PendingCode {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.reload()
	before := len(p.pending)
	p.pruneLocked()
	if len(p.pending) != before {
		_ = p.saveLocked()
	}
	out := make([]PendingCode, 0, len(p.pending))
	for code, entry := range p.pending {
		out = append(out, PendingCode{
			Code:    code,
			Adapter: entry.Adapter,
			UserID:  entry.UserID,
			ChatID:  entry.ChatID,
			Created: entry.Created,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// Approve exchanges a code for the user it admitted, removing the code. The
// caller adds the returned user to the runtime allowlist.
func (p *Pairing) Approve(code string) (adapter, userID string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.reload()
	p.pruneLocked()
	entry, ok := p.pending[code]
	if !ok {
		return "", "", fmt.Errorf("配对码 %s 不存在或已过期", code)
	}
	delete(p.pending, code)
	if err := p.saveLocked(); err != nil {
		return "", "", err
	}
	return entry.Adapter, entry.UserID, nil
}

func (p *Pairing) pruneLocked() {
	now := time.Now()
	for code, entry := range p.pending {
		at, err := time.Parse(time.RFC3339, entry.Created)
		if err != nil || now.Sub(at) > PairingCodeTTL {
			delete(p.pending, code)
		}
	}
}

func (p *Pairing) saveLocked() error {
	raw, err := json.MarshalIndent(p.pending, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p.path); err != nil {
		return err
	}
	if info, err := os.Stat(p.path); err == nil {
		p.mod = info.ModTime()
	}
	return nil
}
