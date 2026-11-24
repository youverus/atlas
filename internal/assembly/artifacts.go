package assembly

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pi-go/internal/artifact"
	"pi-go/internal/tool"
	artifacttool "pi-go/internal/tool/artifact"
)

// ArtifactSummary describes one versioned artifact in a session.
type ArtifactSummary struct {
	ID          string
	Kind        string
	Title       string
	Description string
	Language    string
	ContentType string
	Version     int
	Bytes       int
	SHA256      string
	CreatedAt   int64
	UpdatedAt   int64
}

// ArtifactVersion describes one immutable snapshot of an artifact.
type ArtifactVersion struct {
	Version     int
	Title       string
	Description string
	Kind        string
	Language    string
	ContentType string
	Bytes       int
	SHA256      string
	SourcePath  string
	CreatedAt   int64
	Action      string
	Summary     string
}

// ArtifactDetail includes version history and content for one artifact.
type ArtifactDetail struct {
	ArtifactSummary
	ActiveVersion int
	Content       string
	Versions      []ArtifactVersion
}

// ArtifactExportResult reports the normalized workspace path and byte size of
// an exported artifact.
type ArtifactExportResult struct {
	Path  string
	Bytes int
}

// ArtifactTools builds the built-in artifact toolset bound to repo (with
// fallback resolving the default session when ctx carries no SessionContext).
func ArtifactTools(cwd string, repo *artifact.Repo, fallback func() (string, string)) []tool.Tool {
	if repo == nil {
		return nil
	}
	return artifacttool.Tools(artifacttool.Options{
		Cwd: cwd,
		ResolveStore: func(ctx context.Context) (*artifact.Store, error) {
			if sess, ok := tool.SessionFromContext(ctx); ok {
				return repo.ForSession(sess.ID, sess.File), nil
			}
			if fallback != nil {
				id, file := fallback()
				return repo.ForSession(id, file), nil
			}
			return repo.ForSession("", ""), nil
		},
	})
}

func (k *Kernel) artifactStore(sessionID, sessionFile string) (*artifact.Store, error) {
	if k == nil || k.artifacts == nil {
		return nil, errors.New("assembly: artifact store is unavailable")
	}
	return k.artifacts.ForSession(sessionID, sessionFile), nil
}

// ListArtifacts lists all artifacts belonging to the specified session.
func (k *Kernel) ListArtifacts(sessionID, sessionFile string) ([]ArtifactSummary, error) {
	store, err := k.artifactStore(sessionID, sessionFile)
	if err != nil {
		return nil, err
	}
	items, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactSummary, len(items))
	for i, item := range items {
		out[i] = mapArtifactSummary(item)
	}
	return out, nil
}

// GetArtifact loads metadata, version history, and content for id in the
// specified session. version <= 0 returns the latest version.
func (k *Kernel) GetArtifact(sessionID, sessionFile, id string, version int) (ArtifactDetail, error) {
	store, err := k.artifactStore(sessionID, sessionFile)
	if err != nil {
		return ArtifactDetail{}, err
	}
	detail, err := store.Get(id, version)
	if err != nil {
		return ArtifactDetail{}, err
	}
	versions := make([]ArtifactVersion, len(detail.Versions))
	for i, v := range detail.Versions {
		versions[i] = ArtifactVersion{
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
	return ArtifactDetail{
		ArtifactSummary: mapArtifactSummary(detail.Summary),
		ActiveVersion:   detail.ActiveVersion,
		Content:         detail.Content,
		Versions:        versions,
	}, nil
}

// DeleteArtifact removes an artifact and all of its versions from the session.
func (k *Kernel) DeleteArtifact(sessionID, sessionFile, id string) error {
	store, err := k.artifactStore(sessionID, sessionFile)
	if err != nil {
		return err
	}
	return store.Delete(id)
}

// ExportArtifact writes an artifact version inside cwd at relPath (or a default
// filename under artifacts/ when relPath is empty).
func (k *Kernel) ExportArtifact(sessionID, sessionFile, cwd, id string, version int, relPath string) (ArtifactExportResult, error) {
	detail, err := k.GetArtifact(sessionID, sessionFile, id, version)
	if err != nil {
		return ArtifactExportResult{}, err
	}
	targetRel := strings.TrimSpace(relPath)
	if targetRel == "" {
		ext := artifact.DefaultExtension(detail.Kind, detail.Language)
		targetRel = filepath.ToSlash(filepath.Join("artifacts", detail.ID+"."+ext))
	}
	dest, normalizedRel, err := resolveConfinedExportPath(cwd, targetRel)
	if err != nil {
		return ArtifactExportResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return ArtifactExportResult{}, fmt.Errorf("artifact: create directory: %w", err)
	}
	// Check realpath of parent directory after MkdirAll to guard against symlink escapes.
	realRoot, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return ArtifactExportResult{}, fmt.Errorf("artifact: resolve workspace: %w", err)
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(dest))
	if err != nil {
		return ArtifactExportResult{}, fmt.Errorf("artifact: resolve export directory: %w", err)
	}
	parentRel, err := filepath.Rel(realRoot, realParent)
	if err != nil || parentRel == ".." || strings.HasPrefix(parentRel, ".."+string(filepath.Separator)) {
		return ArtifactExportResult{}, fmt.Errorf("artifact: export path %q escapes workspace", targetRel)
	}
	if info, statErr := os.Lstat(dest); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return ArtifactExportResult{}, fmt.Errorf("artifact: export target %q must not be a symlink", targetRel)
	}
	data := []byte(detail.Content)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return ArtifactExportResult{}, fmt.Errorf("artifact: write %s: %w", normalizedRel, err)
	}
	return ArtifactExportResult{
		Path:  normalizedRel,
		Bytes: len(data),
	}, nil
}

func resolveConfinedExportPath(cwd, relPath string) (string, string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", "", errors.New("artifact: workspace directory is unavailable")
	}
	if filepath.IsAbs(relPath) {
		return "", "", fmt.Errorf("artifact: export path %q must be relative to the workspace", relPath)
	}
	clean := filepath.Clean(filepath.FromSlash(relPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("artifact: export path %q escapes workspace", relPath)
	}
	absRoot, err := filepath.Abs(cwd)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(absRoot, clean), filepath.ToSlash(clean), nil
}

func mapArtifactSummary(item artifact.Summary) ArtifactSummary {
	return ArtifactSummary{
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
