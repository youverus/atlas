package httpapi

import (
	"encoding/json"
	"net/http"
)

// SchedulerController manages user-configured scheduled agent tasks. Builds
// without one answer 501.
type SchedulerController interface {
	ListSchedules() (ScheduleList, error)
	CreateSchedule(ScheduleInput) (ScheduledTask, error)
	UpdateSchedule(id string, input ScheduleInput) (ScheduledTask, error)
	DeleteSchedule(id string) error
	RunScheduleNow(id string) (ScheduledTask, error)
}

// ScheduleList holds all configured tasks plus the host default working directory.
type ScheduleList struct {
	DefaultCWD string          `json:"defaultCwd,omitempty"`
	Tasks      []ScheduledTask `json:"tasks"`
}

// ScheduleInput is the editable payload for creating or updating a scheduled task.
type ScheduleInput struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Prompt          string `json:"prompt"`
	Model           string `json:"model,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	SessionMode     string `json:"sessionMode"`               // new | continue
	ScheduleType    string `json:"scheduleType"`              // daily | interval
	Hour            int    `json:"hour"`                      // 0..23 when daily
	Minute          int    `json:"minute"`                    // 0..59 when daily
	Weekdays        []int  `json:"weekdays,omitempty"`        // 0=Sun..6=Sat; empty means every day
	IntervalMinutes int    `json:"intervalMinutes,omitempty"` // >= 1 when interval
}

// ScheduledTask describes a persisted scheduled task along with runtime status.
type ScheduledTask struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Prompt          string `json:"prompt"`
	Model           string `json:"model,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	SessionMode     string `json:"sessionMode"`
	ScheduleType    string `json:"scheduleType"`
	Hour            int    `json:"hour"`
	Minute          int    `json:"minute"`
	Weekdays        []int  `json:"weekdays,omitempty"`
	IntervalMinutes int    `json:"intervalMinutes,omitempty"`
	Running         bool   `json:"running"`
	LastSession     string `json:"lastSession,omitempty"`
	LastRunAt       string `json:"lastRunAt,omitempty"`
	NextRunAt       string `json:"nextRunAt,omitempty"`
	LastStatus      string `json:"lastStatus,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	LastSummary     string `json:"lastSummary,omitempty"`
	LastDurationMs  int64  `json:"lastDurationMs,omitempty"`
	RunCount        int    `json:"runCount,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

func (s *Server) mountSchedules() {
	s.mux.HandleFunc("GET /api/schedules", s.serveSchedulesList)
	s.mux.HandleFunc("POST /api/schedules", s.serveSchedulesCreate)
	s.mux.HandleFunc("PUT /api/schedules/{id}", s.serveSchedulesUpdate)
	s.mux.HandleFunc("DELETE /api/schedules/{id}", s.serveSchedulesDelete)
	s.mux.HandleFunc("POST /api/schedules/{id}/run", s.serveSchedulesRunNow)
}

func (s *Server) needScheduler(w http.ResponseWriter) bool {
	if s.scheduler != nil {
		return true
	}
	http.Error(w, "httpapi: scheduler is not configured", http.StatusNotImplemented)
	return false
}

func normalizeScheduleTask(t ScheduledTask) ScheduledTask {
	if t.Weekdays == nil {
		t.Weekdays = []int{}
	}
	return t
}

func normalizeScheduleList(list ScheduleList) ScheduleList {
	if list.Tasks == nil {
		list.Tasks = []ScheduledTask{}
	}
	for i := range list.Tasks {
		list.Tasks[i] = normalizeScheduleTask(list.Tasks[i])
	}
	return list
}

func (s *Server) serveSchedulesList(w http.ResponseWriter, r *http.Request) {
	if !s.needScheduler(w) {
		return
	}
	list, err := s.scheduler.ListSchedules()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, normalizeScheduleList(list))
}

func (s *Server) serveSchedulesCreate(w http.ResponseWriter, r *http.Request) {
	if !s.needScheduler(w) {
		return
	}
	var input ScheduleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	task, err := s.scheduler.CreateSchedule(input)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeScheduleTask(task))
}

func (s *Server) serveSchedulesUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.needScheduler(w) {
		return
	}
	id := r.PathValue("id")
	var input ScheduleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	task, err := s.scheduler.UpdateSchedule(id, input)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeScheduleTask(task))
}

func (s *Server) serveSchedulesDelete(w http.ResponseWriter, r *http.Request) {
	if !s.needScheduler(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.scheduler.DeleteSchedule(id); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveSchedulesRunNow(w http.ResponseWriter, r *http.Request) {
	if !s.needScheduler(w) {
		return
	}
	id := r.PathValue("id")
	task, err := s.scheduler.RunScheduleNow(id)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeScheduleTask(task))
}
