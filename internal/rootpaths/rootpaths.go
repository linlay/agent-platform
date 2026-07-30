package rootpaths

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/pathutil"
)

type Zone string

const (
	ZoneCurrentChat Zone = "current_chat"
	ZoneOtherChat   Zone = "other_chat"
	ZoneWorkspace   Zone = "workspace"
	ZoneExternal    Zone = "external"
)

var (
	ErrWorkspaceEqualsChatsRoot = errors.New("workspace must not equal chats root")
	ErrWorkspaceInsideChatsRoot = errors.New("workspace must not be inside chats root")
	ErrPathCrossesChatRoot      = errors.New("path_crosses_chat_root")
)

type Roots struct {
	Workspace pathutil.Canonical
	Chats     pathutil.Canonical
	Chat      pathutil.Canonical
}

func New(workspaceRoot string, chatsRoot string, chatDir string) (Roots, error) {
	var roots Roots
	var err error
	if strings.TrimSpace(workspaceRoot) != "" {
		roots.Workspace, err = pathutil.Canonicalize(workspaceRoot)
		if err != nil {
			return Roots{}, fmt.Errorf("resolve workspace root: %w", err)
		}
	}
	if strings.TrimSpace(chatsRoot) != "" {
		roots.Chats, err = pathutil.Canonicalize(chatsRoot)
		if err != nil {
			return Roots{}, fmt.Errorf("resolve chats root: %w", err)
		}
	}
	if strings.TrimSpace(chatDir) != "" {
		roots.Chat, err = pathutil.Canonicalize(chatDir)
		if err != nil {
			return Roots{}, fmt.Errorf("resolve current chat root: %w", err)
		}
	}
	if err := ValidateWorkspaceChats(roots.Workspace, roots.Chats); err != nil {
		return Roots{}, err
	}
	return roots, nil
}

func ValidateWorkspaceChats(workspace pathutil.Canonical, chats pathutil.Canonical) error {
	if workspace.Key == "" || chats.Key == "" {
		return nil
	}
	if workspace.Key == chats.Key {
		return ErrWorkspaceEqualsChatsRoot
	}
	if pathutil.WithinRoot(workspace, chats) {
		return ErrWorkspaceInsideChatsRoot
	}
	return nil
}

func (r Roots) Classify(rawPath string) (Zone, pathutil.Canonical, error) {
	candidate, err := pathutil.Canonicalize(rawPath)
	if err != nil {
		return ZoneExternal, pathutil.Canonical{}, err
	}
	return r.ClassifyCanonical(candidate), candidate, nil
}

func (r Roots) ClassifyCanonical(candidate pathutil.Canonical) Zone {
	if r.Chat.Key != "" && pathutil.WithinRoot(candidate, r.Chat) {
		return ZoneCurrentChat
	}
	if r.Chats.Key != "" && pathutil.WithinRoot(candidate, r.Chats) {
		return ZoneOtherChat
	}
	if r.Workspace.Key != "" && pathutil.WithinRoot(candidate, r.Workspace) {
		return ZoneWorkspace
	}
	return ZoneExternal
}

func (r Roots) RequireWorkspacePath(rawPath string) (pathutil.Canonical, error) {
	zone, candidate, err := r.Classify(rawPath)
	if err != nil {
		return pathutil.Canonical{}, err
	}
	switch zone {
	case ZoneCurrentChat, ZoneOtherChat:
		return pathutil.Canonical{}, fmt.Errorf("%w: workspace-relative paths must not enter the chats root; use @chat for the current chat", ErrPathCrossesChatRoot)
	case ZoneWorkspace, ZoneExternal:
		return candidate, nil
	}
	return candidate, nil
}

func (r Roots) WorkspaceContainsChatsRoot() bool {
	return r.Workspace.Key != "" &&
		r.Chats.Key != "" &&
		r.Workspace.Key != r.Chats.Key &&
		pathutil.WithinRoot(r.Chats, r.Workspace)
}

func (r Roots) ContainerMaskedPaths() ([]string, error) {
	if !r.WorkspaceContainsChatsRoot() {
		return nil, nil
	}
	rel, err := filepath.Rel(r.Workspace.Host, r.Chats.Host)
	if err != nil {
		return nil, fmt.Errorf("resolve chats root relative to workspace: %w", err)
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, fmt.Errorf("resolve chats root relative to workspace: invalid relation")
	}
	return []string{path.Join("/workspace", rel)}, nil
}
