package server

import (
	"errors"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"

	"agent-platform/internal/agentconfig"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/hostshell"
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
	def, release, ok := acquireAgentRuntime(s.deps.Registry, agentKey)
	transferred := false
	defer func() {
		if !transferred {
			releaseQuery(release)
		}
	}()
	if !ok {
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusBadRequest, message: "agent not found"}
	}
	cwd, err := s.resolveTerminalWorkspace(def)
	if err != nil {
		return terminalpkg.OpenResult{}, err
	}
	launch, shellErr := hostshell.Resolve(s.deps.Config.Bash, hostshell.Options{
		GOOS: runtime.GOOS, Interactive: true, CWD: cwd,
		Env: append(os.Environ(), terminalEnvironment(def, cwd)...), TempDir: hostshell.TempDir(nil),
	})
	if shellErr != nil {
		return terminalpkg.OpenResult{}, &statusError{status: http.StatusServiceUnavailable, message: shellErr.Error()}
	}
	// Strip chat/identity values from the complete inherited environment, not
	// only definition overrides. Terminal is never a chat execution channel.
	filteredEnv := launch.Env[:0]
	for _, entry := range launch.Env {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, agentconfig.EnvChatDir) && !strings.EqualFold(name, agentconfig.EnvAccessToken) {
			filteredEnv = append(filteredEnv, entry)
		}
	}
	result, openErr := s.terminals.Open(terminalpkg.OpenRequest{
		OnExit:      release,
		OwnerKey:    ownerKey,
		AgentKey:    agentKey,
		TerminalKey: strings.TrimSpace(payload.TerminalKey),
		CWD:         cwd,
		Shell:       launch.Executable,
		Args:        launch.Args,
		Managed:     launch.GitBash,
		Cols:        payload.Cols,
		Rows:        payload.Rows,
		Env:         filteredEnv,
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
	transferred = !result.Reused
	return result, nil
}

func terminalEnvironment(def catalog.AgentDefinition, workspaceDir string) []string {
	env := agentconfig.Merge(
		runtimeAgentEnv(def.Runtime["env"]),
		agentconfig.HostEnvironment(def.RuntimeDir, workspaceDir, ""),
	)
	if len(def.ConnectorBinDirs) > 0 {
		if env == nil {
			env = map[string]string{}
		}
		current := env["PATH"]
		if current == "" {
			current = os.Getenv("PATH")
		}
		env["PATH"] = connector.PathValue(current, def.ConnectorBinDirs, string(os.PathListSeparator))
	}
	for key := range env {
		if strings.EqualFold(strings.TrimSpace(key), agentconfig.EnvChatDir) ||
			strings.EqualFold(strings.TrimSpace(key), agentconfig.EnvAccessToken) {
			delete(env, key)
		}
	}
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
	root := effectiveLocalWorkspaceRoot(def)
	if root == "" {
		return "", &statusError{status: http.StatusBadRequest, message: "workspace_unavailable: terminal requires a workspace"}
	}
	resolved, err := resolveHostWorkspaceRoot(root)
	if err != nil {
		return "", &statusError{status: http.StatusBadRequest, message: err.Error()}
	}
	if err := validateWorkspaceChatsSeparation(resolved, s.deps.Config.Paths.ChatsDir); err != nil {
		return "", &statusError{status: http.StatusBadRequest, message: err.Error()}
	}
	return resolved, nil
}

func resolveTerminalShell(configured string) string {
	return resolveTerminalShellForGOOS(configured, os.Getenv("SHELL"), runtime.GOOS)
}

func resolveTerminalShellForGOOS(configured string, envShell string, goos string) string {
	return hostshell.TerminalExecutable(configured, envShell, goos)
}
