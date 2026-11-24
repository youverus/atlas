package device

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pi-go/internal/config"
	"pi-go/internal/httpapi"
	"pi-go/internal/project"
)

const maxDeviceLogLines = 80

// EventPublisher receives rewritten SSE frames from connected remote devices.
type EventPublisher interface {
	PublishEvent(name string, data []byte)
}

// Options configures Manager for production or test wiring.
type Options struct {
	DevicesPath     string
	ProjectsPath    string
	LocalConfigPath string
	AtlasHome       string
	Version         string
	SourceRoot      string
	Runner          Runner
}

type activeRemoteTarget struct {
	deviceID   string
	sessionKey string
	remoteDir  string
}

type connState struct {
	status        string // offline | connecting | online | error
	errMsg        string
	remoteOS      string
	remoteArch    string
	remoteVersion string
	remoteHome    string
	remoteAtlas   string
	token         string
	logs          []string

	cancel context.CancelFunc
	tunnel TunnelHandle
	client *remoteClient
}

// Manager implements httpapi.DeviceController, httpapi.RemoteRouter, and
// creates a CompositeHost spanning the local kernel and all remote targets.
type Manager struct {
	store           *Store
	projects        *project.Store
	runner          Runner
	atlasHome       string
	localConfigPath string
	version         string
	sourceRoot      string

	pubMu     sync.RWMutex
	publisher EventPublisher

	mu     sync.RWMutex
	conns  map[string]*connState
	active *activeRemoteTarget
}

// NewManager initializes the device inventory, project store view, and runner.
func NewManager(opts Options) *Manager {
	atlasHome := strings.TrimSpace(opts.AtlasHome)
	if atlasHome == "" {
		atlasHome, _ = config.DefaultHome()
	}
	devicesPath := strings.TrimSpace(opts.DevicesPath)
	if devicesPath == "" && atlasHome != "" {
		devicesPath = filepath.Join(atlasHome, FileName)
	}
	projectsPath := strings.TrimSpace(opts.ProjectsPath)
	if projectsPath == "" && atlasHome != "" {
		projectsPath = filepath.Join(atlasHome, project.FileName)
	}
	localConfigPath := strings.TrimSpace(opts.LocalConfigPath)
	if localConfigPath == "" && atlasHome != "" {
		localConfigPath = filepath.Join(atlasHome, config.FileName)
	}
	runner := opts.Runner
	if runner == nil {
		runner = &execRunner{}
	}
	return &Manager{
		store:           OpenStore(devicesPath),
		projects:        project.Open(projectsPath),
		runner:          runner,
		atlasHome:       atlasHome,
		localConfigPath: localConfigPath,
		version:         strings.TrimSpace(opts.Version),
		sourceRoot:      strings.TrimSpace(opts.SourceRoot),
		conns:           map[string]*connState{},
	}
}

// SetPublisher attaches the local HTTP SSE server used to relay remote events.
func (m *Manager) SetPublisher(pub EventPublisher) {
	m.pubMu.Lock()
	m.publisher = pub
	m.pubMu.Unlock()
}

func (m *Manager) publish(name string, payload []byte) {
	m.pubMu.RLock()
	pub := m.publisher
	m.pubMu.RUnlock()
	if pub != nil {
		pub.PublishEvent(name, payload)
	}
}

// WrapHost wraps the local webhost Host so projects, sessions, browsing, and
// turns route transparently between local and connected remote machines.
func (m *Manager) WrapHost(local httpapi.Host) *CompositeHost {
	return &CompositeHost{local: local, mgr: m}
}

// Close tears down all active SSH tunnels and background SSE bridges.
func (m *Manager) Close() error {
	m.mu.Lock()
	var toClose []*connState
	for _, st := range m.conns {
		toClose = append(toClose, st)
	}
	m.conns = map[string]*connState{}
	m.mu.Unlock()

	for _, st := range toClose {
		if st.cancel != nil {
			st.cancel()
		}
		if st.tunnel != nil {
			_ = st.tunnel.Close()
		}
	}
	return nil
}

// Devices returns configured SSH devices, their connection state, and local
// `~/.ssh/config` host aliases.
func (m *Manager) Devices() (httpapi.DeviceList, error) {
	devs := m.store.List()
	infos := make([]httpapi.DeviceInfo, 0, len(devs))
	m.mu.RLock()
	for _, dev := range devs {
		infos = append(infos, m.buildInfoLocked(dev))
	}
	m.mu.RUnlock()
	return httpapi.DeviceList{
		ConfigPath: m.store.Path(),
		SSHHosts:   ReadSSHConfigHosts(),
		Devices:    infos,
	}, nil
}

func (m *Manager) buildInfoLocked(dev Device) httpapi.DeviceInfo {
	info := httpapi.DeviceInfo{
		ID:            dev.ID,
		Name:          dev.Name,
		Host:          dev.Host,
		Port:          dev.Port,
		User:          dev.User,
		IdentityFile:  dev.IdentityFile,
		RemoteBin:     dev.RemoteBin,
		Status:        "offline",
		LastConnected: dev.LastConnected,
		Logs:          []string{},
	}
	if st, ok := m.conns[dev.ID]; ok {
		if st.status != "" {
			info.Status = st.status
		}
		info.Error = st.errMsg
		info.RemoteOS = st.remoteOS
		info.RemoteArch = st.remoteArch
		info.RemoteVersion = st.remoteVersion
		info.RemoteHome = st.remoteHome
		if len(st.logs) > 0 {
			info.Logs = append([]string(nil), st.logs...)
		}
	}
	return info
}

func (m *Manager) deviceInfo(id string) httpapi.DeviceInfo {
	dev, ok := m.store.Get(id)
	if !ok {
		return httpapi.DeviceInfo{ID: id, Status: "offline", Logs: []string{}}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buildInfoLocked(dev)
}

func (m *Manager) appendLog(id, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	stamp := time.Now().Format("15:04:05") + " " + line
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.ensureConnLocked(id)
	st.logs = append(st.logs, stamp)
	if len(st.logs) > maxDeviceLogLines {
		st.logs = append([]string(nil), st.logs[len(st.logs)-maxDeviceLogLines:]...)
	}
}

func (m *Manager) ensureConnLocked(id string) *connState {
	st, ok := m.conns[id]
	if !ok {
		st = &connState{status: "offline"}
		m.conns[id] = st
	}
	return st
}

// SaveDevice creates or edits a remote SSH device configuration.
func (m *Manager) SaveDevice(id string, input httpapi.DeviceInput) (httpapi.DeviceInfo, error) {
	dev, err := m.store.Save(id, input)
	if err != nil {
		return httpapi.DeviceInfo{}, err
	}
	return m.deviceInfo(dev.ID), nil
}

// DeleteDevice disconnects an active device, removes its project entries from
// `projects.json`, and deletes the device record.
func (m *Manager) DeleteDevice(id string) error {
	_, _ = m.DisconnectDevice(id)
	_ = m.projects.RemoveAllForDevice(id)
	m.mu.Lock()
	delete(m.conns, id)
	if m.active != nil && m.active.deviceID == id {
		m.active = nil
	}
	m.mu.Unlock()
	return m.store.Delete(id)
}

// DisconnectDevice shuts down the tunnel and event bridge for id.
func (m *Manager) DisconnectDevice(id string) (httpapi.DeviceInfo, error) {
	dev, ok := m.store.Get(id)
	if !ok {
		return httpapi.DeviceInfo{}, fmt.Errorf("device: unknown device %q", id)
	}
	m.mu.Lock()
	st := m.ensureConnLocked(id)
	cancel := st.cancel
	tunnel := st.tunnel
	st.cancel = nil
	st.tunnel = nil
	st.client = nil
	st.status = "offline"
	st.errMsg = ""
	if m.active != nil && m.active.deviceID == id {
		m.active = nil
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if tunnel != nil {
		_ = tunnel.Close()
	}
	m.appendLog(id, "已断开连接")
	return m.deviceInfo(dev.ID), nil
}

// ConnectDevice probes remote OS/arch over SSH, installs or upgrades `atlas`
// if needed, provisions missing config.json, starts `atlas -serve`, establishes
// the loopback SSH tunnel, and starts the SSE bridge.
func (m *Manager) ConnectDevice(ctx context.Context, id string) (httpapi.DeviceInfo, error) {
	dev, ok := m.store.Get(id)
	if !ok {
		return httpapi.DeviceInfo{}, fmt.Errorf("device: unknown device %q", id)
	}

	m.mu.Lock()
	st := m.ensureConnLocked(id)
	if st.status == "online" && st.client != nil {
		info := m.buildInfoLocked(dev)
		m.mu.Unlock()
		return info, nil
	}
	oldCancel := st.cancel
	oldTunnel := st.tunnel
	st.cancel = nil
	st.tunnel = nil
	st.client = nil
	st.status = "connecting"
	st.errMsg = ""
	st.logs = nil
	m.mu.Unlock()

	if oldCancel != nil {
		oldCancel()
	}
	if oldTunnel != nil {
		_ = oldTunnel.Close()
	}

	failConn := func(err error) (httpapi.DeviceInfo, error) {
		m.mu.Lock()
		st := m.ensureConnLocked(id)
		st.status = "error"
		st.errMsg = err.Error()
		st.client = nil
		m.mu.Unlock()
		m.appendLog(id, "连接失败: "+err.Error())
		return m.deviceInfo(id), err
	}

	logFn := func(msg string) { m.appendLog(id, msg) }
	token, err := m.loadOrCreateToken(id)
	if err != nil {
		return failConn(err)
	}

	probe, err := m.runner.Probe(ctx, dev, logFn)
	if err != nil {
		return failConn(err)
	}
	remoteAtlasHome := probe.DefaultPath
	if remoteAtlasHome == "" {
		if probe.Home != "" {
			remoteAtlasHome = path.Join(probe.Home, ".atlas")
		} else {
			remoteAtlasHome = "$HOME/.atlas"
		}
	}

	m.mu.Lock()
	st = m.ensureConnLocked(id)
	st.remoteOS = probe.OS
	st.remoteArch = probe.Arch
	st.remoteHome = probe.Home
	st.remoteAtlas = remoteAtlasHome
	st.remoteVersion = probe.AtlasVer
	st.token = token
	m.mu.Unlock()

	binPath := probe.AtlasBin
	customBin := strings.TrimSpace(dev.RemoteBin)
	if customBin != "" {
		if binPath == "" {
			return failConn(fmt.Errorf("device %s: 指定的远程二进制不可执行或不存在: %s", dev.Name, customBin))
		}
	} else if binPath == "" || (m.version != "" && probe.AtlasVer != m.version) {
		localBin, resolveErr := resolveAtlasBinary(ctx, m.atlasHome, probe.OS, probe.Arch, m.version, m.sourceRoot, logFn)
		if resolveErr != nil {
			return failConn(resolveErr)
		}
		f, openErr := os.Open(localBin)
		if openErr != nil {
			return failConn(fmt.Errorf("device %s: open binary %s: %w", dev.Name, localBin, openErr))
		}
		targetBin := path.Join(remoteAtlasHome, "bin", "atlas")
		uploadErr := m.runner.WriteFile(ctx, dev, targetBin, "0755", f, logFn)
		_ = f.Close()
		if uploadErr != nil {
			return failConn(uploadErr)
		}
		binPath = targetBin
		probe.ServeAlive = false // force restart with newly uploaded binary
		if m.version != "" {
			m.mu.Lock()
			st.remoteVersion = m.version
			m.mu.Unlock()
		}
	}

	if !probe.HasConfig {
		cfgData, readErr := os.ReadFile(m.localConfigPath)
		if readErr != nil {
			return failConn(fmt.Errorf("device %s: 远程缺少 ~/.atlas/config.json 且读取本地配置失败 (%w)", dev.Name, readErr))
		}
		remoteCfg := path.Join(remoteAtlasHome, config.FileName)
		if err := m.runner.WriteFile(ctx, dev, remoteCfg, "0600", bytes.NewReader(cfgData), logFn); err != nil {
			return failConn(err)
		}
		logFn("已自动同步本地 config.json 到远程主机")
	}

	var remotePort int
	var tunnel TunnelHandle
	var client *remoteClient

	reused := false
	if probe.ServeAlive && probe.ServePort > 0 && (m.version == "" || probe.ServeVer == m.version || customBin != "") {
		logFn(fmt.Sprintf("发现运行中的远程 atlas -serve (PID %d, port %d)，尝试复用...", probe.ServePID, probe.ServePort))
		lp, portErr := reserveLocalPort()
		if portErr == nil {
			tun, tunErr := m.runner.OpenTunnel(dev, lp, probe.ServePort)
			if tunErr == nil {
				candClient := newRemoteClient(dev.ID, dev.Name, fmt.Sprintf("http://127.0.0.1:%d", lp), token)
				state, status, waitErr := waitForRemoteReady(ctx, candClient, tun)
				if waitErr == nil {
					remotePort = probe.ServePort
					tunnel = tun
					client = candClient
					reused = true
					m.mu.Lock()
					if state.Version != "" {
						st.remoteVersion = state.Version
					}
					m.mu.Unlock()
					_ = status
				} else {
					_ = tun.Close()
					logFn("已有远程实例未响应同一令牌，将重新拉起: " + waitErr.Error())
				}
			}
		}
	}

	if !reused {
		var serveVer string
		if probe.ServePID > 0 {
			remotePort, serveVer, err = m.runner.RestartServe(ctx, dev, probe.ServePID, binPath, token, logFn)
		} else {
			remotePort, serveVer, err = m.runner.StartServe(ctx, dev, binPath, token, logFn)
		}
		if err != nil {
			return failConn(err)
		}
		lp, portErr := reserveLocalPort()
		if portErr != nil {
			return failConn(fmt.Errorf("device %s: allocate local tunnel port: %w", dev.Name, portErr))
		}
		tunnel, err = m.runner.OpenTunnel(dev, lp, remotePort)
		if err != nil {
			return failConn(err)
		}
		client = newRemoteClient(dev.ID, dev.Name, fmt.Sprintf("http://127.0.0.1:%d", lp), token)
		state, _, waitErr := waitForRemoteReady(ctx, client, tunnel)
		if waitErr != nil {
			_ = tunnel.Close()
			return failConn(waitErr)
		}
		if serveVer == "" {
			serveVer = state.Version
		}
		m.mu.Lock()
		if serveVer != "" {
			st.remoteVersion = serveVer
		}
		m.mu.Unlock()
	}

	// Use background context decoupled from HTTP request timeout for persistent tunnel + SSE loop.
	connCtx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	st = m.ensureConnLocked(id)
	st.status = "online"
	st.errMsg = ""
	st.cancel = cancel
	st.tunnel = tunnel
	st.client = client
	m.mu.Unlock()

	_ = m.store.TouchConnected(id)
	logFn(fmt.Sprintf("已连接到远程主机 (%s/%s, port %d)", probe.OS, probe.Arch, remotePort))

	go m.watchSSE(connCtx, id, client)
	go m.watchTunnel(connCtx, id, tunnel)

	return m.deviceInfo(id), nil
}

func waitForRemoteReady(ctx context.Context, client *remoteClient, tunnel TunnelHandle) (remoteStatePayload, int, error) {
	tunnelDead := make(chan error, 1)
	if tunnel != nil {
		go func() {
			tunnelDead <- tunnel.Wait()
		}()
	}

	deadline := time.Now().Add(8 * time.Second)
	var lastErr error
	for {
		callCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		state, status, err := client.fetchState(callCtx)
		cancel()
		if err == nil {
			return state, status, nil
		}
		lastErr = err
		if status == http.StatusUnauthorized {
			return remoteStatePayload{}, status, fmt.Errorf("unauthorized token (HTTP 401)")
		}
		if time.Now().After(deadline) {
			return remoteStatePayload{}, status, fmt.Errorf("remote tunnel health check timeout: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return remoteStatePayload{}, 0, ctx.Err()
		case tunErr := <-tunnelDead:
			if tunErr == nil {
				return remoteStatePayload{}, 0, fmt.Errorf("ssh tunnel exited unexpectedly")
			}
			return remoteStatePayload{}, 0, fmt.Errorf("ssh tunnel failed: %w", tunErr)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (m *Manager) watchTunnel(ctx context.Context, id string, tunnel TunnelHandle) {
	if tunnel == nil {
		return
	}
	err := tunnel.Wait()
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	st, ok := m.conns[id]
	if ok && st.tunnel == tunnel {
		st.cancel()
		st.tunnel = nil
		st.client = nil
		st.status = "error"
		if err != nil {
			st.errMsg = "SSH 隧道中断: " + err.Error()
		} else {
			st.errMsg = "SSH 隧道已关闭"
		}
	}
	m.mu.Unlock()
	m.appendLog(id, "SSH 隧道意外断开")
}

func (m *Manager) watchSSE(ctx context.Context, id string, client *remoteClient) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := client.runSSE(ctx, func(name string, payload []byte) {
			m.publish(name, payload)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.appendLog(id, "SSE 事件流重连中: "+err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// SyncDeviceConfig copies local config.json to the remote host and asks a
// connected remote server to hot-reload its configuration.
func (m *Manager) SyncDeviceConfig(ctx context.Context, id string) error {
	dev, ok := m.store.Get(id)
	if !ok {
		return fmt.Errorf("device: unknown device %q", id)
	}
	cfgData, err := os.ReadFile(m.localConfigPath)
	if err != nil {
		return fmt.Errorf("device %s: read local config %s: %w", dev.Name, m.localConfigPath, err)
	}
	logFn := func(msg string) { m.appendLog(id, msg) }

	m.mu.RLock()
	st := m.conns[id]
	var remoteAtlas string
	var client *remoteClient
	if st != nil {
		remoteAtlas = st.remoteAtlas
		client = st.client
	}
	m.mu.RUnlock()

	if remoteAtlas == "" {
		probe, pErr := m.runner.Probe(ctx, dev, logFn)
		if pErr != nil {
			return pErr
		}
		remoteAtlas = probe.DefaultPath
		if remoteAtlas == "" {
			remoteAtlas = path.Join(probe.Home, ".atlas")
		}
	}
	remoteCfg := path.Join(remoteAtlas, config.FileName)
	if err := m.runner.WriteFile(ctx, dev, remoteCfg, "0600", bytes.NewReader(cfgData), logFn); err != nil {
		return err
	}
	if client != nil {
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/config/reload", map[string]any{}, nil); err != nil {
			return fmt.Errorf("device %s: config uploaded but remote reload failed: %w", dev.Name, err)
		}
	}
	logFn("已将本地 config.json 同步至远程主机")
	return nil
}

func (m *Manager) loadOrCreateToken(id string) (string, error) {
	if m.atlasHome == "" {
		var buf [24]byte
		_, _ = rand.Read(buf[:])
		return hex.EncodeToString(buf[:]), nil
	}
	dir := filepath.Join(m.atlasHome, "device-tokens")
	p := filepath.Join(dir, id+".token")
	if raw, err := os.ReadFile(p); err == nil {
		tok := strings.TrimSpace(string(raw))
		if len(tok) >= 16 {
			return tok, nil
		}
	}
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf[:])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func (m *Manager) clientFor(deviceID string) (*remoteClient, error) {
	m.mu.RLock()
	st := m.conns[deviceID]
	if st != nil && st.status == "online" && st.client != nil {
		c := st.client
		m.mu.RUnlock()
		return c, nil
	}
	m.mu.RUnlock()
	return nil, fmt.Errorf("device %q is offline", deviceID)
}

func (m *Manager) ensureConnectedClient(ctx context.Context, deviceID string) (*remoteClient, error) {
	if client, err := m.clientFor(deviceID); err == nil {
		return client, nil
	}
	if _, err := m.ConnectDevice(ctx, deviceID); err != nil {
		return nil, err
	}
	return m.clientFor(deviceID)
}

func (m *Manager) setActiveRemote(deviceID, sessionKey, remoteDir string) {
	m.mu.Lock()
	m.active = &activeRemoteTarget{
		deviceID:   deviceID,
		sessionKey: sessionKey,
		remoteDir:  remoteDir,
	}
	m.mu.Unlock()
}

func (m *Manager) clearActiveRemote() {
	m.mu.Lock()
	m.active = nil
	m.mu.Unlock()
}

func (m *Manager) activeTarget() (activeRemoteTarget, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == nil {
		return activeRemoteTarget{}, false
	}
	return *m.active, true
}

// ForwardAsk intercepts turns targeted at `remote://<deviceID>/...` sessions.
func (m *Manager) ForwardAsk(ctx context.Context, sessionKey, prompt string, attachments []httpapi.Attachment) (bool, error) {
	devID, rawSession, ok := ParseRemoteKey(sessionKey)
	if !ok {
		return false, nil
	}
	client, err := m.clientFor(devID)
	if err != nil {
		return true, err
	}
	return true, client.forwardAsk(ctx, rawSession, prompt, attachments)
}

// ForwardStop sends a stop request to the device owning the named round.
func (m *Manager) ForwardStop(ctx context.Context, sessionKey, turnID string, side bool) (bool, int, error) {
	devID, rawSession, ok := ParseRemoteKey(sessionKey)
	if !ok {
		return false, 0, nil
	}
	client, err := m.clientFor(devID)
	if err != nil {
		return true, http.StatusBadGateway, err
	}
	status, err := client.doJSON(ctx, http.MethodPost, "/api/stop", map[string]any{
		"session": rawSession,
		"turnId":  turnID,
		"side":    side,
	}, nil)
	return true, status, err
}

// RemoteTurnID returns the current main turn ID from the remote state or stream.
func (m *Manager) RemoteTurnID(sessionKey string) string {
	devID, _, ok := ParseRemoteKey(sessionKey)
	if !ok {
		return ""
	}
	client, err := m.clientFor(devID)
	if err != nil {
		return ""
	}
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.running[sessionKey]
}

// ForwardApproval answers an approval prompt originating from a remote host.
func (m *Manager) ForwardApproval(ctx context.Context, questionID string, allowed, always bool) (bool, error) {
	devID, _, ok := ParseRemoteKey(questionID)
	if !ok {
		return false, nil
	}
	client, err := m.clientFor(devID)
	if err != nil {
		return true, err
	}
	return true, client.forwardApproval(ctx, questionID, allowed, always)
}

// ForwardCompact runs `/compact` on the owning remote session.
func (m *Manager) ForwardCompact(ctx context.Context, sessionKey, note string) (bool, bool, error) {
	devID, rawSession, ok := ParseRemoteKey(sessionKey)
	if !ok {
		return false, false, nil
	}
	client, err := m.clientFor(devID)
	if err != nil {
		return true, false, err
	}
	okResp, err := client.forwardCompact(ctx, rawSession, note)
	return true, okResp, err
}

// ForwardSideAsk sends a side-conversation turn to the owning remote host.
func (m *Manager) ForwardSideAsk(ctx context.Context, sessionKey, prompt string) (bool, error) {
	devID, rawSession, ok := ParseRemoteKey(sessionKey)
	if !ok {
		return false, nil
	}
	client, err := m.clientFor(devID)
	if err != nil {
		return true, err
	}
	return true, client.forwardSideAsk(ctx, rawSession, prompt)
}

// IsRemoteSideRunning checks whether a remote side conversation turn is running.
func (m *Manager) IsRemoteSideRunning(sessionKey string) bool {
	devID, _, ok := ParseRemoteKey(sessionKey)
	if !ok {
		return false
	}
	client, err := m.clientFor(devID)
	if err != nil {
		return false
	}
	return client.isSideRunning(sessionKey)
}

// RunningRemoteSessions returns wrapped session keys currently running across
// all connected remote hosts.
func (m *Manager) RunningRemoteSessions() []string {
	m.mu.RLock()
	clients := make([]*remoteClient, 0, len(m.conns))
	for _, st := range m.conns {
		if st.status == "online" && st.client != nil {
			clients = append(clients, st.client)
		}
	}
	m.mu.RUnlock()

	var out []string
	for _, c := range clients {
		out = append(out, c.runningSessionKeys()...)
	}
	sort.Strings(out)
	return out
}

// PendingRemoteApprovals gathers open approval prompts across connected remote
// hosts for `/api/state` page reload recovery.
func (m *Manager) PendingRemoteApprovals() []httpapi.PendingQuestionItem {
	m.mu.RLock()
	clients := make([]*remoteClient, 0, len(m.conns))
	for _, st := range m.conns {
		if st.status == "online" && st.client != nil {
			clients = append(clients, st.client)
		}
	}
	m.mu.RUnlock()

	var out []httpapi.PendingQuestionItem
	for _, c := range clients {
		out = append(out, c.pendingApprovals()...)
	}
	return out
}

// ActiveRemoteDevice reports the active remote device and directory when the
// currently viewed session belongs to a remote machine.
func (m *Manager) ActiveRemoteDevice() (deviceID, directory string, ok bool) {
	target, has := m.activeTarget()
	if !has {
		return "", "", false
	}
	return target.deviceID, target.remoteDir, true
}

// SSHArgsForDirectory returns `ssh -t ...` arguments for opening an embedded
// interactive terminal inside remoteDir on deviceID.
func (m *Manager) SSHArgsForDirectory(deviceID, remoteDir string) ([]string, string, error) {
	dev, ok := m.store.Get(deviceID)
	if !ok {
		return nil, "", fmt.Errorf("device: unknown device %q", deviceID)
	}
	return SSHInteractiveArgs(dev, remoteDir), dev.Name, nil
}

// SSHArgsForActive returns SSH interactive terminal args when viewing a remote
// project, or ok=false when viewing a local project.
func (m *Manager) SSHArgsForActive() (deviceName string, args []string, ok bool) {
	target, has := m.activeTarget()
	if !has {
		return "", nil, false
	}
	args, devName, err := m.SSHArgsForDirectory(target.deviceID, target.remoteDir)
	if err != nil {
		return "", nil, false
	}
	return devName, args, true
}
