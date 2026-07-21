package kbase

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/models"
)

type capabilityResolver struct {
	options ManagerOptions
	agents  AgentSource
	models  *models.ModelRegistry
}

func newCapabilityResolver(options ManagerOptions, agents AgentSource, modelRegistry *models.ModelRegistry) *capabilityResolver {
	return &capabilityResolver{options: options, agents: agents, models: modelRegistry}
}

func (r *capabilityResolver) AgentSpec(agentKey string) (AgentSpec, error) {
	if r == nil || r.agents == nil {
		return AgentSpec{}, managerUnavailableError()
	}
	agentKey = strings.TrimSpace(agentKey)
	definition, ok := r.agents.Agent(agentKey)
	if !ok {
		return AgentSpec{}, &PolicyError{Kind: ErrorNotFound, Message: fmt.Sprintf("agent %s not found", agentKey)}
	}
	return definition, nil
}

func (r *capabilityResolver) Specs() []AgentSpec {
	if r == nil || r.agents == nil {
		return nil
	}
	return r.agents.Agents()
}

func (r *capabilityResolver) Keys() []string {
	var keys []string
	for _, spec := range r.Specs() {
		if key := strings.TrimSpace(spec.Key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func (r *capabilityResolver) HasRequired() bool {
	for _, spec := range r.Specs() {
		if spec.Requirement == RequirementRequired {
			return true
		}
	}
	return false
}

func (r *capabilityResolver) StorageDirForSpec(spec AgentSpec) string {
	if strings.EqualFold(strings.TrimSpace(spec.Config.Storage.Location), "workspace") {
		return filepath.Join(strings.TrimSpace(spec.Config.Source.Root), ".kbase")
	}
	return filepath.Join(r.options.RuntimeDir, strings.TrimSpace(spec.Key))
}

func (r *capabilityResolver) Resolve(agentKey string) (resolvedConfig, *Embedder, error) {
	if r == nil || r.agents == nil || r.models == nil {
		return resolvedConfig{}, nil, managerUnavailableError()
	}
	definition, err := r.AgentSpec(agentKey)
	if err != nil {
		return resolvedConfig{}, nil, err
	}
	agentKey = strings.TrimSpace(agentKey)
	workspace := strings.TrimSpace(definition.Config.Source.Root)
	if workspace == "" || strings.EqualFold(workspace, WorkspaceRootChat) {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s kbaseConfig.source.root is required for KBASE", agentKey)
	}
	if !filepath.IsAbs(workspace) {
		return resolvedConfig{}, nil, fmt.Errorf("agent %s kbaseConfig.source.root must resolve to an absolute path", agentKey)
	}
	embedding, provider, err := r.ResolveEmbedding(agentKey, definition.Config.Embedding)
	if err != nil {
		if definition.Requirement == RequirementOptional {
			return resolvedConfig{}, nil, &PolicyError{Kind: ErrorUnavailable, Message: "KBASE embedding configuration is unavailable: " + err.Error()}
		}
		return resolvedConfig{}, nil, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" || embedding.Model == "" || embedding.Dimension <= 0 {
		if definition.Requirement == RequirementOptional {
			return resolvedConfig{}, nil, &PolicyError{Kind: ErrorUnavailable, Message: fmt.Sprintf("KBASE embedding provider %s is unavailable", provider.Key)}
		}
		return resolvedConfig{}, nil, fmt.Errorf("provider %s embedding requires baseUrl/model/dimension", provider.Key)
	}
	storage := strings.ToLower(strings.TrimSpace(definition.Config.Storage.Location))
	if storage == "" {
		storage = "runtime"
	}
	var storageDir string
	switch storage {
	case "runtime":
		storageDir = filepath.Join(r.options.RuntimeDir, definition.Key)
	case "workspace":
		storageDir = filepath.Join(workspace, ".kbase")
	default:
		return resolvedConfig{}, nil, fmt.Errorf("kbaseConfig.storage.location must be runtime or workspace")
	}
	cfg := resolvedConfig{
		AgentKey: definition.Key, WorkspaceRoot: workspace, StorageDir: storageDir, Storage: storage,
		Embedding: embedding, Include: append([]string(nil), definition.Config.Include...),
		Exclude: append([]string(nil), definition.Config.Exclude...), Chunk: NormalizeChunkConfig(definition.Config.Chunk),
		Retrieval: definition.Config.Retrieval, Extraction: r.options.Extraction,
		FTSTokenizer: firstNonBlank(r.options.Index.FTSBaseTokenizer, defaultFTSTokenizer),
	}
	cfg.IndexHash = computeIndexHash(cfg)
	cfg.QueryHash = computeQueryHash(cfg)
	return cfg, newEmbedderForSnapshot(baseURL, provider.APIKey, embedding), nil
}

func (r *capabilityResolver) ResolveEmbedding(agentKey string, agentEmbedding EmbeddingConfig) (EmbeddingSnapshot, models.ProviderDefinition, error) {
	modelKey := firstNonBlank(agentEmbedding.ModelKey, r.options.DefaultEmbeddingModelKey)
	if modelKey == "" {
		return EmbeddingSnapshot{}, models.ProviderDefinition{}, fmt.Errorf("agent %s kbaseConfig.embedding.modelKey is required", agentKey)
	}
	model, provider, err := r.models.GetEmbedding(modelKey)
	if err != nil {
		return EmbeddingSnapshot{}, models.ProviderDefinition{}, err
	}
	return EmbeddingSnapshot{
		ModelKey: model.Key, ProviderKey: provider.Key, Model: model.ModelID,
		Dimension:    model.Embedding.Dimension,
		Timeout:      firstPositive(model.Embedding.Timeout, provider.Embedding.Timeout, 15),
		EndpointPath: strings.TrimSpace(model.Embedding.EndpointPath),
	}, provider, nil
}

func (r *capabilityResolver) EmbedderForRetrieval(ctx context.Context, cfg resolvedConfig, generationID string, fallback *Embedder) (*Embedder, int, error) {
	if strings.TrimSpace(generationID) == "" {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation is not ready"}
	}
	control, err := OpenReadControlStore(cfg.StorageDir)
	if err != nil {
		return nil, 0, err
	}
	generation, err := control.Generation(ctx, generationID)
	_ = control.Close()
	if err != nil {
		return nil, 0, err
	}
	if generation == nil {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation metadata is missing"}
	}
	if strings.TrimSpace(generation.EmbeddingModelKey) == "" {
		if generation.EmbeddingModel == cfg.Embedding.Model && generation.EmbeddingDimension == cfg.Embedding.Dimension {
			return fallback, generation.EmbeddingDimension, nil
		}
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding snapshot is incomplete"}
	}
	embedding, provider, err := r.ResolveEmbedding(cfg.AgentKey, EmbeddingConfig{ModelKey: generation.EmbeddingModelKey})
	if err != nil {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding model is unavailable: " + err.Error()}
	}
	if embedding.Model != generation.EmbeddingModel || embedding.Dimension != generation.EmbeddingDimension {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding model definition changed; rebuild before querying"}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if baseURL == "" {
		return nil, 0, &PolicyError{Kind: ErrorUnavailable, Message: "active KBASE generation embedding provider has no base URL"}
	}
	return newEmbedderForSnapshot(baseURL, provider.APIKey, embedding), generation.EmbeddingDimension, nil
}

func newEmbedderForSnapshot(baseURL, apiKey string, embedding EmbeddingSnapshot) *Embedder {
	embedder := NewEmbedder(baseURL, apiKey, embedding.Model, embedding.Dimension, embedding.Timeout)
	if strings.TrimSpace(embedding.EndpointPath) != "" {
		embedder.EndpointPath = embedding.EndpointPath
	}
	return embedder
}
