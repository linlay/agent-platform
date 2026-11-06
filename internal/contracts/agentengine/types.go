package agentengine

import "encoding/json"

type PlatformTarget struct {
	Os   string `json:"os"`
	Arch string `json:"arch"`
}

type EngineDependency struct {
	Path     *string `json:"path,omitempty"`
	Entry    *string `json:"entry,omitempty"`
	ID       string  `json:"id"`
	Version  string  `json:"version"`
	Delivery string  `json:"delivery"`
}

type EngineLaunch struct {
	Runtime string   `json:"runtime"`
	Entry   string   `json:"entry"`
	Args    []string `json:"args"`
}

type AdapterVersion struct {
	Package       string `json:"package"`
	Version       string `json:"version"`
	ACPSDKVersion string `json:"acpSdkVersion"`
}

type EngineAuthentication struct {
	TerminalMethods []TerminalAuthLaunch `json:"terminalMethods"`
	AgentMethods    []string             `json:"agentMethods,omitempty"`
}

type EngineResource struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Protocol       string                `json:"protocol"`
	Transport      string                `json:"transport"`
	Launch         EngineLaunch          `json:"launch"`
	Adapter        *AdapterVersion       `json:"adapter,omitempty"`
	Dependencies   []EngineDependency    `json:"dependencies"`
	Authentication *EngineAuthentication `json:"authentication,omitempty"`
}

type EnginePluginManifest struct {
	PluginAPIVersion int64          `json:"pluginApiVersion"`
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Version          string         `json:"version"`
	Description      string         `json:"description"`
	Platform         PlatformTarget `json:"platform"`
	Desktop          struct {
		BundleTopLevelDir string `json:"bundleTopLevelDir"`
	} `json:"desktop"`
	Requires struct {
		PlatformAgentEngineAPI  int64 `json:"platformAgentEngineApi"`
		WebclientAgentEngineAPI int64 `json:"webclientAgentEngineApi"`
	} `json:"requires"`
	Resources struct {
		AgentEngines []EngineResource `json:"agentEngines"`
	} `json:"resources"`
}

type EngineInstallation struct {
	EngineID        string `json:"engineId"`
	PluginID        string `json:"pluginId"`
	PluginVersion   string `json:"pluginVersion"`
	InstallationRef string `json:"installationRef"`
	ArtifactDigest  string `json:"artifactDigest"`
	Enabled         bool   `json:"enabled"`
}

type EngineUpsert struct {
	APIVersion      int64  `json:"apiVersion"`
	Revision        int64  `json:"revision"`
	EngineID        string `json:"engineId"`
	PluginID        string `json:"pluginId"`
	PluginVersion   string `json:"pluginVersion"`
	InstallationRef string `json:"installationRef"`
	ArtifactDigest  string `json:"artifactDigest"`
	Enabled         bool   `json:"enabled"`
}

type EngineSnapshot struct {
	APIVersion int64                `json:"apiVersion"`
	Revision   int64                `json:"revision"`
	Entries    []EngineInstallation `json:"entries"`
}

type EngineRevoke struct {
	APIVersion int64  `json:"apiVersion"`
	Revision   int64  `json:"revision"`
	EngineID   string `json:"engineId"`
}

type ACPProject struct {
	Engine        string `json:"engine"`
	RuntimeConfig struct {
		EngineID      string `json:"engineId"`
		WorkspaceRoot string `json:"workspaceRoot"`
	} `json:"runtimeConfig"`
}

type ConfigSelection struct {
	ConfigID string          `json:"configId"`
	Value    json.RawMessage `json:"value"`
}

type SessionBinding struct {
	SchemaVersion   int64             `json:"schemaVersion"`
	ChatID          string            `json:"chatId"`
	EngineID        string            `json:"engineId"`
	ProfileID       string            `json:"profileId"`
	WorkspaceRoot   string            `json:"workspaceRoot"`
	ACPSessionID    string            `json:"acpSessionId"`
	ContextState    string            `json:"contextState"`
	ConfigSelection []ConfigSelection `json:"configSelection"`
	LastUsed        *struct {
		PluginVersion  string `json:"pluginVersion"`
		ArtifactDigest string `json:"artifactDigest"`
	} `json:"lastUsed,omitempty"`
}

type ConfigChoice struct {
	Value string  `json:"value"`
	Name  string  `json:"name"`
	Group *string `json:"group,omitempty"`
}

type ExternalConfigOption = json.RawMessage

// ConfigPreview contains options only, never an executable session handle.
type ConfigPreview struct {
	EngineID      string                 `json:"engineId"`
	ConfigOptions []ExternalConfigOption `json:"configOptions"`
}

type PreparedSession struct {
	ChatID         string                 `json:"chatId"`
	EngineID       string                 `json:"engineId"`
	PreparationID  string                 `json:"preparationId"`
	ConfigRevision int64                  `json:"configRevision"`
	ConfigOptions  []ExternalConfigOption `json:"configOptions"`
}

type ConfigUpdate struct {
	ChatID         string            `json:"chatId"`
	PreparationID  string            `json:"preparationId"`
	ConfigRevision int64             `json:"configRevision"`
	Selections     []ConfigSelection `json:"selections"`
}

type TerminalAuthLaunch struct {
	MethodID string   `json:"methodId"`
	Args     []string `json:"args"`
}

type AuthMethod struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
}

type AuthStatus struct {
	EngineID        string       `json:"engineId"`
	State           string       `json:"state"`
	Methods         []AuthMethod `json:"methods"`
	AuthOperationID *string      `json:"authOperationId,omitempty"`
}

type AuthStart struct {
	EngineID string `json:"engineId"`
	MethodID string `json:"methodId"`
}

type ElicitationRequest = json.RawMessage

type QuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type ExternalQuestion = json.RawMessage

type EngineError struct {
	Code int64  `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ErrorCode      string  `json:"errorCode"`
		Reason         *string `json:"reason,omitempty"`
		ExecutionState string  `json:"executionState"`
		Retryable      bool    `json:"retryable"`
	} `json:"data"`
}

type ManagementBootstrap struct {
	SchemaVersion    int64  `json:"schemaVersion"`
	HostID           string `json:"hostId"`
	LaunchNonce      string `json:"launchNonce"`
	ManagementSecret string `json:"managementSecret"`
	DeliveryRoot     string `json:"deliveryRoot"`
}

type ManagementStatus struct {
	APIVersion      int64  `json:"apiVersion"`
	HostID          string `json:"hostId"`
	Epoch           string `json:"epoch"`
	Ready           bool   `json:"ready"`
	AppliedRevision int64  `json:"appliedRevision"`
}

type SyncAck struct {
	APIVersion        int64    `json:"apiVersion"`
	AppliedRevision   int64    `json:"appliedRevision"`
	Action            string   `json:"action"`
	Ready             bool     `json:"ready"`
	DrainingEngineIds []string `json:"drainingEngineIds"`
}

type ExecutionBackend string

type EngineCapabilities struct {
	Question      bool `json:"question"`
	Permission    bool `json:"permission"`
	Configuration bool `json:"configuration"`
	Resume        bool `json:"resume"`
	Image         bool `json:"image"`
	File          bool `json:"file"`
	Planning      bool `json:"planning"`
	AccessLevel   bool `json:"access_level"`
	ServiceTier   bool `json:"service_tier"`
	Steer         bool `json:"steer"`
	BooleanConfig bool `json:"boolean_config"`
}

type EngineDirectoryEntry struct {
	EngineID        string             `json:"engineId"`
	PluginID        string             `json:"pluginId"`
	PluginVersion   string             `json:"pluginVersion"`
	Enabled         bool               `json:"enabled"`
	SyncState       string             `json:"syncState"`
	DependencyState string             `json:"dependencyState"`
	AuthState       string             `json:"authState"`
	RuntimeState    string             `json:"runtimeState"`
	Capabilities    EngineCapabilities `json:"capabilities"`
}

type EngineDirectory struct {
	APIVersion        int64                  `json:"apiVersion"`
	CatalogGeneration string                 `json:"catalogGeneration"`
	Revision          int64                  `json:"revision"`
	Ready             bool                   `json:"ready"`
	Engines           []EngineDirectoryEntry `json:"engines"`
}

type DirectoryChanged struct {
	CatalogGeneration string `json:"catalogGeneration"`
	Revision          int64  `json:"revision"`
}

type EpochMillis int64

type QuestionAnswer struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
}

type DeliverySource struct {
	Kind             string `json:"kind"`
	AuthorizationRef string `json:"authorizationRef"`
	ArchiveDigest    string `json:"archiveDigest"`
}

type TrustedDeliveryRecord struct {
	SchemaVersion   int64          `json:"schemaVersion"`
	InstallationRef string         `json:"installationRef"`
	OwnerHostID     string         `json:"ownerHostId"`
	PluginID        string         `json:"pluginId"`
	PluginVersion   string         `json:"pluginVersion"`
	Platform        PlatformTarget `json:"platform"`
	ArtifactDigest  string         `json:"artifactDigest"`
	Source          DeliverySource `json:"source"`
}

type SessionPrepare struct {
	AgentKey string `json:"agentKey,omitempty"`
	ChatID   string `json:"chatId"`
}

type AuthCancel struct {
	AuthOperationID string `json:"authOperationId"`
}
