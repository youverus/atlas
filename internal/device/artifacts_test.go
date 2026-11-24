package device

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pi-go/internal/httpapi"
)

type stubArtifactHost struct {
	httpapi.Host
	list    []httpapi.ArtifactSummary
	details map[string]map[int]httpapi.ArtifactDetail
	deleted []string
	exports []httpapi.ArtifactExportResult
}

func (s *stubArtifactHost) ListArtifacts() ([]httpapi.ArtifactSummary, error) {
	return append([]httpapi.ArtifactSummary(nil), s.list...), nil
}

func (s *stubArtifactHost) GetArtifact(id string, version int) (httpapi.ArtifactDetail, error) {
	byVer, ok := s.details[id]
	if !ok {
		return httpapi.ArtifactDetail{}, errors.New("artifact: not found")
	}
	if version <= 0 {
		for v := range byVer {
			if v > version {
				version = v
			}
		}
	}
	d, ok := byVer[version]
	if !ok {
		return httpapi.ArtifactDetail{}, errors.New("artifact: version not found")
	}
	return d, nil
}

func (s *stubArtifactHost) DeleteArtifact(id string) error {
	if _, ok := s.details[id]; !ok {
		return errors.New("artifact: not found")
	}
	delete(s.details, id)
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *stubArtifactHost) ExportArtifact(id string, version int, relPath string) (httpapi.ArtifactExportResult, error) {
	d, err := s.GetArtifact(id, version)
	if err != nil {
		return httpapi.ArtifactExportResult{}, err
	}
	res := httpapi.ArtifactExportResult{Path: relPath, Bytes: len(d.Content)}
	s.exports = append(s.exports, res)
	return res, nil
}

func TestCompositeHostRoutesArtifactsBetweenLocalAndActiveRemote(t *testing.T) {
	home := t.TempDir()
	mgr := NewManager(Options{
		DevicesPath:  filepath.Join(home, FileName),
		ProjectsPath: filepath.Join(home, "projects.json"),
		AtlasHome:    home,
	})
	defer mgr.Close()

	localStub := &stubArtifactHost{
		list: []httpapi.ArtifactSummary{{ID: "local-doc", Kind: "markdown", Title: "Local", Version: 1}},
		details: map[string]map[int]httpapi.ArtifactDetail{
			"local-doc": {
				1: {
					ArtifactSummary: httpapi.ArtifactSummary{ID: "local-doc", Kind: "markdown", Title: "Local", Version: 1},
					ActiveVersion:   1,
					Content:         "# local",
				},
			},
		},
	}

	remoteStub := &stubArtifactHost{
		list: []httpapi.ArtifactSummary{{ID: "remote chart", Kind: "html", Title: "Remote", Version: 2}},
		details: map[string]map[int]httpapi.ArtifactDetail{
			"remote chart": {
				1: {
					ArtifactSummary: httpapi.ArtifactSummary{ID: "remote chart", Kind: "html", Title: "Remote v1", Version: 2},
					ActiveVersion:   1,
					Content:         "<p>v1</p>",
				},
				2: {
					ArtifactSummary: httpapi.ArtifactSummary{ID: "remote chart", Kind: "html", Title: "Remote v2", Version: 2},
					ActiveVersion:   2,
					Content:         "<p>v2</p>",
				},
			},
		},
	}

	const token = "0123456789abcdef0123456789abcdef"
	remoteSrv := httpapi.New(httpapi.Options{
		AuthToken: token,
		Artifacts: remoteStub,
	})
	ts := httptest.NewServer(remoteSrv.Handler())
	defer ts.Close()

	composite := mgr.WrapHost(localStub)

	// 1. With no active remote target, CompositeHost delegates to local.
	localList, err := composite.ListArtifacts()
	if err != nil || len(localList) != 1 || localList[0].ID != "local-doc" {
		t.Fatalf("local ListArtifacts = (%+v, %v)", localList, err)
	}
	localDetail, err := composite.GetArtifact("local-doc", 1)
	if err != nil || localDetail.Content != "# local" {
		t.Fatalf("local GetArtifact = (%+v, %v)", localDetail, err)
	}

	// 2. Activate remote target and verify CRUD + export route over HTTP to remote.
	devID := "gpu-box"
	mgr.mu.Lock()
	mgr.conns[devID] = &connState{
		status: "online",
		client: newRemoteClient(devID, "GPU Box", ts.URL, token),
	}
	mgr.active = &activeRemoteTarget{
		deviceID:   devID,
		sessionKey: WrapRemoteKey(devID, "/remote/s1.jsonl"),
		remoteDir:  "/remote/work",
	}
	mgr.mu.Unlock()

	remoteList, err := composite.ListArtifacts()
	if err != nil || len(remoteList) != 1 || remoteList[0].ID != "remote chart" {
		t.Fatalf("remote ListArtifacts = (%+v, %v)", remoteList, err)
	}
	remoteV1, err := composite.GetArtifact("remote chart", 1)
	if err != nil || remoteV1.Content != "<p>v1</p>" || remoteV1.ActiveVersion != 1 {
		t.Fatalf("remote GetArtifact v1 = (%+v, %v)", remoteV1, err)
	}
	expRes, err := composite.ExportArtifact("remote chart", 2, "out/remote.html")
	if err != nil || expRes.Path != "out/remote.html" || !strings.Contains(expRes.Path, "remote.html") {
		t.Fatalf("remote ExportArtifact = (%+v, %v)", expRes, err)
	}
	if len(remoteStub.exports) != 1 || remoteStub.exports[0].Bytes != len("<p>v2</p>") {
		t.Fatalf("remote exports recorded = %+v", remoteStub.exports)
	}
	if err := composite.DeleteArtifact("remote chart"); err != nil {
		t.Fatalf("remote DeleteArtifact: %v", err)
	}
	if len(remoteStub.deleted) != 1 || remoteStub.deleted[0] != "remote chart" {
		t.Fatalf("remote deleted = %v", remoteStub.deleted)
	}
	// Ensure local artifact remained untouched.
	localAfter, err := localStub.ListArtifacts()
	if err != nil || len(localAfter) != 1 {
		t.Fatalf("local artifacts mutated unexpectedly: %+v", localAfter)
	}
}
