package tools

import (
	"path/filepath"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

type resolvedToolImageSource struct {
	Name     string
	Path     string
	MimeHint string
}

type toolImageSourcePolicy struct {
	SourceInvalidCode        string
	ReferenceNameInvalidCode string
	ChatUnavailableCode      string
	FilePathInvalidCode      string
	FilePathBlockedCode      string
	DeviceBlockedCode        string
	ApprovalRequiredCode     string
	ApprovalMessage          string
	Error                    func(string, string, map[string]any) ToolExecutionResult
}

func isPlainFileName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.Base(name) != name {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
}

func (t *RuntimeToolExecutor) planToolImageSource(raw any, execCtx *ExecutionContext, policy toolImageSourcePolicy) (resolvedToolImageSource, filetools.AccessPlan, ToolExecutionResult, bool) {
	item := AnyMapNode(raw)
	referenceName := strings.TrimSpace(AnyStringNode(item["referenceName"]))
	filePath := strings.TrimSpace(AnyStringNode(item["filePath"]))
	mimeHint := ""
	session := t.policySession(execCtx)
	if (referenceName == "" && filePath == "") || (referenceName != "" && filePath != "") {
		return resolvedToolImageSource{}, filetools.AccessPlan{}, policy.Error(policy.SourceInvalidCode, "each image must provide exactly one of referenceName or filePath", nil), true
	}
	if referenceName != "" {
		if !isPlainFileName(referenceName) {
			return resolvedToolImageSource{}, filetools.AccessPlan{}, policy.Error(policy.ReferenceNameInvalidCode, "referenceName must be a file name without path separators", map[string]any{"referenceName": referenceName}), true
		}
		chatID := ""
		if execCtx != nil {
			chatID = strings.TrimSpace(execCtx.Request.ChatID)
			if chatID == "" {
				chatID = strings.TrimSpace(execCtx.Session.ChatID)
			}
		}
		if chatID == "" || strings.TrimSpace(t.cfg.Paths.ChatsDir) == "" {
			return resolvedToolImageSource{}, filetools.AccessPlan{}, policy.Error(policy.ChatUnavailableCode, "chat context is required to load referenceName images", nil), true
		}
		if execCtx != nil {
			for _, ref := range execCtx.Request.References {
				if strings.EqualFold(strings.TrimSpace(ref.Name), referenceName) {
					mimeHint = ref.MimeType
					break
				}
			}
		}
		filePath = filepath.Join(t.cfg.Paths.ChatsDir, chatID, referenceName)
		// The resource root is derived from trusted runtime configuration and the
		// current Chat identity, including internal callers without local paths.
		session.ChatRoot = filepath.Join(t.cfg.Paths.ChatsDir, chatID)
	}

	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, session, filetools.ReadAccess, filePath)
	if err != nil {
		code := policy.FilePathInvalidCode
		if strings.Contains(err.Error(), "workspace_unavailable") {
			code = "workspace_unavailable"
		}
		return resolvedToolImageSource{}, filetools.AccessPlan{}, policy.Error(code, err.Error(), nil), true
	}
	if access.Blocked {
		return resolvedToolImageSource{}, filetools.AccessPlan{}, policy.Error(policy.FilePathBlockedCode, access.Reason, map[string]any{"filePath": access.Path}), true
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return resolvedToolImageSource{}, filetools.AccessPlan{}, policy.Error(policy.DeviceBlockedCode, "device file is blocked", map[string]any{"filePath": access.Path}), true
	}
	name := referenceName
	if name == "" {
		name = filepath.Base(access.Path)
	}
	return resolvedToolImageSource{Name: name, Path: access.Path, MimeHint: mimeHint}, access, ToolExecutionResult{}, false
}

func (t *RuntimeToolExecutor) resolveToolImageSource(raw any, execCtx *ExecutionContext, policy toolImageSourcePolicy) (resolvedToolImageSource, ToolExecutionResult, bool) {
	source, access, result, handled := t.planToolImageSource(raw, execCtx, policy)
	if handled {
		return source, result, true
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeReadApproval(execCtx, access) {
		return resolvedToolImageSource{}, fileAccessApprovalRequired(policy.ApprovalRequiredCode, policy.ApprovalMessage, access), true
	}
	return source, ToolExecutionResult{}, false
}
