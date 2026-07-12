package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const BTWRootDirName = ".btw"

var (
	ErrBTWNotFound = errors.New("btw branch not found")
	ErrBTWExists   = errors.New("btw branch already exists")
)

type BTWRepository interface {
	CreateBTWBranch(parentChatID string, btwID string) (*BTWBranchStore, error)
	OpenBTWBranch(parentChatID string, btwID string) (*BTWBranchStore, error)
	DeleteBTWBranch(parentChatID string, btwID string) error
}

type BTWBranchStore struct {
	owner        *FileStore
	parentChatID string
	btwID        string
	path         string
}

func ValidBTWID(value string) bool {
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

func IsBTWInternalPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if clean == "." || clean == "" {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == BTWRootDirName {
			return true
		}
	}
	return false
}

func (s *FileStore) CreateBTWBranch(parentChatID string, btwID string) (*BTWBranchStore, error) {
	branch, err := s.newBTWBranch(parentChatID, btwID)
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
		return nil, ErrBTWExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	root := filepath.Dir(branch.path)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(root, "."+branch.btwID+".tmp-*")
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

func (s *FileStore) OpenBTWBranch(parentChatID string, btwID string) (*BTWBranchStore, error) {
	branch, err := s.newBTWBranch(parentChatID, btwID)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(branch.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrBTWNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrBTWNotFound
	}
	return branch, nil
}

func (s *FileStore) DeleteBTWBranch(parentChatID string, btwID string) error {
	branch, err := s.newBTWBranch(parentChatID, btwID)
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

func (s *FileStore) newBTWBranch(parentChatID string, btwID string) (*BTWBranchStore, error) {
	parentChatID = strings.TrimSpace(parentChatID)
	btwID = strings.TrimSpace(btwID)
	if !ValidChatID(parentChatID) || !ValidBTWID(btwID) {
		return nil, os.ErrPermission
	}
	return &BTWBranchStore{
		owner:        s,
		parentChatID: parentChatID,
		btwID:        btwID,
		path:         filepath.Join(s.ChatDir(parentChatID), BTWRootDirName, btwID+".jsonl"),
	}, nil
}

func (b *BTWBranchStore) Path() string {
	if b == nil {
		return ""
	}
	return b.path
}

func (b *BTWBranchStore) AppendQueryLine(_ string, line QueryLine) error {
	if b == nil || b.owner == nil {
		return ErrBTWNotFound
	}
	return b.append(line)
}

func (b *BTWBranchStore) AppendStepLine(_ string, line StepLine) error {
	if b == nil || b.owner == nil {
		return ErrBTWNotFound
	}
	return b.append(line)
}

func (b *BTWBranchStore) AppendEventLine(_ string, line EventLine) error {
	if b == nil || b.owner == nil {
		return ErrBTWNotFound
	}
	return b.append(line)
}

func (b *BTWBranchStore) AppendSubmitLine(_ string, line SubmitLine) error {
	if b == nil || b.owner == nil {
		return ErrBTWNotFound
	}
	return b.append(line)
}

func (b *BTWBranchStore) append(payload any) error {
	b.owner.mu.Lock()
	defer b.owner.mu.Unlock()
	if _, err := os.Stat(b.path); errors.Is(err, os.ErrNotExist) {
		return ErrBTWNotFound
	} else if err != nil {
		return err
	}
	return b.owner.appendJSONLineLocked(b.path, payload)
}

func (b *BTWBranchStore) LoadRawMessages(k int) ([]map[string]any, error) {
	if b == nil {
		return nil, ErrBTWNotFound
	}
	return loadRawMessagesFromPath(b.path, k)
}

func (b *BTWBranchStore) LoadAllSystemInits() (SystemInitIndex, error) {
	if b == nil {
		return nil, ErrBTWNotFound
	}
	inits, _, err := loadSystemInitsFromPath(b.path)
	return inits, err
}
