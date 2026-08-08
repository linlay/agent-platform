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

func (t *RuntimeToolExecutor) resolveToolImageSource(raw any, execCtx *ExecutionContext, policy toolImageSourcePolicy) (resolvedToolImageSource, ToolExecutionResult, bool) {
	item := AnyMapNode(raw)
	referenceName := strings.TrimSpace(FirstNonEmptyString(item["reference_name"], item["referenceName"]))
	filePath := strings.TrimSpace(FirstNonEmptyString(item["file_path"], item["filePath"]))
	if (referenceName == "" && filePath == "") || (referenceName != "" && filePath != "") {
		return resolvedToolImageSource{}, policy.Error(policy.SourceInvalidCode, "each image must provide exactly one of reference_name or file_path", nil), true
	}
	if referenceName != "" {
		if !isPlainFileName(referenceName) {
			return resolvedToolImageSource{}, policy.Error(policy.ReferenceNameInvalidCode, "reference_name must be a file name without path separators", map[string]any{"referenceName": referenceName}), true
		}
		chatID := ""
		if execCtx != nil {
			chatID = strings.TrimSpace(execCtx.Request.ChatID)
			if chatID == "" {
				chatID = strings.TrimSpace(execCtx.Session.ChatID)
			}
		}
		if chatID == "" || strings.TrimSpace(t.cfg.Paths.ChatsDir) == "" {
			return resolvedToolImageSource{}, policy.Error(policy.ChatUnavailableCode, "chat context is required to load reference_name images", nil), true
		}
		mimeHint := ""
		if execCtx != nil {
			for _, ref := range execCtx.Request.References {
				if strings.EqualFold(strings.TrimSpace(ref.Name), referenceName) {
					mimeHint = ref.MimeType
					break
				}
			}
		}
		return resolvedToolImageSource{
			Name:     referenceName,
			Path:     filepath.Join(t.cfg.Paths.ChatsDir, chatID, referenceName),
			MimeHint: mimeHint,
		}, ToolExecutionResult{}, false
	}

	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessPolicySession(execCtx), filetools.ReadAccess, filePath)
	if err != nil {
		code := policy.FilePathInvalidCode
		if strings.Contains(err.Error(), "workspace_unavailable") {
			code = "workspace_unavailable"
		}
		return resolvedToolImageSource{}, policy.Error(code, err.Error(), nil), true
	}
	if access.Blocked {
		return resolvedToolImageSource{}, policy.Error(policy.FilePathBlockedCode, access.Reason, map[string]any{"filePath": access.Path}), true
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return resolvedToolImageSource{}, policy.Error(policy.DeviceBlockedCode, "device file is blocked", map[string]any{"filePath": access.Path}), true
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeReadApproval(execCtx, access) {
		return resolvedToolImageSource{}, fileAccessApprovalRequired(policy.ApprovalRequiredCode, policy.ApprovalMessage, access), true
	}
	return resolvedToolImageSource{Name: filepath.Base(access.Path), Path: access.Path}, ToolExecutionResult{}, false
}
