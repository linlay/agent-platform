package kbx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agent-platform/internal/kbase"
	"agent-platform/internal/models"
)

// NewConfiguredCenterEngine publishes one deployment-wide connection snapshot.
// FILE is injected into each child; --config bridges CLI versions that only
// recognize --config / CONFIG_DIR. No per-library model settings are stored.
func NewConfiguredCenterEngine(file string, registry *models.ModelRegistry, modelKey, prompt string) (*CenterEngine, error) {
	if !filepath.IsAbs(file) {
		return nil, fmt.Errorf("KBX config file must be absolute")
	}
	if prompt == "" {
		prompt = "raw"
	}
	if prompt != "raw" && prompt != "qwen3" {
		return nil, fmt.Errorf("invalid KBX embedding prompt")
	}
	m := NewManager(Options{DefaultEmbeddingModelKey: modelKey}, nil, registry)
	raw, err := m.config(library{spec: kbase.AgentSpec{Config: kbase.DefaultConfig()}}, true)
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
	return &CenterEngine{runner: cliRunner{configFile: file}, embedding: modelKey != ""}, nil
}
