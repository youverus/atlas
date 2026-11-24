package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pi-go/internal/wire"
)

// projectHost overrides only the methods the project routes touch. The embedded
// interface stands in for the rest of Host, and a route that reached one of them
// would panic — which is the right answer for a test about these five calls.
type projectHost struct {
	Host

	groups   []ProjectGroup
	added    []string
	named    map[string]string
	removed  []string
	newIn    []string
	newPlain int
	addErr   error
}

func (h *projectHost) Projects(archived bool) (ProjectList, error) {
	return ProjectList{Groups: h.groups}, nil
}

func (h *projectHost) AddProject(path string) error {
	if h.addErr != nil {
		return h.addErr
	}
	h.added = append(h.added, path)
	return nil
}

func (h *projectHost) AddNamedProject(path, name string) error {
	if err := h.AddProject(path); err != nil {
		return err
	}
	if name != "" {
		if h.named == nil {
			h.named = map[string]string{}
		}
		h.named[path] = name
	}
	return nil
}

func (h *projectHost) RemoveProject(path string) error {
	h.removed = append(h.removed, path)
	return nil
}

func (h *projectHost) NewSession() error {
	h.newPlain++
	return nil
}

func (h *projectHost) NewSessionIn(cwd string) error {
	h.newIn = append(h.newIn, cwd)
	return nil
}

// picker answers with the folder the reader would have chosen.
type picker struct {
	path string
	err  error
}

func (p picker) PickFolder() (string, error) { return p.path, p.err }

func projectServer(t *testing.T, host Host, chooser Picker) *httptest.Server {
	t.Helper()
	server := New(Options{
		Turn:   func(context.Context, string, func(wire.AgentEvent) error) (Receipt, error) { return Receipt{}, nil },
		Host:   host,
		Picker: chooser,
	})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	response, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestProjectsRouteDrawsTheRail(t *testing.T) {
	host := &projectHost{groups: []ProjectGroup{
		{Path: "/work/atlas", Name: "atlas", Default: true, Sessions: []SessionInfo{{Path: "/s/1", ID: "1"}}},
		{Path: "/work/docs", Name: "docs", Sessions: []SessionInfo{}},
	}}
	server := projectServer(t, host, nil)

	response, err := http.Get(server.URL + "/api/projects")
	if err != nil {
		t.Fatalf("get projects: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var list ProjectList
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Groups) != 2 {
		t.Fatalf("groups = %v, want two", list.Groups)
	}
	if !list.Groups[0].Default || list.Groups[1].Default {
		t.Fatalf("default flag = %v/%v, want only the first", list.Groups[0].Default, list.Groups[1].Default)
	}
	if len(list.Groups[0].Sessions) != 1 {
		t.Fatalf("sessions = %v, want one row on the first group", list.Groups[0].Sessions)
	}
	// The rows arrive as an empty array rather than null: a frontend that has to
	// special-case null renders nothing on the first launch.
	if list.Groups[1].Sessions == nil {
		t.Fatal("an empty group's sessions decoded as null")
	}
}

// The primary button starts a conversation where the last one ran; a project's
// own button names its folder. Both go through one route, and getting the branch
// wrong would silently write into the wrong project.
func TestNewSessionRouteChoosesTheDirectory(t *testing.T) {
	host := &projectHost{}
	server := projectServer(t, host, nil)

	if response := postJSON(t, server.URL+"/api/sessions/new", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("empty body status = %d", response.StatusCode)
	}
	if host.newPlain != 1 || len(host.newIn) != 0 {
		t.Fatalf("plain = %d, in = %v, want the current directory", host.newPlain, host.newIn)
	}

	if response := postJSON(t, server.URL+"/api/sessions/new", `{"cwd":"/work/docs"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("cwd status = %d", response.StatusCode)
	}
	if len(host.newIn) != 1 || host.newIn[0] != "/work/docs" {
		t.Fatalf("in = %v, want the named folder", host.newIn)
	}
	if host.newPlain != 1 {
		t.Fatal("a named folder also started one in the current directory")
	}
}

func TestProjectAddAndRemovePassThePath(t *testing.T) {
	host := &projectHost{}
	server := projectServer(t, host, nil)

	if response := postJSON(t, server.URL+"/api/projects", `{"path":"/work/docs"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("add status = %d", response.StatusCode)
	}
	if len(host.added) != 1 || host.added[0] != "/work/docs" {
		t.Fatalf("added = %v", host.added)
	}
	if response := postJSON(t, server.URL+"/api/projects", `{"path":"/work/api","name":"Backend API"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("add named status = %d", response.StatusCode)
	}
	if host.named["/work/api"] != "Backend API" {
		t.Fatalf("named = %v, want Backend API for /work/api", host.named)
	}
	// A rejected folder is the reader's typo, not a server fault.
	host.addErr = errors.New("project: /nope does not exist")
	if response := postJSON(t, server.URL+"/api/projects", `{"path":"/nope"}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("rejected add status = %d, want 400", response.StatusCode)
	}
	// An empty or whitespace-only path never reaches the host.
	if response := postJSON(t, server.URL+"/api/projects", `{"path":""}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty path status = %d, want 400", response.StatusCode)
	}
	if response := postJSON(t, server.URL+"/api/projects", `{"path":"   ","name":"Blank"}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank path status = %d, want 400", response.StatusCode)
	}

	if response := postJSON(t, server.URL+"/api/projects/remove", `{"path":"/work/docs"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("remove status = %d", response.StatusCode)
	}
	if len(host.removed) != 1 || host.removed[0] != "/work/docs" {
		t.Fatalf("removed = %v", host.removed)
	}
}

// 501 is the signal, not a failure: it is how the page learns that this build
// has no chooser and it should ask for a path instead.
func TestProjectPickWithoutAChooserIsNotImplemented(t *testing.T) {
	server := projectServer(t, &projectHost{}, nil)
	if response := postJSON(t, server.URL+"/api/projects/pick", ""); response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", response.StatusCode)
	}
}

func TestProjectPickReturnsTheChosenFolder(t *testing.T) {
	server := projectServer(t, &projectHost{}, picker{path: "/work/docs"})
	response := postJSON(t, server.URL+"/api/projects/pick", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var answer struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer.Path != "/work/docs" {
		t.Fatalf("path = %q", answer.Path)
	}
}

type statusHost struct {
	Host
	archived  bool
	directory string
}

func (h *statusHost) SessionArchived() bool    { return h.archived }
func (h *statusHost) SessionDirectory() string { return h.directory }

func TestStateReportsLiveSessionStatusWithoutBreakingPartialHosts(t *testing.T) {
	// Partial Host stub without SessionStatus must still answer /api/state.
	partial := projectServer(t, &projectHost{}, nil)
	resp, err := http.Get(partial.URL + "/api/state")
	if err != nil {
		t.Fatalf("get partial state: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("partial /api/state = %d, want 200", resp.StatusCode)
	}

	host := &statusHost{archived: true, directory: "/work/docs"}
	server := projectServer(t, host, nil)
	response, err := http.Get(server.URL + "/api/state")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var state struct {
		Archived  bool   `json:"archived"`
		Directory string `json:"directory"`
	}
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !state.Archived || state.Directory != "/work/docs" {
		t.Fatalf("state = %+v, want archived=true and directory=/work/docs", state)
	}
}

type remoteProjectStub struct {
	*projectHost
	remoteAdded   [][3]string
	remoteRemoved [][2]string
	remoteNewIn   [][2]string
	browseCalls   []string
	discoverCalls []string
}

func (r *remoteProjectStub) AddRemoteProject(deviceID, path, name string) error {
	r.remoteAdded = append(r.remoteAdded, [3]string{deviceID, path, name})
	return nil
}

func (r *remoteProjectStub) RemoveRemoteProject(deviceID, path string) error {
	r.remoteRemoved = append(r.remoteRemoved, [2]string{deviceID, path})
	return nil
}

func (r *remoteProjectStub) NewRemoteSessionIn(deviceID, cwd string) error {
	r.remoteNewIn = append(r.remoteNewIn, [2]string{deviceID, cwd})
	return nil
}

func (r *remoteProjectStub) BrowseDirs(deviceID, path string, includeHidden bool) (DirBrowseResult, error) {
	r.browseCalls = append(r.browseCalls, deviceID+":"+path)
	return DirBrowseResult{
		DeviceID: deviceID,
		Path:     "/srv/app",
		Parent:   "/srv",
		Home:     "/home/ubuntu",
		Entries:  []BrowseDirEntry{{Name: "api", Path: "/srv/app/api"}},
	}, nil
}

func (r *remoteProjectStub) DiscoverProjects(deviceID string) ([]DiscoveredProject, error) {
	r.discoverCalls = append(r.discoverCalls, deviceID)
	return []DiscoveredProject{{DeviceID: deviceID, Path: "/srv/app", Name: "app", SessionCount: 2, Added: true}}, nil
}

func TestRemoteProjectRoutesAndDirectoryDiscovery(t *testing.T) {
	// Partial stub without browse/discover/remote must answer 501 on those routes.
	partial := projectServer(t, &projectHost{}, nil)
	if resp, err := http.Get(partial.URL + "/api/projects/browse"); err != nil {
		t.Fatalf("browse partial: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("partial browse status = %d, want 501", resp.StatusCode)
		}
	}
	if resp, err := http.Get(partial.URL + "/api/projects/discover"); err != nil {
		t.Fatalf("discover partial: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("partial discover status = %d, want 501", resp.StatusCode)
		}
	}
	if resp := postJSON(t, partial.URL+"/api/projects", `{"deviceId":"gpu-1","path":"/srv/app"}`); resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("partial remote add status = %d, want 501", resp.StatusCode)
	}

	host := &remoteProjectStub{projectHost: &projectHost{}}
	server := projectServer(t, host, nil)

	if resp := postJSON(t, server.URL+"/api/projects", `{"deviceId":"gpu-1","path":"/srv/app","name":"Remote App"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("remote add status = %d", resp.StatusCode)
	}
	if len(host.remoteAdded) != 1 || host.remoteAdded[0] != [3]string{"gpu-1", "/srv/app", "Remote App"} {
		t.Fatalf("remoteAdded = %v", host.remoteAdded)
	}
	if resp := postJSON(t, server.URL+"/api/sessions/new", `{"deviceId":"gpu-1","cwd":"/srv/app"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("remote new session status = %d", resp.StatusCode)
	}
	if len(host.remoteNewIn) != 1 || host.remoteNewIn[0] != [2]string{"gpu-1", "/srv/app"} {
		t.Fatalf("remoteNewIn = %v", host.remoteNewIn)
	}
	if resp := postJSON(t, server.URL+"/api/projects/remove", `{"deviceId":"gpu-1","path":"/srv/app"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("remote remove status = %d", resp.StatusCode)
	}
	if len(host.remoteRemoved) != 1 || host.remoteRemoved[0] != [2]string{"gpu-1", "/srv/app"} {
		t.Fatalf("remoteRemoved = %v", host.remoteRemoved)
	}

	browseResp, err := http.Get(server.URL + "/api/projects/browse?device=gpu-1&path=/srv/app")
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	defer browseResp.Body.Close()
	if browseResp.StatusCode != http.StatusOK {
		t.Fatalf("browse status = %d", browseResp.StatusCode)
	}
	var browse DirBrowseResult
	if err := json.NewDecoder(browseResp.Body).Decode(&browse); err != nil {
		t.Fatalf("decode browse: %v", err)
	}
	if browse.DeviceID != "gpu-1" || browse.Path != "/srv/app" || len(browse.Entries) != 1 {
		t.Fatalf("browse = %+v", browse)
	}

	discResp, err := http.Get(server.URL + "/api/projects/discover?device=gpu-1")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	defer discResp.Body.Close()
	if discResp.StatusCode != http.StatusOK {
		t.Fatalf("discover status = %d", discResp.StatusCode)
	}
	var disc DiscoverProjectsResult
	if err := json.NewDecoder(discResp.Body).Decode(&disc); err != nil {
		t.Fatalf("decode discover: %v", err)
	}
	if disc.DeviceID != "gpu-1" || len(disc.Projects) != 1 || !disc.Projects[0].Added {
		t.Fatalf("discover = %+v", disc)
	}
}
