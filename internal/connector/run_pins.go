package connector

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PinRuns are durable references for suspended/crashed Runs; terminal owners remove
// them. The same files let startup recreate version-specific MCP routes.
func (s Sources) RunPinPath(runID string) string {
	return filepath.Join(s.SharedRoot(), ".run-pins", fmt.Sprintf("%x.json", sha256.Sum256([]byte(runID))))
}
func (s Sources) PinRun(runID string, mounts []AgentRuntime) error {
	path := s.RunPinPath(runID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(mounts)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pin-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (s Sources) PinnedRuntimes() ([]AgentRuntime, error) {
	entries, err := os.ReadDir(filepath.Join(s.SharedRoot(), ".run-pins"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []AgentRuntime
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var mounts []AgentRuntime
		if err := ReadJSON(filepath.Join(s.SharedRoot(), ".run-pins", entry.Name()), &mounts); err != nil {
			return nil, err
		}
		for _, mount := range mounts {
			if !ValidID(mount.ID) || !validDigest(mount.Digest) || mount.Dir != filepath.Join(s.SharedRoot(), mount.ID, mount.Digest) {
				return nil, fmt.Errorf("invalid durable connector pin")
			}
		}
		result = append(result, mounts...)
	}
	return result, nil
}
