package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "agent-platform/internal/contracts"
)

func (t *RuntimeToolExecutor) invokeArtifactPublish(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	artifacts, _ := args["artifacts"]
	published := make([]map[string]any, 0)
	if execCtx != nil {
		log.Printf("[artifact-publish] chatsDir=%s chatID=%s runID=%s artifacts=%v",
			t.cfg.Paths.ChatsDir, execCtx.Session.ChatID, execCtx.Session.RunID, artifacts)
		published = publishArtifacts(t.cfg.Paths.ChatsDir, execCtx.Session.ChatID, execCtx.Session.RunID, artifacts)
		log.Printf("[artifact-publish] published=%d items=%v", len(published), published)
		if t.artifactPusher != nil {
			for _, item := range published {
				t.artifactPusher.Push(execCtx.Session.ChatID, item)
			}
		}
	}
	payload := map[string]any{
		"status":             "published",
		"artifacts":          artifacts,
		"publishedArtifacts": published,
	}
	return structuredResult(payload), nil
}

// publishArtifacts resolves artifact paths from the sandbox /workspace to the
// local chat directory. Files already inside the current chat stay in place;
// files outside the chat but inside the server workspace are materialized into
// artifacts/<runId>/. Mirrors Java ArtifactPublishService.publish().
// coerceArtifactList 容错：部分 LLM（Qwen3.5 等）在 tool_call 里会把 array 参数
// 错误地序列化成 JSON 字符串（`"[{...}]"` 而不是 `[{...}]`）。这里先按原生数组
// 断言，失败则尝试 json.Unmarshal 字符串。单个对象也兼容（包装成单元素数组）。
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

func publishArtifacts(chatsRoot string, chatID string, runID string, raw any) []map[string]any {
	if strings.TrimSpace(chatsRoot) == "" || strings.TrimSpace(chatID) == "" {
		return nil
	}
	items := coerceArtifactList(raw)
	if len(items) == 0 {
		return nil
	}
	chatDir := filepath.Join(chatsRoot, chatID)
	published := make([]map[string]any, 0, len(items))
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
			continue
		}
		if rawPath == "" {
			continue
		}

		sourcePath := resolveArtifactSourcePath(rawPath, chatDir)
		if sourcePath == "" {
			log.Printf("[artifact-publish] skip: path resolve failed rawPath=%s chatDir=%s", rawPath, chatDir)
			continue
		}
		info, err := os.Stat(sourcePath)
		if err != nil || info.IsDir() {
			log.Printf("[artifact-publish] skip: file not found sourcePath=%s err=%v", sourcePath, err)
			continue
		}

		artifactID := AnyStringNode(mapped["artifactId"])
		if artifactID == "" {
			artifactID = fmt.Sprintf("artifact_%d_%d", time.Now().UnixMilli(), index)
		}

		filename := filepath.Base(sourcePath)

		targetPath := sourcePath
		relativePath, relErr := filepath.Rel(chatDir, targetPath)
		if relErr != nil || isPathOutsideBase(relativePath) {
			artifactsDir := filepath.Join(chatDir, "artifacts", runID)
			if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
				log.Printf("[artifact-publish] skip: create artifacts dir failed dir=%s err=%v", artifactsDir, err)
				continue
			}
			targetPath = deduplicateTargetPath(artifactsDir, filename, sourcePath)
			if !samePath(sourcePath, targetPath) {
				if copyErr := copyFile(sourcePath, targetPath); copyErr != nil {
					log.Printf("[artifact-publish] skip: copy failed source=%s target=%s err=%v", sourcePath, targetPath, copyErr)
					continue
				}
			}
			relativePath, relErr = filepath.Rel(chatDir, targetPath)
			if relErr != nil || isPathOutsideBase(relativePath) {
				log.Printf("[artifact-publish] skip: target escaped chat dir target=%s chatDir=%s", targetPath, chatDir)
				continue
			}
		}

		sha256hex := sha256Hex(targetPath)
		publishedFilename := filepath.Base(targetPath)
		relativePath = filepath.ToSlash(relativePath)
		published = append(published, map[string]any{
			"artifactId": artifactID,
			"name":       publishedFilename,
			"mimeType":   guessMimeType(publishedFilename),
			"sizeBytes":  info.Size(),
			"sha256":     sha256hex,
			"url":        artifactResourceURL(chatID, relativePath),
			"type":       defaultStringArg(mapped, "type", "file"),
		})
	}
	return published
}

func resolveArtifactSourcePath(rawPath string, chatDir string) string {
	const sandboxPrefix = "/workspace"
	normalized := strings.TrimSpace(rawPath)
	if strings.HasPrefix(normalized, sandboxPrefix) {
		suffix := strings.TrimPrefix(normalized, sandboxPrefix)
		suffix = strings.TrimLeft(suffix, "/")
		if suffix == "" {
			return chatDir
		}
		resolved := filepath.Clean(filepath.Join(chatDir, suffix))
		if !pathWithinBase(resolved, chatDir) {
			return ""
		}
		return resolved
	}
	if !filepath.IsAbs(normalized) {
		resolved := filepath.Clean(filepath.Join(chatDir, normalized))
		if !pathWithinBase(resolved, chatDir) {
			return ""
		}
		return resolved
	}
	resolved := filepath.Clean(normalized)
	if pathWithinBase(resolved, chatDir) {
		return resolved
	}
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return ""
	}
	if pathWithinBase(resolved, workspaceRoot) {
		return resolved
	}
	return ""
}

func deduplicateTargetPath(dir string, filename string, sourcePath string) string {
	baseName := filename
	ext := ""
	if dotIdx := strings.LastIndex(filename, "."); dotIdx > 0 {
		baseName = filename[:dotIdx]
		ext = filename[dotIdx:]
	}
	counter := 0
	for {
		candidateName := filename
		if counter > 0 {
			candidateName = fmt.Sprintf("%s-%d%s", baseName, counter, ext)
		}
		candidate := filepath.Join(dir, candidateName)
		info, err := os.Stat(candidate)
		if err != nil {
			return candidate
		}
		if info.Mode().IsRegular() && sameFileContent(sourcePath, candidate) {
			return candidate
		}
		counter++
	}
}

func sameFileContent(left string, right string) bool {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false
	}
	if leftInfo.Size() != rightInfo.Size() {
		return false
	}
	leftData, err := os.ReadFile(left)
	if err != nil {
		return false
	}
	rightData, err := os.ReadFile(right)
	if err != nil {
		return false
	}
	return string(leftData) == string(rightData)
}

func samePath(left string, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func artifactResourceURL(chatID string, relativePath string) string {
	file := filepath.ToSlash(filepath.Join(chatID, relativePath))
	return "/api/resource?file=" + url.QueryEscape(file)
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
