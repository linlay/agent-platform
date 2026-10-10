package kbx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agent-platform/internal/kbases"
)

func (s *ModelConfigSource) selection(d kbases.Definition) (key, prompt string) {
	key, prompt = s.ModelKey, s.Prompt
	if d.Models != nil && d.Models.Embedding != nil {
		key = d.Models.Embedding.ModelKey
		if d.Models.Embedding.Prompt != "" {
			prompt = d.Models.Embedding.Prompt
		}
	}
	return
}

// Each invocation gets an immutable private snapshot in its library directory.
// Removing it after the child exits prevents retaining rotated credentials.
type libraryConfigRunner struct{ source *ModelConfigSource }

func (r libraryConfigRunner) Run(ctx context.Context, db string, cfg []byte, args ...string) ([]byte, error) {
	if r.source == nil || !filepath.IsAbs(r.source.File) {
		return nil, fmt.Errorf("KBX config file must be absolute")
	}
	sum := sha256.Sum256([]byte(db))
	dir := filepath.Dir(r.source.File)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	for _, part := range []string{"", "libraries", hex.EncodeToString(sum[:])} {
		dir = filepath.Join(dir, part)
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("KBX library config directory must not be a symlink")
		}
	}
	f, err := os.CreateTemp(dir, "config-*.yml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(cfg); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return (cliRunner{configFile: f.Name()}).Run(ctx, db, cfg, args...)
}

func (e *LibraryEngine) libraryConfig(d kbases.Definition, embedding bool) ([]byte, error) {
	m := NewManager(Options{ConfigSource: e.configSource}, nil, nil)
	return m.config(library{definition: d}, embedding)
}

// The contract excludes credentials and transport tuning. Those take effect on
// the next invocation without rebuilding the vectors.
func (e *LibraryEngine) VectorFingerprint(d kbases.Definition) string {
	if e.configSource == nil {
		return ""
	}
	key, prompt := e.configSource.selection(d)
	if key == "" {
		return ""
	}
	contract := map[string]any{"key": key, "prompt": prompt}
	cfg, err := e.libraryConfig(d, true)
	if err != nil {
		contract["unavailable"] = true
	} else {
		var v map[string]any
		_ = json.Unmarshal(cfg, &v)
		role := v["models"].(map[string]any)["embedding"].(map[string]any)
		contract["url"], contract["model"], contract["prompt"] = role["url"], role["model"], role["prompt"]
		if model, _, err := e.configSource.Registry.GetEmbedding(key); err == nil {
			contract["dimension"] = model.Embedding.Dimension
		}
	}
	raw, _ := json.Marshal(contract)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (e *LibraryEngine) RebuildVectors(ctx context.Context, db string, d kbases.Definition) error {
	cfg, err := e.libraryConfig(d, true)
	if err != nil {
		return err
	}
	return e.embedLibrary(ctx, db, cfg, true)
}
func (e *LibraryEngine) embedLibrary(ctx context.Context, db string, cfg []byte, force bool) error {
	var settings struct {
		Models struct{ Embedding json.RawMessage }
	}
	if err := json.Unmarshal(cfg, &settings); err != nil {
		return err
	}
	if len(settings.Models.Embedding) == 0 || string(settings.Models.Embedding) == "null" {
		return nil
	}
	m := NewManager(Options{}, nil, nil)
	m.runner = e.runner
	l := library{database: db}
	status, statusErr := m.retryMaintenance(ctx, l, cfg, "status", "status")
	if statusErr != nil {
		return statusErr
	}
	if status.Index == nil {
		return fmt.Errorf("KBX status did not confirm index state")
	}
	if status.Index.Selected.Documents == 0 {
		return nil
	}
	args := []string{"embed"}
	if force {
		args = append(args, "--force")
	}
	r, err := m.retryMaintenance(ctx, l, cfg, "embed", args...)
	if !force && r.Error != nil && r.Error.Code == "VECTOR_CONTRACT_INCOMPATIBLE" {
		r, err = m.retryMaintenance(ctx, l, cfg, "embed", "embed", "--force")
	}
	// --force resets the whole vector index; continuation must never repeat it.
	if err != nil && r.Status == "partial" && r.StopReason == "SESSION_LIMIT" && r.Continuation.CanRetry {
		r, err = m.retryMaintenance(ctx, l, cfg, "embed", "embed")
	}
	if err != nil {
		return err
	}
	if r.Index == nil || !r.Index.Selected.Vector.Complete || (r.Index.Selected.Vector.Compatible != nil && !*r.Index.Selected.Vector.Compatible) {
		return fmt.Errorf("KBX embed did not confirm a complete compatible vector index")
	}
	return nil
}
