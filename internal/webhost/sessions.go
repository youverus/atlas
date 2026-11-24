package webhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"pi-go/internal/assembly"
	"pi-go/internal/httpapi"
	"pi-go/internal/wire"
)

type sessionSlot struct {
	live          *assembly.LiveSession
	mu            sync.Mutex
	sideInherited int
}

func (s *sessionSlot) setSideInherited(n int) {
	s.mu.Lock()
	s.sideInherited = n
	s.mu.Unlock()
}

func (s *sessionSlot) getSideInherited() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sideInherited
}

func seedSlots(kernel *assembly.Kernel) (string, map[string]*sessionSlot) {
	slots := make(map[string]*sessionSlot)
	if kernel == nil || kernel.App == nil {
		return "", slots
	}
	key := sessionSlotKey(kernel.App.Session().File())
	slots[key] = &sessionSlot{
		live: &assembly.LiveSession{App: kernel.App, Gate: kernel.Gate},
	}
	return key, slots
}

func sessionSlotKey(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func (h *Host) activeSlot() *sessionSlot {
	h.mu.RLock()
	slot := h.slots[h.activeKey]
	h.mu.RUnlock()
	if slot != nil {
		return slot
	}
	if h.kernel != nil && h.kernel.App != nil {
		return &sessionSlot{
			live: &assembly.LiveSession{App: h.kernel.App, Gate: h.kernel.Gate},
		}
	}
	return nil
}

func (h *Host) slotFor(sessionPath string) (*sessionSlot, error) {
	if sessionPath == "" {
		slot := h.activeSlot()
		if slot == nil || slot.live == nil || slot.live.App == nil {
			return nil, errors.New("webhost: no active session")
		}
		return slot, nil
	}
	h.mu.RLock()
	for key, slot := range h.slots {
		if slot != nil && slot.live != nil && slot.live.App != nil &&
			(key == sessionPath || sameSessionFile(slot.live.App.Session().File(), sessionPath)) {
			h.mu.RUnlock()
			return slot, nil
		}
	}
	h.mu.RUnlock()
	return nil, errors.New("webhost: session is not open")
}

// CurrentSession reports the file path key of the conversation currently in view.
func (h *Host) CurrentSession() string {
	slot := h.activeSlot()
	if slot == nil || slot.live == nil || slot.live.App == nil {
		return ""
	}
	return slot.live.App.Session().File()
}

// SessionAsk runs one prompt on the named session without blocking other sessions.
func (h *Host) SessionAsk(ctx context.Context, sessionPath, prompt string, emit func(wire.AgentEvent) error) (wire.Usage, error) {
	return h.SessionAskWithAttachments(ctx, sessionPath, prompt, nil, emit)
}

// SessionAskWithAttachments builds a multimodal prompt from attachments (images,
// PDFs, and text files) and runs one turn on the named session.
func (h *Host) SessionAskWithAttachments(ctx context.Context, sessionPath, prompt string, attachments []httpapi.Attachment, emit func(wire.AgentEvent) error) (wire.Usage, error) {
	slot, err := h.slotFor(sessionPath)
	if err != nil {
		return wire.Usage{}, err
	}
	items := make([]assembly.AttachmentInput, 0, len(attachments))
	for _, a := range attachments {
		items = append(items, assembly.AttachmentInput{
			Kind:     a.Kind,
			Name:     a.Name,
			MIMEType: a.MIMEType,
			Data:     a.Data,
		})
	}
	attachDir := filepath.Join(slot.live.App.Session().Dir(), assembly.AttachmentDirName)
	msg, err := assembly.BuildUserMessageWithAttachments(ctx, prompt, items, attachDir)
	if err != nil {
		return wire.Usage{}, err
	}
	result, err := slot.live.App.AskInteractive(ctx, msg, wire.SubagentRoles(prompt), emit)
	return result.Usage, err
}

// SubagentResultsPending reports whether an open session can receive a result.
func (h *Host) SubagentResultsPending(sessionPath string) bool {
	slot, err := h.slotFor(sessionPath)
	return err == nil && slot.live.App.HasSubagentResults()
}

// RunSubagentResults runs a completion turn in the originating session.
func (h *Host) RunSubagentResults(ctx context.Context, sessionPath string, emit func(wire.AgentEvent) error) (httpapi.Receipt, error) {
	slot, err := h.slotFor(sessionPath)
	if err != nil {
		return httpapi.Receipt{}, err
	}
	_, err = slot.live.App.ProcessSubagentResults(ctx, emit)
	return httpapi.Receipt{}, err
}

// StopSubagent cancels only tasks owned by the named session.
func (h *Host) StopSubagent(sessionPath, id string) error {
	slot, err := h.slotFor(sessionPath)
	if err != nil {
		return err
	}
	return h.kernel.StopSubagent(slot.live.App.SessionID(), id)
}

// SessionCompact runs one manual compaction on the named session.
func (h *Host) SessionCompact(ctx context.Context, sessionPath, note string, emit func(wire.AgentEvent) error) error {
	slot, err := h.slotFor(sessionPath)
	if err != nil {
		return err
	}
	_, err = slot.live.App.Compact(ctx, strings.TrimSpace(note), emit)
	return err
}

// SessionSideAsk runs one prompt on the named session's side conversation.
func (h *Host) SessionSideAsk(ctx context.Context, sessionPath, prompt string, emit func(wire.AgentEvent) error) (wire.Usage, error) {
	slot, err := h.slotFor(sessionPath)
	if err != nil {
		return wire.Usage{}, err
	}
	side := slot.live.App.Side()
	if side == nil {
		return wire.Usage{}, errors.New("no side conversation is open")
	}
	result, err := side.Ask(ctx, wire.UserMessage(prompt), emit)
	return result.Usage, err
}

func (h *Host) activateSlot(live *assembly.LiveSession) string {
	key := sessionSlotKey(live.App.Session().File())
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneEmptySlotsLocked(key)
	if existing, ok := h.slots[key]; ok && existing != nil {
		h.activeKey = key
		return key
	}
	h.slots[key] = &sessionSlot{live: live}
	h.activeKey = key
	return key
}

func (h *Host) findSlotKeyLocked(path string) (string, *sessionSlot, bool) {
	if slot, ok := h.slots[sessionSlotKey(path)]; ok && slot != nil {
		return sessionSlotKey(path), slot, true
	}
	for key, slot := range h.slots {
		if slot != nil && slot.live != nil && slot.live.App != nil && sameSessionFile(slot.live.App.Session().File(), path) {
			return key, slot, true
		}
	}
	return "", nil, false
}

// pruneEmptySlotsLocked drops unpersisted, idle 0-entry slots other than keepKey
// so switching back and forth to "new conversation" does not accumulate dead slots.
func (h *Host) pruneEmptySlotsLocked(keepKey string) {
	for key, slot := range h.slots {
		if key == keepKey || slot == nil || slot.live == nil || slot.live.App == nil {
			continue
		}
		if slot.live.App.Busy() {
			continue
		}
		manager := slot.live.App.Session()
		if manager.EntryCount() > 0 {
			continue
		}
		if _, err := os.Stat(manager.File()); err == nil {
			continue
		}
		delete(h.slots, key)
	}
}

func (h *Host) rekeyActiveSlotLocked(oldKey string, slot *sessionSlot) {
	newKey := sessionSlotKey(slot.live.App.Session().File())
	if oldKey != "" && oldKey != newKey {
		delete(h.slots, oldKey)
	}
	h.slots[newKey] = slot
	h.activeKey = newKey
}
