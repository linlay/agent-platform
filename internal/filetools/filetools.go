package filetools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
)

type AccessMode string

const (
	ReadAccess  AccessMode = "read"
	WriteAccess AccessMode = "write"
)

type ResolvedPath struct {
	Raw  string
	Path string
	Root string
}

type WritePlan struct {
	FilePath    string
	Root        string
	Content     []byte
	Description string
	Fingerprint string
	RuleKey     string
	CommandText string
}

func ResolvePath(cfg config.FileToolsConfig, mode AccessMode, rawPath string) (ResolvedPath, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return ResolvedPath{}, fmt.Errorf("file_path is required")
	}
	candidate := expandHome(rawPath)
	if !filepath.IsAbs(candidate) {
		workingDir := cfg.WorkingDirectory
		if strings.TrimSpace(workingDir) == "" {
			workingDir = "."
		}
		candidate = filepath.Join(expandHome(workingDir), candidate)
	}
	candidate = filepath.Clean(candidate)
	realCandidate, err := evaluatePath(candidate)
	if err != nil {
		return ResolvedPath{}, err
	}
	roots := cfg.AllowedReadPaths
	if mode == WriteAccess {
		roots = cfg.AllowedWritePaths
	}
	root, ok := firstAllowedRoot(cfg.WorkingDirectory, roots, realCandidate)
	if !ok {
		return ResolvedPath{}, fmt.Errorf("path not allowed: %s", rawPath)
	}
	return ResolvedPath{
		Raw:  rawPath,
		Path: realCandidate,
		Root: root,
	}, nil
}

func BuildWritePlan(cfg config.FileToolsConfig, args map[string]any) (WritePlan, error) {
	resolved, err := ResolvePath(cfg, WriteAccess, AnyStringNode(args["file_path"]))
	if err != nil {
		return WritePlan{}, err
	}
	content := AnyStringNode(args["content"])
	description := strings.TrimSpace(AnyStringNode(args["description"]))
	if description == "" {
		return WritePlan{}, fmt.Errorf("description is required for write")
	}
	if len([]byte(content)) > maxPositive(cfg.MaxWriteBytes, 1<<20) {
		return WritePlan{}, fmt.Errorf("content exceeds max write bytes")
	}
	contentBytes := []byte(content)
	sum := sha256.Sum256([]byte(resolved.Path + "\x00" + hex.EncodeToString(sha256Bytes(contentBytes))))
	fingerprint := hex.EncodeToString(sum[:])
	rootHash := sha256.Sum256([]byte(resolved.Root))
	ruleKey := "file-write::" + hex.EncodeToString(rootHash[:8])
	return WritePlan{
		FilePath:    resolved.Path,
		Root:        resolved.Root,
		Content:     contentBytes,
		Description: description,
		Fingerprint: fingerprint,
		RuleKey:     ruleKey,
		CommandText: fmt.Sprintf("write %s (%d bytes)", resolved.Path, len(contentBytes)),
	}, nil
}

func ConsumeWriteApproval(execCtx *ExecutionContext, plan WritePlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileWriteRuleApprovals != nil && execCtx.FileWriteRuleApprovals[plan.RuleKey] {
		return true
	}
	if strings.TrimSpace(plan.Fingerprint) == "" || len(execCtx.FileWriteApprovals) == 0 {
		return false
	}
	remaining := execCtx.FileWriteApprovals[plan.Fingerprint]
	if remaining <= 0 {
		return false
	}
	if remaining == 1 {
		delete(execCtx.FileWriteApprovals, plan.Fingerprint)
		return true
	}
	execCtx.FileWriteApprovals[plan.Fingerprint] = remaining - 1
	return true
}

func RegisterExactWriteApproval(execCtx *ExecutionContext, fingerprint string) {
	if execCtx == nil || strings.TrimSpace(fingerprint) == "" {
		return
	}
	if execCtx.FileWriteApprovals == nil {
		execCtx.FileWriteApprovals = map[string]int{}
	}
	execCtx.FileWriteApprovals[fingerprint]++
}

func RegisterRuleWriteApproval(execCtx *ExecutionContext, ruleKey string) {
	if execCtx == nil || strings.TrimSpace(ruleKey) == "" {
		return
	}
	if execCtx.FileWriteRuleApprovals == nil {
		execCtx.FileWriteRuleApprovals = map[string]bool{}
	}
	execCtx.FileWriteRuleApprovals[ruleKey] = true
}

func HasWriteApproval(execCtx *ExecutionContext, plan WritePlan) bool {
	if execCtx == nil {
		return false
	}
	if execCtx.FileWriteRuleApprovals != nil && execCtx.FileWriteRuleApprovals[plan.RuleKey] {
		return true
	}
	return execCtx.FileWriteApprovals != nil && execCtx.FileWriteApprovals[plan.Fingerprint] > 0
}

func evaluatePath(path string) (string, error) {
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(evaluated), nil
	}
	existing := path
	missing := []string{}
	for {
		if existing == "." || existing == string(filepath.Separator) || existing == "" {
			break
		}
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	evaluatedParent, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return filepath.Clean(filepath.Join(append([]string{evaluatedParent}, missing...)...)), nil
}

func firstAllowedRoot(workingDir string, roots []string, path string) (string, bool) {
	for _, root := range roots {
		resolvedRoot := strings.TrimSpace(root)
		if resolvedRoot == "" {
			continue
		}
		resolvedRoot = expandHome(resolvedRoot)
		if !filepath.IsAbs(resolvedRoot) {
			base := workingDir
			if strings.TrimSpace(base) == "" {
				base = "."
			}
			resolvedRoot = filepath.Join(expandHome(base), resolvedRoot)
		}
		evaluatedRoot, err := evaluatePath(filepath.Clean(resolvedRoot))
		if err != nil {
			continue
		}
		if path == evaluatedRoot || strings.HasPrefix(path, evaluatedRoot+string(os.PathSeparator)) {
			return evaluatedRoot, true
		}
	}
	return "", false
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func sha256Bytes(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

func maxPositive(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
