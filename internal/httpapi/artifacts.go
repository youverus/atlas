package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ArtifactSummary describes one versioned artifact in the active session.
type ArtifactSummary struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Language    string `json:"language,omitempty"`
	ContentType string `json:"contentType"`
	Version     int    `json:"version"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// ArtifactVersion describes one immutable snapshot of an artifact.
type ArtifactVersion struct {
	Version     int    `json:"version"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"`
	Language    string `json:"language,omitempty"`
	ContentType string `json:"contentType"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	SourcePath  string `json:"sourcePath,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	Action      string `json:"action,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// ArtifactDetail carries an artifact's metadata, version history, and content.
type ArtifactDetail struct {
	ArtifactSummary
	ActiveVersion int               `json:"activeVersion"`
	Content       string            `json:"content"`
	Versions      []ArtifactVersion `json:"versions"`
}

// ArtifactExportResult reports where an artifact version was written inside
// the active session's workspace.
type ArtifactExportResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// ArtifactController manages session-scoped artifacts. Builds without one
// answer 501.
type ArtifactController interface {
	ListArtifacts() ([]ArtifactSummary, error)
	GetArtifact(id string, version int) (ArtifactDetail, error)
	DeleteArtifact(id string) error
	ExportArtifact(id string, version int, path string) (ArtifactExportResult, error)
}

func (s *Server) mountArtifacts() {
	s.mux.HandleFunc("GET /api/artifacts", s.serveArtifactList)
	s.mux.HandleFunc("GET /api/artifacts/{id}", s.serveArtifactGet)
	s.mux.HandleFunc("DELETE /api/artifacts/{id}", s.serveArtifactDelete)
	s.mux.HandleFunc("POST /api/artifacts/{id}/export", s.serveArtifactExport)
}

func (s *Server) needArtifacts(w http.ResponseWriter) bool {
	if s.artifacts != nil {
		return true
	}
	http.Error(w, "httpapi: artifacts are unavailable", http.StatusNotImplemented)
	return false
}

func normalizeArtifactDetail(detail ArtifactDetail) ArtifactDetail {
	if detail.Versions == nil {
		detail.Versions = []ArtifactVersion{}
	}
	return detail
}

func (s *Server) serveArtifactList(w http.ResponseWriter, r *http.Request) {
	if !s.needArtifacts(w) {
		return
	}
	list, err := s.artifacts.ListArtifacts()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []ArtifactSummary{}
	}
	writeJSON(w, list)
}

func (s *Server) serveArtifactGet(w http.ResponseWriter, r *http.Request) {
	if !s.needArtifacts(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	version := 0
	if rawV := strings.TrimSpace(r.URL.Query().Get("v")); rawV != "" {
		parsed, err := strconv.Atoi(rawV)
		if err != nil || parsed <= 0 {
			http.Error(w, "httpapi: invalid artifact version", http.StatusBadRequest)
			return
		}
		version = parsed
	}
	detail, err := s.artifacts.GetArtifact(id, version)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	writeJSON(w, normalizeArtifactDetail(detail))
}

func (s *Server) serveArtifactDelete(w http.ResponseWriter, r *http.Request) {
	if !s.needArtifacts(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.artifacts.DeleteArtifact(id); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) serveArtifactExport(w http.ResponseWriter, r *http.Request) {
	if !s.needArtifacts(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var request struct {
		Version int    `json:"version"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&request); err != nil {
		http.Error(w, "httpapi: bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.artifacts.ExportArtifact(id, request.Version, request.Path)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	writeJSON(w, result)
}
