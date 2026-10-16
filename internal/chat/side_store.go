package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Side lanes run hidden branch Runs of a Chat. Each lane keeps its branches
// under chats/<chatId>/.<lane>/<sideId>.jsonl; the file name is the branch
// identity, so persisted lines never repeat it.
const (
	SideLaneBTW     = "btw"
	SideLaneExplain = "explain"
)

var (
	ErrSideNotFound = errors.New("side branch not found")
	ErrSideExists   = errors.New("side branch already exists")
)

type SideRepository interface {
	CreateSideBranch(parentChatID string, lane string, sideID string) (*SideBranchStore, error)
	OpenSideBranch(parentChatID string, lane string, sideID string) (*SideBranchStore, error)
	DeleteSideBranch(parentChatID string, lane string, sideID string) error
}

type SideBranchStore struct {
	owner        *FileStore
	parentChatID string
	sideID       string
	path         string
}

// IsSideLane reports a lane whose Runs live in a side branch.
func IsSideLane(lane string) bool {
	return lane == SideLaneBTW || lane == SideLaneExplain
}

func sideRootDirName(lane string) string {
	return "." + lane
}

func isSideRootDirName(name string) bool {
	return name == sideRootDirName(SideLaneBTW) || name == sideRootDirName(SideLaneExplain)
}

func ValidSideID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func IsSideInternalPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if clean == "." || clean == "" {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if isSideRootDirName(part) {
			return true
		}
	}
	return false
}

func (s *FileStore) CreateSideBranch(parentChatID string, lane string, sideID string) (*SideBranchStore, error) {
	branch, err := s.newSideBranch(parentChatID, lane, sideID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	summary, err := s.loadSummary(branch.parentChatID)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, ErrChatNotFound
	}
	if _, err := os.Stat(branch.path); err == nil {
		return nil, ErrSideExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	root := filepath.Dir(branch.path)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(root, "."+branch.sideID+".tmp-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := readPersistedJSONLines(s.chatJSONLPath(branch.parentChatID)); err != nil {
		return nil, err
	}

	_, parentJSONL, err := readJSONLineRecords(s.chatJSONLPath(branch.parentChatID))
	if err != nil {
		return nil, err
	}
	if len(parentJSONL) > 0 {
		if _, err := tmp.Write(parentJSONL); err != nil {
			return nil, err
		}
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpPath, branch.path); err != nil {
		return nil, err
	}
	committed = true
	return branch, nil
}

func (s *FileStore) OpenSideBranch(parentChatID string, lane string, sideID string) (*SideBranchStore, error) {
	branch, err := s.newSideBranch(parentChatID, lane, sideID)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(branch.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSideNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrSideNotFound
	}
	return branch, nil
}

func (s *FileStore) DeleteSideBranch(parentChatID string, lane string, sideID string) error {
	branch, err := s.newSideBranch(parentChatID, lane, sideID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(branch.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Dir(branch.path))
	return nil
}

func (s *FileStore) newSideBranch(parentChatID string, lane string, sideID string) (*SideBranchStore, error) {
	parentChatID = strings.TrimSpace(parentChatID)
	sideID = strings.TrimSpace(sideID)
	if !ValidChatID(parentChatID) || !IsSideLane(lane) || !ValidSideID(sideID) {
		return nil, os.ErrPermission
	}
	return &SideBranchStore{
		owner:        s,
		parentChatID: parentChatID,
		sideID:       sideID,
		path:         filepath.Join(s.ChatDir(parentChatID), sideRootDirName(lane), sideID+".jsonl"),
	}, nil
}

func (b *SideBranchStore) Path() string {
	if b == nil {
		return ""
	}
	return b.path
}

func (b *SideBranchStore) AppendQueryLine(_ string, line QueryLine) error {
	if b == nil || b.owner == nil {
		return ErrSideNotFound
	}
	return b.append(line)
}

func (b *SideBranchStore) AppendStepLine(_ string, line StepLine) error {
	if b == nil || b.owner == nil {
		return ErrSideNotFound
	}
	return b.append(line)
}

func (b *SideBranchStore) AppendEventLine(_ string, line EventLine) error {
	if b == nil || b.owner == nil {
		return ErrSideNotFound
	}
	return b.append(line)
}

func (b *SideBranchStore) AppendSteerLine(_ string, line SteerLine) error {
	if b == nil || b.owner == nil {
		return ErrSideNotFound
	}
	return b.append(line)
}

func (b *SideBranchStore) AppendSubmitLine(_ string, line SubmitLine) error {
	if b == nil || b.owner == nil {
		return ErrSideNotFound
	}
	return b.append(line)
}

func (b *SideBranchStore) append(payload any) error {
	b.owner.mu.Lock()
	defer b.owner.mu.Unlock()
	if _, err := os.Stat(b.path); errors.Is(err, os.ErrNotExist) {
		return ErrSideNotFound
	} else if err != nil {
		return err
	}
	return b.owner.appendJSONLineLocked(b.path, payload)
}

func (b *SideBranchStore) LoadRawMessages(k int) ([]map[string]any, error) {
	if b == nil {
		return nil, ErrSideNotFound
	}
	return loadRawMessagesFromPath(b.path, k)
}

func (b *SideBranchStore) LoadAllSystemInits() (SystemInitIndex, error) {
	if b == nil {
		return nil, ErrSideNotFound
	}
	return loadSystemInitsFromPath(b.path)
}
