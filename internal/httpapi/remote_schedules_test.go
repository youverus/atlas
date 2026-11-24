package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubRemoteController struct {
	st       RemoteStatus
	saved    *FeishuConfigUpdate
	started  bool
	stopped  bool
	approved string
	allowed  [2]string
	revoked  [2]string
	unbound  string
}

func (s *stubRemoteController) RemoteStatus() (RemoteStatus, error) {
	return s.st, nil
}

func (s *stubRemoteController) SaveFeishuConfig(req FeishuConfigUpdate) (RemoteStatus, error) {
	s.saved = &req
	s.st.Feishu.AppID = req.AppID
	s.st.Feishu.Enabled = req.Enabled
	return s.st, nil
}

func (s *stubRemoteController) StartRemote() (RemoteStatus, error) {
	s.started = true
	s.st.State = "running"
	return s.st, nil
}

func (s *stubRemoteController) StopRemote() (RemoteStatus, error) {
	s.stopped = true
	s.st.State = "stopped"
	return s.st, nil
}

func (s *stubRemoteController) ApprovePairing(code string) (RemoteStatus, error) {
	s.approved = code
	return s.st, nil
}

func (s *stubRemoteController) AllowRemoteUser(adapter, userID string) (RemoteStatus, error) {
	s.allowed = [2]string{adapter, userID}
	return s.st, nil
}

func (s *stubRemoteController) RevokeRemoteUser(adapter, userID string) (RemoteStatus, error) {
	s.revoked = [2]string{adapter, userID}
	return s.st, nil
}

func (s *stubRemoteController) UnbindRemoteChat(key string) (RemoteStatus, error) {
	s.unbound = key
	return s.st, nil
}

type stubSchedulerController struct {
	tasks   []ScheduledTask
	deleted string
	runID   string
}

func (s *stubSchedulerController) ListSchedules() (ScheduleList, error) {
	return ScheduleList{DefaultCWD: "/tmp/work", Tasks: s.tasks}, nil
}

func (s *stubSchedulerController) CreateSchedule(in ScheduleInput) (ScheduledTask, error) {
	t := ScheduledTask{
		ID:              "task_1",
		Name:            in.Name,
		Enabled:         in.Enabled,
		Prompt:          in.Prompt,
		Model:           in.Model,
		CWD:             in.CWD,
		SessionMode:     in.SessionMode,
		ScheduleType:    in.ScheduleType,
		Hour:            in.Hour,
		Minute:          in.Minute,
		Weekdays:        in.Weekdays,
		IntervalMinutes: in.IntervalMinutes,
	}
	s.tasks = append(s.tasks, t)
	return t, nil
}

func (s *stubSchedulerController) UpdateSchedule(id string, in ScheduleInput) (ScheduledTask, error) {
	t := ScheduledTask{ID: id, Name: in.Name, Prompt: in.Prompt, Enabled: in.Enabled}
	s.tasks = []ScheduledTask{t}
	return t, nil
}

func (s *stubSchedulerController) DeleteSchedule(id string) error {
	s.deleted = id
	return nil
}

func (s *stubSchedulerController) RunScheduleNow(id string) (ScheduledTask, error) {
	s.runID = id
	return ScheduledTask{ID: id, Running: true}, nil
}

func doLocalReq(h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRemoteAndSchedulerRoutes501AndRoundTrip(t *testing.T) {
	bare := New(Options{}).Handler()
	if rec := doLocalReq(bare, "GET", "/api/remote", nil); rec.Code != http.StatusNotImplemented {
		t.Fatalf("GET /api/remote without controller = %d, want 501", rec.Code)
	}
	if rec := doLocalReq(bare, "GET", "/api/schedules", nil); rec.Code != http.StatusNotImplemented {
		t.Fatalf("GET /api/schedules without controller = %d, want 501", rec.Code)
	}

	remote := &stubRemoteController{st: RemoteStatus{State: "stopped"}}
	sched := &stubSchedulerController{}
	h := New(Options{Remote: remote, Scheduler: sched}).Handler()

	// Remote routes
	if rec := doLocalReq(h, "PUT", "/api/remote/feishu", FeishuConfigUpdate{Enabled: true, AppID: "cli_1"}); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/remote/feishu = %d: %s", rec.Code, rec.Body.String())
	}
	if remote.saved == nil || remote.saved.AppID != "cli_1" {
		t.Fatalf("saved config missing: %+v", remote.saved)
	}
	if rec := doLocalReq(h, "POST", "/api/remote/start", nil); rec.Code != http.StatusOK || !remote.started {
		t.Fatalf("POST /api/remote/start failed: %d started=%v", rec.Code, remote.started)
	}
	if rec := doLocalReq(h, "POST", "/api/remote/approve", map[string]string{"code": "123456"}); rec.Code != http.StatusOK || remote.approved != "123456" {
		t.Fatalf("POST /api/remote/approve failed: %d approved=%q", rec.Code, remote.approved)
	}
	if rec := doLocalReq(h, "POST", "/api/remote/allowlist", map[string]string{"adapter": "feishu", "userId": "u1"}); rec.Code != http.StatusOK || remote.allowed[1] != "u1" {
		t.Fatalf("POST /api/remote/allowlist failed: %d", rec.Code)
	}
	if rec := doLocalReq(h, "POST", "/api/remote/allowlist/remove", map[string]string{"adapter": "feishu", "userId": "u1"}); rec.Code != http.StatusOK || remote.revoked[1] != "u1" {
		t.Fatalf("POST /api/remote/allowlist/remove failed: %d", rec.Code)
	}
	if rec := doLocalReq(h, "POST", "/api/remote/bindings/remove", map[string]string{"key": "feishu:oc_1"}); rec.Code != http.StatusOK || remote.unbound != "feishu:oc_1" {
		t.Fatalf("POST /api/remote/bindings/remove failed: %d", rec.Code)
	}
	if rec := doLocalReq(h, "POST", "/api/remote/stop", nil); rec.Code != http.StatusOK || !remote.stopped {
		t.Fatalf("POST /api/remote/stop failed: %d stopped=%v", rec.Code, remote.stopped)
	}

	// Schedule routes
	if rec := doLocalReq(h, "POST", "/api/schedules", ScheduleInput{Name: "日报", Enabled: true, Prompt: "汇总"}); rec.Code != http.StatusOK {
		t.Fatalf("POST /api/schedules = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doLocalReq(h, "GET", "/api/schedules", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/schedules = %d", rec.Code)
	} else {
		var list ScheduleList
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Tasks) != 1 {
			t.Fatalf("unexpected GET /api/schedules body: %s (%v)", rec.Body.String(), err)
		}
	}
	if rec := doLocalReq(h, "PUT", "/api/schedules/task_1", ScheduleInput{Name: "日报2", Prompt: "汇总2"}); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/schedules/task_1 = %d", rec.Code)
	}
	if rec := doLocalReq(h, "POST", "/api/schedules/task_1/run", nil); rec.Code != http.StatusOK || sched.runID != "task_1" {
		t.Fatalf("POST /api/schedules/task_1/run = %d runID=%q", rec.Code, sched.runID)
	}
	if rec := doLocalReq(h, "DELETE", "/api/schedules/task_1", nil); rec.Code != http.StatusOK || sched.deleted != "task_1" {
		t.Fatalf("DELETE /api/schedules/task_1 = %d deleted=%q", rec.Code, sched.deleted)
	}
}
