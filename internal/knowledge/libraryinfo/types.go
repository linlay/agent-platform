package libraryinfo

// LibraryOption is the public retrieval directory; it exposes no source paths.
type LibraryOption struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Description       string              `json:"description,omitempty"`
	Collections       []CollectionSummary `json:"collections"`
	Readable          bool                `json:"readable"`
	Indexing          bool                `json:"indexing"`
	Degraded          bool                `json:"degraded"`
	UnavailableReason string              `json:"unavailableReason,omitempty"`
}

type CollectionSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type Capability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type Directory struct {
	AgentKey       string          `json:"agentKey"`
	Capability     Capability      `json:"capability"`
	BoundLibraryID string          `json:"boundLibraryId,omitempty"`
	Libraries      []LibraryOption `json:"libraries"`
}
