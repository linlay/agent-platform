package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/catalog"
	terminalpkg "agent-platform/internal/terminal"
)

func (s *Server) openTerminalSession(payload terminalOpenPayload, ownerKey string) (terminalpkg.OpenResult, *statusError) {
	if s == nil || s.terminals == nil {
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusServiceUnavailable, message: "terminal manager is not configured"}
	}
	ownerKey = strings.TrimSpace(ownerKey)
	if ownerKey == "" {
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusForbidden, message: "terminal owner is required"}
	}
	agentKey := strings.TrimSpace(payload.AgentKey)
	if agentKey == "" {
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusBadRequest, message: "agentKey is required"}
	}
	if s.deps.Registry == nil {
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusServiceUnavailable, message: "agent registry is not configured"}
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}
	cwd, err := s.resolveTerminalWorkspace(def)
	if err != nil {
		return terminalpkg.OpenResult{}, err
	}
	result, openErr := s.terminals.Open(terminalpkg.OpenRequest{
		OwnerKey:    ownerKey,
		AgentKey:    agentKey,
		TerminalKey: strings.TrimSpace(payload.TerminalKey),
		ChatID:      strings.TrimSpace(payload.ChatID),
		CWD:         cwd,
		Shell:       resolveTerminalShell(s.deps.Config.Bash.ShellExecutable),
		Cols:        payload.Cols,
		Rows:        payload.Rows,
		Env:         terminalEnvironment(def),
	})
	if openErr != nil {
		if errors.Is(openErr, terminalpkg.ErrUnsupported) {
			return terminalpkg.OpenResult{}, &statusError{status: http.StatusNotImplemented, message: "terminal is unsupported on this platform"}
		}
		if errors.Is(openErr, terminalpkg.ErrSessionConflict) {
			return terminalpkg.OpenResult{}, &statusError{status: http.StatusConflict, message: openErr.Error()}
		}
		if errors.Is(openErr, terminalpkg.ErrInvalidKey) {
			return terminalpkg.OpenResult{}, &statusError{status: http.StatusBadRequest, message: openErr.Error()}
		}
		if errors.Is(openErr, terminalpkg.ErrSessionLimit) {
			return terminalpkg.OpenResult{}, &statusError{status: http.StatusTooManyRequests, message: openErr.Error()}
		}
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusInternalServerError, message: openErr.Error()}
	}
	return result, nil
}

func terminalEnvironment(def catalog.AgentDefinition) []string {
	env := agentconfig.Merge(
		agentconfig.Environment(def.AgentDir),
		runtimeAgentEnv(def.Runtime["env"]),
	)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys)+2)
	for _, key := range keys {
		entries = append(entries, key+"="+env[key])
	}
	return append(entries, "TERM=xterm-256color", "COLORTERM=truecolor")
}

func (s *Server) resolveTerminalWorkspace(def catalog.AgentDefinition) (string, *statusError) {
	root := strings.TrimSpace(def.Workspace.Root)
	if root == "" || strings.EqualFold(root, catalog.AgentWorkspaceRootChat) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", &statusError{status: http.StatusInternalServerError, message: err.Error()}
		}
		root = cwd
	}
	if !filepath.IsAbs(root) {
		return "", &statusError{status: http.StatusBadRequest, message: "agent workspace must be absolute, @chat, or empty"}
	}
	dir, err := validatedWorkspaceDir(root)
	if err != nil {
		if statusErr, ok := err.(agentStatusError); ok {
			return "", &statusError{status: statusErr.status, message: statusErr.message}
		}
		return "", &statusError{status: http.StatusBadRequest, message: err.Error()}
	}
	return dir, nil
}

func resolveTerminalShell(configured string) string {
	return resolveTerminalShellForGOOS(configured, os.Getenv("SHELL"), runtime.GOOS)
}

func resolveTerminalShellForGOOS(configured string, envShell string, goos string) string {
	if shell := strings.TrimSpace(configured); shell != "" {
		return shell
	}
	if goos == "windows" {
		return "powershell.exe"
	}
	if shell := strings.TrimSpace(envShell); shell != "" {
		return shell
	}
	return "/bin/bash"
}
