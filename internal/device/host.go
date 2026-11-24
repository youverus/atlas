package device

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"pi-go/internal/httpapi"
	"pi-go/internal/project"
)

// CompositeHost routes local calls to webhost and remote-prefixed or active
// remote calls to the owning remote `atlas -serve` client.
type CompositeHost struct {
	local httpapi.Host
	mgr   *Manager
}

var (
	_ httpapi.Host               = (*CompositeHost)(nil)
	_ httpapi.NamedProjectHost   = (*CompositeHost)(nil)
	_ httpapi.RemoteProjectHost  = (*CompositeHost)(nil)
	_ httpapi.ProjectBrowser     = (*CompositeHost)(nil)
	_ httpapi.ProjectDiscoverer  = (*CompositeHost)(nil)
	_ httpapi.SessionStatus      = (*CompositeHost)(nil)
	_ httpapi.LiveModelStatus    = (*CompositeHost)(nil)
	_ httpapi.SessionTracker     = (*CompositeHost)(nil)
	_ httpapi.ConfigReloader     = (*CompositeHost)(nil)
	_ httpapi.MCPStatus          = (*CompositeHost)(nil)
	_ httpapi.MCPSettings        = (*CompositeHost)(nil)
	_ httpapi.SkillStatus        = (*CompositeHost)(nil)
	_ httpapi.SkillSettings      = (*CompositeHost)(nil)
	_ httpapi.PluginStatus       = (*CompositeHost)(nil)
	_ httpapi.BrowserStatus      = (*CompositeHost)(nil)
	_ httpapi.ModelConfigEditor  = (*CompositeHost)(nil)
	_ httpapi.ArtifactController = (*CompositeHost)(nil)
)

func (h *CompositeHost) activeRemoteClient() (*remoteClient, activeRemoteTarget, error) {
	target, ok := h.mgr.activeTarget()
	if !ok {
		return nil, activeRemoteTarget{}, nil
	}
	client, err := h.mgr.clientFor(target.deviceID)
	if err != nil {
		return nil, target, err
	}
	return client, target, nil
}

func (h *CompositeHost) callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 25*time.Second)
}

func (h *CompositeHost) refreshActiveRemoteState(ctx context.Context, client *remoteClient, deviceID string) error {
	state, _, err := client.fetchState(ctx)
	if err != nil {
		return err
	}
	sessKey := ""
	if state.Session != "" {
		sessKey = WrapRemoteKey(deviceID, state.Session)
	}
	h.mgr.setActiveRemote(deviceID, sessKey, state.Directory)
	return nil
}

// CurrentSession reports the currently viewed local or wrapped remote session path.
func (h *CompositeHost) CurrentSession() string {
	if target, ok := h.mgr.activeTarget(); ok {
		return target.sessionKey
	}
	if tracker, ok := h.local.(httpapi.SessionTracker); ok {
		return tracker.CurrentSession()
	}
	return ""
}

// SessionArchived reports whether the currently viewed conversation is archived.
func (h *CompositeHost) SessionArchived() bool {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		if state, _, err := client.fetchState(ctx); err == nil {
			return state.Archived
		}
		client.mu.RLock()
		archived := client.lastState.archived
		client.mu.RUnlock()
		return archived
	}
	if st, ok := h.local.(httpapi.SessionStatus); ok {
		return st.SessionArchived()
	}
	return false
}

// SessionDirectory reports the working directory of the currently viewed session.
func (h *CompositeHost) SessionDirectory() string {
	if client, target, err := h.activeRemoteClient(); err == nil && client != nil {
		return target.remoteDir
	}
	if st, ok := h.local.(httpapi.SessionStatus); ok {
		return st.SessionDirectory()
	}
	return ""
}

// SessionModel reports the active `provider/model` label of the viewed session.
func (h *CompositeHost) SessionModel() string {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		if state, _, err := client.fetchState(ctx); err == nil && state.Model != "" {
			return state.Model
		}
		client.mu.RLock()
		m := client.lastState.model
		client.mu.RUnlock()
		return m
	}
	if lm, ok := h.local.(httpapi.LiveModelStatus); ok {
		return lm.SessionModel()
	}
	return ""
}

// Sessions merges local sessions with connected remote sessions.
func (h *CompositeHost) Sessions(archived bool) ([]httpapi.SessionInfo, error) {
	localSessions, err := h.local.Sessions(archived)
	if err != nil {
		return nil, err
	}
	active, hasRemote := h.mgr.activeTarget()
	if hasRemote {
		for i := range localSessions {
			localSessions[i].Current = false
		}
	}

	h.mgr.mu.RLock()
	clients := make([]*remoteClient, 0, len(h.mgr.conns))
	for _, st := range h.mgr.conns {
		if st.status == "online" && st.client != nil {
			clients = append(clients, st.client)
		}
	}
	h.mgr.mu.RUnlock()

	ctx, cancel := h.callCtx()
	defer cancel()
	for _, c := range clients {
		var remoteList []httpapi.SessionInfo
		endpoint := "/api/sessions"
		if archived {
			endpoint += "?archived=true"
		}
		if _, rErr := c.doJSON(ctx, http.MethodGet, endpoint, nil, &remoteList); rErr != nil {
			continue
		}
		for _, item := range remoteList {
			wrapped := WrapRemoteKey(c.deviceID, item.Path)
			item.Path = wrapped
			item.DeviceID = c.deviceID
			item.Current = hasRemote && active.deviceID == c.deviceID && active.sessionKey == wrapped
			localSessions = append(localSessions, item)
		}
	}
	return localSessions, nil
}

// OpenSession switches view to either a remote session (`remote://<deviceID>/...`)
// or a local session.
func (h *CompositeHost) OpenSession(path string) error {
	if devID, rawPath, ok := ParseRemoteKey(path); ok {
		ctx, cancel := h.callCtx()
		defer cancel()
		client, err := h.mgr.ensureConnectedClient(ctx, devID)
		if err != nil {
			return err
		}
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/sessions/open", map[string]string{"path": rawPath}, nil); err != nil {
			return err
		}
		return h.refreshActiveRemoteState(ctx, client, devID)
	}
	if err := h.local.OpenSession(path); err != nil {
		return err
	}
	h.mgr.clearActiveRemote()
	return nil
}

// NewSession starts a new conversation on whichever target (local or remote) is active.
func (h *CompositeHost) NewSession() error {
	client, target, err := h.activeRemoteClient()
	if err != nil {
		return err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		body := map[string]string{}
		if target.remoteDir != "" {
			body["cwd"] = target.remoteDir
		}
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/sessions/new", body, nil); err != nil {
			return err
		}
		return h.refreshActiveRemoteState(ctx, client, target.deviceID)
	}
	if err := h.local.NewSession(); err != nil {
		return err
	}
	h.mgr.clearActiveRemote()
	return nil
}

// NewSessionIn starts a new local conversation in cwd.
func (h *CompositeHost) NewSessionIn(cwd string) error {
	if devID, rawDir, ok := ParseRemoteKey(cwd); ok {
		return h.NewRemoteSessionIn(devID, rawDir)
	}
	if err := h.local.NewSessionIn(cwd); err != nil {
		return err
	}
	h.mgr.clearActiveRemote()
	return nil
}

// NewRemoteSessionIn starts a new conversation inside cwd on remote deviceID.
func (h *CompositeHost) NewRemoteSessionIn(deviceID, cwd string) error {
	ctx, cancel := h.callCtx()
	defer cancel()
	client, err := h.mgr.ensureConnectedClient(ctx, deviceID)
	if err != nil {
		return err
	}
	cleanDir := ""
	if strings.TrimSpace(cwd) != "" {
		var cleanErr error
		cleanDir, cleanErr = project.CleanRemotePath(cwd)
		if cleanErr != nil {
			return cleanErr
		}
		// Ensure the remote server knows about the project before requesting NewSessionIn.
		_, _ = client.doJSON(ctx, http.MethodPost, "/api/projects", map[string]string{"path": cleanDir}, nil)
	}
	body := map[string]string{}
	if cleanDir != "" {
		body["cwd"] = cleanDir
	}
	if _, err := client.doJSON(ctx, http.MethodPost, "/api/sessions/new", body, nil); err != nil {
		return err
	}
	return h.refreshActiveRemoteState(ctx, client, deviceID)
}

func (h *CompositeHost) ForkPoints() []httpapi.ForkPoint {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var pts []httpapi.ForkPoint
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/sessions/fork-points", nil, &pts); err == nil {
			return pts
		}
		return nil
	}
	return h.local.ForkPoints()
}

func (h *CompositeHost) ForkSession(entryID, name string) error {
	client, target, err := h.activeRemoteClient()
	if err != nil {
		return err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/sessions/fork", map[string]string{
			"entryId": entryID,
			"name":    name,
		}, nil); err != nil {
			return err
		}
		return h.refreshActiveRemoteState(ctx, client, target.deviceID)
	}
	return h.local.ForkSession(entryID, name)
}

func (h *CompositeHost) ArchiveSession(p string) error {
	if devID, rawPath, ok := ParseRemoteKey(p); ok {
		ctx, cancel := h.callCtx()
		defer cancel()
		client, err := h.mgr.ensureConnectedClient(ctx, devID)
		if err != nil {
			return err
		}
		_, err = client.doJSON(ctx, http.MethodPost, "/api/sessions/archive", map[string]string{"path": rawPath}, nil)
		return err
	}
	return h.local.ArchiveSession(p)
}

func (h *CompositeHost) RestoreSession(p string) error {
	if devID, rawPath, ok := ParseRemoteKey(p); ok {
		ctx, cancel := h.callCtx()
		defer cancel()
		client, err := h.mgr.ensureConnectedClient(ctx, devID)
		if err != nil {
			return err
		}
		_, err = client.doJSON(ctx, http.MethodPost, "/api/sessions/restore", map[string]string{"path": rawPath}, nil)
		return err
	}
	return h.local.RestoreSession(p)
}

func (h *CompositeHost) DeleteSession(p string) error {
	if devID, rawPath, ok := ParseRemoteKey(p); ok {
		ctx, cancel := h.callCtx()
		defer cancel()
		client, err := h.mgr.ensureConnectedClient(ctx, devID)
		if err != nil {
			return err
		}
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/sessions/delete", map[string]string{"path": rawPath}, nil); err != nil {
			return err
		}
		if target, has := h.mgr.activeTarget(); has && target.sessionKey == p {
			_ = h.refreshActiveRemoteState(ctx, client, devID)
		}
		return nil
	}
	return h.local.DeleteSession(p)
}

func (h *CompositeHost) Transcript() ([]httpapi.TranscriptBlock, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return nil, err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var blocks []httpapi.TranscriptBlock
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/sessions/transcript", nil, &blocks); err != nil {
			return nil, err
		}
		return blocks, nil
	}
	return h.local.Transcript()
}

func (h *CompositeHost) Trajectory() ([]httpapi.TrajectoryRow, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return nil, err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var rows []httpapi.TrajectoryRow
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/sessions/trajectory", nil, &rows); err != nil {
			return nil, err
		}
		return rows, nil
	}
	return h.local.Trajectory()
}

func (h *CompositeHost) Side() (httpapi.SideView, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return httpapi.SideView{}, err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var view httpapi.SideView
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/side", nil, &view); err != nil {
			return httpapi.SideView{}, err
		}
		return view, nil
	}
	return h.local.Side()
}

func (h *CompositeHost) OpenSide() error {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		_, err := client.doJSON(ctx, http.MethodPost, "/api/side", map[string]any{}, nil)
		return err
	}
	return h.local.OpenSide()
}

func (h *CompositeHost) CloseSide() error {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		_, err := client.doJSON(ctx, http.MethodPost, "/api/side/close", map[string]any{}, nil)
		return err
	}
	return h.local.CloseSide()
}

// Projects returns local project groups followed by persisted remote project
// groups, fetching sessions for online remote devices and showing offline status
// for disconnected targets.
func (h *CompositeHost) Projects(archived bool) (httpapi.ProjectList, error) {
	localList, err := h.local.Projects(archived)
	if err != nil {
		return httpapi.ProjectList{}, err
	}
	active, hasRemote := h.mgr.activeTarget()
	if hasRemote {
		for gi := range localList.Groups {
			for si := range localList.Groups[gi].Sessions {
				localList.Groups[gi].Sessions[si].Current = false
			}
		}
	}

	stored := h.mgr.projects.List()
	ctx, cancel := h.callCtx()
	defer cancel()

	// Fetch remote project lists at most once per connected device.
	remoteProjectsByDev := map[string]httpapi.ProjectList{}
	remoteFetchErrByDev := map[string]string{}

	for _, entry := range stored {
		if project.IsLocalDevice(entry.DeviceID) {
			continue
		}
		devInfo := h.mgr.deviceInfo(entry.DeviceID)
		client, _ := h.mgr.clientFor(entry.DeviceID)

		if client != nil {
			if _, ok := remoteProjectsByDev[entry.DeviceID]; !ok {
				if _, seenErr := remoteFetchErrByDev[entry.DeviceID]; !seenErr {
					endpoint := "/api/projects"
					if archived {
						endpoint += "?archived=true"
					}
					var pl httpapi.ProjectList
					if _, rErr := client.doJSON(ctx, http.MethodGet, endpoint, nil, &pl); rErr != nil {
						remoteFetchErrByDev[entry.DeviceID] = rErr.Error()
					} else {
						remoteProjectsByDev[entry.DeviceID] = pl
					}
				}
			}
		}

		cleanPath, cleanErr := project.CleanRemotePath(entry.Path)
		if cleanErr != nil {
			cleanPath = strings.TrimSpace(entry.Path)
		}
		displayName := strings.TrimSpace(entry.Name)
		if displayName == "" {
			displayName = path.Base(cleanPath)
			if displayName == "/" || displayName == "." || displayName == "" {
				displayName = cleanPath
			}
		}

		var sessions []httpapi.SessionInfo
		missingFlag := false
		if client != nil {
			if pl, ok := remoteProjectsByDev[entry.DeviceID]; ok {
				matched := false
				for _, rg := range pl.Groups {
					rgPath, errRg := project.CleanRemotePath(rg.Path)
					if errRg != nil {
						rgPath = strings.TrimSpace(rg.Path)
					}
					if rgPath == cleanPath {
						matched = true
						missingFlag = rg.Missing
						sessions = make([]httpapi.SessionInfo, 0, len(rg.Sessions))
						for _, s := range rg.Sessions {
							wrapped := WrapRemoteKey(entry.DeviceID, s.Path)
							s.Path = wrapped
							s.DeviceID = entry.DeviceID
							s.Current = hasRemote && active.deviceID == entry.DeviceID && active.sessionKey == wrapped
							sessions = append(sessions, s)
						}
						break
					}
				}
				if !matched && !archived {
					// Ensure remote knows about this added project path for future session launches.
					_, _ = client.doJSON(ctx, http.MethodPost, "/api/projects", map[string]string{
						"path": cleanPath,
						"name": entry.Name,
					}, nil)
				}
			}
		}

		if archived && len(sessions) == 0 {
			continue
		}
		if sessions == nil {
			sessions = []httpapi.SessionInfo{}
		}
		remoteErr := devInfo.Error
		if fetchErr := remoteFetchErrByDev[entry.DeviceID]; fetchErr != "" {
			remoteErr = fetchErr
		}
		localList.Groups = append(localList.Groups, httpapi.ProjectGroup{
			Path:        cleanPath,
			Name:        displayName,
			DeviceID:    entry.DeviceID,
			DeviceName:  devInfo.Name,
			DeviceState: devInfo.Status,
			RemoteError: remoteErr,
			Missing:     missingFlag,
			Sessions:    sessions,
		})
	}
	return localList, nil
}

func (h *CompositeHost) AddProject(p string) error {
	return h.AddNamedProject(p, "")
}

func (h *CompositeHost) AddNamedProject(p, name string) error {
	if named, ok := h.local.(httpapi.NamedProjectHost); ok {
		return named.AddNamedProject(p, name)
	}
	return h.local.AddProject(p)
}

func (h *CompositeHost) RemoveProject(p string) error {
	return h.local.RemoveProject(p)
}

// AddRemoteProject validates or connects deviceID, registers the directory on
// the remote server, and persists the remote project entry locally.
func (h *CompositeHost) AddRemoteProject(deviceID, remotePath, name string) error {
	if _, ok := h.mgr.store.Get(deviceID); !ok {
		return fmt.Errorf("device: unknown device %q", deviceID)
	}
	clean, err := project.CleanRemotePath(remotePath)
	if err != nil {
		return err
	}
	ctx, cancel := h.callCtx()
	defer cancel()
	client, err := h.mgr.ensureConnectedClient(ctx, deviceID)
	if err != nil {
		return err
	}
	if _, err := client.doJSON(ctx, http.MethodPost, "/api/projects", map[string]string{
		"path": clean,
		"name": strings.TrimSpace(name),
	}, nil); err != nil {
		return err
	}
	_, err = h.mgr.projects.AddRemote(deviceID, clean, name)
	return err
}

// RemoveRemoteProject forgets the remote project locally and removes it from
// the remote host's projects.json if currently connected.
func (h *CompositeHost) RemoveRemoteProject(deviceID, remotePath string) error {
	clean, err := project.CleanRemotePath(remotePath)
	if err != nil {
		return err
	}
	if err := h.mgr.projects.RemoveForDevice(deviceID, clean); err != nil {
		return err
	}
	if client, err := h.mgr.clientFor(deviceID); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		_, _ = client.doJSON(ctx, http.MethodPost, "/api/projects/remove", map[string]string{"path": clean}, nil)
	}
	return nil
}

// BrowseDirs delegates directory browsing to local webhost or the target remote device.
func (h *CompositeHost) BrowseDirs(deviceID, remotePath string, includeHidden bool) (httpapi.DirBrowseResult, error) {
	if project.IsLocalDevice(deviceID) {
		browser, ok := h.local.(httpapi.ProjectBrowser)
		if !ok {
			return httpapi.DirBrowseResult{}, fmt.Errorf("local directory browser unavailable")
		}
		return browser.BrowseDirs(project.LocalDeviceID, remotePath, includeHidden)
	}
	ctx, cancel := h.callCtx()
	defer cancel()
	client, err := h.mgr.ensureConnectedClient(ctx, deviceID)
	if err != nil {
		return httpapi.DirBrowseResult{}, err
	}
	endpoint := client.remoteQueryURL("/api/projects/browse", map[string]string{
		"path":   remotePath,
		"hidden": fmt.Sprintf("%t", includeHidden),
	})
	var res httpapi.DirBrowseResult
	if _, err := client.doJSON(ctx, http.MethodGet, endpoint, nil, &res); err != nil {
		return httpapi.DirBrowseResult{}, err
	}
	res.DeviceID = deviceID
	return res, nil
}

// DiscoverProjects discovers existing projects and historical sessions on local or remote deviceID.
func (h *CompositeHost) DiscoverProjects(deviceID string) ([]httpapi.DiscoveredProject, error) {
	if project.IsLocalDevice(deviceID) {
		discoverer, ok := h.local.(httpapi.ProjectDiscoverer)
		if !ok {
			return nil, fmt.Errorf("local project discovery unavailable")
		}
		return discoverer.DiscoverProjects(project.LocalDeviceID)
	}
	ctx, cancel := h.callCtx()
	defer cancel()
	client, err := h.mgr.ensureConnectedClient(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	var res httpapi.DiscoverProjectsResult
	if _, err := client.doJSON(ctx, http.MethodGet, "/api/projects/discover", nil, &res); err != nil {
		return nil, err
	}

	// Mark which remote projects have already been added to local desktop projects.json.
	addedRemote := map[string]bool{}
	for _, entry := range h.mgr.projects.List() {
		if entry.DeviceID == deviceID {
			if p, pErr := project.CleanRemotePath(entry.Path); pErr == nil {
				addedRemote[p] = true
			}
		}
	}
	for i := range res.Projects {
		res.Projects[i].DeviceID = deviceID
		if p, pErr := project.CleanRemotePath(res.Projects[i].Path); pErr == nil {
			res.Projects[i].Added = addedRemote[p]
		}
	}
	return res.Projects, nil
}

func (h *CompositeHost) Models() (httpapi.ModelList, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res httpapi.ModelList
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/models", nil, &res); err != nil {
			return httpapi.ModelList{}, err
		}
		return res, nil
	}
	return h.local.Models()
}

func (h *CompositeHost) SetModel(provider, id string) error {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		_, err := client.doJSON(ctx, http.MethodPost, "/api/models", map[string]string{
			"provider": provider,
			"id":       id,
		}, nil)
		return err
	}
	return h.local.SetModel(provider, id)
}

func (h *CompositeHost) SetThinking(level string) error {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		_, err := client.doJSON(ctx, http.MethodPost, "/api/thinking", map[string]string{"level": level}, nil)
		return err
	}
	return h.local.SetThinking(level)
}

func (h *CompositeHost) Permission() (httpapi.PermissionState, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res httpapi.PermissionState
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/permission", nil, &res); err != nil {
			return httpapi.PermissionState{}, err
		}
		return res, nil
	}
	return h.local.Permission()
}

func (h *CompositeHost) SetPermission(mode string) error {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		_, err := client.doJSON(ctx, http.MethodPost, "/api/permission", map[string]string{"mode": mode}, nil)
		return err
	}
	return h.local.SetPermission(mode)
}

func (h *CompositeHost) Stats() (httpapi.Stats, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res httpapi.Stats
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/stats", nil, &res); err != nil {
			return httpapi.Stats{}, err
		}
		return res, nil
	}
	return h.local.Stats()
}

func (h *CompositeHost) ReadDir(p string) (httpapi.DirListing, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return httpapi.DirListing{}, err
	}
	if client == nil {
		return h.local.ReadDir(p)
	}
	ctx, cancel := h.callCtx()
	defer cancel()
	endpoint := client.remoteQueryURL("/api/files", map[string]string{"path": p})
	var res httpapi.DirListing
	_, err = client.doJSON(ctx, http.MethodGet, endpoint, nil, &res)
	return res, err
}

func (h *CompositeHost) ReadFile(p string) (httpapi.FileContent, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		endpoint := client.remoteQueryURL("/api/file", map[string]string{"path": p})
		var res httpapi.FileContent
		if _, err := client.doJSON(ctx, http.MethodGet, endpoint, nil, &res); err != nil {
			return httpapi.FileContent{}, err
		}
		return res, nil
	}
	return h.local.ReadFile(p)
}

func (h *CompositeHost) ReloadConfig() error {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		_, rErr := client.doJSON(ctx, http.MethodPost, "/api/config/reload", map[string]any{}, nil)
		return rErr
	}
	if reloader, ok := h.local.(httpapi.ConfigReloader); ok {
		return reloader.ReloadConfig()
	}
	return fmt.Errorf("reload unsupported")
}

func (h *CompositeHost) MCP() (httpapi.MCPState, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res httpapi.MCPState
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/mcp", nil, &res); err != nil {
			return httpapi.MCPState{}, err
		}
		return res, nil
	}
	if mcp, ok := h.local.(httpapi.MCPStatus); ok {
		return mcp.MCP()
	}
	return httpapi.MCPState{}, fmt.Errorf("mcp unsupported")
}

func (h *CompositeHost) RefreshMCP(name string) (httpapi.MCPState, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res httpapi.MCPState
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/mcp/refresh", map[string]string{"name": name}, &res); err != nil {
			return httpapi.MCPState{}, err
		}
		return res, nil
	}
	if mcp, ok := h.local.(httpapi.MCPStatus); ok {
		return mcp.RefreshMCP(name)
	}
	return httpapi.MCPState{}, fmt.Errorf("mcp refresh unsupported")
}

// SetMCPEnabled changes the configuration on the host that owns the session.
func (h *CompositeHost) SetMCPEnabled(ctx context.Context, name string, enabled bool) (httpapi.MCPState, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return httpapi.MCPState{}, err
	}
	if client != nil {
		var state httpapi.MCPState
		_, err := client.doJSON(ctx, http.MethodPost, "/api/mcp/enabled", map[string]any{"name": name, "enabled": enabled}, &state)
		return state, err
	}
	if settings, ok := h.local.(httpapi.MCPSettings); ok {
		return settings.SetMCPEnabled(ctx, name, enabled)
	}
	return httpapi.MCPState{}, fmt.Errorf("mcp settings unsupported")
}

func (h *CompositeHost) Skills() (httpapi.SkillState, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res httpapi.SkillState
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/skills", nil, &res); err != nil {
			return httpapi.SkillState{}, err
		}
		return res, nil
	}
	if sk, ok := h.local.(httpapi.SkillStatus); ok {
		return sk.Skills()
	}
	return httpapi.SkillState{}, fmt.Errorf("skills unsupported")
}

func (h *CompositeHost) SkillContent(name string) (httpapi.SkillDetail, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		endpoint := client.remoteQueryURL("/api/skills/content", map[string]string{"name": name})
		var res httpapi.SkillDetail
		if _, err := client.doJSON(ctx, http.MethodGet, endpoint, nil, &res); err != nil {
			return httpapi.SkillDetail{}, err
		}
		return res, nil
	}
	if sk, ok := h.local.(httpapi.SkillStatus); ok {
		return sk.SkillContent(name)
	}
	return httpapi.SkillDetail{}, fmt.Errorf("skill content unsupported")
}

func (h *CompositeHost) LoadSkillPrompt(name, args string) (string, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res struct {
			Prompt string `json:"prompt"`
		}
		if _, err := client.doJSON(ctx, http.MethodPost, "/api/skills/load", map[string]string{"name": name, "args": args}, &res); err != nil {
			return "", err
		}
		return res.Prompt, nil
	}
	if sk, ok := h.local.(httpapi.SkillStatus); ok {
		return sk.LoadSkillPrompt(name, args)
	}
	return "", fmt.Errorf("load skill prompt unsupported")
}

// SetSkillEnabled changes the configuration on the host that owns the session.
func (h *CompositeHost) SetSkillEnabled(ctx context.Context, name string, enabled bool) (httpapi.SkillState, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return httpapi.SkillState{}, err
	}
	if client != nil {
		var state httpapi.SkillState
		_, err := client.doJSON(ctx, http.MethodPost, "/api/skills/enabled", map[string]any{"name": name, "enabled": enabled}, &state)
		return state, err
	}
	if settings, ok := h.local.(httpapi.SkillSettings); ok {
		return settings.SetSkillEnabled(ctx, name, enabled)
	}
	return httpapi.SkillState{}, fmt.Errorf("skill settings unsupported")
}

func (h *CompositeHost) Plugins() (httpapi.PluginState, error) {
	if client, _, err := h.activeRemoteClient(); err == nil && client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var res httpapi.PluginState
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/plugins", nil, &res); err != nil {
			return httpapi.PluginState{}, err
		}
		return res, nil
	}
	if pl, ok := h.local.(httpapi.PluginStatus); ok {
		return pl.Plugins()
	}
	return httpapi.PluginState{}, fmt.Errorf("plugins unsupported")
}

func (h *CompositeHost) BrowserState() (httpapi.BrowserState, error) {
	if br, ok := h.local.(httpapi.BrowserStatus); ok {
		return br.BrowserState()
	}
	return httpapi.BrowserState{}, fmt.Errorf("browser unsupported")
}

func (h *CompositeHost) BrowserOpen(rawURL string) (httpapi.BrowserTabInfo, error) {
	if br, ok := h.local.(httpapi.BrowserStatus); ok {
		return br.BrowserOpen(rawURL)
	}
	return httpapi.BrowserTabInfo{}, fmt.Errorf("browser unsupported")
}

func (h *CompositeHost) BrowserNavigate(tabID, rawURL string) (httpapi.BrowserTabInfo, error) {
	if br, ok := h.local.(httpapi.BrowserStatus); ok {
		return br.BrowserNavigate(tabID, rawURL)
	}
	return httpapi.BrowserTabInfo{}, fmt.Errorf("browser unsupported")
}

func (h *CompositeHost) BrowserAction(tabID, action string) error {
	if br, ok := h.local.(httpapi.BrowserStatus); ok {
		return br.BrowserAction(tabID, action)
	}
	return fmt.Errorf("browser unsupported")
}

func (h *CompositeHost) BrowserScreenshot(tabID string) (httpapi.BrowserCapture, error) {
	if br, ok := h.local.(httpapi.BrowserStatus); ok {
		return br.BrowserScreenshot(tabID)
	}
	return httpapi.BrowserCapture{}, fmt.Errorf("browser unsupported")
}

func (h *CompositeHost) ModelConfig() (httpapi.ModelConfig, error) {
	if ed, ok := h.local.(httpapi.ModelConfigEditor); ok {
		return ed.ModelConfig()
	}
	return httpapi.ModelConfig{}, fmt.Errorf("model config editor unsupported")
}

func (h *CompositeHost) SaveModelConfig(cfg httpapi.ModelConfig) error {
	if ed, ok := h.local.(httpapi.ModelConfigEditor); ok {
		return ed.SaveModelConfig(cfg)
	}
	return fmt.Errorf("model config editor unsupported")
}

// ListArtifacts returns artifacts for the active session (remote or local).
func (h *CompositeHost) ListArtifacts() ([]httpapi.ArtifactSummary, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return nil, err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		var out []httpapi.ArtifactSummary
		if _, err := client.doJSON(ctx, http.MethodGet, "/api/artifacts", nil, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	if ac, ok := h.local.(httpapi.ArtifactController); ok {
		return ac.ListArtifacts()
	}
	return []httpapi.ArtifactSummary{}, nil
}

// GetArtifact loads an artifact (or specific version) from the active host.
func (h *CompositeHost) GetArtifact(id string, version int) (httpapi.ArtifactDetail, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return httpapi.ArtifactDetail{}, err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		endpoint := "/api/artifacts/" + url.PathEscape(id)
		if version > 0 {
			endpoint += "?v=" + strconv.Itoa(version)
		}
		var out httpapi.ArtifactDetail
		if _, err := client.doJSON(ctx, http.MethodGet, endpoint, nil, &out); err != nil {
			return httpapi.ArtifactDetail{}, err
		}
		return out, nil
	}
	if ac, ok := h.local.(httpapi.ArtifactController); ok {
		return ac.GetArtifact(id, version)
	}
	return httpapi.ArtifactDetail{}, fmt.Errorf("artifacts unsupported")
}

// DeleteArtifact deletes an artifact from the active host.
func (h *CompositeHost) DeleteArtifact(id string) error {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		endpoint := "/api/artifacts/" + url.PathEscape(id)
		if _, err := client.doJSON(ctx, http.MethodDelete, endpoint, nil, nil); err != nil {
			return err
		}
		return nil
	}
	if ac, ok := h.local.(httpapi.ArtifactController); ok {
		return ac.DeleteArtifact(id)
	}
	return fmt.Errorf("artifacts unsupported")
}

// ExportArtifact exports an artifact version into the active workspace.
func (h *CompositeHost) ExportArtifact(id string, version int, relPath string) (httpapi.ArtifactExportResult, error) {
	client, _, err := h.activeRemoteClient()
	if err != nil {
		return httpapi.ArtifactExportResult{}, err
	}
	if client != nil {
		ctx, cancel := h.callCtx()
		defer cancel()
		endpoint := "/api/artifacts/" + url.PathEscape(id) + "/export"
		body := map[string]any{
			"version": version,
			"path":    relPath,
		}
		var out httpapi.ArtifactExportResult
		if _, err := client.doJSON(ctx, http.MethodPost, endpoint, body, &out); err != nil {
			return httpapi.ArtifactExportResult{}, err
		}
		return out, nil
	}
	if ac, ok := h.local.(httpapi.ArtifactController); ok {
		return ac.ExportArtifact(id, version, relPath)
	}
	return httpapi.ArtifactExportResult{}, fmt.Errorf("artifacts unsupported")
}
