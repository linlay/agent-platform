package kbase

import (
	"context"
	"log"
	"path/filepath"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

const editingIndexHookName = "kbase-index"

// AfterFileChange synchronously makes a successful KBASE editing write visible
// to retrieval. It is deliberately a Manager hook so it reuses the existing
// refresh coordinator, storage lock, generation rules, and watcher hash
// deduplication.
func (m *Manager) AfterFileChange(ctx context.Context, event contracts.FileChangeEvent) contracts.FileChangeHookResult {
	if m == nil || m.resolver == nil {
		return contracts.FileChangeHookResult{}
	}
	agentKey := strings.TrimSpace(event.AgentKey)
	if agentKey == "" {
		return contracts.FileChangeHookResult{}
	}
	spec, err := m.resolver.AgentSpec(agentKey)
	if err != nil || spec.Requirement != RequirementRequired {
		return contracts.FileChangeHookResult{}
	}

	relativePath, err := editingRelativeSourcePath(spec.Config.Source.Root, event.FilePath)
	if err != nil {
		return contracts.FileChangeHookResult{
			Name:    editingIndexHookName,
			Status:  "failed",
			Message: err.Error(),
		}
	}
	if !strings.EqualFold(filepath.Ext(relativePath), ".md") {
		return contracts.FileChangeHookResult{
			Name:     editingIndexHookName,
			Status:   "skipped",
			FilePath: relativePath,
			Reason:   "unsupported_extension",
		}
	}
	if !shouldIndexPath(
		relativePath,
		compileMatchers(spec.Config.Include),
		compileMatchers(append(DefaultExcludePatterns(), spec.Config.Exclude...)),
	) {
		logKBaseEditingHook(event, relativePath, "skipped", "")
		return contracts.FileChangeHookResult{
			Name:     editingIndexHookName,
			Status:   "skipped",
			FilePath: relativePath,
			Reason:   "excluded_by_kbase_config",
		}
	}

	result, refreshErr := m.Refresh(ctx, agentKey, RefreshOptions{
		Mode:  "editing",
		Scope: "delta",
		Paths: []string{relativePath},
	})
	data := refreshHookData(result)
	if refreshErr != nil {
		m.state.SetFailure(agentKey, refreshErr)
		logKBaseEditingHook(event, relativePath, "failed", result.Scope)
		return contracts.FileChangeHookResult{
			Name:     editingIndexHookName,
			Status:   "failed",
			FilePath: relativePath,
			Message:  refreshErr.Error(),
			Data:     data,
		}
	}
	logKBaseEditingHook(event, relativePath, "success", result.Scope)
	return contracts.FileChangeHookResult{
		Name:     editingIndexHookName,
		Status:   "success",
		FilePath: relativePath,
		Data:     data,
	}
}

func editingRelativeSourcePath(sourceRoot string, filePath string) (string, error) {
	root, err := pathutil.Canonicalize(sourceRoot)
	if err != nil {
		return "", err
	}
	target, err := pathutil.Canonicalize(filePath)
	if err != nil {
		return "", err
	}
	if !pathutil.WithinRoot(target, root) {
		return "", &PolicyError{Kind: ErrorInvalid, Message: "edited file is outside the KBASE source root"}
	}
	relativePath, err := filepath.Rel(root.Host, target.Host)
	if err != nil {
		return "", err
	}
	return normalizeIndexedPath(relativePath), nil
}

func refreshHookData(result RefreshResult) map[string]any {
	data := map[string]any{
		"scope":         result.Scope,
		"changedFiles":  result.ChangedFiles,
		"indexedChunks": result.IndexedChunks,
	}
	if result.Status != "" {
		data["status"] = result.Status
	}
	if result.CandidatePaths != 0 {
		data["candidatePaths"] = result.CandidatePaths
	}
	if result.ScannedFiles != 0 {
		data["scannedFiles"] = result.ScannedFiles
	}
	if result.NewFiles != 0 {
		data["newFiles"] = result.NewFiles
	}
	if result.ModifiedFiles != 0 {
		data["modifiedFiles"] = result.ModifiedFiles
	}
	if result.UnchangedFiles != 0 {
		data["unchangedFiles"] = result.UnchangedFiles
	}
	if result.EmbeddedChunks != 0 {
		data["embeddedChunks"] = result.EmbeddedChunks
	}
	if result.ReusedChunks != 0 {
		data["reusedChunks"] = result.ReusedChunks
	}
	if result.Error != "" {
		data["error"] = result.Error
	}
	return data
}

func logKBaseEditingHook(event contracts.FileChangeEvent, relativePath string, status string, scope string) {
	log.Printf(
		"[kbase][editing] index hook agent=%s chat=%s run=%s path=%s beforeSha=%s afterSha=%s scope=%s status=%s",
		strings.TrimSpace(event.AgentKey),
		strings.TrimSpace(event.ChatID),
		strings.TrimSpace(event.RunID),
		strings.TrimSpace(relativePath),
		strings.TrimSpace(event.PreviousContentSHA256),
		strings.TrimSpace(event.ContentSHA256),
		strings.TrimSpace(scope),
		strings.TrimSpace(status),
	)
}
