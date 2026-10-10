package knowledge

// CollectionScope describes a library collection at Run admission. It is data,
// not an authorization; only a dedicated Host KBASE session may grant editing.
type CollectionScope struct {
	Name        string `json:"name"`
	SourcePath  string `json:"sourcePath"`
	Description string `json:"description"`
	Editable    bool   `json:"editable"`
}
