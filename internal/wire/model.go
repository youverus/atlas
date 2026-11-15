package wire

// Model is the subset of pi's model catalog entry the kernel needs. Catalog
// enrichment (cost tables, thinking maps, compat flags) is layered on later by
// internal/catalog.
type Model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	API           string `json:"api"`
	Provider      string `json:"provider"`
	BaseURL       string `json:"baseUrl,omitempty"`
	Reasoning     bool   `json:"reasoning,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	MaxTokens     int    `json:"maxTokens,omitempty"`

	// Input lists the modalities the model accepts, for example ["text"] or
	// ["text","image"]. An empty list means "not declared": adapters that must
	// decide whether an image can travel treat it as unknown rather than as
	// text-only, because a missing declaration should not silently drop data.
	Input []string `json:"input,omitempty"`
}
