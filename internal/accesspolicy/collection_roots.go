package accesspolicy

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

// SessionEditableCollectionRoot matches a current canonical target against the
// immutable Host roots captured at admission. Replacing a root with a symlink
// must not redirect its grant. Container sessions never receive these grants.
func SessionEditableCollectionRoot(session contracts.QuerySession, path string) (string, bool) {
	if session.ScopedFilePolicy == nil || session.AgentHasRuntimeSandbox {
		return "", false
	}
	target, err := pathutil.Canonicalize(path)
	if err != nil {
		return "", false
	}
	for _, root := range session.ScopedFilePolicy.EditableCollectionRoots {
		if pathutil.WithinRoot(target, pathutil.Canonical{Host: root, Key: pathutil.CanonicalKey(root)}) {
			return root, true
		}
	}
	return "", false
}

// PathInSessionMutationScope is deliberately separate from Workspace identity:
// publishing, project paths and container mounts still use the real Workspace.
func PathInSessionMutationScope(session contracts.QuerySession, path string) bool {
	_, collection := SessionEditableCollectionRoot(session, path)
	return collection || PathInSessionWorkspace(session, path)
}
