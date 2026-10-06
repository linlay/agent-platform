package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

// Like connector snapshots, this private Run state is bound once and retained.
// It contains all prepared stages, including stages not yet written to history.
type runPromptSnapshot struct {
	InitialCacheKey           string                        `json:"initialCacheKey"`
	EnvironmentPromptTemplate string                        `json:"environmentPromptTemplate"`
	RunID                     string                        `json:"runId"`
	ChatID                    string                        `json:"chatId"`
	SubTaskID                 string                        `json:"subTaskId"`
	Locale                    string                        `json:"locale"`
	Profiles                  []contracts.SystemInitProfile `json:"profiles"`
}

func (s *Builder) promptSnapshotPath(runID, subTaskID string) string {
	// Builders without a runtime root (e.g. isolated unit tests) do not persist state.
	if runID == "" || s.deps.Config.Paths.EffectiveStateDir() == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(runID + "\x00" + subTaskID))
	return filepath.Join(s.deps.Config.Paths.EffectiveStateDir(), "run-prompts", hex.EncodeToString(digest[:])+".json")
}

func (s *Builder) loadPromptSnapshot(runID, subTaskID string) (*runPromptSnapshot, error) {
	path := s.promptSnapshotPath(runID, subTaskID)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot runPromptSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.RunID != runID || snapshot.SubTaskID != subTaskID || snapshot.Locale == "" || snapshot.InitialCacheKey == "" || len(snapshot.Profiles) == 0 {
		return nil, fmt.Errorf("invalid Run prompt snapshot")
	}
	for i := range snapshot.Profiles {
		snapshot.Profiles[i].Initial = snapshot.Profiles[i].CacheKey == snapshot.InitialCacheKey
	}
	return &snapshot, nil
}

// RunPromptLocale is used only by trusted runtime callers deriving a child Run.
func (s *Builder) RunPromptLocale(runID string) (string, error) {
	snapshot, err := s.loadPromptSnapshot(runID, "")
	if err != nil {
		return "", err
	}
	if snapshot == nil {
		return s.deps.Config.Prompts.Runtime.ResolveLocale(""), nil
	}
	return snapshot.Locale, nil
}

func (s *Builder) restorePromptLocale(session *contracts.QuerySession) error {
	snapshot, err := s.loadPromptSnapshot(session.RunID, session.SubTaskID)
	if err != nil {
		return err
	}
	if snapshot != nil {
		if snapshot.ChatID != session.ChatID {
			return fmt.Errorf("Run prompt snapshot Chat mismatch")
		}
		session.Locale = snapshot.Locale
		session.EnvironmentPromptTemplate = snapshot.EnvironmentPromptTemplate
	}
	return nil
}

func (s *Builder) frozenSystemProfiles(req runtimetypes.QueryCommand, session *contracts.QuerySession) ([]contracts.SystemInitProfile, error) {
	previous, err := s.loadPromptSnapshot(session.RunID, session.SubTaskID)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		if previous.ChatID != session.ChatID {
			return nil, fmt.Errorf("Run prompt snapshot Chat mismatch")
		}
		session.PromptSnapshotRestored = true
		session.Locale = previous.Locale
		session.EnvironmentPromptTemplate = previous.EnvironmentPromptTemplate
		return previous.Profiles, nil
	}
	profiles, err := s.deps.Profiles.Profiles(req, *session)
	if err != nil || len(profiles) == 0 {
		return profiles, err
	}
	path := s.promptSnapshotPath(session.RunID, session.SubTaskID)
	if path == "" {
		return profiles, nil
	}
	snapshot := runPromptSnapshot{EnvironmentPromptTemplate: session.EnvironmentPromptTemplate, RunID: session.RunID, ChatID: session.ChatID, SubTaskID: session.SubTaskID, Locale: s.deps.Config.Prompts.Runtime.ResolveLocale(session.Locale), Profiles: profiles}
	for _, profile := range profiles {
		if profile.Initial {
			snapshot.InitialCacheKey = profile.CacheKey
		}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return nil, fmt.Errorf("bind Run prompt snapshot: %w", err)
	}
	return profiles, nil
}
