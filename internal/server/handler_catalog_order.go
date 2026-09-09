package server

import (
	"context"
	"net/http"
	"strings"
)

func (s *Server) catalogOrderUser(ctx context.Context) (string, error) {
	if principal := PrincipalFromContext(ctx); principal != nil && strings.TrimSpace(principal.Subject) != "" {
		return "user:" + strings.TrimSpace(principal.Subject), nil
	}
	if s.deps.Config.Auth.Enabled {
		return "", newAgentStatusError(http.StatusUnauthorized, "auth.required", "authenticated user is required")
	}
	return "local", nil
}
