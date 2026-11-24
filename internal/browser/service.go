package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultActionTimeout = 20 * time.Second
	defaultNavTimeout    = 30 * time.Second
	maxTabsLimit         = 20
	maxScreenshotBytes   = 12 * 1024 * 1024
)

// Options configures a managed Browser Service.
type Options struct {
	Executable          string
	ProfileDir          string
	Headless            bool
	HeadlessOverride    *bool
	AllowPrivateNetwork bool
}

// State is the top-level runtime snapshot shown to UI and CLI callers.
type State struct {
	Available  bool      `json:"available"`
	Executable string    `json:"executable,omitempty"`
	ProfileDir string    `json:"profileDir"`
	Running    bool      `json:"running"`
	Headless   bool      `json:"headless"`
	ActiveTab  string    `json:"activeTab,omitempty"`
	Tabs       []TabInfo `json:"tabs"`
}

// TabInfo describes one managed Chromium page target.
type TabInfo struct {
	ID           string `json:"id"`
	TargetID     string `json:"targetId"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Generation   int    `json:"generation"`
	Loading      bool   `json:"loading"`
	CanGoBack    bool   `json:"canGoBack"`
	CanGoForward bool   `json:"canGoForward"`
	Control      string `json:"control"`
	Paused       bool   `json:"paused,omitempty"`
	Active       bool   `json:"active"`
	CreatedAt    int64  `json:"createdAt"`
}

// TabSummary is the compact tab descriptor included in inspect responses.
type TabSummary struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Generation int    `json:"generation"`
	Loading    bool   `json:"loading"`
	Active     bool   `json:"active"`
}

// ClickResult reports the outcome of a click operation.
type ClickResult struct {
	OK         bool   `json:"ok"`
	Action     string `json:"action"`
	TabID      string `json:"tabId"`
	URL        string `json:"url"`
	Generation int    `json:"generation"`
	Navigated  bool   `json:"navigated,omitempty"`
}

type tabRecord struct {
	mu           sync.Mutex
	actionMu     sync.Mutex
	info         TabInfo
	cdpSessionID string
	snapshot     *storedSnapshot
	inflightReqs map[string]struct{}
	lastNetEvent time.Time
	loadNotify   chan struct{}
	cancelWait   context.CancelFunc
}

// Service owns the lifecycle of one managed Chromium process and its tabs.
type Service struct {
	opts        Options
	inspections *inspectionStore

	mu          sync.Mutex
	launched    *launchedBrowser
	cdp         *cdpClient
	unsub       func()
	tabs        map[string]*tabRecord
	byTarget    map[string]*tabRecord
	bySession   map[string]*tabRecord
	activeTabID string
	closed      bool
}

// NewService constructs a lazily started Browser Service. No Chromium process is
// spawned until a user or agent opens a tab.
func NewService(opts Options) *Service {
	if strings.TrimSpace(opts.ProfileDir) == "" {
		opts.ProfileDir = DefaultProfileDir()
	}
	return &Service{
		opts:        opts,
		inspections: newInspectionStore(),
		tabs:        make(map[string]*tabRecord),
		byTarget:    make(map[string]*tabRecord),
		bySession:   make(map[string]*tabRecord),
	}
}

// Configure updates executable and headless policy at runtime (e.g., on config reload).
// Changes to launch options apply on the next Chromium launch.
func (s *Service) Configure(opts Options) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.Executable = opts.Executable
	s.opts.Headless = opts.Headless
	s.opts.HeadlessOverride = opts.HeadlessOverride
	if strings.TrimSpace(opts.ProfileDir) != "" {
		s.opts.ProfileDir = opts.ProfileDir
	}
	if opts.AllowPrivateNetwork {
		s.opts.AllowPrivateNetwork = true
	}
}

func (s *Service) effectiveHeadlessLocked() bool {
	return resolveHeadless(s.opts, runtime.GOOS, os.Getenv)
}

// State reports browser binary availability, running status, and tracked tabs
// without spawning Chromium if it is not yet running.
func (s *Service) State() State {
	s.mu.Lock()
	exeOpt := strings.TrimSpace(s.opts.Executable)
	profileDir := s.opts.ProfileDir
	headless := s.effectiveHeadlessLocked()
	running := s.cdp != nil && s.launched != nil
	if running {
		select {
		case <-s.cdp.Done():
			running = false
		default:
		}
	}
	activeTab := s.activeTabID
	tabs := s.listLocked()
	s.mu.Unlock()

	exe := exeOpt
	if exe == "" {
		exe = FindExecutable()
	}
	return State{
		Available:  exe != "",
		Executable: exe,
		ProfileDir: profileDir,
		Running:    running,
		Headless:   headless,
		ActiveTab:  activeTab,
		Tabs:       tabs,
	}
}

// Close shuts down the CDP connection and terminates the managed Chromium child.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	cdp := s.cdp
	launched := s.launched
	unsub := s.unsub
	s.cdp = nil
	s.launched = nil
	s.unsub = nil
	tabs := maps.Clone(s.tabs)
	clear(s.tabs)
	clear(s.byTarget)
	clear(s.bySession)
	s.activeTabID = ""
	s.mu.Unlock()

	for _, tab := range tabs {
		tab.mu.Lock()
		if tab.cancelWait != nil {
			tab.cancelWait()
		}
		tab.mu.Unlock()
	}
	if unsub != nil {
		unsub()
	}
	if cdp != nil {
		_ = cdp.Call(context.Background(), "", "Browser.close", nil, nil)
		_ = cdp.Close()
	}
	if launched != nil {
		launched.Close()
	}
	return nil
}

func (s *Service) ensureRunning(ctx context.Context) (*cdpClient, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("browser: service is closed")
	}
	if s.cdp != nil && s.launched != nil {
		select {
		case <-s.cdp.Done():
			s.resetDeadLocked()
		default:
			client := s.cdp
			s.mu.Unlock()
			return client, nil
		}
	}
	exe := s.opts.Executable
	profileDir := s.opts.ProfileDir
	headless := s.effectiveHeadlessLocked()
	s.mu.Unlock()

	launched, err := launchBrowser(ctx, LaunchOptions{
		Executable: exe,
		ProfileDir: profileDir,
		Headless:   headless,
	})
	if err != nil {
		return nil, err
	}

	client, err := dialCDP(ctx, launched.wsURL)
	if err != nil {
		launched.Close()
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = client.Close()
		launched.Close()
		return nil, errors.New("browser: service is closed")
	}
	if s.cdp != nil {
		// Another goroutine won the race.
		existing := s.cdp
		s.mu.Unlock()
		_ = client.Close()
		launched.Close()
		return existing, nil
	}
	s.launched = launched
	s.cdp = client
	s.unsub = client.Subscribe(s.handleCDPEvent)
	s.mu.Unlock()

	if err := client.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		_ = client.Close()
		launched.Close()
		s.mu.Lock()
		s.resetDeadLocked()
		s.mu.Unlock()
		return nil, err
	}
	_ = s.syncExistingTargets(ctx, client)
	return client, nil
}

func (s *Service) resetDeadLocked() {
	if s.unsub != nil {
		s.unsub()
		s.unsub = nil
	}
	if s.cdp != nil {
		_ = s.cdp.Close()
		s.cdp = nil
	}
	if s.launched != nil {
		s.launched.Close()
		s.launched = nil
	}
	clear(s.tabs)
	clear(s.byTarget)
	clear(s.bySession)
	s.activeTabID = ""
}

func (s *Service) syncExistingTargets(ctx context.Context, client *cdpClient) error {
	var resp struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := client.Call(ctx, "", "Target.getTargets", nil, &resp); err != nil {
		return err
	}
	for _, info := range resp.TargetInfos {
		if info.Type != "page" {
			continue
		}
		_, _ = s.attachOrRegisterTarget(ctx, client, info.TargetID, info.URL, info.Title, "user", false)
	}
	return nil
}

func (s *Service) attachOrRegisterTarget(ctx context.Context, client *cdpClient, targetID, rawURL, title, control string, activate bool) (*tabRecord, error) {
	s.mu.Lock()
	if existing, ok := s.byTarget[targetID]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.mu.Unlock()

	var attachResp struct {
		SessionID string `json:"sessionId"`
	}
	if err := client.Call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": targetID,
		"flatten":  true,
	}, &attachResp); err != nil {
		return nil, err
	}

	_ = client.Call(ctx, attachResp.SessionID, "Page.enable", nil, nil)
	_ = client.Call(ctx, attachResp.SessionID, "Runtime.enable", nil, nil)
	_ = client.Call(ctx, attachResp.SessionID, "Network.enable", nil, nil)

	id := newUUID()
	if title == "" {
		title = "New tab"
	}
	rec := &tabRecord{
		cdpSessionID: attachResp.SessionID,
		inflightReqs: make(map[string]struct{}),
		loadNotify:   make(chan struct{}, 1),
		info: TabInfo{
			ID:        id,
			TargetID:  targetID,
			URL:       RedactURL(rawURL),
			Title:     title,
			Control:   control,
			Active:    activate,
			CreatedAt: time.Now().UnixMilli(),
		},
	}

	s.mu.Lock()
	if existing, ok := s.byTarget[targetID]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.tabs[id] = rec
	s.byTarget[targetID] = rec
	s.bySession[attachResp.SessionID] = rec
	if activate || s.activeTabID == "" {
		s.setActiveLocked(id)
	}
	s.mu.Unlock()
	return rec, nil
}

func (s *Service) setActiveLocked(tabID string) {
	s.activeTabID = tabID
	for id, rec := range s.tabs {
		rec.mu.Lock()
		rec.info.Active = id == tabID
		rec.mu.Unlock()
	}
}

func (s *Service) listLocked() []TabInfo {
	out := make([]TabInfo, 0, len(s.tabs))
	for _, rec := range s.tabs {
		rec.mu.Lock()
		out = append(out, rec.info)
		rec.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// ListTabs returns all tracked browser tabs ordered by creation time.
func (s *Service) ListTabs() []TabInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

// OpenTab opens a new managed browser tab and navigates to rawURL.
func (s *Service) OpenTab(ctx context.Context, rawURL string, userInitiated, activate bool) (TabInfo, error) {
	resolved, err := ResolveAddress(rawURL, userInitiated)
	if err != nil {
		return TabInfo{}, err
	}
	checked, err := CheckNavigationURL(ctx, resolved, URLPolicyOptions{
		UserInitiated:       userInitiated,
		AllowPrivateNetwork: s.opts.AllowPrivateNetwork,
	})
	if err != nil {
		return TabInfo{}, err
	}

	client, err := s.ensureRunning(ctx)
	if err != nil {
		return TabInfo{}, err
	}

	s.mu.Lock()
	tabCount := len(s.tabs)
	s.mu.Unlock()
	if tabCount >= maxTabsLimit {
		return TabInfo{}, fmt.Errorf("browser: maximum tab limit (%d) reached", maxTabsLimit)
	}

	control := "agent"
	if userInitiated {
		control = "user"
	}

	// Reuse a single initial blank tab created at Chromium launch if it has not navigated anywhere yet.
	var reuse *tabRecord
	s.mu.Lock()
	if len(s.tabs) == 1 {
		for _, candidate := range s.tabs {
			candidate.mu.Lock()
			if candidate.info.URL == "about:blank" && candidate.info.Generation == 0 && !candidate.info.Loading {
				reuse = candidate
			}
			candidate.mu.Unlock()
		}
	}
	s.mu.Unlock()

	var rec *tabRecord
	if reuse != nil {
		rec = reuse
		rec.mu.Lock()
		rec.info.Control = control
		rec.mu.Unlock()
		if activate {
			_ = s.ActivateTab(ctx, rec.info.ID)
		}
	} else {
		var createResp struct {
			TargetID string `json:"targetId"`
		}
		if err := client.Call(ctx, "", "Target.createTarget", map[string]any{
			"url":        "about:blank",
			"background": !activate,
		}, &createResp); err != nil {
			return TabInfo{}, err
		}
		rec, err = s.attachOrRegisterTarget(ctx, client, createResp.TargetID, "about:blank", "New tab", control, activate)
		if err != nil {
			return TabInfo{}, err
		}
	}

	if checked != "about:blank" {
		if _, err := s.navigateRecord(ctx, client, rec, checked, userInitiated); err != nil {
			return TabInfo{}, err
		}
	}
	if activate {
		_ = client.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": rec.info.TargetID}, nil)
		_ = client.Call(ctx, rec.cdpSessionID, "Page.bringToFront", nil, nil)
	}
	return snapshotTabInfo(rec), nil
}

// Navigate navigates an existing tab to rawURL.
func (s *Service) Navigate(ctx context.Context, tabID, rawURL string, userInitiated bool) (TabInfo, error) {
	resolved, err := ResolveAddress(rawURL, userInitiated)
	if err != nil {
		return TabInfo{}, err
	}
	checked, err := CheckNavigationURL(ctx, resolved, URLPolicyOptions{
		UserInitiated:       userInitiated,
		AllowPrivateNetwork: s.opts.AllowPrivateNetwork,
	})
	if err != nil {
		return TabInfo{}, err
	}
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	return s.navigateRecord(ctx, client, rec, checked, userInitiated)
}

func (s *Service) navigateRecord(ctx context.Context, client *cdpClient, rec *tabRecord, checkedURL string, userInitiated bool) (TabInfo, error) {
	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()

	if !userInitiated {
		if err := ensureAgentAllowed(rec); err != nil {
			return TabInfo{}, err
		}
	}
	navCtx, cancel := context.WithTimeout(ctx, defaultNavTimeout)
	defer cancel()

	rec.mu.Lock()
	rec.info.Loading = true
	if userInitiated {
		rec.info.Control = "user"
		rec.info.Paused = false
	} else {
		rec.info.Control = "agent"
	}
	drainNotify(rec.loadNotify)
	rec.mu.Unlock()

	var navResp struct {
		FrameID   string `json:"frameId"`
		ErrorText string `json:"errorText"`
	}
	if err := client.Call(navCtx, rec.cdpSessionID, "Page.navigate", map[string]any{"url": checkedURL}, &navResp); err != nil {
		rec.mu.Lock()
		rec.info.Loading = false
		rec.mu.Unlock()
		return TabInfo{}, err
	}
	if navResp.ErrorText != "" {
		rec.mu.Lock()
		rec.info.Loading = false
		rec.mu.Unlock()
		return TabInfo{}, fmt.Errorf("browser: navigation failed: %s", navResp.ErrorText)
	}

	if err := s.waitForLoadComplete(navCtx, client, rec); err != nil {
		return TabInfo{}, err
	}
	s.refreshMetadata(navCtx, client, rec)
	return snapshotTabInfo(rec), nil
}

// ActivateTab focuses a managed tab in Chromium and marks it active.
func (s *Service) ActivateTab(ctx context.Context, tabID string) error {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.setActiveLocked(tabID)
	s.mu.Unlock()
	if err := client.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": rec.info.TargetID}, nil); err != nil {
		return err
	}
	return client.Call(ctx, rec.cdpSessionID, "Page.bringToFront", nil, nil)
}

// TakeOver switches a tab to user control and cancels any in-flight agent wait.
func (s *Service) TakeOver(ctx context.Context, tabID string) error {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	rec.mu.Lock()
	rec.info.Control = "user"
	rec.info.Paused = true
	if rec.cancelWait != nil {
		rec.cancelWait()
	}
	rec.mu.Unlock()
	s.mu.Lock()
	s.setActiveLocked(tabID)
	s.mu.Unlock()
	_ = client.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": rec.info.TargetID}, nil)
	_ = client.Call(ctx, rec.cdpSessionID, "Page.bringToFront", nil, nil)
	return nil
}

// ResumeAgent hands control of a paused tab back to the Agent.
func (s *Service) ResumeAgent(tabID string) error {
	_, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	rec.mu.Lock()
	rec.info.Paused = false
	rec.info.Control = "agent"
	rec.mu.Unlock()
	return nil
}

// GoBack moves back one entry in the tab's navigation history.
func (s *Service) GoBack(ctx context.Context, tabID string) (TabInfo, error) {
	return s.navigateHistory(ctx, tabID, -1)
}

// GoForward moves forward one entry in the tab's navigation history.
func (s *Service) GoForward(ctx context.Context, tabID string) (TabInfo, error) {
	return s.navigateHistory(ctx, tabID, 1)
}

func (s *Service) navigateHistory(ctx context.Context, tabID string, delta int) (TabInfo, error) {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()

	var hist struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID    int    `json:"id"`
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := client.Call(ctx, rec.cdpSessionID, "Page.getNavigationHistory", nil, &hist); err != nil {
		return TabInfo{}, err
	}
	targetIdx := hist.CurrentIndex + delta
	if targetIdx < 0 || targetIdx >= len(hist.Entries) {
		return TabInfo{}, errors.New("browser: no navigation history entry in that direction")
	}

	rec.mu.Lock()
	rec.info.Loading = true
	drainNotify(rec.loadNotify)
	rec.mu.Unlock()

	if err := client.Call(ctx, rec.cdpSessionID, "Page.navigateToHistoryEntry", map[string]any{
		"entryId": hist.Entries[targetIdx].ID,
	}, nil); err != nil {
		rec.mu.Lock()
		rec.info.Loading = false
		rec.mu.Unlock()
		return TabInfo{}, err
	}
	navCtx, cancel := context.WithTimeout(ctx, defaultNavTimeout)
	defer cancel()
	if err := s.waitForLoadComplete(navCtx, client, rec); err != nil {
		return TabInfo{}, err
	}
	s.refreshMetadata(navCtx, client, rec)
	return snapshotTabInfo(rec), nil
}

// Reload reloads the tab and waits for load completion.
func (s *Service) Reload(ctx context.Context, tabID string) (TabInfo, error) {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()

	rec.mu.Lock()
	rec.info.Loading = true
	drainNotify(rec.loadNotify)
	rec.mu.Unlock()

	if err := client.Call(ctx, rec.cdpSessionID, "Page.reload", nil, nil); err != nil {
		rec.mu.Lock()
		rec.info.Loading = false
		rec.mu.Unlock()
		return TabInfo{}, err
	}
	navCtx, cancel := context.WithTimeout(ctx, defaultNavTimeout)
	defer cancel()
	if err := s.waitForLoadComplete(navCtx, client, rec); err != nil {
		return TabInfo{}, err
	}
	s.refreshMetadata(navCtx, client, rec)
	return snapshotTabInfo(rec), nil
}

// Stop aborts pending page navigation on tabID.
func (s *Service) Stop(ctx context.Context, tabID string) error {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	return client.Call(ctx, rec.cdpSessionID, "Page.stopLoading", nil, nil)
}

// CloseTab closes one browser tab and removes it from tracking.
func (s *Service) CloseTab(ctx context.Context, tabID string) error {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	var closeResp struct {
		Success bool `json:"success"`
	}
	if err := client.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": rec.info.TargetID}, &closeResp); err != nil {
		return err
	}
	s.removeTab(rec.info.TargetID)
	return nil
}

// CloseAllTabs closes every open tab in the managed browser.
func (s *Service) CloseAllTabs(ctx context.Context) error {
	tabs := s.ListTabs()
	for _, tab := range tabs {
		if err := s.CloseTab(ctx, tab.ID); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot collects visible interactive element refs and visible text from tabID.
func (s *Service) Snapshot(ctx context.Context, tabID string, maxNodes, maxTextChars int) (Snapshot, error) {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return Snapshot{}, err
	}
	if maxNodes <= 0 {
		maxNodes = defaultSnapshotMaxNodes
	}
	if maxNodes > 2000 {
		maxNodes = 2000
	}
	if maxTextChars <= 0 {
		maxTextChars = defaultSnapshotMaxText
	}
	if maxTextChars > 200000 {
		maxTextChars = 200000
	}

	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()
	return s.captureSnapshot(ctx, client, rec, maxNodes, maxTextChars)
}

// InspectInput configures a browser_inspect call.
type InspectInput struct {
	TabID             string
	SinceInspectionID string
	MaxNodes          int
	MaxTextChars      int
	IncludeScreenshot bool
	ScreenshotFormat  string
	ScreenshotQuality int
}

// Inspect atomically collects compact page state and refs, omitting unchanged
// snapshot details when sinceInspectionId matches the current state.
func (s *Service) Inspect(ctx context.Context, input InspectInput) (InspectResult, *ScreenshotResult, error) {
	tabID := strings.TrimSpace(input.TabID)
	if tabID == "" {
		s.mu.Lock()
		tabID = s.activeTabID
		if tabID == "" {
			for _, rec := range s.tabs {
				tabID = rec.info.ID
				break
			}
		}
		s.mu.Unlock()
	}
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return InspectResult{}, nil, err
	}

	maxNodes := input.MaxNodes
	if maxNodes <= 0 {
		maxNodes = defaultInspectMaxNodes
	}
	if maxNodes > 1000 {
		maxNodes = 1000
	}
	maxTextChars := input.MaxTextChars
	if maxTextChars <= 0 {
		maxTextChars = defaultInspectMaxTextChars
	}
	if maxTextChars > 60000 {
		maxTextChars = 60000
	}

	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()

	s.refreshMetadata(ctx, client, rec)
	snap, err := s.captureSnapshot(ctx, client, rec, maxNodes, maxTextChars)
	if err != nil {
		return InspectResult{}, nil, err
	}

	info := snapshotTabInfo(rec)
	hash := hashInspectState(info.Generation, snap.URL, snap.Title, info.Loading, &snap)
	inspectID, changed := s.inspections.Record(info.ID, info.Generation, hash, input.SinceInspectionID)

	var shot *ScreenshotResult
	shotTruncated := false
	if input.IncludeScreenshot && changed {
		captured, shotErr := s.captureScreenshot(ctx, client, rec, ScreenshotInput{
			TabID:   info.ID,
			Mode:    "viewport",
			Format:  input.ScreenshotFormat,
			Quality: input.ScreenshotQuality,
		})
		if shotErr != nil {
			shotTruncated = true
		} else {
			shot = &captured
		}
	}

	allTabs := s.ListTabs()
	summaries := make([]TabSummary, 0, min(len(allTabs), 20))
	for _, t := range allTabs {
		if len(summaries) >= 20 {
			break
		}
		summaries = append(summaries, TabSummary{
			ID:         t.ID,
			URL:        t.URL,
			Title:      t.Title,
			Generation: t.Generation,
			Loading:    t.Loading,
			Active:     t.Active,
		})
	}

	res := InspectResult{
		InspectionID: inspectID,
		TabID:        info.ID,
		Generation:   info.Generation,
		Tabs:         summaries,
		URL:          info.URL,
		Title:        info.Title,
		Loading:      info.Loading,
		Changed:      changed,
		Truncated: InspectTruncated{
			Text:       len(snap.Text) >= maxTextChars && snap.Truncated,
			Nodes:      len(snap.Nodes) >= maxNodes && snap.Truncated,
			Screenshot: shotTruncated,
			Tabs:       len(allTabs) > len(summaries),
		},
		UntrustedWebContent: true,
	}
	if changed {
		res.Snapshot = &snap
	}
	return res, shot, nil
}

// ScreenshotInput configures a screenshot capture.
type ScreenshotInput struct {
	TabID      string
	Mode       string
	Format     string
	Quality    int
	SnapshotID string
	Ref        string
	Generation int
}

// Screenshot captures the viewport, bounded full page, or one snapshot element.
func (s *Service) Screenshot(ctx context.Context, input ScreenshotInput) (ScreenshotResult, error) {
	client, rec, err := s.requireTab(input.TabID)
	if err != nil {
		return ScreenshotResult{}, err
	}
	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()
	return s.captureScreenshot(ctx, client, rec, input)
}

func (s *Service) captureScreenshot(ctx context.Context, client *cdpClient, rec *tabRecord, input ScreenshotInput) (ScreenshotResult, error) {
	mode := input.Mode
	if mode == "" {
		mode = "viewport"
	}
	format := input.Format
	if format == "" {
		format = "png"
	}
	if format != "png" && format != "jpeg" {
		return ScreenshotResult{}, fmt.Errorf("browser: unsupported screenshot format %q", format)
	}
	quality := input.Quality
	if quality <= 0 || quality > 100 {
		quality = 80
	}

	rec.mu.Lock()
	gen := rec.info.Generation
	snap := rec.snapshot
	rec.mu.Unlock()

	params := map[string]any{
		"format":      format,
		"fromSurface": true,
	}
	if format == "jpeg" {
		params["quality"] = quality
	}

	width, height := 0, 0
	switch mode {
	case "viewport":
		var vp struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		}
		if err := evalJSON(ctx, client, rec.cdpSessionID, 0, "({ width: Math.max(1, innerWidth), height: Math.max(1, innerHeight) })", &vp); err == nil {
			width, height = int(math.Ceil(vp.Width)), int(math.Ceil(vp.Height))
		}
	case "full-page":
		var metrics struct {
			CSSContentSize struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"cssContentSize"`
		}
		if err := client.Call(ctx, rec.cdpSessionID, "Page.getLayoutMetrics", nil, &metrics); err != nil {
			return ScreenshotResult{}, err
		}
		w := int(math.Ceil(metrics.CSSContentSize.Width))
		h := int(math.Ceil(metrics.CSSContentSize.Height))
		if w <= 0 || h <= 0 || h > 16384 {
			return ScreenshotResult{}, errors.New("browser: full-page dimensions exceed limits")
		}
		params["captureBeyondViewport"] = true
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": w, "height": h, "scale": 1}
		width, height = w, h
	case "element":
		if snap == nil || snap.id != input.SnapshotID || snap.generation != input.Generation || input.Generation != gen {
			return ScreenshotResult{}, errors.New("STALE_ELEMENT_REF: snapshot reference is stale; run browser_inspect again")
		}
		node, ok := snap.nodes[input.Ref]
		if !ok || node.Bounds == nil {
			return ScreenshotResult{}, errors.New("STALE_ELEMENT_REF: element bounds unavailable")
		}
		x := max(0, int(math.Floor(node.Bounds.X)))
		y := max(0, int(math.Floor(node.Bounds.Y)))
		w := max(1, int(math.Ceil(node.Bounds.Width)))
		h := max(1, int(math.Ceil(node.Bounds.Height)))
		params["clip"] = map[string]any{"x": x, "y": y, "width": w, "height": h, "scale": 1}
		width, height = w, h
	default:
		return ScreenshotResult{}, fmt.Errorf("browser: unsupported screenshot mode %q", mode)
	}

	var shotResp struct {
		Data string `json:"data"`
	}
	if err := client.Call(ctx, rec.cdpSessionID, "Page.captureScreenshot", params, &shotResp); err != nil {
		return ScreenshotResult{}, err
	}
	if len(shotResp.Data) > maxScreenshotBytes*4/3+16 {
		return ScreenshotResult{}, errors.New("browser: screenshot exceeds size limit")
	}
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
	}
	return ScreenshotResult{
		TabID:               rec.info.ID,
		MIME:                mime,
		Base64:              shotResp.Data,
		Width:               width,
		Height:              height,
		Mode:                mode,
		Generation:          gen,
		UntrustedWebContent: true,
	}, nil
}

// ClickInput identifies a snapshot element to click.
type ClickInput struct {
	TabID      string
	Ref        string
	SnapshotID string
	Generation int
	Button     string
	ClickCount int
	Modifiers  []string
}

// Click clicks a snapshot element ref and reports whether navigation happened.
func (s *Service) Click(ctx context.Context, input ClickInput) (ClickResult, error) {
	client, rec, err := s.requireTab(input.TabID)
	if err != nil {
		return ClickResult{}, err
	}
	button := input.Button
	if button == "" {
		button = "left"
	}
	clickCount := input.ClickCount
	if clickCount == 0 {
		clickCount = 1
	}
	if !slices.Contains([]string{"left", "middle", "right"}, button) || (clickCount != 1 && clickCount != 2) {
		return ClickResult{}, errors.New("browser: invalid click options")
	}
	modMask, err := modifierMask(input.Modifiers)
	if err != nil {
		return ClickResult{}, err
	}

	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()
	if err := ensureAgentAllowed(rec); err != nil {
		return ClickResult{}, err
	}

	frameID, offset, err := assertSnapshotRef(rec, input.SnapshotID, input.Ref, input.Generation)
	if err != nil {
		return ClickResult{}, err
	}

	worldID, err := s.frameContextID(ctx, client, rec.cdpSessionID, frameID)
	if err != nil {
		return ClickResult{}, err
	}
	var point *elementPointResult
	if err := evalJSON(ctx, client, rec.cdpSessionID, worldID, elementPointScript(input.SnapshotID, input.Ref, false), &point); err != nil {
		return ClickResult{}, err
	}
	if point == nil {
		return ClickResult{}, errors.New("STALE_ELEMENT_REF: element is no longer visible or enabled")
	}
	if point.ExternalURL != "" {
		return ClickResult{}, fmt.Errorf("browser: external protocol %q is blocked", point.ExternalURL)
	}

	x := point.X + offset.X
	y := point.Y + offset.Y
	rec.mu.Lock()
	beforeGen := rec.info.Generation
	drainNotify(rec.loadNotify)
	rec.mu.Unlock()

	if err := dispatchMouseClick(ctx, client, rec.cdpSessionID, x, y, button, clickCount, modMask); err != nil {
		return ClickResult{}, err
	}

	// Briefly wait to detect navigation triggered by the click.
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return ClickResult{}, ctx.Err()
	}

	rec.mu.Lock()
	navigating := rec.info.Loading || rec.info.Generation != beforeGen
	rec.mu.Unlock()
	if navigating {
		navCtx, cancel := context.WithTimeout(ctx, defaultNavTimeout)
		defer cancel()
		_ = s.waitForLoadComplete(navCtx, client, rec)
		s.refreshMetadata(navCtx, client, rec)
	}

	info := snapshotTabInfo(rec)
	return ClickResult{
		OK:         true,
		Action:     "clicked",
		TabID:      info.ID,
		URL:        info.URL,
		Generation: info.Generation,
		Navigated:  info.Generation != beforeGen,
	}, nil
}

// ClickAt dispatches a mouse click at explicit viewport coordinates.
func (s *Service) ClickAt(ctx context.Context, tabID string, x, y float64, button string, clickCount int, modifiers []string) (ClickResult, error) {
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return ClickResult{}, err
	}
	if button == "" {
		button = "left"
	}
	if clickCount == 0 {
		clickCount = 1
	}
	if x < 0 || y < 0 || !slices.Contains([]string{"left", "middle", "right"}, button) || (clickCount != 1 && clickCount != 2) {
		return ClickResult{}, errors.New("browser: invalid click coordinates or options")
	}
	modMask, err := modifierMask(modifiers)
	if err != nil {
		return ClickResult{}, err
	}

	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()
	if err := ensureAgentAllowed(rec); err != nil {
		return ClickResult{}, err
	}

	var vp struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := evalJSON(ctx, client, rec.cdpSessionID, 0, "({ width: innerWidth, height: innerHeight })", &vp); err == nil {
		if vp.Width > 0 && (x >= vp.Width || y >= vp.Height) {
			return ClickResult{}, errors.New("browser: coordinates are outside the viewport")
		}
	}

	if err := dispatchMouseClick(ctx, client, rec.cdpSessionID, x, y, button, clickCount, modMask); err != nil {
		return ClickResult{}, err
	}
	info := snapshotTabInfo(rec)
	return ClickResult{
		OK:         true,
		Action:     "clicked",
		TabID:      info.ID,
		URL:        info.URL,
		Generation: info.Generation,
	}, nil
}

// Type focuses a snapshot element, clears existing value, inserts text, and
// optionally presses Enter to submit.
func (s *Service) Type(ctx context.Context, tabID, snapshotID, ref string, generation int, text string, submit bool) error {
	if len(text) > 64*1024 || strings.ContainsRune(text, '\x00') {
		return errors.New("browser: text input is invalid")
	}
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()
	if err := ensureAgentAllowed(rec); err != nil {
		return err
	}

	frameID, offset, err := assertSnapshotRef(rec, snapshotID, ref, generation)
	if err != nil {
		return err
	}
	worldID, err := s.frameContextID(ctx, client, rec.cdpSessionID, frameID)
	if err != nil {
		return err
	}
	var point *elementPointResult
	if err := evalJSON(ctx, client, rec.cdpSessionID, worldID, elementPointScript(snapshotID, ref, true), &point); err != nil {
		return err
	}
	if point == nil {
		return errors.New("STALE_ELEMENT_REF: element is no longer editable")
	}

	if err := dispatchMouseClick(ctx, client, rec.cdpSessionID, point.X+offset.X, point.Y+offset.Y, "left", 1, 0); err != nil {
		return err
	}

	// Select all and clear existing input so typing replaces cleanly.
	selectMod := 2 // Ctrl
	if runtime.GOOS == "darwin" {
		selectMod = 4 // Meta/Command
	}
	_ = dispatchKey(ctx, client, rec.cdpSessionID, "a", "KeyA", 65, selectMod)
	_ = dispatchKey(ctx, client, rec.cdpSessionID, "Backspace", "Backspace", 8, 0)

	if text != "" {
		if err := client.Call(ctx, rec.cdpSessionID, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
			return err
		}
	}
	if submit {
		if err := dispatchKey(ctx, client, rec.cdpSessionID, "Enter", "Enter", 13, 0); err != nil {
			return err
		}
	}
	return nil
}

// Press sends an allowlisted keyboard key event to tabID.
func (s *Service) Press(ctx context.Context, tabID, key string, modifiers []string) error {
	spec, ok := lookupKeySpec(key)
	if !ok {
		return fmt.Errorf("browser: key %q is not allowed", key)
	}
	modMask, err := modifierMask(modifiers)
	if err != nil {
		return err
	}
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()
	if err := ensureAgentAllowed(rec); err != nil {
		return err
	}
	return dispatchKey(ctx, client, rec.cdpSessionID, spec.key, spec.code, spec.vk, modMask)
}

// Scroll dispatches a Chromium mouseWheel scroll event on tabID.
func (s *Service) Scroll(ctx context.Context, tabID string, deltaX, deltaY int, x, y *int) error {
	if deltaX == 0 && deltaY == 0 {
		return errors.New("browser: scroll delta is empty")
	}
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return err
	}
	rec.actionMu.Lock()
	defer rec.actionMu.Unlock()
	if err := ensureAgentAllowed(rec); err != nil {
		return err
	}

	px, py := 200.0, 200.0
	var vp struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := evalJSON(ctx, client, rec.cdpSessionID, 0, "({ width: Math.max(1, innerWidth), height: Math.max(1, innerHeight) })", &vp); err == nil {
		px = vp.Width / 2
		py = vp.Height / 2
	}
	if x != nil {
		px = float64(*x)
	}
	if y != nil {
		py = float64(*y)
	}
	return client.Call(ctx, rec.cdpSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseWheel",
		"x":      px,
		"y":      py,
		"deltaX": deltaX,
		"deltaY": deltaY,
	}, nil)
}

// Wait waits for load, network-idle, selector, or text condition on tabID.
func (s *Service) Wait(ctx context.Context, tabID, condition, value string, timeoutMs int) (TabInfo, error) {
	if condition == "" {
		condition = "load"
	}
	if timeoutMs <= 0 {
		timeoutMs = 15000
	}
	if timeoutMs > 120000 {
		timeoutMs = 120000
	}
	client, rec, err := s.requireTab(tabID)
	if err != nil {
		return TabInfo{}, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	rec.mu.Lock()
	if rec.cancelWait != nil {
		rec.cancelWait()
	}
	rec.cancelWait = cancel
	rec.mu.Unlock()
	defer func() {
		rec.mu.Lock()
		rec.cancelWait = nil
		rec.mu.Unlock()
	}()

	switch condition {
	case "load":
		if err := s.waitForLoadComplete(waitCtx, client, rec); err != nil {
			return TabInfo{}, err
		}
	case "network-idle":
		if err := s.waitForNetworkIdle(waitCtx, rec); err != nil {
			return TabInfo{}, err
		}
	case "selector":
		if strings.TrimSpace(value) == "" {
			return TabInfo{}, errors.New("browser: selector wait requires value")
		}
		arg, _ := json.Marshal(value)
		expr := fmt.Sprintf("Boolean(document.querySelector(%s))", string(arg))
		if err := pollCondition(waitCtx, client, rec.cdpSessionID, expr); err != nil {
			return TabInfo{}, err
		}
	case "text":
		if strings.TrimSpace(value) == "" {
			return TabInfo{}, errors.New("browser: text wait requires value")
		}
		arg, _ := json.Marshal(value)
		expr := fmt.Sprintf("(document.body?.innerText || '').includes(%s)", string(arg))
		if err := pollCondition(waitCtx, client, rec.cdpSessionID, expr); err != nil {
			return TabInfo{}, err
		}
	default:
		return TabInfo{}, fmt.Errorf("browser: unsupported wait condition %q", condition)
	}

	s.refreshMetadata(waitCtx, client, rec)
	return snapshotTabInfo(rec), nil
}

// --- Internal helpers ---

func (s *Service) requireTab(tabID string) (*cdpClient, *tabRecord, error) {
	if strings.TrimSpace(tabID) == "" {
		return nil, nil, errors.New("browser: tabId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cdp == nil {
		return nil, nil, errors.New("browser: Chromium is not running; call browser_open first")
	}
	rec, ok := s.tabs[tabID]
	if !ok {
		return nil, nil, fmt.Errorf("TAB_NOT_FOUND: browser tab %q does not exist", tabID)
	}
	return s.cdp, rec, nil
}

func (s *Service) removeTab(targetID string) {
	s.mu.Lock()
	rec, ok := s.byTarget[targetID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.byTarget, targetID)
	delete(s.tabs, rec.info.ID)
	delete(s.bySession, rec.cdpSessionID)
	if s.activeTabID == rec.info.ID {
		s.activeTabID = ""
		for id := range s.tabs {
			s.setActiveLocked(id)
			break
		}
	}
	s.mu.Unlock()
	s.inspections.ClearTab(rec.info.ID)
}

func snapshotTabInfo(rec *tabRecord) TabInfo {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.info
}

func ensureAgentAllowed(rec *tabRecord) error {
	rec.mu.Lock()
	paused := rec.info.Paused
	rec.mu.Unlock()
	if paused {
		return errors.New("USER_TOOK_CONTROL: User paused agent control on this tab; wait for resume or open a new tab")
	}
	return nil
}

func assertSnapshotRef(rec *tabRecord, snapshotID, ref string, generation int) (string, Rect, error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.snapshot == nil || rec.snapshot.id != snapshotID || rec.snapshot.generation != generation || rec.info.Generation != generation {
		return "", Rect{}, errors.New("STALE_ELEMENT_REF: page changed or snapshot expired; call browser_inspect again")
	}
	if _, ok := rec.snapshot.nodes[ref]; !ok {
		return "", Rect{}, fmt.Errorf("STALE_ELEMENT_REF: ref %q is not in the current snapshot", ref)
	}
	return rec.snapshot.frameIDs[ref], rec.snapshot.offsets[ref], nil
}

func (s *Service) handleCDPEvent(sessionID, method string, params json.RawMessage) {
	switch method {
	case "Target.targetCreated":
		var ev struct {
			TargetInfo struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				Title    string `json:"title"`
				URL      string `json:"url"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &ev) == nil && ev.TargetInfo.Type == "page" {
			go func() {
				s.mu.Lock()
				client := s.cdp
				s.mu.Unlock()
				if client != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_, _ = s.attachOrRegisterTarget(ctx, client, ev.TargetInfo.TargetID, ev.TargetInfo.URL, ev.TargetInfo.Title, "user", false)
				}
			}()
		}
		return
	case "Target.targetDestroyed":
		var ev struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(params, &ev) == nil && ev.TargetID != "" {
			s.removeTab(ev.TargetID)
		}
		return
	case "Target.targetInfoChanged":
		var ev struct {
			TargetInfo struct {
				TargetID string `json:"targetId"`
				Title    string `json:"title"`
				URL      string `json:"url"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &ev) == nil {
			s.mu.Lock()
			rec := s.byTarget[ev.TargetInfo.TargetID]
			s.mu.Unlock()
			if rec != nil {
				rec.mu.Lock()
				if ev.TargetInfo.Title != "" {
					rec.info.Title = ev.TargetInfo.Title
				}
				if ev.TargetInfo.URL != "" {
					rec.info.URL = RedactURL(ev.TargetInfo.URL)
				}
				rec.mu.Unlock()
			}
		}
		return
	}

	if sessionID == "" {
		return
	}
	s.mu.Lock()
	rec := s.bySession[sessionID]
	s.mu.Unlock()
	if rec == nil {
		return
	}

	switch method {
	case "Page.frameNavigated":
		var ev struct {
			Frame struct {
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
			} `json:"frame"`
		}
		if json.Unmarshal(params, &ev) == nil && ev.Frame.ParentID == "" {
			rec.mu.Lock()
			rec.info.URL = RedactURL(ev.Frame.URL)
			rec.info.Generation++
			rec.snapshot = nil
			rec.mu.Unlock()
			s.inspections.ClearTab(rec.info.ID)
		}
	case "Page.loadEventFired", "Page.frameStoppedLoading":
		rec.mu.Lock()
		rec.info.Loading = false
		signalNotify(rec.loadNotify)
		rec.mu.Unlock()
	case "Network.requestWillBeSent":
		var ev struct {
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(params, &ev) == nil && ev.RequestID != "" {
			rec.mu.Lock()
			rec.inflightReqs[ev.RequestID] = struct{}{}
			rec.lastNetEvent = time.Now()
			rec.mu.Unlock()
		}
	case "Network.loadingFinished", "Network.loadingFailed":
		var ev struct {
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(params, &ev) == nil && ev.RequestID != "" {
			rec.mu.Lock()
			delete(rec.inflightReqs, ev.RequestID)
			rec.lastNetEvent = time.Now()
			rec.mu.Unlock()
		}
	}
}

type frameEntry struct {
	id     string
	url    string
	offset Rect
}

func (s *Service) collectFrames(ctx context.Context, client *cdpClient, cdpSessionID string) []frameEntry {
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"frame"`
			ChildFrames []struct {
				Frame struct {
					ID  string `json:"id"`
					URL string `json:"url"`
				} `json:"frame"`
			} `json:"childFrames"`
		} `json:"frameTree"`
	}
	if err := client.Call(ctx, cdpSessionID, "Page.getFrameTree", nil, &tree); err != nil {
		return []frameEntry{{id: "", offset: Rect{}}}
	}
	frames := []frameEntry{{id: tree.FrameTree.Frame.ID, url: tree.FrameTree.Frame.URL}}
	for _, child := range tree.FrameTree.ChildFrames {
		var owner struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		var offset Rect
		if err := client.Call(ctx, cdpSessionID, "DOM.getFrameOwner", map[string]any{"frameId": child.Frame.ID}, &owner); err == nil && owner.BackendNodeID != 0 {
			var box struct {
				Model struct {
					Content []float64 `json:"content"`
				} `json:"model"`
			}
			if err := client.Call(ctx, cdpSessionID, "DOM.getBoxModel", map[string]any{"backendNodeId": owner.BackendNodeID}, &box); err == nil && len(box.Model.Content) >= 2 {
				offset.X = box.Model.Content[0]
				offset.Y = box.Model.Content[1]
			}
		}
		frames = append(frames, frameEntry{id: child.Frame.ID, url: child.Frame.URL, offset: offset})
	}
	return frames
}

func (s *Service) frameContextID(ctx context.Context, client *cdpClient, cdpSessionID, frameID string) (int, error) {
	if frameID == "" {
		return 0, nil
	}
	var res struct {
		ExecutionContextID int `json:"executionContextId"`
	}
	err := client.Call(ctx, cdpSessionID, "Page.createIsolatedWorld", map[string]any{
		"frameId":   frameID,
		"worldName": "atlas-browser",
	}, &res)
	if err != nil {
		return 0, nil
	}
	return res.ExecutionContextID, nil
}

func (s *Service) captureSnapshot(ctx context.Context, client *cdpClient, rec *tabRecord, maxNodes, maxTextChars int) (Snapshot, error) {
	snapshotID := newUUID()
	rec.mu.Lock()
	startGen := rec.info.Generation
	tabID := rec.info.ID
	tabURL := rec.info.URL
	tabTitle := rec.info.Title
	rec.mu.Unlock()

	frames := s.collectFrames(ctx, client, rec.cdpSessionID)
	nodes := make([]SnapshotNode, 0, min(maxNodes, 64))
	nodeMap := make(map[string]SnapshotNode)
	frameIDs := make(map[string]string)
	offsets := make(map[string]Rect)
	var textParts []string
	textLen := 0
	textTrunc := false
	nodesTrunc := false

	for idx, frame := range frames {
		if len(nodes) >= maxNodes || textLen >= maxTextChars {
			nodesTrunc = true
			textTrunc = true
			break
		}
		worldID, _ := s.frameContextID(ctx, client, rec.cdpSessionID, frame.id)
		var parsed rawSnapshotPayload
		script := createSnapshotScript(snapshotID, maxNodes-len(nodes), maxTextChars-textLen, len(nodes))
		if err := evalJSON(ctx, client, rec.cdpSessionID, worldID, script, &parsed); err != nil {
			continue
		}
		frameLabel := fmt.Sprintf("f%d", idx)
		for _, node := range parsed.Nodes {
			if node.Bounds != nil {
				b := *node.Bounds
				b.X += frame.offset.X
				b.Y += frame.offset.Y
				node.Bounds = &b
			}
			node.FrameID = frameLabel
			if frame.url != "" {
				node.FrameURL = RedactURL(frame.url)
			}
			nodes = append(nodes, node)
			nodeMap[node.Ref] = node
			frameIDs[node.Ref] = frame.id
			offsets[node.Ref] = frame.offset
		}
		if parsed.Text != "" {
			textParts = append(textParts, parsed.Text)
			textLen += len(parsed.Text) + 1
		}
		textTrunc = textTrunc || parsed.TextTruncated
		nodesTrunc = nodesTrunc || parsed.NodesTrunc
	}

	fullText := strings.Join(textParts, "\n")
	if len(fullText) > maxTextChars {
		fullText = fullText[:maxTextChars]
		textTrunc = true
	}

	rec.mu.Lock()
	if rec.info.Generation != startGen {
		rec.mu.Unlock()
		return Snapshot{}, errors.New("INSPECTION_STALE: page navigated while capturing snapshot")
	}
	rec.snapshot = &storedSnapshot{
		id:         snapshotID,
		generation: startGen,
		nodes:      nodeMap,
		frameIDs:   frameIDs,
		offsets:    offsets,
	}
	rec.mu.Unlock()

	return Snapshot{
		TabID:               tabID,
		SnapshotID:          snapshotID,
		Generation:          startGen,
		URL:                 tabURL,
		Title:               tabTitle,
		Text:                fullText,
		Nodes:               nodes,
		Truncated:           textTrunc || nodesTrunc,
		UntrustedWebContent: true,
	}, nil
}

func (s *Service) waitForLoadComplete(ctx context.Context, client *cdpClient, rec *tabRecord) error {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		var ready string
		if err := evalJSON(ctx, client, rec.cdpSessionID, 0, "document.readyState", &ready); err == nil && ready == "complete" {
			rec.mu.Lock()
			rec.info.Loading = false
			rec.mu.Unlock()
			return nil
		}
		select {
		case <-rec.loadNotify:
			return nil
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) waitForNetworkIdle(ctx context.Context, rec *tabRecord) error {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		rec.mu.Lock()
		inflight := len(rec.inflightReqs)
		last := rec.lastNetEvent
		rec.mu.Unlock()
		if inflight == 0 && (last.IsZero() || time.Since(last) >= 450*time.Millisecond) {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) refreshMetadata(ctx context.Context, client *cdpClient, rec *tabRecord) {
	var meta struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := evalJSON(ctx, client, rec.cdpSessionID, 0, "({ url: location.href, title: document.title })", &meta); err == nil {
		rec.mu.Lock()
		if meta.URL != "" {
			rec.info.URL = RedactURL(meta.URL)
		}
		if meta.Title != "" {
			rec.info.Title = meta.Title
		}
		rec.mu.Unlock()
	}
	var hist struct {
		CurrentIndex int   `json:"currentIndex"`
		Entries      []any `json:"entries"`
	}
	if err := client.Call(ctx, rec.cdpSessionID, "Page.getNavigationHistory", nil, &hist); err == nil {
		rec.mu.Lock()
		rec.info.CanGoBack = hist.CurrentIndex > 0
		rec.info.CanGoForward = hist.CurrentIndex >= 0 && hist.CurrentIndex < len(hist.Entries)-1
		rec.mu.Unlock()
	}
}

func pollCondition(ctx context.Context, client *cdpClient, cdpSessionID, expr string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var matched bool
		if err := evalJSON(ctx, client, cdpSessionID, 0, expr, &matched); err == nil && matched {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func evalJSON(ctx context.Context, client *cdpClient, cdpSessionID string, contextID int, expr string, out any) error {
	params := map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	if contextID != 0 {
		params["contextId"] = contextID
	}
	var resp struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := client.Call(ctx, cdpSessionID, "Runtime.evaluate", params, &resp); err != nil {
		return err
	}
	if resp.ExceptionDetails != nil {
		return fmt.Errorf("browser js error: %s", resp.ExceptionDetails.Text)
	}
	if out == nil || len(resp.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Result.Value, out)
}

func dispatchMouseClick(ctx context.Context, client *cdpClient, cdpSessionID string, x, y float64, button string, clickCount, modifiers int) error {
	if err := client.Call(ctx, cdpSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type":      "mouseMoved",
		"x":         x,
		"y":         y,
		"modifiers": modifiers,
	}, nil); err != nil {
		return err
	}
	if err := client.Call(ctx, cdpSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mousePressed",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
		"modifiers":  modifiers,
	}, nil); err != nil {
		return err
	}
	return client.Call(ctx, cdpSessionID, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mouseReleased",
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
		"modifiers":  modifiers,
	}, nil)
}

func dispatchKey(ctx context.Context, client *cdpClient, cdpSessionID, key, code string, vk, modifiers int) error {
	if err := client.Call(ctx, cdpSessionID, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyDown",
		"key":                   key,
		"code":                  code,
		"modifiers":             modifiers,
		"windowsVirtualKeyCode": vk,
		"nativeVirtualKeyCode":  vk,
	}, nil); err != nil {
		return err
	}
	return client.Call(ctx, cdpSessionID, "Input.dispatchKeyEvent", map[string]any{
		"type":                  "keyUp",
		"key":                   key,
		"code":                  code,
		"modifiers":             modifiers,
		"windowsVirtualKeyCode": vk,
		"nativeVirtualKeyCode":  vk,
	}, nil)
}

func modifierMask(modifiers []string) (int, error) {
	mask := 0
	for _, m := range modifiers {
		switch strings.ToLower(m) {
		case "alt":
			mask |= 1
		case "ctrl", "control":
			mask |= 2
		case "meta", "command", "cmd":
			mask |= 4
		case "shift":
			mask |= 8
		default:
			return 0, fmt.Errorf("browser: unsupported modifier %q", m)
		}
	}
	return mask, nil
}

type keySpec struct {
	key  string
	code string
	vk   int
}

var namedKeys = map[string]keySpec{
	"Enter":      {"Enter", "Enter", 13},
	"Tab":        {"Tab", "Tab", 9},
	"Escape":     {"Escape", "Escape", 27},
	"Backspace":  {"Backspace", "Backspace", 8},
	"Delete":     {"Delete", "Delete", 46},
	"ArrowUp":    {"ArrowUp", "ArrowUp", 38},
	"ArrowDown":  {"ArrowDown", "ArrowDown", 40},
	"ArrowLeft":  {"ArrowLeft", "ArrowLeft", 37},
	"ArrowRight": {"ArrowRight", "ArrowRight", 39},
	"Home":       {"Home", "Home", 36},
	"End":        {"End", "End", 35},
	"PageUp":     {"PageUp", "PageUp", 33},
	"PageDown":   {"PageDown", "PageDown", 34},
	"Space":      {" ", "Space", 32},
}

func lookupKeySpec(key string) (keySpec, bool) {
	if spec, ok := namedKeys[key]; ok {
		return spec, true
	}
	if len(key) == 1 {
		ch := key[0]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			upper := strings.ToUpper(key)
			return keySpec{key: key, code: "Key" + upper, vk: int(upper[0])}, true
		}
		if ch >= '0' && ch <= '9' {
			return keySpec{key: key, code: "Digit" + key, vk: int(ch)}, true
		}
	}
	if strings.HasPrefix(key, "F") && len(key) <= 3 {
		for n := 1; n <= 12; n++ {
			if key == fmt.Sprintf("F%d", n) {
				return keySpec{key: key, code: key, vk: 111 + n}, true
			}
		}
	}
	return keySpec{}, false
}

func signalNotify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func drainNotify(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
