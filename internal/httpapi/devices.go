package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// DeviceInfo describes one configured remote SSH compute host and its live
// connection state.
type DeviceInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	Port          int      `json:"port,omitempty"`
	User          string   `json:"user,omitempty"`
	IdentityFile  string   `json:"identityFile,omitempty"`
	RemoteBin     string   `json:"remoteBin,omitempty"`
	Status        string   `json:"status"` // offline | connecting | online | error
	Error         string   `json:"error,omitempty"`
	RemoteOS      string   `json:"remoteOs,omitempty"`
	RemoteArch    string   `json:"remoteArch,omitempty"`
	RemoteVersion string   `json:"remoteVersion,omitempty"`
	RemoteHome    string   `json:"remoteHome,omitempty"`
	LastConnected string   `json:"lastConnected,omitempty"`
	Logs          []string `json:"logs"`
}

// DeviceInput carries editable fields when adding or updating a remote device.
type DeviceInput struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port,omitempty"`
	User         string `json:"user,omitempty"`
	IdentityFile string `json:"identityFile,omitempty"`
	RemoteBin    string `json:"remoteBin,omitempty"`
}

// DeviceList is the full snapshot returned by GET /api/devices.
type DeviceList struct {
	ConfigPath string       `json:"configPath,omitempty"`
	SSHHosts   []string     `json:"sshHosts,omitempty"`
	Devices    []DeviceInfo `json:"devices"`
}

// DeviceController manages SSH remote compute devices and their forwarded
// `atlas -serve` connections. Builds without one answer 501.
type DeviceController interface {
	Devices() (DeviceList, error)
	SaveDevice(id string, input DeviceInput) (DeviceInfo, error)
	DeleteDevice(id string) error
	ConnectDevice(ctx context.Context, id string) (DeviceInfo, error)
	DisconnectDevice(id string) (DeviceInfo, error)
	SyncDeviceConfig(ctx context.Context, id string) error
}

// PendingQuestionItem mirrors the /api/state pending approval payload so
// RemoteRouter can contribute open approvals from connected remote devices.
type PendingQuestionItem struct {
	ID string `json:"id"`
	Question
}

type remoteTurnStopper interface {
	ForwardStop(ctx context.Context, sessionKey, turnID string, side bool) (handled bool, status int, err error)
	RemoteTurnID(sessionKey string) string
}

// RemoteRouter intercepts turn, approval, compact, side-conversation, and
// state inspection calls when the target session belongs to a connected remote
// device.
type RemoteRouter interface {
	ForwardAsk(ctx context.Context, sessionKey, prompt string, attachments []Attachment) (handled bool, err error)
	ForwardApproval(ctx context.Context, questionID string, allowed, always bool) (handled bool, err error)
	ForwardCompact(ctx context.Context, sessionKey, note string) (handled bool, ok bool, err error)
	ForwardSideAsk(ctx context.Context, sessionKey, prompt string) (handled bool, err error)
	IsRemoteSideRunning(sessionKey string) bool
	RunningRemoteSessions() []string
	PendingRemoteApprovals() []PendingQuestionItem
	ActiveRemoteDevice() (deviceID, directory string, ok bool)
}

func (s *Server) mountDevices() {
	s.mux.HandleFunc("GET /api/devices", s.serveDevices)
	s.mux.HandleFunc("POST /api/devices", s.serveDeviceCreate)
	s.mux.HandleFunc("PUT /api/devices/{id}", s.serveDeviceUpdate)
	s.mux.HandleFunc("DELETE /api/devices/{id}", s.serveDeviceDelete)
	s.mux.HandleFunc("POST /api/devices/{id}/connect", s.serveDeviceConnect)
	s.mux.HandleFunc("POST /api/devices/{id}/disconnect", s.serveDeviceDisconnect)
	s.mux.HandleFunc("POST /api/devices/{id}/sync-config", s.serveDeviceSyncConfig)
}

func (s *Server) needDevices(w http.ResponseWriter) bool {
	if s.devices != nil {
		return true
	}
	http.Error(w, "httpapi: remote devices are not configured", http.StatusNotImplemented)
	return false
}

func normalizeDeviceList(list DeviceList) DeviceList {
	if list.SSHHosts == nil {
		list.SSHHosts = []string{}
	}
	if list.Devices == nil {
		list.Devices = []DeviceInfo{}
	}
	for i := range list.Devices {
		if list.Devices[i].Logs == nil {
			list.Devices[i].Logs = []string{}
		}
	}
	return list
}

func normalizeDeviceInfo(info DeviceInfo) DeviceInfo {
	if info.Logs == nil {
		info.Logs = []string{}
	}
	return info
}

func (s *Server) serveDevices(w http.ResponseWriter, r *http.Request) {
	if !s.needDevices(w) {
		return
	}
	list, err := s.devices.Devices()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, normalizeDeviceList(list))
}

func (s *Server) serveDeviceCreate(w http.ResponseWriter, r *http.Request) {
	if !s.needDevices(w) {
		return
	}
	var input DeviceInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&input); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	info, err := s.devices.SaveDevice("", input)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeDeviceInfo(info))
}

func (s *Server) serveDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.needDevices(w) {
		return
	}
	id := r.PathValue("id")
	var input DeviceInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&input); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	info, err := s.devices.SaveDevice(id, input)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeDeviceInfo(info))
}

func (s *Server) serveDeviceDelete(w http.ResponseWriter, r *http.Request) {
	if !s.needDevices(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.devices.DeleteDevice(id); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveDeviceConnect(w http.ResponseWriter, r *http.Request) {
	if !s.needDevices(w) {
		return
	}
	id := r.PathValue("id")
	info, err := s.devices.ConnectDevice(r.Context(), id)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, normalizeDeviceInfo(info))
}

func (s *Server) serveDeviceDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.needDevices(w) {
		return
	}
	id := r.PathValue("id")
	info, err := s.devices.DisconnectDevice(id)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeDeviceInfo(info))
}

func (s *Server) serveDeviceSyncConfig(w http.ResponseWriter, r *http.Request) {
	if !s.needDevices(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.devices.SyncDeviceConfig(r.Context(), id); err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
