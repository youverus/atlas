package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotFound reports a missing artifact ID or version.
	ErrNotFound = errors.New("artifact: not found")
)

const metaFileName = "meta.json"

type diskMeta struct {
	Summary
	Versions []VersionInfo `json:"versions"`
}

type memEntry struct {
	meta     diskMeta
	contents map[int]string
}

var (
	dirLocksMu sync.Mutex
	dirLocks   = map[string]*sync.Mutex{}
)

func lockForDir(dir string) *sync.Mutex {
	clean := filepath.Clean(dir)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	dirLocksMu.Lock()
	defer dirLocksMu.Unlock()
	mu, ok := dirLocks[clean]
	if !ok {
		mu = &sync.Mutex{}
		dirLocks[clean] = mu
	}
	return mu
}

// Store persists versioned artifacts for a single conversation either on disk
// or in memory.
type Store struct {
	dir string

	mu  *sync.Mutex
	mem map[string]*memEntry
}

// OpenDir returns a disk-backed Store rooted at dir. The directory is created
// lazily on the first Publish call.
func OpenDir(dir string) *Store {
	clean := filepath.Clean(dir)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	return &Store{
		dir: clean,
		mu:  lockForDir(clean),
	}
}

// NewMemory returns an in-memory Store for ephemeral or non-persisted sessions.
func NewMemory() *Store {
	return &Store{
		mu:  &sync.Mutex{},
		mem: map[string]*memEntry{},
	}
}

// Dir reports the backing directory, or "" for an in-memory store.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// List returns summaries of all artifacts in the store, newest updated first.
func (s *Store) List() ([]Summary, error) {
	if s == nil {
		return []Summary{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []Summary
	if s.dir == "" {
		out = make([]Summary, 0, len(s.mem))
		for _, entry := range s.mem {
			out = append(out, entry.meta.Summary)
		}
	} else {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			if os.IsNotExist(err) {
				return []Summary{}, nil
			}
			return nil, fmt.Errorf("artifact: list %s: %w", s.dir, err)
		}
		out = make([]Summary, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() || ValidateID(entry.Name()) != nil {
				continue
			}
			meta, err := s.readMetaLocked(entry.Name())
			if err != nil {
				continue
			}
			out = append(out, meta.Summary)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get returns the metadata, version history, and content for id. A version <= 0
// returns the latest version.
func (s *Store) Get(id string, version int) (Detail, error) {
	if err := ValidateID(id); err != nil {
		return Detail{}, err
	}
	if s == nil {
		return Detail{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir == "" {
		entry, ok := s.mem[id]
		if !ok {
			return Detail{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		target := version
		if target <= 0 {
			target = entry.meta.Version
		}
		content, ok := entry.contents[target]
		if !ok {
			return Detail{}, fmt.Errorf("%w: %s v%d", ErrNotFound, id, target)
		}
		return Detail{
			Summary:       entry.meta.Summary,
			ActiveVersion: target,
			Content:       content,
			Versions:      append([]VersionInfo(nil), entry.meta.Versions...),
		}, nil
	}

	meta, err := s.readMetaLocked(id)
	if err != nil {
		return Detail{}, err
	}
	target := version
	if target <= 0 {
		target = meta.Version
	}
	if !hasVersion(meta.Versions, target) {
		return Detail{}, fmt.Errorf("%w: %s v%d", ErrNotFound, id, target)
	}
	raw, err := os.ReadFile(s.versionPath(id, target))
	if err != nil {
		if os.IsNotExist(err) {
			return Detail{}, fmt.Errorf("%w: %s v%d", ErrNotFound, id, target)
		}
		return Detail{}, fmt.Errorf("artifact: read %s v%d: %w", id, target, err)
	}
	return Detail{
		Summary:       meta.Summary,
		ActiveVersion: target,
		Content:       string(raw),
		Versions:      append([]VersionInfo(nil), meta.Versions...),
	}, nil
}

// Publish creates a new artifact or records a new immutable version of an
// existing artifact.
func (s *Store) Publish(req PublishRequest) (Detail, error) {
	if s == nil {
		return Detail{}, errors.New("artifact: store is not initialized")
	}
	if err := ValidateID(req.ID); err != nil {
		return Detail{}, err
	}
	if !ValidKind(req.Kind) {
		return Detail{}, fmt.Errorf("artifact: unsupported kind %q (want html, svg, markdown, code, or csv)", req.Kind)
	}
	if err := validateContent(req.Content); err != nil {
		return Detail{}, err
	}
	title, err := normalizeTextField(req.Title, MaxTitleRunes, "title")
	if err != nil {
		return Detail{}, err
	}
	description, err := normalizeTextField(req.Description, MaxDescriptionRunes, "description")
	if err != nil {
		return Detail{}, err
	}
	summary, err := normalizeTextField(req.Summary, MaxSummaryRunes, "summary")
	if err != nil {
		return Detail{}, err
	}
	language := strings.ToLower(strings.TrimSpace(req.Language))
	if len(language) > MaxLanguageLength {
		return Detail{}, fmt.Errorf("artifact: language %q exceeds %d characters", language, MaxLanguageLength)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var existing *diskMeta
	if s.dir == "" {
		if entry, ok := s.mem[req.ID]; ok {
			copyMeta := entry.meta
			existing = &copyMeta
		} else if len(s.mem) >= MaxArtifacts {
			return Detail{}, fmt.Errorf("artifact: session limit of %d artifacts reached", MaxArtifacts)
		}
	} else {
		meta, readErr := s.readMetaLocked(req.ID)
		if readErr == nil {
			existing = &meta
		} else if !errors.Is(readErr, ErrNotFound) {
			return Detail{}, readErr
		} else if count, countErr := s.countArtifactsLocked(); countErr == nil && count >= MaxArtifacts {
			return Detail{}, fmt.Errorf("artifact: session limit of %d artifacts reached", MaxArtifacts)
		}
	}

	now := time.Now().UnixMilli()
	version := 1
	createdAt := now
	if existing != nil {
		if existing.Version >= MaxVersions {
			return Detail{}, fmt.Errorf("artifact: %q reached maximum of %d versions", req.ID, MaxVersions)
		}
		version = existing.Version + 1
		createdAt = existing.CreatedAt
		if title == "" {
			title = existing.Title
		}
		if description == "" {
			description = existing.Description
		}
		if language == "" && req.Kind == existing.Kind {
			language = existing.Language
		}
	}
	if title == "" {
		title = req.ID
	}

	contentType := ContentTypeFor(req.Kind, language)
	sum := sha256.Sum256([]byte(req.Content))
	hash := hex.EncodeToString(sum[:])

	verInfo := VersionInfo{
		Version:     version,
		Title:       title,
		Description: description,
		Kind:        req.Kind,
		Language:    language,
		ContentType: contentType,
		Bytes:       len(req.Content),
		SHA256:      hash,
		SourcePath:  strings.TrimSpace(req.SourcePath),
		ToolCallID:  strings.TrimSpace(req.ToolCallID),
		CreatedAt:   now,
		Action:      "publish",
		Summary:     summary,
	}

	var versions []VersionInfo
	if existing != nil {
		versions = append(versions, existing.Versions...)
	}
	versions = append(versions, verInfo)

	meta := diskMeta{
		Summary: Summary{
			ID:          req.ID,
			Kind:        req.Kind,
			Title:       title,
			Description: description,
			Language:    language,
			ContentType: contentType,
			Version:     version,
			Bytes:       len(req.Content),
			SHA256:      hash,
			CreatedAt:   createdAt,
			UpdatedAt:   now,
		},
		Versions: versions,
	}

	if err := s.saveLocked(meta, version, req.Content); err != nil {
		return Detail{}, err
	}
	return Detail{
		Summary:       meta.Summary,
		ActiveVersion: version,
		Content:       req.Content,
		Versions:      append([]VersionInfo(nil), meta.Versions...),
	}, nil
}

// Patch applies sequential exact string edits to the latest version of id and
// persists a new immutable version.
func (s *Store) Patch(req PatchRequest) (Detail, error) {
	if s == nil {
		return Detail{}, errors.New("artifact: store is not initialized")
	}
	if err := ValidateID(req.ID); err != nil {
		return Detail{}, err
	}
	if len(req.Edits) == 0 {
		return Detail{}, errors.New("artifact: at least one edit is required")
	}
	if len(req.Edits) > MaxEdits {
		return Detail{}, fmt.Errorf("artifact: at most %d edits are allowed per patch", MaxEdits)
	}
	title, err := normalizeTextField(req.Title, MaxTitleRunes, "title")
	if err != nil {
		return Detail{}, err
	}
	description, err := normalizeTextField(req.Description, MaxDescriptionRunes, "description")
	if err != nil {
		return Detail{}, err
	}
	summary, err := normalizeTextField(req.Summary, MaxSummaryRunes, "summary")
	if err != nil {
		return Detail{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var meta diskMeta
	var currentContent string
	if s.dir == "" {
		entry, ok := s.mem[req.ID]
		if !ok {
			return Detail{}, fmt.Errorf("%w: %s", ErrNotFound, req.ID)
		}
		meta = entry.meta
		currentContent = entry.contents[meta.Version]
	} else {
		var readErr error
		meta, readErr = s.readMetaLocked(req.ID)
		if readErr != nil {
			return Detail{}, readErr
		}
		raw, readErr := os.ReadFile(s.versionPath(req.ID, meta.Version))
		if readErr != nil {
			return Detail{}, fmt.Errorf("artifact: read %s v%d: %w", req.ID, meta.Version, readErr)
		}
		currentContent = string(raw)
	}

	if meta.Version >= MaxVersions {
		return Detail{}, fmt.Errorf("artifact: %q reached maximum of %d versions", req.ID, MaxVersions)
	}

	updated := currentContent
	for idx, edit := range req.Edits {
		if edit.OldText == "" {
			return Detail{}, fmt.Errorf("artifact: edit[%d].old_text must not be empty", idx)
		}
		count := strings.Count(updated, edit.OldText)
		if count == 0 {
			return Detail{}, fmt.Errorf("artifact: edit[%d].old_text not found in %q v%d", idx, req.ID, meta.Version)
		}
		if count > 1 && !edit.ReplaceAll {
			return Detail{}, fmt.Errorf("artifact: edit[%d].old_text matched %d times in %q v%d; provide more context or set replace_all=true", idx, count, req.ID, meta.Version)
		}
		if edit.ReplaceAll {
			updated = strings.ReplaceAll(updated, edit.OldText, edit.NewText)
		} else {
			updated = strings.Replace(updated, edit.OldText, edit.NewText, 1)
		}
	}
	if err := validateContent(updated); err != nil {
		return Detail{}, err
	}

	now := time.Now().UnixMilli()
	nextVersion := meta.Version + 1
	if title == "" {
		title = meta.Title
	}
	if description == "" {
		description = meta.Description
	}
	sum := sha256.Sum256([]byte(updated))
	hash := hex.EncodeToString(sum[:])

	verInfo := VersionInfo{
		Version:     nextVersion,
		Title:       title,
		Description: description,
		Kind:        meta.Kind,
		Language:    meta.Language,
		ContentType: meta.ContentType,
		Bytes:       len(updated),
		SHA256:      hash,
		ToolCallID:  strings.TrimSpace(req.ToolCallID),
		CreatedAt:   now,
		Action:      "patch",
		Summary:     summary,
	}
	meta.Title = title
	meta.Description = description
	meta.Version = nextVersion
	meta.Bytes = len(updated)
	meta.SHA256 = hash
	meta.UpdatedAt = now
	meta.Versions = append(append([]VersionInfo(nil), meta.Versions...), verInfo)

	if err := s.saveLocked(meta, nextVersion, updated); err != nil {
		return Detail{}, err
	}
	return Detail{
		Summary:       meta.Summary,
		ActiveVersion: nextVersion,
		Content:       updated,
		Versions:      append([]VersionInfo(nil), meta.Versions...),
	}, nil
}

// Delete removes an artifact and all of its stored versions.
func (s *Store) Delete(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir == "" {
		if _, ok := s.mem[id]; !ok {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		delete(s.mem, id)
		return nil
	}

	artDir := s.artifactDir(id)
	if _, err := os.Stat(artDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return fmt.Errorf("artifact: delete %s: %w", id, err)
	}
	if err := os.RemoveAll(artDir); err != nil {
		return fmt.Errorf("artifact: delete %s: %w", id, err)
	}
	return nil
}

func (s *Store) artifactDir(id string) string {
	return filepath.Join(s.dir, id)
}

func (s *Store) metaPath(id string) string {
	return filepath.Join(s.dir, id, metaFileName)
}

func (s *Store) versionPath(id string, version int) string {
	return filepath.Join(s.dir, id, "v"+strconv.Itoa(version)+".dat")
}

func (s *Store) countArtifactsLocked() (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && ValidateID(entry.Name()) == nil {
			count++
		}
	}
	return count, nil
}

func (s *Store) readMetaLocked(id string) (diskMeta, error) {
	raw, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return diskMeta{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return diskMeta{}, fmt.Errorf("artifact: read metadata for %s: %w", id, err)
	}
	var meta diskMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return diskMeta{}, fmt.Errorf("artifact: parse metadata for %s: %w", id, err)
	}
	return meta, nil
}

func (s *Store) saveLocked(meta diskMeta, version int, content string) error {
	if s.dir == "" {
		entry := s.mem[meta.ID]
		if entry == nil {
			entry = &memEntry{contents: map[int]string{}}
			s.mem[meta.ID] = entry
		}
		entry.meta = meta
		entry.contents[version] = content
		return nil
	}

	artDir := s.artifactDir(meta.ID)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		return fmt.Errorf("artifact: mkdir %s: %w", artDir, err)
	}
	if err := writeFileAtomic(s.versionPath(meta.ID, version), []byte(content)); err != nil {
		return fmt.Errorf("artifact: write %s v%d: %w", meta.ID, version, err)
	}
	encoded, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("artifact: encode metadata for %s: %w", meta.ID, err)
	}
	encoded = append(encoded, '\n')
	if err := writeFileAtomic(s.metaPath(meta.ID), encoded); err != nil {
		return fmt.Errorf("artifact: write metadata for %s: %w", meta.ID, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func hasVersion(versions []VersionInfo, target int) bool {
	for _, v := range versions {
		if v.Version == target {
			return true
		}
	}
	return false
}

// Repo resolves the Store owning a given session, using disk storage beside
// persisted sessions and process-local memory storage for ephemeral sessions.
type Repo struct {
	mu  sync.Mutex
	mem map[string]*Store
}

// NewRepo constructs a session-aware artifact Store resolver.
func NewRepo() *Repo {
	return &Repo{mem: map[string]*Store{}}
}

// ForSession returns the Store for sessionFile when non-empty, or an in-memory
// Store keyed by sessionID.
func (r *Repo) ForSession(sessionID, sessionFile string) *Store {
	if strings.TrimSpace(sessionFile) != "" {
		return OpenDir(sessionFile + ".artifacts")
	}
	key := strings.TrimSpace(sessionID)
	if key == "" {
		key = "__default__"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if store, ok := r.mem[key]; ok {
		return store
	}
	store := NewMemory()
	r.mem[key] = store
	return store
}
