package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deviceControllerStub struct {
	list        DeviceList
	saved       []DeviceInput
	savedID     string
	deletedID   string
	connectedID string
	disconnID   string
	syncedID    string
}

func (d *deviceControllerStub) Devices() (DeviceList, error) {
	return d.list, nil
}

func (d *deviceControllerStub) SaveDevice(id string, input DeviceInput) (DeviceInfo, error) {
	d.savedID = id
	d.saved = append(d.saved, input)
	if id == "" {
		id = "dev-1"
	}
	return DeviceInfo{ID: id, Name: input.Name, Host: input.Host, Status: "offline"}, nil
}

func (d *deviceControllerStub) DeleteDevice(id string) error {
	d.deletedID = id
	return nil
}

func (d *deviceControllerStub) ConnectDevice(ctx context.Context, id string) (DeviceInfo, error) {
	d.connectedID = id
	return DeviceInfo{ID: id, Name: "GPU", Host: "gpu", Status: "online"}, nil
}

func (d *deviceControllerStub) DisconnectDevice(id string) (DeviceInfo, error) {
	d.disconnID = id
	return DeviceInfo{ID: id, Name: "GPU", Host: "gpu", Status: "offline"}, nil
}

func (d *deviceControllerStub) SyncDeviceConfig(ctx context.Context, id string) error {
	d.syncedID = id
	return nil
}

type remoteRouterStub struct {
	asked          []string
	approved       []string
	compacted      []string
	sideAsked      []string
	sideRunning    bool
	runningRemote  []string
	pendingRemote  []PendingQuestionItem
	activeDeviceID string
	activeDir      string
}

func (r *remoteRouterStub) ForwardAsk(ctx context.Context, sessionKey, prompt string, attachments []Attachment) (bool, error) {
	if !strings.HasPrefix(sessionKey, "remote://") {
		return false, nil
	}
	r.asked = append(r.asked, sessionKey+":"+prompt)
	return true, nil
}

func (r *remoteRouterStub) ForwardApproval(ctx context.Context, questionID string, allowed, always bool) (bool, error) {
	if !strings.HasPrefix(questionID, "remote://") {
		return false, nil
	}
	r.approved = append(r.approved, questionID)
	return true, nil
}

func (r *remoteRouterStub) ForwardCompact(ctx context.Context, sessionKey, note string) (bool, bool, error) {
	if !strings.HasPrefix(sessionKey, "remote://") {
		return false, false, nil
	}
	r.compacted = append(r.compacted, sessionKey)
	return true, true, nil
}

func (r *remoteRouterStub) ForwardSideAsk(ctx context.Context, sessionKey, prompt string) (bool, error) {
	if !strings.HasPrefix(sessionKey, "remote://") {
		return false, nil
	}
	r.sideAsked = append(r.sideAsked, sessionKey+":"+prompt)
	return true, nil
}

func (r *remoteRouterStub) IsRemoteSideRunning(sessionKey string) bool {
	return strings.HasPrefix(sessionKey, "remote://") && r.sideRunning
}

func (r *remoteRouterStub) RunningRemoteSessions() []string {
	return r.runningRemote
}

func (r *remoteRouterStub) PendingRemoteApprovals() []PendingQuestionItem {
	return r.pendingRemote
}

func (r *remoteRouterStub) ActiveRemoteDevice() (string, string, bool) {
	if r.activeDeviceID == "" {
		return "", "", false
	}
	return r.activeDeviceID, r.activeDir, true
}

func TestDevicesRoutesAndNotImplemented(t *testing.T) {
	bare := New(Options{})
	bareHTTP := httptest.NewServer(bare.Handler())
	defer bareHTTP.Close()

	resp, err := http.Get(bareHTTP.URL + "/api/devices")
	if err != nil {
		t.Fatalf("get devices without controller: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}

	ctrl := &deviceControllerStub{
		list: DeviceList{
			ConfigPath: "/tmp/devices.json",
			SSHHosts:   []string{"gpu-box"},
			Devices:    []DeviceInfo{{ID: "gpu-box", Name: "GPU", Host: "gpu-box", Status: "offline"}},
		},
	}
	srv := New(Options{Devices: ctrl})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err = http.Get(ts.URL + "/api/devices")
	if err != nil {
		t.Fatalf("get devices: %v", err)
	}
	var list DeviceList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		resp.Body.Close()
		t.Fatalf("decode devices: %v", err)
	}
	resp.Body.Close()
	if len(list.Devices) != 1 || list.Devices[0].Logs == nil || len(list.SSHHosts) != 1 {
		t.Fatalf("list = %+v", list)
	}

	resp = postJSON(t, ts.URL+"/api/devices", `{"name":"Box","host":"box.internal"}`)
	if resp.StatusCode != http.StatusOK || len(ctrl.saved) != 1 || ctrl.savedID != "" {
		t.Fatalf("create status=%d savedID=%q", resp.StatusCode, ctrl.savedID)
	}

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/devices/gpu-box", strings.NewReader(`{"name":"GPU2","host":"gpu-box"}`))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put device: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK || ctrl.savedID != "gpu-box" {
		t.Fatalf("update status=%d savedID=%q", putResp.StatusCode, ctrl.savedID)
	}

	if r := postJSON(t, ts.URL+"/api/devices/gpu-box/connect", ""); r.StatusCode != http.StatusOK || ctrl.connectedID != "gpu-box" {
		t.Fatalf("connect status=%d id=%q", r.StatusCode, ctrl.connectedID)
	}
	if r := postJSON(t, ts.URL+"/api/devices/gpu-box/sync-config", ""); r.StatusCode != http.StatusOK || ctrl.syncedID != "gpu-box" {
		t.Fatalf("sync status=%d id=%q", r.StatusCode, ctrl.syncedID)
	}
	if r := postJSON(t, ts.URL+"/api/devices/gpu-box/disconnect", ""); r.StatusCode != http.StatusOK || ctrl.disconnID != "gpu-box" {
		t.Fatalf("disconnect status=%d id=%q", r.StatusCode, ctrl.disconnID)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/devices/gpu-box", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete device: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK || ctrl.deletedID != "gpu-box" {
		t.Fatalf("delete status=%d deletedID=%q", delResp.StatusCode, ctrl.deletedID)
	}
}

func TestBearerAuthAndLoopbackListenOn(t *testing.T) {
	srv := New(Options{AuthToken: "secret-token-123"})
	if _, err := srv.ListenOn("0.0.0.0:0"); err == nil {
		t.Fatal("ListenOn(0.0.0.0:0) should refuse non-loopback address")
	}
	addr, err := srv.ListenOn("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenOn loopback: %v", err)
	}
	defer srv.Close()

	base := "http://" + addr
	resp, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatalf("unauth get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer secret-token-123")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("auth get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %d, want 200", resp.StatusCode)
	}
}

func TestRemoteRouterForwardingAndPublishEvent(t *testing.T) {
	router := &remoteRouterStub{
		runningRemote:  []string{"remote://gpu-1/s/1"},
		pendingRemote:  []PendingQuestionItem{{ID: "remote://gpu-1/q1", Question: Question{Tool: "bash", Subject: "ls"}}},
		activeDeviceID: "gpu-1",
		activeDir:      "/srv/app",
	}
	sessionHost := &remoteSessionTracker{id: "remote://gpu-1/s/1"}

	srv := New(Options{
		Host:         sessionHost,
		RemoteRouter: router,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// /api/state includes remote running sessions and remote pending approvals.
	resp, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	var state struct {
		RunningSessions []string              `json:"runningSessions"`
		Pending         []PendingQuestionItem `json:"pending"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		resp.Body.Close()
		t.Fatalf("decode state: %v", err)
	}
	resp.Body.Close()
	if len(state.RunningSessions) != 1 || state.RunningSessions[0] != "remote://gpu-1/s/1" {
		t.Fatalf("RunningSessions = %v", state.RunningSessions)
	}
	if len(state.Pending) != 1 || state.Pending[0].ID != "remote://gpu-1/q1" {
		t.Fatalf("Pending = %+v", state.Pending)
	}

	if r := postJSON(t, ts.URL+"/api/ask", `{"prompt":"hello remote"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("ask status = %d, want 202", r.StatusCode)
	}
	if len(router.asked) != 1 {
		t.Fatalf("router.asked = %v", router.asked)
	}
	if r := postJSON(t, ts.URL+"/api/approval", `{"id":"remote://gpu-1/q1","allowed":true}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("approval status = %d, want 204", r.StatusCode)
	}
	if len(router.approved) != 1 {
		t.Fatalf("router.approved = %v", router.approved)
	}
	if r := postJSON(t, ts.URL+"/api/compact", `{"note":"trim"}`); r.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want 200", r.StatusCode)
	}
	if len(router.compacted) != 1 {
		t.Fatalf("router.compacted = %v", router.compacted)
	}
	if r := postJSON(t, ts.URL+"/api/side/ask", `{"prompt":"side remote"}`); r.StatusCode != http.StatusAccepted {
		t.Fatalf("side ask status = %d, want 202", r.StatusCode)
	}
	if len(router.sideAsked) != 1 {
		t.Fatalf("router.sideAsked = %v", router.sideAsked)
	}

	// PublishEvent delivers bridged frame to /api/events.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	evReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	evResp, err := http.DefaultClient.Do(evReq)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer evResp.Body.Close()

	srv.PublishEvent("status", []byte(`{"session":"remote://gpu-1/s/1","running":true}`))
	scanner := bufio.NewScanner(evResp.Body)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `remote://gpu-1/s/1`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("did not observe published remote status event on SSE stream")
	}
}

type remoteSessionTracker struct {
	Host
	id string
}

func (r *remoteSessionTracker) CurrentSession() string   { return r.id }
func (r *remoteSessionTracker) SessionArchived() bool    { return false }
func (r *remoteSessionTracker) SessionDirectory() string { return "remote://gpu-1/srv/app" }
func (r *remoteSessionTracker) Side() (SideView, error)  { return SideView{Open: true}, nil }
