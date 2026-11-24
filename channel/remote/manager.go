// Package remote manages embedded channel gateways (such as Feishu) inside
// the Atlas desktop/HTTP control plane.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pi-go/channel"
	"pi-go/channel/feishu"
	"pi-go/internal/config"
	"pi-go/internal/httpapi"
)

const maxRecentLogs = 40

// Options configures a remote gateway Manager.
type Options struct {
	ChannelsPath string // defaults to channel.ChannelsPath()
	DataDir      string // defaults to channel.DataPath()
	ConfigPath   string // Atlas config.json path passed to AssemblyRunner
	DefaultCWD   string // fallback cwd when neither channels.json nor binding specifies one
	Version      string // version label for AssemblyRunner
}

// Manager implements httpapi.RemoteController over channels.json and the live
// Feishu WebSocket gateway.
type Manager struct {
	channelsPath string
	dataDir      string
	configPath   string
	defaultCWD   string
	version      string

	logBuf *ringLogger
	logger *log.Logger

	mu         sync.Mutex
	allowlist  *channel.Allowlist
	pairing    *channel.Pairing
	store      *channel.BindingStore
	runner     *channel.AssemblyRunner
	gateway    *channel.Gateway
	runCancel  context.CancelFunc
	runDone    chan struct{}
	state      string // stopped | starting | running | error
	connection string // "" | connecting | connected | reconnecting | error
	lastError  string
	startedAt  time.Time
}

// NewManager opens the persistent channel stores (creating empty in-memory stores
// if files do not yet exist) and returns a ready Manager.
func NewManager(opts Options) (*Manager, error) {
	channelsPath := opts.ChannelsPath
	if channelsPath == "" {
		p, err := channel.ChannelsPath()
		if err != nil {
			return nil, err
		}
		channelsPath = p
	}
	dataDir := opts.DataDir
	if dataDir == "" {
		d, err := channel.DataPath()
		if err != nil {
			return nil, err
		}
		dataDir = d
	}

	ring := &ringLogger{cap: maxRecentLogs}
	logger := log.New(ring, "", 0)

	allowlist, err := channel.OpenAllowlist(filepath.Join(dataDir, "allowlist.json"))
	if err != nil {
		return nil, err
	}
	pairing, err := channel.OpenPairing(filepath.Join(dataDir, "pairing.json"))
	if err != nil {
		return nil, err
	}
	store, err := channel.OpenBindings(filepath.Join(dataDir, "bindings.json"), opts.DefaultCWD)
	if err != nil {
		return nil, err
	}

	return &Manager{
		channelsPath: channelsPath,
		dataDir:      dataDir,
		configPath:   opts.ConfigPath,
		defaultCWD:   opts.DefaultCWD,
		version:      opts.Version,
		logBuf:       ring,
		logger:       logger,
		allowlist:    allowlist,
		pairing:      pairing,
		store:        store,
		state:        "stopped",
	}, nil
}

// StartIfEnabled launches the gateway when channels.json marks Feishu enabled.
// Startup failures are recorded in LastError rather than crashing Atlas launch.
func (m *Manager) StartIfEnabled() {
	sections, err := channel.LoadSections(m.channelsPath)
	if err != nil {
		m.setErrorState(err.Error())
		return
	}
	raw, ok := sections["feishu"]
	if !ok {
		return
	}
	cfg, err := feishu.ParseRaw(raw)
	if err != nil {
		m.setErrorState(err.Error())
		return
	}
	if !cfg.Enabled {
		return
	}
	if _, err := m.StartRemote(); err != nil {
		m.setErrorState(err.Error())
		m.logger.Printf("自动启动飞书远程控制失败: %v", err)
	}
}

// Close stops any active gateway cleanly when Atlas exits.
func (m *Manager) Close() error {
	_, err := m.StopRemote()
	return err
}

func (m *Manager) setErrorState(msg string) {
	m.mu.Lock()
	m.state = "error"
	m.connection = "error"
	m.lastError = msg
	m.mu.Unlock()
}

// RemoteStatus returns the live gateway status, masked config, pending pairing
// codes, runtime allowlist entries, active bindings, and recent logs.
func (m *Manager) RemoteStatus() (httpapi.RemoteStatus, error) {
	return m.snapshot()
}

// SaveFeishuConfig updates the "feishu" section in channels.json while
// preserving other platform keys and optionally starting/restarting the gateway.
func (m *Manager) SaveFeishuConfig(req httpapi.FeishuConfigUpdate) (httpapi.RemoteStatus, error) {
	dmPolicy := normalizeChoice(req.DMPolicy, "pairing", "pairing", "allowlist", "open", "disabled")
	if dmPolicy == "" {
		return httpapi.RemoteStatus{}, fmt.Errorf("无效的私聊策略: %s", req.DMPolicy)
	}
	groupPolicy := normalizeChoice(req.GroupPolicy, "allowlist", "allowlist", "open", "admin_only", "disabled")
	if groupPolicy == "" {
		return httpapi.RemoteStatus{}, fmt.Errorf("无效的群聊策略: %s", req.GroupPolicy)
	}

	cwd := strings.TrimSpace(req.CWD)
	if cwd != "" {
		if expanded, err := config.ExpandHome(cwd); err == nil {
			cwd = expanded
		}
		info, err := os.Stat(cwd)
		if err != nil || !info.IsDir() {
			return httpapi.RemoteStatus{}, fmt.Errorf("工作目录不存在或不是文件夹: %s", cwd)
		}
	}

	sections, err := channel.LoadSections(m.channelsPath)
	if err != nil {
		return httpapi.RemoteStatus{}, err
	}
	if sections == nil {
		sections = channel.Section{}
	}

	var existing feishu.Config
	if raw, ok := sections["feishu"]; ok {
		existing, _ = feishu.ParseRaw(raw)
	}

	nextSecret := existing.AppSecret
	switch {
	case req.ClearSecret:
		nextSecret = ""
	case strings.TrimSpace(req.AppSecret) != "":
		candidate := strings.TrimSpace(req.AppSecret)
		if candidate != maskSecret(existing.AppSecret) {
			nextSecret = candidate
		}
	}

	requireMention := req.RequireMention
	nextCfg := feishu.Config{
		Enabled:        req.Enabled,
		AppID:          strings.TrimSpace(req.AppID),
		AppSecret:      nextSecret,
		DMPolicy:       dmPolicy,
		GroupPolicy:    groupPolicy,
		AllowedUsers:   cleanStringSlice(req.AllowedUsers),
		Groups:         existing.Groups,
		Admins:         cleanStringSlice(req.Admins),
		RequireMention: &requireMention,
		AllowBots:      req.AllowBots,
		CWD:            cwd,
	}

	rawCfg, err := json.Marshal(nextCfg)
	if err != nil {
		return httpapi.RemoteStatus{}, err
	}
	sections["feishu"] = rawCfg

	if err := saveSections(m.channelsPath, sections); err != nil {
		return httpapi.RemoteStatus{}, err
	}

	effectiveCWD := cwd
	if effectiveCWD == "" {
		effectiveCWD = m.defaultCWD
	}
	m.store.SetDefaultCWD(effectiveCWD)

	m.mu.Lock()
	wasRunning := m.gateway != nil
	m.mu.Unlock()

	if wasRunning || req.StartAfterSave {
		if wasRunning {
			if _, err := m.StopRemote(); err != nil {
				return httpapi.RemoteStatus{}, err
			}
		}
		if req.StartAfterSave || wasRunning {
			return m.StartRemote()
		}
	}
	return m.snapshot()
}

// StartRemote parses Feishu credentials and starts the live WebSocket gateway.
func (m *Manager) StartRemote() (httpapi.RemoteStatus, error) {
	sections, err := channel.LoadSections(m.channelsPath)
	if err != nil {
		return httpapi.RemoteStatus{}, err
	}
	raw, ok := sections["feishu"]
	if !ok {
		return httpapi.RemoteStatus{}, errors.New("请先填写并保存飞书 App ID 和 App Secret")
	}
	cfg, err := feishu.Parse(raw)
	if err != nil {
		return httpapi.RemoteStatus{}, err
	}

	m.mu.Lock()
	if m.gateway != nil {
		m.mu.Unlock()
		return m.snapshot()
	}

	effectiveCWD := cfg.CWD
	if effectiveCWD == "" {
		effectiveCWD = m.defaultCWD
	}
	if effectiveCWD != "" {
		if expanded, expErr := config.ExpandHome(effectiveCWD); expErr == nil {
			effectiveCWD = expanded
		}
	}
	m.store.SetDefaultCWD(effectiveCWD)

	dedupe, err := channel.OpenDedupe(filepath.Join(m.dataDir, "dedupe.json"), 8192)
	if err != nil {
		m.mu.Unlock()
		return httpapi.RemoteStatus{}, err
	}

	adapter, err := feishu.New(cfg, m.allowlist, m.pairing, m.logger)
	if err != nil {
		_ = dedupe.Close()
		m.mu.Unlock()
		return httpapi.RemoteStatus{}, err
	}

	gateway, err := channel.New(channel.Options{
		Adapters: []channel.Adapter{adapter},
		Store:    m.store,
		Dedupe:   dedupe,
		Logger:   m.logger,
	})
	if err != nil {
		_ = dedupe.Close()
		m.mu.Unlock()
		return httpapi.RemoteStatus{}, err
	}

	version := m.version
	if version == "" {
		version = "desktop"
	}
	runner := channel.NewAssemblyRunner(m.configPath, version, m.store, gateway, m.logger)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	m.gateway = gateway
	m.runner = runner
	m.runCancel = cancel
	m.runDone = done
	m.state = "running"
	m.connection = "connecting"
	m.lastError = ""
	m.startedAt = time.Now()
	m.mu.Unlock()

	adapter.SetStatusHandler(func(connState, detail string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.gateway != gateway {
			return
		}
		m.connection = connState
		if connState == "error" && detail != "" {
			m.lastError = detail
		} else if connState == "connected" {
			m.lastError = ""
		}
	})

	m.logger.Printf("飞书远程控制启动中（默认目录 %s）", effectiveCWD)

	go func() {
		defer close(done)
		defer func() { _ = runner.Close() }()
		startErr := gateway.Start(runCtx, runner)

		m.mu.Lock()
		defer m.mu.Unlock()
		if m.gateway == gateway {
			m.gateway = nil
			m.runner = nil
			m.runCancel = nil
			if startErr != nil && runCtx.Err() == nil {
				m.state = "error"
				m.connection = "error"
				m.lastError = startErr.Error()
				m.logger.Printf("飞书远程控制异常退出: %v", startErr)
			} else {
				m.state = "stopped"
				m.connection = ""
				m.logger.Printf("飞书远程控制已停止")
			}
		}
	}()

	return m.snapshot()
}

// StopRemote halts any active Feishu WebSocket gateway and closes runner kernels.
func (m *Manager) StopRemote() (httpapi.RemoteStatus, error) {
	m.mu.Lock()
	cancel := m.runCancel
	gw := m.gateway
	done := m.runDone
	if cancel != nil {
		cancel()
	}
	if gw != nil {
		gw.Close()
	}
	m.mu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			m.logger.Printf("停止飞书网关等待超时，后台将继续清理")
		}
	}

	return m.snapshot()
}

// ApprovePairing exchanges a 6-digit pairing code and admits the user to the runtime allowlist.
func (m *Manager) ApprovePairing(code string) (httpapi.RemoteStatus, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return httpapi.RemoteStatus{}, errors.New("请输入配对码")
	}
	adapter, userID, err := m.pairing.Approve(code)
	if err != nil {
		return httpapi.RemoteStatus{}, err
	}
	if err := m.allowlist.Add(adapter, userID); err != nil {
		return httpapi.RemoteStatus{}, err
	}
	m.logger.Printf("已批准 %s 用户 %s（配对码 %s）", adapter, userID, code)
	return m.snapshot()
}

// AllowRemoteUser manually admits userID for adapter into runtime allowlist.
func (m *Manager) AllowRemoteUser(adapter, userID string) (httpapi.RemoteStatus, error) {
	adapter = strings.TrimSpace(adapter)
	if adapter == "" {
		adapter = "feishu"
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return httpapi.RemoteStatus{}, errors.New("用户 ID 不能为空")
	}
	if err := m.allowlist.Add(adapter, userID); err != nil {
		return httpapi.RemoteStatus{}, err
	}
	m.logger.Printf("已手动添加 %s 白名单用户 %s", adapter, userID)
	return m.snapshot()
}

// RevokeRemoteUser removes userID from the runtime allowlist for adapter.
func (m *Manager) RevokeRemoteUser(adapter, userID string) (httpapi.RemoteStatus, error) {
	adapter = strings.TrimSpace(adapter)
	if adapter == "" {
		adapter = "feishu"
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return httpapi.RemoteStatus{}, errors.New("用户 ID 不能为空")
	}
	if err := m.allowlist.Remove(adapter, userID); err != nil {
		return httpapi.RemoteStatus{}, err
	}
	m.logger.Printf("已移除 %s 白名单用户 %s", adapter, userID)
	return m.snapshot()
}

// UnbindRemoteChat clears a chat binding and closes its live kernel slot if open.
func (m *Manager) UnbindRemoteChat(key string) (httpapi.RemoteStatus, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return httpapi.RemoteStatus{}, errors.New("会话绑定 key 不能为空")
	}
	m.mu.Lock()
	runner := m.runner
	m.mu.Unlock()
	if runner != nil {
		runner.Drop(key)
	}
	if err := m.store.Delete(key); err != nil {
		return httpapi.RemoteStatus{}, err
	}
	m.logger.Printf("已解除远程会话绑定 %s", key)
	return m.snapshot()
}

func (m *Manager) snapshot() (httpapi.RemoteStatus, error) {
	sections, err := channel.LoadSections(m.channelsPath)
	if err != nil {
		return httpapi.RemoteStatus{}, err
	}
	var cfg feishu.Config
	if raw, ok := sections["feishu"]; ok {
		cfg, err = feishu.ParseRaw(raw)
		if err != nil {
			return httpapi.RemoteStatus{}, err
		}
	} else {
		cfg, _ = feishu.ParseRaw(nil)
	}

	envID := strings.TrimSpace(os.Getenv("ATLAS_FEISHU_APP_ID"))
	envSecret := strings.TrimSpace(os.Getenv("ATLAS_FEISHU_APP_SECRET"))
	effectiveAppID := cfg.AppID
	if envID != "" {
		effectiveAppID = envID
	}

	secretMode := "unset"
	secretMasked := ""
	hasSecret := false
	switch {
	case envSecret != "":
		secretMode = "env"
		secretMasked = "环境变量 ATLAS_FEISHU_APP_SECRET"
		hasSecret = true
	case strings.HasPrefix(strings.TrimSpace(cfg.AppSecret), "env:"):
		secretMode = "env"
		secretMasked = strings.TrimSpace(cfg.AppSecret)
		hasSecret = true
	case cfg.AppSecret != "":
		secretMode = "literal"
		secretMasked = maskSecret(cfg.AppSecret)
		hasSecret = true
	}

	pendingRaw := m.pairing.List()
	pending := make([]httpapi.RemotePairingCode, 0, len(pendingRaw))
	for _, item := range pendingRaw {
		pending = append(pending, httpapi.RemotePairingCode{
			Code:    item.Code,
			Adapter: item.Adapter,
			UserID:  item.UserID,
			ChatID:  item.ChatID,
			Created: item.Created,
		})
	}

	allowMap := m.allowlist.List()
	allowEntries := make([]httpapi.RemoteAllowlistEntry, 0)
	for adapter, ids := range allowMap {
		for _, id := range ids {
			allowEntries = append(allowEntries, httpapi.RemoteAllowlistEntry{
				Adapter: adapter,
				UserID:  id,
			})
		}
	}
	sort.Slice(allowEntries, func(i, j int) bool {
		if allowEntries[i].Adapter != allowEntries[j].Adapter {
			return allowEntries[i].Adapter < allowEntries[j].Adapter
		}
		return allowEntries[i].UserID < allowEntries[j].UserID
	})

	bindingsMap := m.store.List()
	bindings := make([]httpapi.RemoteBinding, 0, len(bindingsMap))
	for key, b := range bindingsMap {
		bindings = append(bindings, httpapi.RemoteBinding{
			Key:     key,
			Name:    b.Name,
			CWD:     b.CWD,
			Session: b.Session,
			Created: b.Created,
		})
	}
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].Key < bindings[j].Key
	})

	m.mu.Lock()
	state := m.state
	conn := m.connection
	lastErr := m.lastError
	startedAt := ""
	if !m.startedAt.IsZero() && state == "running" {
		startedAt = m.startedAt.Format(time.RFC3339)
	}
	m.mu.Unlock()

	requireMention := true
	if cfg.RequireMention != nil {
		requireMention = *cfg.RequireMention
	}

	return httpapi.RemoteStatus{
		State:      state,
		Connection: conn,
		LastError:  lastErr,
		StartedAt:  startedAt,
		ConfigPath: m.channelsPath,
		DefaultCWD: m.defaultCWD,
		Feishu: httpapi.FeishuSettingsView{
			Enabled:        cfg.Enabled,
			AppID:          effectiveAppID,
			HasSecret:      hasSecret,
			SecretMasked:   secretMasked,
			SecretMode:     secretMode,
			EnvOverride:    envID != "" || envSecret != "",
			DMPolicy:       cfg.DMPolicy,
			GroupPolicy:    cfg.GroupPolicy,
			RequireMention: requireMention,
			AllowBots:      cfg.AllowBots,
			CWD:            cfg.CWD,
			Admins:         nonNilStrings(cfg.Admins),
			AllowedUsers:   nonNilStrings(cfg.AllowedUsers),
		},
		PendingCodes:     pending,
		RuntimeAllowlist: allowEntries,
		Bindings:         bindings,
		RecentLogs:       m.logBuf.Lines(),
	}, nil
}

func saveSections(path string, sections channel.Section) error {
	raw, err := json.MarshalIndent(sections, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func normalizeChoice(v, fallback string, allowed ...string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	for _, option := range allowed {
		if v == option {
			return v
		}
	}
	return ""
}

func cleanStringSlice(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return append([]string(nil), in...)
}

func maskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if strings.HasPrefix(secret, "env:") {
		return secret
	}
	if len(secret) <= 6 {
		return strings.Repeat("*", len(secret))
	}
	return secret[:2] + "••••••" + secret[len(secret)-4:]
}

type ringLogger struct {
	mu    sync.Mutex
	lines []string
	cap   int
}

func (r *ringLogger) Write(p []byte) (int, error) {
	trimmed := bytes.TrimSpace(p)
	if len(trimmed) == 0 {
		return len(p), nil
	}
	stamp := time.Now().Format("15:04:05")
	r.mu.Lock()
	for _, line := range strings.Split(string(trimmed), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r.lines = append(r.lines, stamp+" "+line)
	}
	if len(r.lines) > r.cap {
		r.lines = append([]string(nil), r.lines[len(r.lines)-r.cap:]...)
	}
	r.mu.Unlock()
	return len(p), nil
}

func (r *ringLogger) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}
