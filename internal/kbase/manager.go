package kbase

import (
	"context"
	"time"

	"agent-platform/internal/models"
	"agent-platform/internal/supportpkg"
)

// Manager is the public KBASE facade. Runtime state and implementation details
// are owned by the focused package-private components assembled in NewManager.
type Manager struct {
	resolver    *capabilityResolver
	state       *capabilityState
	validator   *storageValidator
	lance       *lanceRuntime
	generations *generationService
	refresh     *refreshCoordinator
	watchers    *watchSupervisor
	lifecycle   *lifecycleSupervisor
	query       *queryService
	status      *statusService
	files       *fileService
	auditor     *storageAuditor
}

type ManagerOptions struct {
	RuntimeDir               string
	DefaultEmbeddingModelKey string
	RefreshDebounce          time.Duration
	ReconcileInterval        time.Duration
	Extraction               ExtractionConfig
	Index                    IndexOptions
	Maintenance              MaintenanceOptions
}

type IndexOptions struct {
	FTSBaseTokenizer string
	ANNMinRows       int
}

type MaintenanceOptions struct {
	OptimizeChangeThreshold int
	OptimizeInterval        time.Duration
	VersionRetention        time.Duration
}

type AgentSource interface {
	Agents() []AgentSpec
	Agent(key string) (AgentSpec, bool)
}

func NewManager(options ManagerOptions, agents AgentSource, modelRegistry *models.ModelRegistry) *Manager {
	resolver := newCapabilityResolver(options, agents, modelRegistry)
	state := newCapabilityState()
	lance := newLanceRuntime()
	generations := newGenerationService(options.Index, options.Maintenance, resolver, lance)
	refresh := newRefreshCoordinator(resolver, state, generations)
	watchers := newWatchSupervisor(options.RefreshDebounce, resolver, refresh)
	auditor := newStorageAuditor(options.RuntimeDir, resolver)
	lifecycle := newLifecycleSupervisor(options.ReconcileInterval, resolver, watchers, refresh, lance, auditor)
	status := newStatusService(resolver, state, generations, refresh, lance)
	return &Manager{
		resolver: resolver, state: state, validator: newStorageValidator(resolver, state),
		lance: lance, generations: generations, refresh: refresh, watchers: watchers,
		lifecycle: lifecycle, query: newQueryService(resolver, state, generations, refresh, status),
		status: status,
		files:  newFileService(resolver, state), auditor: auditor,
	}
}

func (m *Manager) ValidateConfiguration() error {
	if m == nil {
		return nil
	}
	return m.validator.ValidateOwnership()
}

func (m *Manager) ValidateStorageContracts() error {
	if m == nil {
		return nil
	}
	return m.validator.ValidateRuntimeContracts()
}

func (m *Manager) ValidateAndAdoptStartupStorageContracts() map[string]error {
	if m == nil {
		return map[string]error{}
	}
	return m.validator.ValidateAndAdoptStartup()
}

func (m *Manager) ProbeSidecar(ctx context.Context) (bool, LanceEngineState, error) {
	if m == nil {
		return false, LanceEngineState{}, nil
	}
	return m.lifecycle.ProbeSidecar(ctx)
}

func (m *Manager) WithSupportPackages(registry *supportpkg.Registry) *Manager {
	if m == nil {
		return nil
	}
	m.lance.SetSupportPackages(registry)
	return m
}

func (m *Manager) ValidateAgent(agentKey string) error {
	if m == nil {
		return managerUnavailableError()
	}
	_, err := m.resolver.AgentSpec(agentKey)
	return err
}

func (m *Manager) Start(ctx context.Context) {
	if m != nil {
		m.lifecycle.Start(ctx)
	}
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	return m.lifecycle.Close(ctx)
}

func (m *Manager) ReconcileWatchers(ctx context.Context) {
	if m != nil {
		m.watchers.Reconcile(ctx)
	}
}

func (m *Manager) Refresh(ctx context.Context, agentKey string, options RefreshOptions) (RefreshResult, error) {
	if m == nil {
		err := managerUnavailableError()
		return failedRefresh(agentKey, options.Mode, err), err
	}
	return m.refresh.Refresh(ctx, agentKey, options)
}

func (m *Manager) Status(agentKey string) (Status, error) {
	if m == nil {
		return Status{AgentKey: agentKey, Mode: Mode}, managerUnavailableError()
	}
	return m.status.Status(agentKey)
}

func (m *Manager) Search(ctx context.Context, agentKey, query string, options SearchOptions) (SearchResult, error) {
	if m == nil {
		return SearchResult{}, managerUnavailableError()
	}
	return m.query.Search(ctx, agentKey, query, options)
}

func (m *Manager) Read(agentKey string, options ReadOptions) (ReadResult, error) {
	if m == nil {
		return ReadResult{}, managerUnavailableError()
	}
	return m.query.Read(agentKey, options)
}

func (m *Manager) Files(agentKey string, options FilesOptions) (FilesResult, error) {
	if m == nil {
		return FilesResult{}, managerUnavailableError()
	}
	return m.files.Files(agentKey, options)
}

func (m *Manager) AuditOrphanStorage() ([]OrphanStorage, error) {
	if m == nil {
		return nil, nil
	}
	return m.auditor.Audit()
}

func (m *Manager) RollbackGeneration(ctx context.Context, agentKey, generationID string) (*Generation, error) {
	if m == nil {
		return nil, managerUnavailableError()
	}
	return m.refresh.Rollback(ctx, agentKey, generationID)
}

func managerUnavailableError() error {
	return &PolicyError{Kind: ErrorUnavailable, Message: "kbase manager not configured"}
}
