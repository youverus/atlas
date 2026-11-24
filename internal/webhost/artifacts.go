package webhost

import (
	"errors"

	"pi-go/internal/assembly"
	"pi-go/internal/httpapi"
)

func (h *Host) activeArtifactSession() (string, string, bool) {
	slot := h.activeSlot()
	if slot == nil || slot.live == nil || slot.live.App == nil {
		return "", "", false
	}
	sess := slot.live.App.Session()
	if sess == nil {
		return "", "", false
	}
	return sess.ID(), sess.File(), true
}

// ListArtifacts returns metadata for all artifacts in the active session.
func (h *Host) ListArtifacts() ([]httpapi.ArtifactSummary, error) {
	id, file, ok := h.activeArtifactSession()
	if !ok {
		return []httpapi.ArtifactSummary{}, nil
	}
	items, err := h.kernel.ListArtifacts(id, file)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.ArtifactSummary, len(items))
	for i, item := range items {
		out[i] = mapHTTPArtifactSummary(item)
	}
	return out, nil
}

// GetArtifact loads a single artifact (or a specific historical version) from
// the active session.
func (h *Host) GetArtifact(artifactID string, version int) (httpapi.ArtifactDetail, error) {
	sessID, file, ok := h.activeArtifactSession()
	if !ok {
		return httpapi.ArtifactDetail{}, errors.New("webhost: no active session")
	}
	detail, err := h.kernel.GetArtifact(sessID, file, artifactID, version)
	if err != nil {
		return httpapi.ArtifactDetail{}, err
	}
	versions := make([]httpapi.ArtifactVersion, len(detail.Versions))
	for i, v := range detail.Versions {
		versions[i] = httpapi.ArtifactVersion{
			Version:     v.Version,
			Title:       v.Title,
			Description: v.Description,
			Kind:        v.Kind,
			Language:    v.Language,
			ContentType: v.ContentType,
			Bytes:       v.Bytes,
			SHA256:      v.SHA256,
			SourcePath:  v.SourcePath,
			CreatedAt:   v.CreatedAt,
			Action:      v.Action,
			Summary:     v.Summary,
		}
	}
	return httpapi.ArtifactDetail{
		ArtifactSummary: mapHTTPArtifactSummary(detail.ArtifactSummary),
		ActiveVersion:   detail.ActiveVersion,
		Content:         detail.Content,
		Versions:        versions,
	}, nil
}

// DeleteArtifact deletes an artifact and all of its versions from the active
// session.
func (h *Host) DeleteArtifact(artifactID string) error {
	sessID, file, ok := h.activeArtifactSession()
	if !ok {
		return errors.New("webhost: no active session")
	}
	return h.kernel.DeleteArtifact(sessID, file, artifactID)
}

// ExportArtifact writes a version of an artifact into the active session's
// workspace directory.
func (h *Host) ExportArtifact(artifactID string, version int, relPath string) (httpapi.ArtifactExportResult, error) {
	sessID, file, ok := h.activeArtifactSession()
	if !ok {
		return httpapi.ArtifactExportResult{}, errors.New("webhost: no active session")
	}
	res, err := h.kernel.ExportArtifact(sessID, file, h.root(), artifactID, version, relPath)
	if err != nil {
		return httpapi.ArtifactExportResult{}, err
	}
	return httpapi.ArtifactExportResult{
		Path:  res.Path,
		Bytes: res.Bytes,
	}, nil
}

func mapHTTPArtifactSummary(item assembly.ArtifactSummary) httpapi.ArtifactSummary {
	return httpapi.ArtifactSummary{
		ID:          item.ID,
		Kind:        item.Kind,
		Title:       item.Title,
		Description: item.Description,
		Language:    item.Language,
		ContentType: item.ContentType,
		Version:     item.Version,
		Bytes:       item.Bytes,
		SHA256:      item.SHA256,
		CreatedAt:   item.CreatedAt,
		UpdatedAt:   item.UpdatedAt,
	}
}
