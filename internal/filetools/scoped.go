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

func ValidateScopedRead(session contracts.QuerySession, path string, allowDirectory bool) error {
	policy := session.ScopedFilePolicy
	if policy == nil {
		return nil
	}
	if !policy.AllowRead {
		return scopedError("kbase_editing_mode_required", "KBASE file tools require editingMode=true")
	}
	if strings.TrimSpace(policy.Root) == "" {
		return scopedError("kbase_editing_path_outside_source", "KBASE editing source root is unavailable")
	}
	inSource, err := scopedPathWithinRoot(policy.Root, path)
	if err != nil {
		return scopedError("kbase_editing_path_outside_source", err.Error())
	}
	if !inSource {
		return nil
	}
	return validateScopedPath(*policy, path, allowDirectory, false)
}

func ValidateScopedWrite(session contracts.QuerySession, path string) error {
	policy := session.ScopedFilePolicy
	if policy == nil {
		return nil
	}
	if !policy.AllowWrite {
		return scopedError("kbase_editing_mode_required", "KBASE file mutation requires editingMode=true")
	}
	if strings.TrimSpace(policy.Root) == "" {
		return scopedError("kbase_editing_path_outside_source", "KBASE editing source root is unavailable")
	}
	inSource, err := scopedPathWithinRoot(policy.Root, path)
	if err != nil {
		return scopedError("kbase_editing_path_outside_source", err.Error())
	}
	if !inSource {
		inChat, chatErr := scopedPathWithinRoot(session.RuntimeContext.LocalPaths.ChatAttachmentsDir, path)
		if chatErr != nil {
			return scopedError("kbase_editing_path_outside_source", chatErr.Error())
		}
		if inChat {
			return nil
		}
		return scopedError("kbase_editing_path_outside_source", "KBASE editing write is outside the session workspace and current chatspace")
	}
	if err := validateScopedPath(*policy, path, false, true); err != nil {
		return err
	}
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return scopedError("kbase_editing_invalid_file", "KBASE editing only supports regular files")
		}
	case os.IsNotExist(err):
		if !policy.AllowCreate {
			return scopedError("kbase_editing_create_unsupported", "KBASE editing does not allow creating files")
		}
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
	inside, err := scopedPathWithinRoot(policy.Root, path)
	return err == nil && inside
}

func ScopedPathAllowed(session contracts.QuerySession, path string, allowDirectory bool) bool {
	policy := session.ScopedFilePolicy
	if policy == nil || !policy.AllowRead {
		return policy == nil
	}
	return validateScopedPath(*policy, path, allowDirectory, false) == nil
}

func ScopedFilePolicyRequiresUTF8(session contracts.QuerySession, path string) bool {
	return session.ScopedFilePolicy != nil &&
		ScopedPathInSource(session, path) &&
		session.ScopedFilePolicy.RequireUTF8
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

func validateScopedPath(policy contracts.ScopedFilePolicy, path string, allowDirectory bool, futureTarget bool) error {
	root := strings.TrimSpace(policy.Root)
	if root == "" {
		return scopedError("kbase_editing_path_outside_source", "KBASE editing source root is unavailable")
	}
	rootCanonical, err := pathutil.Canonicalize(root)
	if err != nil {
		return scopedError("kbase_editing_path_outside_source", err.Error())
	}
	targetCanonical, err := pathutil.Canonicalize(path)
	if err != nil {
		return scopedError("kbase_editing_path_outside_source", err.Error())
	}
	if !pathutil.WithinRoot(targetCanonical, rootCanonical) {
		return scopedError("kbase_editing_path_outside_source", "file path is outside the KBASE source root")
	}

	info, statErr := os.Stat(targetCanonical.Host)
	if statErr == nil && info.IsDir() {
		if allowDirectory {
			return nil
		}
		return scopedError("kbase_editing_invalid_file", "KBASE editing only supports files")
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return scopedError("kbase_editing_invalid_file", statErr.Error())
	}
	if os.IsNotExist(statErr) && !futureTarget {
		// Let the concrete read/search tool report its existing not-found error,
		// after the hard root and extension checks below.
	}
	if !scopedExtensionAllowed(policy.AllowedExtensions, path) ||
		!scopedExtensionAllowed(policy.AllowedExtensions, targetCanonical.Host) {
		return scopedError("kbase_editing_extension_unsupported", "KBASE editing v1 only supports .md files")
	}
	return nil
}

func scopedExtensionAllowed(allowed []string, path string) bool {
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(path)))
	if ext == "" {
		return false
	}
	for _, value := range allowed {
		candidate := strings.ToLower(strings.TrimSpace(value))
		if candidate != "" && !strings.HasPrefix(candidate, ".") {
			candidate = "." + candidate
		}
		if ext == candidate {
			return true
		}
	}
	return false
}

func scopedError(code string, message string) error {
	return &ScopedPolicyError{Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}
