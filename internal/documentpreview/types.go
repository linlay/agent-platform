package documentpreview

type Capabilities struct {
	Enabled             bool     `json:"enabled"`
	SupportedExtensions []string `json:"supportedExtensions"`
	MaxFileBytes        int64    `json:"maxFileBytes"`
	OpenMode            string   `json:"openMode"`
}

type Source struct {
	Kind         string `json:"kind"`
	AgentKey     string `json:"agentKey,omitempty"`
	Path         string `json:"path,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	RelativePath string `json:"relativePath,omitempty"`
}

type Request struct {
	RequestID string `json:"requestId"`
	Source    Source `json:"source"`
}

type Result struct {
	PreviewID      string `json:"previewId"`
	SourceRevision string `json:"sourceRevision"`
	OpenMode       string `json:"openMode"`
	URL            string `json:"url"`
	ExpiresAt      int64  `json:"expiresAt"`
}

// Resolved is supplied only after the caller has performed current read access checks.
type Resolved struct{ Path, Identity string }
type Resolver func() (Resolved, error)

type Error struct {
	Code, Message string
	Status        int
}

func (e *Error) Error() string { return e.Message }
func failure(code, message string, status int) *Error {
	return &Error{Code: code, Message: message, Status: status}
}

type record struct {
	Key            string `json:"key"`
	Scope          string `json:"scope"`
	APIBaseURL     string `json:"apiBaseUrl"`
	PublicBaseURL  string `json:"publicBaseUrl"`
	SourceHash     string `json:"sourceHash"`
	DocumentID     string `json:"documentId,omitempty"`
	LinkID         string `json:"linkId,omitempty"`
	URL            string `json:"url,omitempty"`
	ExpiresAt      int64  `json:"expiresAt"`
	LastAccess     int64  `json:"lastAccess"`
	UnknownUpload  bool   `json:"unknownUpload,omitempty"`
	UnknownRequest string `json:"unknownRequest,omitempty"`
	UnknownAt      int64  `json:"unknownAt,omitempty"`
}

type snapshot struct {
	data       []byte
	name, hash string
}
