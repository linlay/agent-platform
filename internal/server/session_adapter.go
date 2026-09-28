package server

import (
	"context"
	"errors"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	session "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/view"
)

func (s *Server) sessionBuilder() *session.Builder { return s.deps.Sessions }
func sessionRequestError(err error) error {
	var e *runtimetypes.RequestError
	if errors.As(err, &e) {
		return &statusError{Status: e.Status, Code: e.Code, Message: e.Message, Data: e.Data}
	}
	return err
}
func (s *Server) buildCompactSession(ctx context.Context, req api.QueryRequest, summary chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
	cmd := queryCommandFromAPI(req)
	cmd.Identity = buildAuthIdentity(PrincipalFromContext(ctx))
	cmd.Role = defaultRole(req.Role)
	result, err := s.sessionBuilder().BuildQuerySession(ctx, cmd, summary, def, options)
	return result, sessionRequestError(err)
}

func mustUseSkillUnavailableStatus(err error) *statusError {
	e := session.MustUseSkillUnavailableStatus(err)
	return &statusError{Status: e.Status, Code: e.Code, Message: e.Message, Data: e.Data}
}

func buildAuthIdentity(principal *Principal) *contracts.AuthIdentity {
	if principal == nil {
		return nil
	}
	identity := &contracts.AuthIdentity{
		Subject:  principal.Subject,
		DeviceID: firstStringClaim(principal.Claims, "deviceId", "device_id"),
		Scope:    firstStringClaim(principal.Claims, "scope"),
	}
	if issuedAt := numericDate(principal.Claims["iat"]); issuedAt > 0 {
		identity.IssuedAt = time.Unix(issuedAt, 0).UTC().Format(time.RFC3339)
	}
	if expiresAt := numericDate(principal.Claims["exp"]); expiresAt > 0 {
		identity.ExpiresAt = time.Unix(expiresAt, 0).UTC().Format(time.RFC3339)
	}
	return identity
}

func (s *Server) viewService() *view.Service { return s.sessionBuilder().ViewService() }
func (s *Server) agentUsesContainerHub(def catalog.AgentDefinition) bool {
	return s.sessionBuilder().AgentUsesContainerHub(def)
}
