package kbx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"agent-platform/internal/knowledge"
	"agent-platform/internal/models"
)

// NewConfiguredCenterEngine validates deployment defaults. Each child receives
// a private library-specific configuration snapshot via --config.
func NewConfiguredCenterEngine(file string, registry *models.ModelRegistry, modelKey, prompt string) (*CenterEngine, error) {
	source := &ModelConfigSource{File: file, Registry: registry, ModelKey: modelKey, Prompt: prompt}
	if _, err := source.Snapshot(); err != nil {
		return nil, err
	}
	return NewCenterEngineWithSource(source), nil
}

// ModelConfigSource supplies shared registry connections and deployment defaults.
// Libraries may select their embedding model; Agents cannot override it.
type ModelConfigSource struct {
	File             string
	Registry         *models.ModelRegistry
	ModelKey, Prompt string
	mu               sync.Mutex
	last             []byte
}

func NewCenterEngineWithSource(source *ModelConfigSource) *CenterEngine {
	return &CenterEngine{runner: libraryConfigRunner{source: source}, embedding: source.ModelKey != "", configSource: source}
}
func (s *ModelConfigSource) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, registry, modelKey, prompt := s.File, s.Registry, s.ModelKey, s.Prompt
	if !filepath.IsAbs(file) {
		return nil, fmt.Errorf("KBX config file must be absolute")
	}
	if prompt == "" {
		prompt = "raw"
	}
	if prompt != "raw" && prompt != "qwen3" && prompt != "embeddinggemma" {
		return nil, fmt.Errorf("invalid KBX embedding prompt")
	}
	m := NewManager(Options{DefaultEmbeddingModelKey: modelKey, EmbeddingPrompt: prompt}, nil, registry)
	raw, err := m.config(library{spec: knowledge.AgentSpec{Config: knowledge.DefaultConfig()}}, true)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if role, ok := cfg["models"].(map[string]any)["embedding"].(map[string]any); ok {
		role["prompt"] = prompt
	}
	raw, err = json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if bytes.Equal(raw, s.last) {
		return append([]byte(nil), raw...), nil
	}
	dir := filepath.Dir(file)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("KBX config directory must not be a symlink")
	}
	if st, err = os.Lstat(file); err == nil && !st.Mode().IsRegular() {
		return nil, fmt.Errorf("KBX config file must be regular")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), file); err != nil {
		return nil, err
	}
	s.last = append([]byte(nil), raw...)
	return raw, nil
}
