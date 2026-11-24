package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubArtifactController struct {
	list    []ArtifactSummary
	details map[string]map[int]ArtifactDetail
	deleted []string
	exports []ArtifactExportResult
	err     error
}

func (s *stubArtifactController) ListArtifacts() ([]ArtifactSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.list, nil
}

func (s *stubArtifactController) GetArtifact(id string, version int) (ArtifactDetail, error) {
	if s.err != nil {
		return ArtifactDetail{}, s.err
	}
	byVer, ok := s.details[id]
	if !ok {
		return ArtifactDetail{}, errors.New("artifact: not found")
	}
	if version <= 0 {
		maxV := 0
		for v := range byVer {
			if v > maxV {
				maxV = v
			}
		}
		version = maxV
	}
	d, ok := byVer[version]
	if !ok {
		return ArtifactDetail{}, errors.New("artifact: version not found")
	}
	return d, nil
}

func (s *stubArtifactController) DeleteArtifact(id string) error {
	if s.err != nil {
		return s.err
	}
	if _, ok := s.details[id]; !ok {
		return errors.New("artifact: not found")
	}
	delete(s.details, id)
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *stubArtifactController) ExportArtifact(id string, version int, path string) (ArtifactExportResult, error) {
	if s.err != nil {
		return ArtifactExportResult{}, s.err
	}
	d, err := s.GetArtifact(id, version)
	if err != nil {
		return ArtifactExportResult{}, err
	}
	outPath := path
	if strings.TrimSpace(outPath) == "" {
		outPath = "artifacts/" + d.ID + ".html"
	}
	res := ArtifactExportResult{Path: outPath, Bytes: len(d.Content)}
	s.exports = append(s.exports, res)
	return res, nil
}

func TestArtifactRoutesReturn501WhenDisabled(t *testing.T) {
	srv := New(Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/artifacts")
	if err != nil {
		t.Fatalf("GET /api/artifacts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
}

func TestArtifactRoutesCRUDAndExport(t *testing.T) {
	ctrl := &stubArtifactController{
		list: []ArtifactSummary{
			{ID: "chart", Kind: "html", Title: "Chart v2", Version: 2, ContentType: "text/html", Bytes: 12},
		},
		details: map[string]map[int]ArtifactDetail{
			"chart": {
				1: {
					ArtifactSummary: ArtifactSummary{ID: "chart", Kind: "html", Title: "Chart v1", Version: 2},
					ActiveVersion:   1,
					Content:         "<h1>v1</h1>",
					Versions:        nil,
				},
				2: {
					ArtifactSummary: ArtifactSummary{ID: "chart", Kind: "html", Title: "Chart v2", Version: 2},
					ActiveVersion:   2,
					Content:         "<h1>v2</h1>",
					Versions: []ArtifactVersion{
						{Version: 1, Kind: "html", Title: "Chart v1"},
						{Version: 2, Kind: "html", Title: "Chart v2"},
					},
				},
			},
		},
	}

	srv := New(Options{Artifacts: ctrl})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// List
	listResp, err := http.Get(ts.URL + "/api/artifacts")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed []ArtifactSummary
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	listResp.Body.Close()
	if len(listed) != 1 || listed[0].ID != "chart" || listed[0].Version != 2 {
		t.Fatalf("listed = %+v", listed)
	}

	// Get latest
	getResp, err := http.Get(ts.URL + "/api/artifacts/chart")
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	var detail ArtifactDetail
	if err := json.NewDecoder(getResp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	getResp.Body.Close()
	if detail.ActiveVersion != 2 || detail.Content != "<h1>v2</h1>" || len(detail.Versions) != 2 {
		t.Fatalf("detail latest = %+v", detail)
	}

	// Get v=1 (normalizes nil Versions slice to [])
	v1Resp, err := http.Get(ts.URL + "/api/artifacts/chart?v=1")
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	var detailV1 ArtifactDetail
	if err := json.NewDecoder(v1Resp.Body).Decode(&detailV1); err != nil {
		t.Fatalf("decode v1: %v", err)
	}
	v1Resp.Body.Close()
	if detailV1.ActiveVersion != 1 || detailV1.Content != "<h1>v1</h1>" || detailV1.Versions == nil {
		t.Fatalf("detail v1 = %+v", detailV1)
	}

	// Invalid ?v=abc -> 400
	badVResp, err := http.Get(ts.URL + "/api/artifacts/chart?v=abc")
	if err != nil {
		t.Fatalf("get invalid v: %v", err)
	}
	badVResp.Body.Close()
	if badVResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid v status = %d, want 400", badVResp.StatusCode)
	}

	// Missing artifact -> 404
	missResp, err := http.Get(ts.URL + "/api/artifacts/missing")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	missResp.Body.Close()
	if missResp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", missResp.StatusCode)
	}

	// Export
	expResp, err := http.Post(ts.URL+"/api/artifacts/chart/export", "application/json", bytes.NewBufferString(`{"version":1,"path":"out/chart.html"}`))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var expResult ArtifactExportResult
	if err := json.NewDecoder(expResp.Body).Decode(&expResult); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	expResp.Body.Close()
	if expResp.StatusCode != http.StatusOK || expResult.Path != "out/chart.html" || expResult.Bytes != len("<h1>v1</h1>") {
		t.Fatalf("export = (%d, %+v)", expResp.StatusCode, expResult)
	}

	// Delete
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/artifacts/chart", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK || len(ctrl.deleted) != 1 || ctrl.deleted[0] != "chart" {
		t.Fatalf("delete status=%d deleted=%v", delResp.StatusCode, ctrl.deleted)
	}
}
