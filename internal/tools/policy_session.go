package tools

import (
	"agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
)

func (t *RuntimeToolExecutor) policySession(ctx *ExecutionContext) QuerySession {
	s := accessPolicySession(ctx)
	if t.cfg.Paths.ChatsDir != "" {
		s.RuntimeContext.LocalPaths.ChatsDir = t.cfg.Paths.ChatsDir
	}
	s.ProtectedPaths = append(append([]string(nil), s.ProtectedPaths...), accesspolicy.PlatformProtectedPaths(t.cfg)...)
	return s
}
