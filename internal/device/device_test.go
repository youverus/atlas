package device

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pi-go/internal/httpapi"
	"pi-go/internal/wire"
)

type fakeTunnel struct {
	ln     net.Listener
	target string
	done   chan error
	once   sync.Once
}

func startFakeTunnel(localPort int, targetAddr string) (*fakeTunnel, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return nil, err
	}
	t := &fakeTunnel{
		ln:     ln,
		target: targetAddr,
		done:   make(chan error, 1),
	}
	go func() {
		for {
			src, err := ln.Accept()
			if err != nil {
				t.once.Do(func() {
					t.done <- nil
					close(t.done)
				})
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				dst, dErr := net.Dial("tcp", targetAddr)
				if dErr != nil {
					return
				}
				defer dst.Close()
				go io.Copy(dst, c)
				_, _ = io.Copy(c, dst)
			}(src)
		}
	}()
	return t, nil
}

func (f *fakeTunnel) Wait() error {
	err := <-f.done
	return err
}

func (f *fakeTunnel) Close() error {
	return f.ln.Close()
}

type fakeRunner struct {
	mu           sync.Mutex
	probe        probeResult
	uploaded     map[string][]byte
	startedToken string
	remoteAddr   string
	remotePort   int
}

func (r *fakeRunner) Probe(ctx context.Context, dev Device, logFn func(string)) (probeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if logFn != nil {
		logFn("mock probe")
	}
	return r.probe, nil
}

func (r *fakeRunner) WriteFile(ctx context.Context, dev Device, remotePath, mode string, data io.Reader, logFn func(string)) error {
	body, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.uploaded == nil {
		r.uploaded = map[string][]byte{}
	}
	r.uploaded[remotePath] = body
	r.mu.Unlock()
	return nil
}

func (r *fakeRunner) StartServe(ctx context.Context, dev Device, binPath, token string, logFn func(string)) (int, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startedToken = token
	return r.remotePort, "0.15.0", nil
}

func (r *fakeRunner) RestartServe(ctx context.Context, dev Device, oldPID int, binPath, token string, logFn func(string)) (int, string, error) {
	return r.StartServe(ctx, dev, binPath, token, logFn)
}

func (r *fakeRunner) OpenTunnel(dev Device, localPort, remotePort int) (TunnelHandle, error) {
	r.mu.Lock()
	addr := r.remoteAddr
	r.mu.Unlock()
	return startFakeTunnel(localPort, addr)
}

type stubLocalHost struct {
	httpapi.Host
	localProjects []httpapi.ProjectGroup
	localSessions []httpapi.SessionInfo
	currentSess   string
	localAsked    []string
}

func (s *stubLocalHost) Sessions(archived bool) ([]httpapi.SessionInfo, error) {
	return append([]httpapi.SessionInfo(nil), s.localSessions...), nil
}
func (s *stubLocalHost) OpenSession(p string) error { s.currentSess = p; return nil }
func (s *stubLocalHost) Projects(archived bool) (httpapi.ProjectList, error) {
	return httpapi.ProjectList{Groups: append([]httpapi.ProjectGroup(nil), s.localProjects...)}, nil
}
func (s *stubLocalHost) BrowseDirs(deviceID, path string, includeHidden bool) (httpapi.DirBrowseResult, error) {
	return httpapi.DirBrowseResult{DeviceID: "", Path: "/local/work", Home: "/Users/me", Entries: []httpapi.BrowseDirEntry{{Name: "proj", Path: "/local/work/proj"}}}, nil
}
func (s *stubLocalHost) DiscoverProjects(deviceID string) ([]httpapi.DiscoveredProject, error) {
	return []httpapi.DiscoveredProject{{Path: "/local/work/proj", Name: "proj", SessionCount: 1}}, nil
}
func (s *stubLocalHost) CurrentSession() string   { return s.currentSess }
func (s *stubLocalHost) SessionArchived() bool    { return false }
func (s *stubLocalHost) SessionDirectory() string { return "/local/work" }
func (s *stubLocalHost) SessionModel() string     { return "local/model-a" }

func TestReadDirReturnsOfflineErrorForActiveRemote(t *testing.T) {
	mgr := &Manager{}
	mgr.setActiveRemote("remote", WrapRemoteKey("remote", "session"), "/remote/project")
	host := mgr.WrapHost(nil)
	if _, err := host.ReadDir(""); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("ReadDir error = %v, want offline error", err)
	}
}

type stubRemoteServerHost struct {
	httpapi.Host
	currentSess string
	currentDir  string
	sessions    []httpapi.SessionInfo
	projects    []httpapi.ProjectGroup
	discovered  []httpapi.DiscoveredProject
}

func (r *stubRemoteServerHost) CurrentSession() string   { return r.currentSess }
func (r *stubRemoteServerHost) SessionArchived() bool    { return false }
func (r *stubRemoteServerHost) SessionDirectory() string { return r.currentDir }
func (r *stubRemoteServerHost) SessionModel() string     { return "remote/gpu-70b" }
func (r *stubRemoteServerHost) Sessions(archived bool) ([]httpapi.SessionInfo, error) {
	return append([]httpapi.SessionInfo(nil), r.sessions...), nil
}
func (r *stubRemoteServerHost) Projects(archived bool) (httpapi.ProjectList, error) {
	return httpapi.ProjectList{Groups: append([]httpapi.ProjectGroup(nil), r.projects...)}, nil
}
func (r *stubRemoteServerHost) AddProject(path string) error {
	r.projects = append(r.projects, httpapi.ProjectGroup{Path: path, Name: "srv", Sessions: r.sessions})
	return nil
}
func (r *stubRemoteServerHost) RemoveProject(path string) error { return nil }
func (r *stubRemoteServerHost) NewSessionIn(cwd string) error {
	r.currentDir = cwd
	r.currentSess = "/home/ubuntu/.atlas/sessions/s-new.jsonl"
	r.sessions = []httpapi.SessionInfo{{Path: r.currentSess, ID: "s-new", Cwd: cwd, Current: true}}
	if len(r.projects) > 0 {
		r.projects[0].Sessions = r.sessions
	}
	return nil
}
func (r *stubRemoteServerHost) OpenSession(p string) error {
	r.currentSess = p
	return nil
}
func (r *stubRemoteServerHost) Transcript() ([]httpapi.TranscriptBlock, error) {
	return []httpapi.TranscriptBlock{{Role: "user", Text: "on gpu"}}, nil
}
func (r *stubRemoteServerHost) BrowseDirs(deviceID, path string, includeHidden bool) (httpapi.DirBrowseResult, error) {
	return httpapi.DirBrowseResult{
		Path:    "/home/ubuntu/workspace",
		Parent:  "/home/ubuntu",
		Home:    "/home/ubuntu",
		Entries: []httpapi.BrowseDirEntry{{Name: "llm-train", Path: "/home/ubuntu/workspace/llm-train"}},
	}, nil
}
func (r *stubRemoteServerHost) DiscoverProjects(deviceID string) ([]httpapi.DiscoveredProject, error) {
	return r.discovered, nil
}

type capturedEvent struct {
	name string
	data string
}

type capturePublisher struct {
	mu     sync.Mutex
	events []capturedEvent
	ch     chan capturedEvent
}

func (p *capturePublisher) PublishEvent(name string, data []byte) {
	ev := capturedEvent{name: name, data: string(data)}
	p.mu.Lock()
	p.events = append(p.events, ev)
	p.mu.Unlock()
	if p.ch != nil {
		select {
		case p.ch <- ev:
		default:
		}
	}
}

func TestStoreValidationSSHArgsAndConfigParse(t *testing.T) {
	tmp := t.TempDir()
	store := OpenStore(filepath.Join(tmp, FileName))

	if _, err := store.Save("", httpapi.DeviceInput{Name: "Bad", Host: "-oProxyCommand=id"}); err == nil {
		t.Fatal("expected option-injection host to be rejected")
	}
	if _, err := store.Save("", httpapi.DeviceInput{Name: "Bad", Host: "host with spaces"}); err == nil {
		t.Fatal("expected whitespace in host to be rejected")
	}
	if _, err := store.Save("", httpapi.DeviceInput{Name: "Bad", Host: "ok.host", Port: 70000}); err == nil {
		t.Fatal("expected out-of-range port to be rejected")
	}

	d1, err := store.Save("", httpapi.DeviceInput{Name: "GPU Box", Host: "10.0.0.8", Port: 2222, User: "ubuntu"})
	if err != nil {
		t.Fatalf("save d1: %v", err)
	}
	if d1.ID != "gpu-box" {
		t.Fatalf("d1.ID = %q, want gpu-box", d1.ID)
	}
	d2, err := store.Save("", httpapi.DeviceInput{Name: "GPU Box", Host: "10.0.0.9"})
	if err != nil {
		t.Fatalf("save d2: %v", err)
	}
	if d2.ID != "gpu-box-2" {
		t.Fatalf("d2.ID = %q, want gpu-box-2", d2.ID)
	}

	args := SSHInteractiveArgs(d1, "/srv/my 'app")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-p 2222") || !strings.Contains(joined, "ubuntu@10.0.0.8") || !strings.Contains(joined, `'"'"'`) {
		t.Fatalf("SSHInteractiveArgs = %v", args)
	}

	sshCfg := filepath.Join(tmp, "ssh_config")
	if err := os.WriteFile(sshCfg, []byte(`
# comment
Host gpu-cluster db-node
  User ops
Host *.internal
  User root
Host !excluded devbox
  HostName 127.0.0.1
`), 0o600); err != nil {
		t.Fatalf("write ssh config: %v", err)
	}
	parsed := parseSSHConfigFile(sshCfg)
	want := []string{"gpu-cluster", "db-node", "devbox"}
	if strings.Join(parsed, ",") != strings.Join(want, ",") {
		t.Fatalf("parsed hosts = %v, want %v", parsed, want)
	}

	wrapped := WrapRemoteKey("gpu-box", "/srv/app/s1.jsonl")
	if wrapped != "remote://gpu-box/srv/app/s1.jsonl" {
		t.Fatalf("wrapped = %q", wrapped)
	}
	devID, rPath, ok := ParseRemoteKey(wrapped)
	if !ok || devID != "gpu-box" || rPath != "/srv/app/s1.jsonl" {
		t.Fatalf("ParseRemoteKey = (%q, %q, %v)", devID, rPath, ok)
	}
}

func TestConnectProvisionRemoteProjectsAndSSEBridge(t *testing.T) {
	home := t.TempDir()
	localCfg := filepath.Join(home, "config.json")
	if err := os.WriteFile(localCfg, []byte(`{"defaultModel":"p/m"}`), 0o600); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	remoteHost := &stubRemoteServerHost{
		currentDir:  "/home/ubuntu/workspace/llm-train",
		currentSess: "/home/ubuntu/.atlas/sessions/s0.jsonl",
		discovered: []httpapi.DiscoveredProject{
			{Path: "/home/ubuntu/workspace/llm-train", Name: "llm-train", SessionCount: 3},
		},
	}

	// Reserve a fixed token file so the remote server knows the same token Manager loads.
	tokenDir := filepath.Join(home, "device-tokens")
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		t.Fatalf("mkdir token dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tokenDir, "gpu-1.token"), []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	var remoteServer *httpapi.Server
	askedRemote := make(chan string, 1)
	approvalResult := make(chan bool, 1)
	remoteServer = httpapi.New(httpapi.Options{
		AuthToken: "0123456789abcdef0123456789abcdef",
		Host:      remoteHost,
		SessionTurn: func(ctx context.Context, session, prompt string, emit func(wire.AgentEvent) error) (httpapi.Receipt, error) {
			askedRemote <- session + "|" + prompt
			_ = emit(wire.AgentEvent{
				Type:           "message_update",
				AssistantEvent: &wire.AssistantEvent{Type: "text_delta", Delta: "from remote"},
			})
			dec := remoteServer.AskContext(ctx, httpapi.Question{Session: session, Subject: "run ls?", Tool: "bash"})
			approvalResult <- dec.Allowed
			return httpapi.Receipt{}, ctx.Err()
		},
	})
	addr, err := remoteServer.ListenOn("127.0.0.1:0")
	if err != nil {
		t.Fatalf("remoteServer.ListenOn: %v", err)
	}
	defer remoteServer.Close()

	_, portStr, _ := net.SplitHostPort(addr)
	var rPort int
	_, _ = fmt.Sscanf(portStr, "%d", &rPort)

	runner := &fakeRunner{
		remoteAddr: addr,
		remotePort: rPort,
		probe: probeResult{
			OS:          "linux",
			Arch:        "amd64",
			Home:        "/home/ubuntu",
			DefaultPath: "/home/ubuntu/.atlas",
			AtlasBin:    "/home/ubuntu/.atlas/bin/atlas",
			AtlasVer:    "0.15.0",
			HasConfig:   false, // triggers auto config provisioning on connect
		},
	}

	pub := &capturePublisher{ch: make(chan capturedEvent, 20)}
	mgr := NewManager(Options{
		DevicesPath:     filepath.Join(home, FileName),
		ProjectsPath:    filepath.Join(home, "projects.json"),
		LocalConfigPath: localCfg,
		AtlasHome:       home,
		Version:         "0.15.0",
		Runner:          runner,
	})
	mgr.SetPublisher(pub)
	defer mgr.Close()

	saved, err := mgr.SaveDevice("", httpapi.DeviceInput{Name: "GPU 1", Host: "gpu1.internal", User: "ubuntu"})
	if err != nil {
		t.Fatalf("SaveDevice: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := mgr.ConnectDevice(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ConnectDevice: %v", err)
	}
	if info.Status != "online" || info.RemoteOS != "linux" {
		t.Fatalf("connected info = %+v", info)
	}
	if _, ok := runner.uploaded["/home/ubuntu/.atlas/config.json"]; !ok {
		t.Fatalf("expected missing remote config.json to be uploaded automatically, got keys: %v", runner.uploaded)
	}

	localStub := &stubLocalHost{
		currentSess: "/local/s0.jsonl",
		localProjects: []httpapi.ProjectGroup{
			{Path: "/local/work", Name: "work", Default: true, Sessions: []httpapi.SessionInfo{{Path: "/local/s0.jsonl", ID: "s0", Current: true}}},
		},
	}
	composite := mgr.WrapHost(localStub)

	// Remote browse + discover
	browse, err := composite.BrowseDirs(saved.ID, "/home/ubuntu/workspace", false)
	if err != nil || len(browse.Entries) != 1 || browse.Entries[0].Name != "llm-train" {
		t.Fatalf("remote BrowseDirs = (%+v, %v)", browse, err)
	}
	disc, err := composite.DiscoverProjects(saved.ID)
	if err != nil || len(disc) != 1 || disc[0].Added {
		t.Fatalf("remote DiscoverProjects before add = (%+v, %v)", disc, err)
	}

	// Add remote project + create new session there + list combined projects
	if err := composite.AddRemoteProject(saved.ID, "/home/ubuntu/workspace/llm-train", "LLM Train"); err != nil {
		t.Fatalf("AddRemoteProject: %v", err)
	}
	discAfter, err := composite.DiscoverProjects(saved.ID)
	if err != nil || len(discAfter) != 1 || !discAfter[0].Added {
		t.Fatalf("remote DiscoverProjects after add = (%+v, %v)", discAfter, err)
	}
	if err := composite.NewRemoteSessionIn(saved.ID, "/home/ubuntu/workspace/llm-train"); err != nil {
		t.Fatalf("NewRemoteSessionIn: %v", err)
	}

	projects, err := composite.Projects(false)
	if err != nil {
		t.Fatalf("composite.Projects: %v", err)
	}
	if len(projects.Groups) != 2 {
		t.Fatalf("projects.Groups = %+v, want 2 groups (local + remote)", projects.Groups)
	}
	remoteGroup := projects.Groups[1]
	if remoteGroup.DeviceID != saved.ID || remoteGroup.DeviceState != "online" || len(remoteGroup.Sessions) != 1 {
		t.Fatalf("remoteGroup = %+v", remoteGroup)
	}
	sessKey := remoteGroup.Sessions[0].Path
	if !strings.HasPrefix(sessKey, "remote://"+saved.ID+"/") || !remoteGroup.Sessions[0].Current {
		t.Fatalf("remote session info = %+v", remoteGroup.Sessions[0])
	}
	// Local session must not claim Current=true while remote session is active.
	if projects.Groups[0].Sessions[0].Current {
		t.Fatal("local session stayed Current=true while remote session is active")
	}

	// Verify active SSH args for remote terminal integration
	if devName, sshArgs, ok := mgr.SSHArgsForActive(); !ok || devName != "GPU 1" || !strings.Contains(strings.Join(sshArgs, " "), "/home/ubuntu/workspace/llm-train") {
		t.Fatalf("SSHArgsForActive = (%q, %v, %v)", devName, sshArgs, ok)
	}

	// Forward turn + observe bridged agent/approval SSE + forward approval answer back
	handled, err := mgr.ForwardAsk(ctx, sessKey, "train status", nil)
	if !handled || err != nil {
		t.Fatalf("ForwardAsk = (%v, %v)", handled, err)
	}
	select {
	case got := <-askedRemote:
		if got != "/home/ubuntu/.atlas/sessions/s-new.jsonl|train status" {
			t.Fatalf("askedRemote = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for remote turn")
	}

	var wrappedApprovalID string
	deadline := time.After(4 * time.Second)
	for wrappedApprovalID == "" {
		select {
		case ev := <-pub.ch:
			if ev.name == "approval" {
				var item httpapi.PendingQuestionItem
				if json.Unmarshal([]byte(ev.data), &item) == nil {
					wrappedApprovalID = item.ID
					if item.Session != sessKey {
						t.Fatalf("bridged approval session = %q, want %q", item.Session, sessKey)
					}
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for bridged approval SSE frame")
		}
	}
	if !strings.HasPrefix(wrappedApprovalID, "remote://"+saved.ID+"/") {
		t.Fatalf("wrappedApprovalID = %q", wrappedApprovalID)
	}
	handled, err = mgr.ForwardApproval(ctx, wrappedApprovalID, true, false)
	if !handled || err != nil {
		t.Fatalf("ForwardApproval = (%v, %v)", handled, err)
	}
	select {
	case allowed := <-approvalResult:
		if !allowed {
			t.Fatal("remote turn received allowed=false, want true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for remote approval decision")
	}

	var firstID string
	for firstID == "" {
		select {
		case ev := <-pub.ch:
			var status struct {
				TurnID  string `json:"turnId"`
				Running bool   `json:"running"`
			}
			if ev.name == "status" && json.Unmarshal([]byte(ev.data), &status) == nil && !status.Running {
				firstID = status.TurnID
			}
		case <-ctx.Done():
			t.Fatal("first remote turn did not finish")
		}
	}
	if handled, err := mgr.ForwardAsk(ctx, sessKey, "stop this turn", nil); !handled || err != nil {
		t.Fatalf("ForwardAsk = (%v, %v)", handled, err)
	}
	var stopID string
	for stopID == "" {
		select {
		case ev := <-pub.ch:
			if ev.name == "approval" {
				stopID = mgr.RemoteTurnID(sessKey)
			}
		case <-ctx.Done():
			t.Fatal("second remote turn did not publish its pending approval")
		}
	}
	if handled, status, err := mgr.ForwardStop(ctx, sessKey, firstID, false); !handled || status != http.StatusConflict || err == nil {
		t.Fatalf("stale ForwardStop = (%v, %v, %v)", handled, status, err)
	}
	if handled, status, err := mgr.ForwardStop(ctx, sessKey, stopID, false); !handled || status != http.StatusAccepted || err != nil {
		t.Fatalf("ForwardStop = (%v, %v, %v)", handled, status, err)
	}
	select {
	case allowed := <-approvalResult:
		if allowed {
			t.Fatal("remote stop approved the pending tool")
		}
	case <-ctx.Done():
		t.Fatal("remote stop did not release the approval waiter")
	}
	for len(mgr.PendingRemoteApprovals()) > 0 {
		select {
		case <-pub.ch:
		case <-ctx.Done():
			t.Fatal("remote approval was not cleared")
		}
	}

	// DeleteDevice cleans up associated remote projects cleanly
	if err := mgr.DeleteDevice(saved.ID); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	afterDelete, err := composite.Projects(false)
	if err != nil || len(afterDelete.Groups) != 1 {
		t.Fatalf("Projects after DeleteDevice = (%+v, %v), want 1 local group", afterDelete, err)
	}
}
