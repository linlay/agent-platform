package kbx

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
)

// Options contains Platform-owned configuration. KBX owns all index operations.
type Options struct {
	RuntimeDir, DefaultEmbeddingModelKey, EmbeddingPrompt string
	StateDir                                              string
	ConfigSource                                          *ModelConfigSource
	KBases                                                *kbases.Service
}
type embeddingModels interface {
	GetEmbedding(string) (models.ModelDefinition, models.ProviderDefinition, error)
}

type Manager struct {
	options       Options
	agents        knowledge.AgentSource
	models        embeddingModels
	runner        Runner
	frozenConfig  []byte
	skipEmbedding bool
}
type library struct {
	searchOptions *knowledge.SearchOptions
	source        kbases.Collection // Only maintenance consumes source configuration.
	spec          knowledge.AgentSpec
	database      string
	collection    string
	definition    kbases.Definition
	release       func()
}

func NewManager(options Options, agents knowledge.AgentSource, registry *models.ModelRegistry) *Manager {
	m := &Manager{options: options, agents: agents, runner: cliRunner{}}
	if options.ConfigSource != nil {
		m.runner = libraryConfigRunner{source: options.ConfigSource}
	}
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
	if m.options.KBases == nil {
		return spec, unavailable("knowledge library service unavailable")
	}
	return spec, nil
}
func (m *Manager) resolve(key string) (library, error) {
	spec, err := m.boundAgent(key)
	if err != nil {
		return library{}, err
	}
	d, db, release, err := m.options.KBases.Acquire(spec.Config.LibraryID)
	if err != nil {
		return library{}, unavailable(err.Error())
	}
	return library{spec: spec, database: db, definition: d, release: release}, nil
}
func (m *Manager) BindKBases(libraryService *kbases.Service) { m.options.KBases = libraryService }
func (m *Manager) ValidateAgent(key string) error {
	spec, err := m.boundAgent(key)
	if err != nil {
		return err
	}
	d, err := m.options.KBases.Get(spec.Config.LibraryID)
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
	if m.frozenConfig != nil {
		return append([]byte(nil), m.frozenConfig...), nil
	}
	if m.options.ConfigSource != nil {
		copy := *m
		copy.options.ConfigSource = nil
		copy.options.DefaultEmbeddingModelKey, copy.options.EmbeddingPrompt = m.options.ConfigSource.selection(l.definition)
		copy.models = nil
		if m.options.ConfigSource.Registry != nil {
			copy.models = m.options.ConfigSource.Registry
		}
		return copy.config(l, embedding)
	}
	cfg := map[string]any{"models": map[string]any{"embedding": nil, "query_expansion": nil, "reranker": nil, "graph_extraction": nil}}
	defaults := knowledge.ChunkSettings{}
	if l.definition.Chunk != nil {
		defaults = *l.definition.Chunk
	}
	chunk, err := knowledge.ResolveSourceChunk(defaults, l.source.Chunk)
	if err != nil {
		return nil, err
	}
	cfg["chunking"] = map[string]any{"strategy": chunk.Strategy, "max_chars": chunk.MaxChars, "overlap_chars": chunk.OverlapChars}
	if l.definition.TextEncoding != "" {
		cfg["text_encoding"] = l.definition.TextEncoding
	}
	key := m.options.DefaultEmbeddingModelKey
	if l.definition.Models != nil && l.definition.Models.Embedding != nil {
		key = l.definition.Models.Embedding.ModelKey
	}
	if embedding && key != "" {
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
		if l.definition.Models != nil && l.definition.Models.Embedding != nil && l.definition.Models.Embedding.Prompt != "" {
			role["prompt"] = l.definition.Models.Embedding.Prompt
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
	if l.definition.VectorsPending && len(args) > 0 {
		if args[0] == "vsearch" {
			return unavailable("knowledge vectors are rebuilding for the current embedding configuration")
		}
		if args[0] == "query" {
			embedding = false
		}
	}
	cfg, err := m.config(l, embedding)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		cfg, err = m.queryConfig(cfg, l.definition, args[0], l.searchOptions)
		if err != nil {
			return err
		}
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
