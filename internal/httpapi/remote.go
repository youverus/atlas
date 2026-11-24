package httpapi

import (
	"encoding/json"
	"net/http"
)

// RemoteController manages external IM control gateways (such as Feishu) embedded
// inside the desktop/web host process. Builds without one answer 501.
type RemoteController interface {
	RemoteStatus() (RemoteStatus, error)
	SaveFeishuConfig(FeishuConfigUpdate) (RemoteStatus, error)
	StartRemote() (RemoteStatus, error)
	StopRemote() (RemoteStatus, error)
	ApprovePairing(code string) (RemoteStatus, error)
	AllowRemoteUser(adapter, userID string) (RemoteStatus, error)
	RevokeRemoteUser(adapter, userID string) (RemoteStatus, error)
	UnbindRemoteChat(key string) (RemoteStatus, error)
}

// RemoteStatus describes the live remote gateway state, saved Feishu config,
// pending pairing requests, runtime allowlist entries, and chat bindings.
type RemoteStatus struct {
	State            string                 `json:"state"`
	Connection       string                 `json:"connection,omitempty"`
	LastError        string                 `json:"lastError,omitempty"`
	StartedAt        string                 `json:"startedAt,omitempty"`
	ConfigPath       string                 `json:"configPath,omitempty"`
	DefaultCWD       string                 `json:"defaultCwd,omitempty"`
	Feishu           FeishuSettingsView     `json:"feishu"`
	PendingCodes     []RemotePairingCode    `json:"pendingCodes"`
	RuntimeAllowlist []RemoteAllowlistEntry `json:"runtimeAllowlist"`
	Bindings         []RemoteBinding        `json:"bindings"`
	RecentLogs       []string               `json:"recentLogs,omitempty"`
}

// FeishuSettingsView exposes non-secret Feishu configuration plus masked secret metadata.
type FeishuSettingsView struct {
	Enabled        bool     `json:"enabled"`
	AppID          string   `json:"appId"`
	HasSecret      bool     `json:"hasSecret"`
	SecretMasked   string   `json:"secretMasked,omitempty"`
	SecretMode     string   `json:"secretMode,omitempty"` // literal | env | unset
	EnvOverride    bool     `json:"envOverride,omitempty"`
	DMPolicy       string   `json:"dmPolicy"`
	GroupPolicy    string   `json:"groupPolicy"`
	RequireMention bool     `json:"requireMention"`
	AllowBots      bool     `json:"allowBots"`
	CWD            string   `json:"cwd,omitempty"`
	Admins         []string `json:"admins"`
	AllowedUsers   []string `json:"allowedUsers"`
}

// FeishuConfigUpdate carries settings submitted from the remote control form.
type FeishuConfigUpdate struct {
	Enabled        bool     `json:"enabled"`
	AppID          string   `json:"appId"`
	AppSecret      string   `json:"appSecret,omitempty"`
	ClearSecret    bool     `json:"clearSecret,omitempty"`
	DMPolicy       string   `json:"dmPolicy"`
	GroupPolicy    string   `json:"groupPolicy"`
	RequireMention bool     `json:"requireMention"`
	AllowBots      bool     `json:"allowBots"`
	CWD            string   `json:"cwd,omitempty"`
	Admins         []string `json:"admins"`
	AllowedUsers   []string `json:"allowedUsers"`
	StartAfterSave bool     `json:"startAfterSave,omitempty"`
}

// RemotePairingCode is a pending pairing code waiting for operator approval.
type RemotePairingCode struct {
	Code    string `json:"code"`
	Adapter string `json:"adapter"`
	UserID  string `json:"userId"`
	ChatID  string `json:"chatId"`
	Created string `json:"created"`
}

// RemoteAllowlistEntry is one runtime-approved user ID for a channel adapter.
type RemoteAllowlistEntry struct {
	Adapter string `json:"adapter"`
	UserID  string `json:"userId"`
}

// RemoteBinding is a persisted chat-to-session mapping created by the gateway.
type RemoteBinding struct {
	Key     string `json:"key"`
	Name    string `json:"name,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Session string `json:"session,omitempty"`
	Created string `json:"created,omitempty"`
}

func (s *Server) mountRemote() {
	s.mux.HandleFunc("GET /api/remote", s.serveRemoteStatus)
	s.mux.HandleFunc("PUT /api/remote/feishu", s.serveRemoteSaveFeishu)
	s.mux.HandleFunc("POST /api/remote/start", s.serveRemoteStart)
	s.mux.HandleFunc("POST /api/remote/stop", s.serveRemoteStop)
	s.mux.HandleFunc("POST /api/remote/approve", s.serveRemoteApprove)
	s.mux.HandleFunc("POST /api/remote/allowlist", s.serveRemoteAllowUser)
	s.mux.HandleFunc("POST /api/remote/allowlist/remove", s.serveRemoteRevokeUser)
	s.mux.HandleFunc("POST /api/remote/bindings/remove", s.serveRemoteUnbindChat)
}

func (s *Server) needRemote(w http.ResponseWriter) bool {
	if s.remote != nil {
		return true
	}
	http.Error(w, "httpapi: remote control is not configured", http.StatusNotImplemented)
	return false
}

func normalizeRemoteStatus(st RemoteStatus) RemoteStatus {
	if st.Feishu.Admins == nil {
		st.Feishu.Admins = []string{}
	}
	if st.Feishu.AllowedUsers == nil {
		st.Feishu.AllowedUsers = []string{}
	}
	if st.PendingCodes == nil {
		st.PendingCodes = []RemotePairingCode{}
	}
	if st.RuntimeAllowlist == nil {
		st.RuntimeAllowlist = []RemoteAllowlistEntry{}
	}
	if st.Bindings == nil {
		st.Bindings = []RemoteBinding{}
	}
	return st
}

func (s *Server) serveRemoteStatus(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	st, err := s.remote.RemoteStatus()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}

func (s *Server) serveRemoteSaveFeishu(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	var req FeishuConfigUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	st, err := s.remote.SaveFeishuConfig(req)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}

func (s *Server) serveRemoteStart(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	st, err := s.remote.StartRemote()
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}

func (s *Server) serveRemoteStop(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	st, err := s.remote.StopRemote()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}

func (s *Server) serveRemoteApprove(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	st, err := s.remote.ApprovePairing(body.Code)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}

func (s *Server) serveRemoteAllowUser(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	var body struct {
		Adapter string `json:"adapter"`
		UserID  string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	st, err := s.remote.AllowRemoteUser(body.Adapter, body.UserID)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}

func (s *Server) serveRemoteRevokeUser(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	var body struct {
		Adapter string `json:"adapter"`
		UserID  string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	st, err := s.remote.RevokeRemoteUser(body.Adapter, body.UserID)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}

func (s *Server) serveRemoteUnbindChat(w http.ResponseWriter, r *http.Request) {
	if !s.needRemote(w) {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	st, err := s.remote.UnbindRemoteChat(body.Key)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, normalizeRemoteStatus(st))
}
