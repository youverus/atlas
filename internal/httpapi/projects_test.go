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
		Turn:   func(context.Context, string, func(wire.AgentEvent) error) error { return nil },
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
	// A rejected folder is the reader's typo, not a server fault.
	host.addErr = errors.New("project: /nope does not exist")
	if response := postJSON(t, server.URL+"/api/projects", `{"path":"/nope"}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("rejected add status = %d, want 400", response.StatusCode)
	}
	// An empty path never reaches the host.
	if response := postJSON(t, server.URL+"/api/projects", `{"path":""}`); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty path status = %d, want 400", response.StatusCode)
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
