package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/internal/connector"
)

// The journal lives outside the per-connector state that DeletePackage removes.
// It contains original Agent source and is private, like credentials.
type connectorDetachFile struct {
	Key         string `json:"key"`
	Path        string `json:"path"`
	Before      string `json:"before"`
	AfterSHA256 string `json:"afterSHA256"`
}
type connectorDetachRecord struct {
	ID    string                `json:"id"`
	Files []connectorDetachFile `json:"files"`
}
type ConnectorDetachMutation struct {
	registry   *FileRegistry
	candidates []AgentConnectorCandidate
	record     connectorDetachRecord
	path       string
	applied    bool
}

func detachJournalPath(cfg config.Config, id string) string {
	return filepath.Join(cfg.Paths.ConnectorSources().PersistentRoot(), ".connector-detach-"+id+".json")
}

func (r *FileRegistry) PrepareConnectorDetach(id string) (*ConnectorDetachMutation, error) {
	users, err := r.ConfiguredConnectorUsers(id)
	if err != nil {
		return nil, err
	}
	m := &ConnectorDetachMutation{registry: r, path: detachJournalPath(r.cfg, id), record: connectorDetachRecord{ID: id}}
	if _, err := os.Lstat(m.path); err == nil {
		return nil, fmt.Errorf("connector detach recovery pending; restart before retrying")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	for _, key := range users {
		candidate, err := r.PrepareAgentConnector(key, id, false)
		if err != nil {
			return nil, fmt.Errorf("cannot detach connector from agent %s: %w", key, err)
		}
		rel, err := filepath.Rel(r.cfg.Paths.AgentsDir, candidate.Source.Source.Path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(candidate.Content))
		m.record.Files = append(m.record.Files, connectorDetachFile{Key: key, Path: rel, Before: candidate.Source.Content, AfterSHA256: hex.EncodeToString(sum[:])})
		m.candidates = append(m.candidates, candidate)
	}
	return m, nil
}
func (m *ConnectorDetachMutation) Apply() error {
	if len(m.candidates) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(m.record)
	if err != nil {
		return err
	}
	if err = writeConnectorDetachJournal(m.path, data); err != nil {
		return err
	}
	m.applied = true
	for i, candidate := range m.candidates {
		if _, err = m.registry.WriteEditableAgentSource(candidate.Source.Key, candidate.Content, candidate.Source.SHA256); err != nil {
			if errors.Is(err, ErrAgentSourceConflict) {
				m.record.Files = m.record.Files[:i]
			} else {
				m.record.Files = m.record.Files[:i+1]
			}
			return errors.Join(err, m.Rollback())
		}
	}
	return nil
}
func (m *ConnectorDetachMutation) Commit() error {
	if !m.applied {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	m.applied = false
	return nil
}
func (m *ConnectorDetachMutation) Rollback() error {
	if !m.applied {
		return nil
	}
	if err := restoreConnectorDetach(m.registry.cfg, m.record); err != nil {
		return err
	}
	return m.Commit()
}
func restoreConnectorDetach(cfg config.Config, record connectorDetachRecord) error {
	var failures []error
	for _, file := range record.Files {
		if file.Path == "." || file.Path != filepath.Clean(file.Path) || filepath.IsAbs(file.Path) || file.Path == ".." || strings.HasPrefix(file.Path, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid connector detach recovery path")
		}
		root, err := filepath.EvalSymlinks(cfg.Paths.AgentsDir)
		if err != nil {
			return err
		}
		path := filepath.Join(root, file.Path)
		// A recovery record never follows an externally replaced source or parent link.
		for parent := path; ; parent = filepath.Dir(parent) {
			info, err := os.Lstat(parent)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return ErrAgentSourceSymlink
			}
			if parent == root {
				break
			}
			if parent == filepath.Dir(parent) {
				return fmt.Errorf("invalid connector detach recovery root")
			}
		}
		current, err := os.ReadFile(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if string(current) == file.Before {
			continue
		}
		sum := sha256.Sum256(current)
		if hex.EncodeToString(sum[:]) != file.AfterSHA256 {
			failures = append(failures, fmt.Errorf("cannot restore agent %s: %w", file.Key, ErrAgentSourceConflict))
			continue
		}
		if err := writeFileAtomic(path, []byte(file.Before), 0644); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Recover before loading/publishing Agents. If the package still exists, undo
// an interrupted detach. If it has been removed, keep the detached sources.
func recoverConnectorDetaches(cfg config.Config) error {
	root := cfg.Paths.ConnectorSources().PersistentRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".connector-detach-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(root, name)
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return fmt.Errorf("invalid connector detach journal")
		}
		var record connectorDetachRecord
		if err := connector.ReadJSON(path, &record); err != nil {
			return err
		}
		if !connector.ValidID(record.ID) || name != ".connector-detach-"+record.ID+".json" {
			return fmt.Errorf("invalid connector detach journal identity")
		}
		if _, err := os.Lstat(filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), record.ID)); err == nil {
			if err := restoreConnectorDetach(cfg, record); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func (m *ConnectorDetachMutation) AgentKeys() []string {
	keys := make([]string, 0, len(m.record.Files))
	for _, file := range m.record.Files {
		keys = append(keys, file.Key)
	}
	return keys
}

// CreateTemp avoids reusing a stale or externally substituted .tmp file and
// creates the source backup with private permissions before writing any bytes.
func writeConnectorDetachJournal(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".connector-detach-tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
