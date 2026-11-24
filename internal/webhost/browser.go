package webhost

import (
	"context"
	"time"

	"pi-go/internal/assembly"
	"pi-go/internal/httpapi"
)

const browserOpTimeout = 35 * time.Second

// BrowserState reports Chromium availability, running state, and managed tabs
// without launching Chromium or interrupting running sessions.
func (h *Host) BrowserState() (httpapi.BrowserState, error) {
	snap := h.kernel.BrowserSnapshot()
	tabs := make([]httpapi.BrowserTabInfo, len(snap.Tabs))
	for i, tab := range snap.Tabs {
		tabs[i] = mapBrowserTabInfo(tab)
	}
	return httpapi.BrowserState{
		Available:  snap.Available,
		Executable: snap.Executable,
		ProfileDir: snap.ProfileDir,
		Running:    snap.Running,
		ActiveTab:  snap.ActiveTab,
		Tabs:       tabs,
	}, nil
}

// BrowserOpen opens url (or search phrase) as a user-initiated tab in the managed browser.
func (h *Host) BrowserOpen(url string) (httpapi.BrowserTabInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), browserOpTimeout)
	defer cancel()
	tab, err := h.kernel.BrowserOpen(ctx, url)
	if err != nil {
		return httpapi.BrowserTabInfo{}, err
	}
	return mapBrowserTabInfo(tab), nil
}

// BrowserNavigate navigates tabID to url on behalf of the user.
func (h *Host) BrowserNavigate(tabID, url string) (httpapi.BrowserTabInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), browserOpTimeout)
	defer cancel()
	tab, err := h.kernel.BrowserNavigate(ctx, tabID, url)
	if err != nil {
		return httpapi.BrowserTabInfo{}, err
	}
	return mapBrowserTabInfo(tab), nil
}

// BrowserAction executes a control action on a managed browser tab.
func (h *Host) BrowserAction(tabID, action string) error {
	ctx, cancel := context.WithTimeout(context.Background(), browserOpTimeout)
	defer cancel()
	return h.kernel.BrowserAction(ctx, tabID, action)
}

// BrowserScreenshot captures a viewport JPEG screenshot for GUI preview.
func (h *Host) BrowserScreenshot(tabID string) (httpapi.BrowserCapture, error) {
	ctx, cancel := context.WithTimeout(context.Background(), browserOpTimeout)
	defer cancel()
	shot, err := h.kernel.BrowserScreenshot(ctx, tabID)
	if err != nil {
		return httpapi.BrowserCapture{}, err
	}
	return httpapi.BrowserCapture{
		TabID:  shot.TabID,
		MIME:   shot.MIME,
		Base64: shot.Base64,
		Width:  shot.Width,
		Height: shot.Height,
	}, nil
}

func mapBrowserTabInfo(tab assembly.BrowserTabInfo) httpapi.BrowserTabInfo {
	return httpapi.BrowserTabInfo{
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
