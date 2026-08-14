package runtimeresource

import "time"

const (
	ModeVersionChange = "version-change"
	ModeManualImport  = "manual-import"
)

type Options struct {
	RuntimeDir       string
	Source           string
	PreviousSource   string
	DesktopFrom      string
	DesktopTo        string
	Mode             string
	Now              func() time.Time
	BeforePublish    func() error
	AfterPublishStep func(relativePath string) error
}

type Stats struct {
	AddedUnits                  int `json:"addedUnits"`
	PreservedUnits              int `json:"preservedUnits"`
	RemovedManagedUnits         int `json:"removedManagedUnits"`
	AddedRegistryFiles          int `json:"addedRegistryFiles"`
	OverwrittenRegistryFiles    int `json:"overwrittenRegistryFiles"`
	RemovedManagedRegistryFiles int `json:"removedManagedRegistryFiles"`
}

type Result struct {
	Changed        bool   `json:"changed"`
	Mode           string `json:"mode"`
	DesktopVersion string `json:"desktopVersion"`
	PackageSHA256  string `json:"packageSha256,omitempty"`
	BackupDir      string `json:"backupDir,omitempty"`
	Stats          Stats  `json:"stats"`
}

type State struct {
	SchemaVersion        int      `json:"schemaVersion"`
	TransactionID        string   `json:"transactionId"`
	DesktopVersion       string   `json:"desktopVersion"`
	PackageSHA256        string   `json:"packageSha256"`
	CompletedAt          string   `json:"completedAt"`
	ManagedUnits         []string `json:"managedUnits"`
	ManagedRegistryFiles []string `json:"managedRegistryFiles"`
	Stats                Stats    `json:"stats"`
}

type backupTarget struct {
	RelativePath string `json:"relativePath"`
	Existed      bool   `json:"existed"`
}

type upgradeJournal struct {
	SchemaVersion int               `json:"schemaVersion"`
	TransactionID string            `json:"transactionId"`
	RuntimeDir    string            `json:"runtimeDir"`
	DesktopFrom   string            `json:"desktopVersionFrom"`
	DesktopTo     string            `json:"desktopVersionTo"`
	Mode          string            `json:"mode"`
	Source        string            `json:"source"`
	PackageSHA256 string            `json:"packageSha256"`
	BackupDir     string            `json:"backupDir"`
	Targets       []backupTarget    `json:"targets"`
	PathModes     map[string]uint32 `json:"pathModes,omitempty"`
	StartedAt     string            `json:"startedAt"`
}
