package wire

const (
	// ArtifactPublishToolName creates or publishes a new version of an artifact.
	ArtifactPublishToolName = "artifact_publish"
	// ArtifactPatchToolName applies incremental edits to produce a new artifact version.
	ArtifactPatchToolName = "artifact_patch"
	// ArtifactReadToolName reads an artifact's metadata and content.
	ArtifactReadToolName = "artifact_read"
	// ArtifactListToolName lists artifacts in the active conversation.
	ArtifactListToolName = "artifact_list"
)

// ArtifactRef is the compact receipt carried in tool results and transcript
// blocks for artifact-producing tool calls.
type ArtifactRef struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Language    string `json:"language,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Version     int    `json:"version"`
	Bytes       int    `json:"bytes,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Action      string `json:"action,omitempty"`
	Summary     string `json:"summary,omitempty"`
	SourcePath  string `json:"sourcePath,omitempty"`
}

// IsArtifactProducer reports whether toolName produces or updates a user-facing
// artifact version whose details carry an ArtifactRef.
func IsArtifactProducer(toolName string) bool {
	return toolName == ArtifactPublishToolName || toolName == ArtifactPatchToolName
}
