package assembly

import (
	"context"
	"fmt"
	"strings"

	"pi-go/internal/browser"
	"pi-go/internal/tool"
	browsertool "pi-go/internal/tool/browser"
)

// BrowserTabInfo describes one managed Chromium tab for front-end adapters.
type BrowserTabInfo struct {
	ID           string
	TargetID     string
	URL          string
	Title        string
	Generation   int
	Loading      bool
	CanGoBack    bool
	CanGoForward bool
	Control      string
	Paused       bool
	Active       bool
	CreatedAt    int64
}

// BrowserState describes the current managed Chromium availability and tabs.
type BrowserState struct {
	Available  bool
	Executable string
	ProfileDir string
	Running    bool
	ActiveTab  string
	Tabs       []BrowserTabInfo
}

// BrowserCapture carries one viewport screenshot for UI previews.
type BrowserCapture struct {
	TabID  string
	MIME   string
	Base64 string
	Width  int
	Height int
}

// BrowserTools returns the Stage-1 browser tools when a Chromium executable is available.
func BrowserTools(svc *browser.Service) []tool.Tool {
	if svc == nil || !svc.State().Available {
		return nil
	}
	return browsertool.Tools(svc)
}

// Browser returns the managed browser service owned by the kernel.
func (k *Kernel) Browser() *browser.Service {
	if k == nil {
		return nil
	}
	return k.browser
}

// BrowserSnapshot returns the current browser state without starting Chromium.
func (k *Kernel) BrowserSnapshot() BrowserState {
	if k == nil || k.browser == nil {
		return BrowserState{}
	}
	state := k.browser.State()
	tabs := make([]BrowserTabInfo, len(state.Tabs))
	for i, tab := range state.Tabs {
		tabs[i] = mapBrowserTab(tab)
	}
	return BrowserState{
		Available:  state.Available,
		Executable: state.Executable,
		ProfileDir: state.ProfileDir,
		Running:    state.Running,
		ActiveTab:  state.ActiveTab,
		Tabs:       tabs,
	}
}

// BrowserOpen opens url (or search phrase) as a user-initiated tab in the managed browser.
func (k *Kernel) BrowserOpen(ctx context.Context, rawURL string) (BrowserTabInfo, error) {
	if k == nil || k.browser == nil {
		return BrowserTabInfo{}, fmt.Errorf("browser service unavailable")
	}
	tab, err := k.browser.OpenTab(ctx, rawURL, true, true)
	if err != nil {
		return BrowserTabInfo{}, err
	}
	return mapBrowserTab(tab), nil
}

// BrowserNavigate navigates tabID to rawURL on behalf of the user.
func (k *Kernel) BrowserNavigate(ctx context.Context, tabID, rawURL string) (BrowserTabInfo, error) {
	if k == nil || k.browser == nil {
		return BrowserTabInfo{}, fmt.Errorf("browser service unavailable")
	}
	tab, err := k.browser.Navigate(ctx, tabID, rawURL, true)
	if err != nil {
		return BrowserTabInfo{}, err
	}
	return mapBrowserTab(tab), nil
}

// BrowserAction executes a control action (activate, back, forward, reload, stop,
// close, close_all, takeover, resume) on a managed browser tab.
func (k *Kernel) BrowserAction(ctx context.Context, tabID, action string) error {
	if k == nil || k.browser == nil {
		return fmt.Errorf("browser service unavailable")
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "activate":
		return k.browser.ActivateTab(ctx, tabID)
	case "back":
		_, err := k.browser.GoBack(ctx, tabID)
		return err
	case "forward":
		_, err := k.browser.GoForward(ctx, tabID)
		return err
	case "reload":
		_, err := k.browser.Reload(ctx, tabID)
		return err
	case "stop":
		return k.browser.Stop(ctx, tabID)
	case "close":
		return k.browser.CloseTab(ctx, tabID)
	case "close_all":
		return k.browser.CloseAllTabs(ctx)
	case "takeover":
		return k.browser.TakeOver(ctx, tabID)
	case "resume":
		return k.browser.ResumeAgent(tabID)
	default:
		return fmt.Errorf("unknown browser action %q", action)
	}
}

// BrowserScreenshot captures a JPEG viewport preview of tabID (or the active tab).
func (k *Kernel) BrowserScreenshot(ctx context.Context, tabID string) (BrowserCapture, error) {
	if k == nil || k.browser == nil {
		return BrowserCapture{}, fmt.Errorf("browser service unavailable")
	}
	target := strings.TrimSpace(tabID)
	if target == "" {
		state := k.browser.State()
		target = state.ActiveTab
		if target == "" && len(state.Tabs) > 0 {
			target = state.Tabs[0].ID
		}
	}
	shot, err := k.browser.Screenshot(ctx, browser.ScreenshotInput{
		TabID:   target,
		Mode:    "viewport",
		Format:  "jpeg",
		Quality: 72,
	})
	if err != nil {
		return BrowserCapture{}, err
	}
	return BrowserCapture{
		TabID:  shot.TabID,
		MIME:   shot.MIME,
		Base64: shot.Base64,
		Width:  shot.Width,
		Height: shot.Height,
	}, nil
}

func mapBrowserTab(tab browser.TabInfo) BrowserTabInfo {
	return BrowserTabInfo{
		ID:           tab.ID,
		TargetID:     tab.TargetID,
		URL:          tab.URL,
		Title:        tab.Title,
		Generation:   tab.Generation,
		Loading:      tab.Loading,
		CanGoBack:    tab.CanGoBack,
		CanGoForward: tab.CanGoForward,
		Control:      tab.Control,
		Paused:       tab.Paused,
		Active:       tab.Active,
		CreatedAt:    tab.CreatedAt,
	}
}
