package tools

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/rootpaths"
)

func (t *RuntimeToolExecutor) invokeArtifactPublish(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	artifacts, _ := args["artifacts"]
	result := artifactPublishResult{
		Status:             "error",
		Artifacts:          artifacts,
		PublishedArtifacts: make([]map[string]any, 0),
		FailedArtifacts:    make([]map[string]any, 0),
	}
	if execCtx != nil {
		log.Printf("[artifact-publish] chatsDir=%s chatID=%s runID=%s artifacts=%v",
			t.cfg.Paths.ChatsDir, execCtx.Session.ChatID, execCtx.Session.RunID, artifacts)
		result = publishArtifacts(
			t.cfg.Paths.ChatsDir,
			execCtx.Session.ChatID,
			execCtx.Session.RunID,
			execCtx.Session.WorkspaceRoot,
			artifacts,
		)
		if result.Status == "published" {
			publishedAt := time.Now().UnixMilli()
			manifestWriter, ok := t.chats.(chat.ArtifactManifestWriter)
			if !ok || manifestWriter == nil {
				result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure("", "artifact_manifest_failed", "artifact manifest store is unavailable"))
			} else if err := manifestWriter.AppendArtifactManifest(execCtx.Session.ChatID, execCtx.Session.RunID, publishedAt, result.PublishedArtifacts); err != nil {
				result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure("", "artifact_manifest_failed", "failed to persist artifact manifest: "+err.Error()))
			}
			result.refreshStatus()
		}
		log.Printf("[artifact-publish] status=%s published=%d failed=%d items=%v failures=%v",
			result.Status, len(result.PublishedArtifacts), len(result.FailedArtifacts), result.PublishedArtifacts, result.FailedArtifacts)
		if t.artifactPusher != nil && result.Status == "published" {
			for _, item := range result.PublishedArtifacts {
				t.artifactPusher.Push(execCtx.Session.ChatID, item)
			}
		}
	} else {
		result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure("", "invalid_artifacts", "artifact publishing requires execution context"))
	}
	result.refreshStatus()
	payload := result.payload()
	exitCode := 0
	if result.Status != "published" {
		exitCode = -1
	}
	toolResult := structuredResultWithExit(payload, exitCode)
	if exitCode != 0 {
		toolResult.Error = "artifact_publish_failed"
	}
	return toolResult, nil
}

// publishArtifacts resolves artifact paths from the semantic workspace or chat
// roots and materializes every published file under the current chat's
// artifacts/<runId>/ directory.
type artifactPublishResult struct {
	Status             string
	Artifacts          any
	PublishedArtifacts []map[string]any
	FailedArtifacts    []map[string]any
}

func (r *artifactPublishResult) refreshStatus() {
	switch {
	case len(r.FailedArtifacts) > 0:
		r.Status = "error"
	case len(r.PublishedArtifacts) > 0:
		r.Status = "published"
	default:
		r.Status = "error"
	}
}

func (r artifactPublishResult) payload() map[string]any {
	published := r.PublishedArtifacts
	publishedCount := len(r.PublishedArtifacts)
	if r.Status != "published" {
		published = []map[string]any{}
		publishedCount = 0
	}
	return map[string]any{
		"status":             r.Status,
		"artifacts":          r.Artifacts,
		"publishedArtifacts": published,
		"failedArtifacts":    r.FailedArtifacts,
		"publishedCount":     publishedCount,
		"failedCount":        len(r.FailedArtifacts),
	}
}

func artifactPublishFailure(path string, code string, message string) map[string]any {
	return map[string]any{
		"path":    path,
		"code":    code,
		"message": message,
	}
}

// coerceArtifactList 容错：部分 LLM（Qwen3.5 等）在 tool_call 里会把 array 参数
// 错误地序列化成 JSON 字符串（`"[{...}]"` 而不是 `[{...}]`）。这里先按原生数组
// 断言，失败则尝试 json.Unmarshal 字符串。单个对象会包装成单元素数组。
func coerceArtifactList(raw any) []any {
	switch v := raw.(type) {
	case []any:
		return v
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		var arr []any
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			return arr
		}
		var single map[string]any
		if err := json.Unmarshal([]byte(trimmed), &single); err == nil {
			return []any{single}
		}
		return nil
	case map[string]any:
		return []any{v}
	}
	return nil
}

func publishArtifacts(chatsRoot string, chatID string, runID string, workspaceRoot string, raw any) artifactPublishResult {
	result := artifactPublishResult{
		Status:             "error",
		Artifacts:          raw,
		PublishedArtifacts: make([]map[string]any, 0),
		FailedArtifacts:    make([]map[string]any, 0),
	}
	if strings.TrimSpace(chatsRoot) == "" || strings.TrimSpace(chatID) == "" {
		result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure("", "invalid_artifacts", "artifact publishing requires chats root and chat id"))
		return result
	}
	items := coerceArtifactList(raw)
	if len(items) == 0 {
		result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure("", "invalid_artifacts", "artifacts must contain at least one publishable item"))
		return result
	}
	chatDir := filepath.Join(chatsRoot, chatID)
	for index, item := range items {
		var rawPath string
		var mapped map[string]any
		switch v := item.(type) {
		case map[string]any:
			mapped = v
			rawPath = AnyStringNode(v["path"])
		case string:
			rawPath = strings.TrimSpace(v)
			mapped = map[string]any{"path": rawPath}
		default:
			result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure("", "invalid_artifacts", "artifact item must be an object with a path"))
			continue
		}
		if rawPath == "" {
			result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure(rawPath, "invalid_artifacts", "artifact path must not be empty"))
			continue
		}

		sourcePath, resolveCode, resolveMessage := resolveArtifactSourcePath(rawPath, workspaceRoot, chatDir)
		if sourcePath == "" {
			log.Printf("[artifact-publish] skip: path resolve failed rawPath=%s chatDir=%s", rawPath, chatDir)
			result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure(rawPath, resolveCode, resolveMessage))
			continue
		}
		info, err := os.Stat(sourcePath)
		if err != nil || info.IsDir() {
			log.Printf("[artifact-publish] skip: file not found sourcePath=%s err=%v", sourcePath, err)
			result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure(rawPath, "file_not_found", "artifact path does not exist or is not a regular file"))
			continue
		}

		artifactID := AnyStringNode(mapped["artifactId"])
		if artifactID == "" {
			artifactID = fmt.Sprintf("artifact_%d_%d", time.Now().UnixMilli(), index)
		}

		filename := filepath.Base(sourcePath)

		artifactsDir := filepath.Join(chatDir, "artifacts", runID)
		if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
			log.Printf("[artifact-publish] skip: create artifacts dir failed dir=%s err=%v", artifactsDir, err)
			result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure(rawPath, "artifact_dir_failed", "failed to create artifact directory: "+err.Error()))
			continue
		}
		targetPath := filepath.Join(artifactsDir, filename)
		if !samePath(sourcePath, targetPath) {
			if copyErr := copyFile(sourcePath, targetPath); copyErr != nil {
				log.Printf("[artifact-publish] skip: copy failed source=%s target=%s err=%v", sourcePath, targetPath, copyErr)
				result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure(rawPath, "copy_failed", "failed to copy artifact into chat directory: "+copyErr.Error()))
				continue
			}
		}
		relativePath, relErr := filepath.Rel(chatDir, targetPath)
		if relErr != nil || isPathOutsideBase(relativePath) {
			log.Printf("[artifact-publish] skip: target escaped chat dir target=%s chatDir=%s", targetPath, chatDir)
			result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure(rawPath, "copy_failed", "published artifact target escaped chat directory"))
			continue
		}

		sha256hex := sha256Hex(targetPath)
		publishedFilename := filepath.Base(targetPath)
		relativePath = filepath.ToSlash(relativePath)
		resourceURL, resourceErr := chat.BuildChatScopeRef(relativePath)
		if resourceErr != nil {
			result.FailedArtifacts = append(result.FailedArtifacts, artifactPublishFailure(rawPath, "resource_url_failed", "failed to create published artifact URL: "+resourceErr.Error()))
			continue
		}
		result.PublishedArtifacts = append(result.PublishedArtifacts, map[string]any{
			"artifactId": artifactID,
			"name":       publishedFilename,
			"mimeType":   guessMimeType(publishedFilename),
			"sizeBytes":  info.Size(),
			"sha256":     sha256hex,
			"url":        resourceURL,
			"type":       defaultStringArg(mapped, "type", "file"),
		})
	}
	result.refreshStatus()
	return result
}

func resolveArtifactSourcePath(rawPath string, workspaceRoot string, chatDir string) (string, string, string) {
	normalized := strings.TrimSpace(rawPath)
	lower := strings.ToLower(normalized)
	if strings.HasPrefix(lower, "file://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "", "path_not_allowed", "artifact path must be a local filesystem path"
	}
	if strings.HasPrefix(normalized, `\\`) || (len(normalized) >= 3 && normalized[1] == ':' && ((normalized[0] >= 'A' && normalized[0] <= 'Z') || (normalized[0] >= 'a' && normalized[0] <= 'z')) && (normalized[2] == '\\' || normalized[2] == '/')) {
		if !filepath.IsAbs(normalized) {
			return "", "path_not_allowed", "artifact path uses an absolute path syntax unsupported by this host"
		}
	}
	cleanedAbsolute := filepath.Clean(normalized)
	if filepath.IsAbs(normalized) && (cleanedAbsolute == "/tmp" || strings.HasPrefix(cleanedAbsolute, "/tmp"+string(os.PathSeparator))) {
		return cleanedAbsolute, "", ""
	}
	roots, err := rootpaths.New(workspaceRoot, filepath.Dir(chatDir), chatDir)
	if err != nil {
		return "", "path_not_allowed", err.Error()
	}
	if normalized == "/chat" || strings.HasPrefix(normalized, "/chat/") ||
		normalized == "@chat" || strings.HasPrefix(normalized, "@chat/") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(normalized, "/chat"), "@chat")
		suffix = strings.TrimLeft(suffix, "/")
		resolved := filepath.Clean(filepath.Join(chatDir, suffix))
		zone, candidate, err := roots.Classify(resolved)
		if err != nil || zone != rootpaths.ZoneCurrentChat {
			return "", "path_not_allowed", "artifact path must stay within the current chat"
		}
		return candidate.Host, "", ""
	}
	if normalized == "/workspace" || strings.HasPrefix(normalized, "/workspace/") ||
		normalized == "@workspace" || strings.HasPrefix(normalized, "@workspace/") {
		if strings.TrimSpace(workspaceRoot) == "" {
			return "", "workspace_unavailable", "artifact workspace path requires a workspace"
		}
		suffix := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(normalized, "/workspace"), "@workspace"), "/")
		resolved := filepath.Clean(filepath.Join(workspaceRoot, filepath.FromSlash(suffix)))
		candidate, err := roots.RequireWorkspacePath(resolved)
		if err != nil {
			if errors.Is(err, rootpaths.ErrPathCrossesChatRoot) {
				return "", "path_crosses_chat_root", err.Error()
			}
			return "", "path_not_allowed", "artifact path must stay within the current workspace"
		}
		if roots.ClassifyCanonical(candidate) != rootpaths.ZoneWorkspace {
			return "", "path_not_allowed", "artifact path must stay within the current workspace"
		}
		return candidate.Host, "", ""
	}
	if !filepath.IsAbs(normalized) {
		if strings.TrimSpace(workspaceRoot) == "" {
			return "", "workspace_unavailable", "relative artifact paths require a workspace"
		}
		resolved := filepath.Clean(filepath.Join(workspaceRoot, normalized))
		candidate, err := roots.RequireWorkspacePath(resolved)
		if err != nil {
			if errors.Is(err, rootpaths.ErrPathCrossesChatRoot) {
				return "", "path_crosses_chat_root", err.Error()
			}
			return "", "path_not_allowed", "artifact path must stay within the current workspace"
		}
		if roots.ClassifyCanonical(candidate) != rootpaths.ZoneWorkspace {
			return "", "path_not_allowed", "artifact path must stay within the current workspace"
		}
		return candidate.Host, "", ""
	}
	zone, candidate, err := roots.Classify(normalized)
	if err != nil {
		return "", "path_not_allowed", err.Error()
	}
	switch zone {
	case rootpaths.ZoneCurrentChat, rootpaths.ZoneWorkspace:
		return candidate.Host, "", ""
	case rootpaths.ZoneOtherChat:
		return "", "path_not_allowed", "artifact path belongs to another chat"
	default:
		return "", "path_not_allowed", "artifact path is outside the current workspace and chat"
	}
}

func samePath(left string, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func pathWithinBase(path string, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return !isPathOutsideBase(rel)
}

func isPathOutsideBase(rel string) bool {
	clean := filepath.Clean(rel)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator))
}

func sha256Hex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

func copyFile(src string, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func guessMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain"
	case ".html":
		return "text/html"
	case ".json":
		return "application/json"
	case ".zip":
		return "application/zip"
	case ".md":
		return "text/markdown"
	default:
		return "application/octet-stream"
	}
}
