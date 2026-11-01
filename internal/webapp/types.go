// Package webapp owns installed WebApp code, owner policy and desired state.
// Transport, gateway and process supervision consume this domain's facade.
package webapp

import (
	"encoding/json"
	"errors"
)

type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string           { return e.Code }
func fail(code string, status int) error { return &Error{code, status} }
func ErrorCode(err error) (string, int) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, e.Status
	}
	return "webapp_storage_unavailable", 503
}

type Manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	ID            string          `json:"id"`
	Key           string          `json:"key"`
	Label         string          `json:"label"`
	Version       string          `json:"version"`
	Target        string          `json:"target"`
	AppConfig     map[string]any  `json:"appConfig"`
	UserConfig    *UserConfig     `json:"userConfig,omitempty"`
	Frontend      Frontend        `json:"frontend"`
	Backend       *Backend        `json:"backend,omitempty"`
	Copilot       *Copilot        `json:"copilot,omitempty"`
	DesktopBridge json.RawMessage `json:"desktopBridge,omitempty"`
}
type Frontend struct {
	Root        string `json:"root"`
	Index       string `json:"index"`
	RouteConfig struct {
		BackendPrefixes    []string `json:"backendPrefixes"`
		NavigationFallback string   `json:"navigationFallback,omitempty"`
	} `json:"routeConfig"`
}
type Backend struct {
	Command struct {
		Type           string `json:"type"`
		Entry          string `json:"entry"`
		Runtime        string `json:"runtime,omitempty"`
		MinimumVersion string `json:"minimumVersion,omitempty"`
	} `json:"command"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
	Health struct {
		Type             string `json:"type"`
		Path             string `json:"path,omitempty"`
		StartupTimeoutMS int    `json:"startupTimeoutMs"`
	} `json:"health"`
	ShutdownTimeoutMS int `json:"shutdownTimeoutMs"`
}
type Copilot struct {
	AgentKey   string `json:"agentKey"`
	References []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"references"`
}
type UserConfig struct {
	Fields []UserField `json:"fields"`
}
type UserField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
	MaxLength   int      `json:"maxLength,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Rows        int      `json:"rows,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Step        float64  `json:"step,omitempty"`
	Source      string   `json:"source,omitempty"`
	Options     []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"options,omitempty"`
}
type Record struct {
	SchemaVersion  int      `json:"schemaVersion"`
	Manifest       Manifest `json:"manifest"`
	Revision       string   `json:"revision"`
	InstallationID string   `json:"installationId"`
	ArchiveSHA256  string   `json:"archiveSha256"`
	ContentSHA256  string   `json:"contentSha256"`
	InstalledAt    int64    `json:"installedAt"`
}
type Desired struct {
	SchemaVersion int    `json:"schemaVersion"`
	State         string `json:"state"`
	Revision      string `json:"revision"`
}
type ConnectorScope struct {
	ConnectorID string `json:"connectorId"`
	Adapter     string `json:"adapter"`
}
type Policy struct {
	SchemaVersion int              `json:"schemaVersion"`
	Revision      string           `json:"revision"`
	AgentKey      string           `json:"agentKey,omitempty"`
	Skills        []string         `json:"skills"`
	Connectors    []ConnectorScope `json:"connectors"`
	Assistant     bool             `json:"assistant"`
	Artifacts     bool             `json:"artifacts"`
	ConnectPorts  []int            `json:"connectPorts"`
}
type Entry struct {
	ID            string `json:"id"`
	Key           string `json:"key"`
	Label         string `json:"label"`
	Version       string `json:"version"`
	Target        string `json:"target"`
	Revision      string `json:"revision"`
	DesiredState  string `json:"desiredState"`
	ObservedState string `json:"observedState"`
	Backend       bool   `json:"backend"`
	URL           string `json:"url,omitempty"`
	InstalledAt   int64  `json:"installedAt"`
}
type Upload struct {
	UploadID  string `json:"uploadId"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"createdAt"`
}
type Review struct {
	Upload
	Manifest         Manifest `json:"manifest"`
	ExpectedRevision string   `json:"expectedRevision"`
	ExpandedBytes    int64    `json:"expandedBytes"`
	FileCount        int      `json:"fileCount"`
}
type InstallRequest struct {
	UploadID         string `json:"uploadId"`
	SHA256           string `json:"sha256"`
	ExpectedRevision string `json:"expectedRevision"`
}
