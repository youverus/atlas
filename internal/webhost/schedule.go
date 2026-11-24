package webhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"pi-go/internal/assembly"
	"pi-go/internal/wire"
)

// RunScheduledTask executes one background scheduled prompt against either a
// fresh session in cwd or the task's previous session file (when sessionMode is
// "continue" and lastSession still exists), optionally overriding the model.
func (h *Host) RunScheduledTask(ctx context.Context, prompt, model, cwd, sessionMode, lastSession string) (string, string, error) {
	if h == nil || h.kernel == nil || !h.kernel.CanSpawnSessions() {
		return "", "", errors.New("webhost: current build cannot run scheduled sessions")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", "", errors.New("webhost: scheduled prompt is empty")
	}

	targetPath := ""
	targetCWD := strings.TrimSpace(cwd)
	if targetCWD == "" {
		targetCWD = h.startupDir()
	}

	if sessionMode == "continue" && strings.TrimSpace(lastSession) != "" {
		if _, err := os.Stat(lastSession); err == nil {
			targetPath = lastSession
			targetCWD = ""
		}
	}

	if targetPath != "" {
		if err := h.ensureSlotIdle(targetPath); err != nil {
			return "", "", err
		}
	}

	live, err := h.kernel.OpenLiveSession(targetPath, targetCWD)
	if err != nil {
		return targetPath, "", err
	}

	if strings.TrimSpace(model) != "" {
		if err := applySessionModel(live, model); err != nil {
			return targetPath, "", err
		}
	}

	var answer strings.Builder
	_, askErr := live.App.Ask(ctx, wire.UserMessage(prompt), func(ev wire.AgentEvent) error {
		if ev.AssistantEvent != nil && ev.AssistantEvent.Type == "text_delta" {
			answer.WriteString(ev.AssistantEvent.Delta)
		}
		return nil
	})

	sessionFile := ""
	if live.App.Session() != nil {
		sessionFile = live.App.Session().File()
	}
	if sessionFile == "" {
		sessionFile = targetPath
	}

	if sessionFile != "" {
		h.syncScheduledSlot(sessionFile)
	}

	return sessionFile, strings.TrimSpace(answer.String()), askErr
}

func (h *Host) ensureSlotIdle(sessionPath string) error {
	h.mu.RLock()
	_, slot, ok := h.findSlotKeyLocked(sessionPath)
	h.mu.RUnlock()
	if ok && slot != nil && slot.live != nil && slot.live.App != nil && slot.live.App.Busy() {
		return errors.New("上一次会话正在主窗口运行中，本次跳过续跑")
	}
	return nil
}

func (h *Host) syncScheduledSlot(sessionFile string) {
	h.mu.Lock()
	key, slot, ok := h.findSlotKeyLocked(sessionFile)
	if !ok || slot == nil || slot.live == nil || slot.live.App == nil || slot.live.App.Busy() {
		h.mu.Unlock()
		return
	}
	if key != h.activeKey {
		delete(h.slots, key)
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	if refreshed, err := h.kernel.OpenLiveSession(sessionFile, ""); err == nil {
		h.mu.Lock()
		if current := h.slots[key]; current != nil && current.live != nil && current.live.App != nil && !current.live.App.Busy() {
			current.live = refreshed
		}
		h.mu.Unlock()
	}
}

func applySessionModel(live *assembly.LiveSession, spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" || live == nil || live.App == nil {
		return nil
	}
	providerName, modelID, hasSlash := strings.Cut(spec, "/")
	if !hasSlash {
		for _, opt := range live.App.Models() {
			if opt.Model.ID == spec {
				providerName = opt.Model.Provider
				modelID = opt.Model.ID
				hasSlash = true
				break
			}
		}
	}
	if !hasSlash || providerName == "" || modelID == "" {
		return fmt.Errorf("未找到模型 %s", spec)
	}
	_, err := live.App.SetModel(providerName, modelID)
	return err
}
