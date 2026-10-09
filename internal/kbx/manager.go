package kbx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbasescenter"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
)

// Options contains Platform-owned configuration. KBX owns all index operations.
type Options struct {
	RuntimeDir, DefaultEmbeddingModelKey, EmbeddingPrompt string
	StateDir                                              string
	ConfigSource                                          *ModelConfigSource
	Center                                                *kbasescenter.Service
}
type embeddingModels interface {
	GetEmbedding(string) (models.ModelDefinition, models.ProviderDefinition, error)
}

type Manager struct {
	options Options
	agents  knowledge.AgentSource
	models  embeddingModels
	runner  Runner
}
type library struct {
	source     kbasescenter.Collection // Only maintenance consumes source configuration.
	spec       knowledge.AgentSpec
	database   string
	collection string
	definition kbasescenter.Definition
	release    func()
}

func NewManager(options Options, agents knowledge.AgentSource, registry *models.ModelRegistry) *Manager {
	m := &Manager{options: options, agents: agents, runner: cliRunner{}}
	if registry != nil {
		m.models = registry
	}
	return m
}
func unavailable(message string) error {
	return &knowledge.PolicyError{Kind: knowledge.ErrorUnavailable, Message: message}
}
func (m *Manager) boundAgent(key string) (knowledge.AgentSpec, error) {
	if m.agents == nil {
		return knowledge.AgentSpec{}, unavailable("knowledge catalog unavailable")
	}
	spec, ok := m.agents.Agent(strings.TrimSpace(key))
	if !ok || spec.Config.LibraryID == "" {
		return spec, &knowledge.PolicyError{Kind: knowledge.ErrorNotFound, Message: "Agent has no library binding"}
	}
	if m.options.Center == nil {
		return spec, unavailable("knowledge center unavailable")
	}
	return spec, nil
}
func (m *Manager) resolve(key string) (library, error) {
	spec, err := m.boundAgent(key)
	if err != nil {
		return library{}, err
	}
	d, db, release, err := m.options.Center.Acquire(spec.Config.LibraryID)
	if err != nil {
		return library{}, unavailable(err.Error())
	}
	return library{spec: spec, database: db, definition: d, release: release}, nil
}
func (m *Manager) BindCenter(center *kbasescenter.Service) { m.options.Center = center }
func (m *Manager) ValidateAgent(key string) error {
	spec, err := m.boundAgent(key)
	if err != nil {
		return err
	}
	d, err := m.options.Center.Get(spec.Config.LibraryID)
	if err != nil {
		return unavailable(err.Error())
	}
	if d.Orphaned || d.Error != "" {
		return unavailable("bound knowledge library is invalid: " + d.Error)
	}
	return nil
}

func (m *Manager) ValidateRun(key string) error {
	l, err := m.resolve(key)
	if err != nil {
		return err
	}
	l.release()
	return nil
}

func (m *Manager) RuntimeSnapshot() knowledge.RuntimeState {
	_, err := builtins.ResolveProcessBuiltin("kbx")
	s := knowledge.RuntimeState{Engine: "kbx", Available: err == nil}
	if err != nil {
		s.LastError = "managed KBX executable unavailable"
	}
	return s
}
func (m *Manager) ProbeRuntime(ctx context.Context) (bool, knowledge.RuntimeState, error) {
	required := false
	s := m.RuntimeSnapshot()
	if !s.Available {
		return required, s, unavailable(s.LastError)
	}
	// Probe the maintenance contract without opening an index or accessing models.
	err := m.probeMaintenance(ctx)
	if err != nil {
		s.Available = false
		s.LastError = err.Error()
	}
	return required, s, err
}
func (m *Manager) config(l library, embedding bool) ([]byte, error) {
	cfg := map[string]any{"models": map[string]any{"embedding": nil, "query_expansion": nil, "reranker": nil, "graph_extraction": nil}}
	chunk := l.source.Chunk
	maxChars, overlap := 3600, 540
	if chunk.Unit == knowledge.ChunkUnitChars {
		maxChars, overlap = chunk.MaxChars, chunk.OverlapChars
	} else if chunk.MaxTokens != 0 && (chunk.MaxTokens != 1000 || chunk.OverlapTokens != 100) {
		return nil, fmt.Errorf("KBX requires character chunking for custom sizes; configure unit: chars")
	}
	cfg["chunking"] = map[string]any{"strategy": "window", "max_chars": maxChars, "overlap_chars": overlap}
	key := m.options.DefaultEmbeddingModelKey
	if embedding && m.options.ConfigSource != nil {
		raw, err := m.options.ConfigSource.Snapshot()
		if err != nil {
			return nil, err
		}
		var shared map[string]any
		if err = json.Unmarshal(raw, &shared); err != nil {
			return nil, err
		}
		cfg["models"] = shared["models"]
	} else if embedding && key != "" {
		if m.models == nil {
			return nil, unavailable("KBX embedding registry unavailable")
		}
		model, provider, err := m.models.GetEmbedding(key)
		if err != nil {
			return nil, err
		}
		endpoint := model.Embedding.EndpointPath
		if endpoint == "" {
			endpoint = "/v1/embeddings"
		}
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = strings.TrimRight(provider.BaseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
		}
		timeout := model.Embedding.Timeout
		if timeout <= 0 {
			timeout = provider.Embedding.Timeout
		}
		if timeout <= 0 {
			timeout = 60
		}
		role := map[string]any{"url": endpoint, "model": model.ModelID, "timeout_ms": timeout * 1000, "prompt": "raw"}
		if m.options.EmbeddingPrompt != "" {
			role["prompt"] = m.options.EmbeddingPrompt
		}
		if model.Embedding.BatchSize > 0 {
			role["batch_size"] = model.Embedding.BatchSize
		}
		if provider.APIKey != "" {
			role["api_key"] = provider.APIKey
		}
		cfg["models"].(map[string]any)["embedding"] = role
	}
	return json.Marshal(cfg)
}
func (m *Manager) call(ctx context.Context, l library, embedding bool, out any, args ...string) error {
	cfg, err := m.config(l, embedding)
	if err != nil {
		return err
	}
	data, err := m.runner.Run(ctx, l.database, cfg, args...)
	if failure := readerFailure(data, args[0]); failure != nil {
		return failure
	}
	if err != nil {
		return unavailable(err.Error())
	}
	if err = decodeEnvelope(data, out); err != nil {
		return unavailable(err.Error())
	}
	return nil
}
func readerContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}
