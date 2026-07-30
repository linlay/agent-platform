package filetools

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

type ScopedPolicyError struct {
	Code    string
	Message string
}

func (e *ScopedPolicyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func ScopedPolicyErrorCode(err error) string {
	if typed, ok := err.(*ScopedPolicyError); ok {
		return typed.Code
	}
	return ""
}

func ValidateScopedRead(_ contracts.QuerySession, _ string, _ bool) error {
	// Dedicated KBASE reads use the same AccessPolicy as every other file-tool
	// read. editingMode only gates mutations whose canonical target is inside
	// the KBASE source.
	return nil
}

func ValidateScopedWrite(session contracts.QuerySession, path string) error {
	policy := session.ScopedFilePolicy
	if policy == nil {
		return nil
	}
	if strings.TrimSpace(policy.WorkspaceRoot) == "" {
		return scopedError("kbase_editing_path_outside_workspace", "KBASE editing workspace root is unavailable")
	}
	inWorkspace, err := scopedPathWithinRoot(policy.WorkspaceRoot, path)
	if err != nil {
		return scopedError("kbase_editing_path_outside_workspace", err.Error())
	}
	if !inWorkspace {
		return nil
	}
	if !policy.WorkspaceMutationEnabled {
		return scopedError("kbase_editing_mode_required", "KBASE workspace mutation requires editingMode=true")
	}
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return scopedError("kbase_editing_invalid_file", "KBASE editing only supports regular files")
		}
	case os.IsNotExist(err):
		if policy.RequireExistingParent {
			parentInfo, parentErr := os.Stat(filepath.Dir(path))
			if parentErr != nil || !parentInfo.IsDir() {
				return scopedError("kbase_editing_parent_missing", "KBASE editing requires an existing parent directory")
			}
		}
	default:
		return scopedError("kbase_editing_invalid_file", err.Error())
	}
	return nil
}

func ScopedPathInSource(session contracts.QuerySession, path string) bool {
	policy := session.ScopedFilePolicy
	if policy == nil {
		return false
	}
	inside, err := scopedPathWithinRoot(policy.WorkspaceRoot, path)
	return err == nil && inside
}

func scopedPathWithinRoot(root string, path string) (bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return false, nil
	}
	rootCanonical, err := pathutil.Canonicalize(root)
	if err != nil {
		return false, err
	}
	targetCanonical, err := pathutil.Canonicalize(path)
	if err != nil {
		return false, err
	}
	return pathutil.WithinRoot(targetCanonical, rootCanonical), nil
}

func scopedError(code string, message string) error {
	return &ScopedPolicyError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}
