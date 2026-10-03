package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type preparedQuery struct {
	Req                api.QueryRequest
	Summary            chat.Summary
	Created            bool
	AgentDef           catalog.AgentDefinition
	TeamSnapshot       *catalog.TeamSnapshot
	Session            contracts.QuerySession
	SystemInitLine     *chat.QueryLineSystem
	ResourceBaseURL    string
	Release            queryReleaseFunc
	ContinueRun        bool
	InitialSeq         int64
	SyntheticBootstrap *stream.SyntheticQuery
	Execution          *queryExecutionOptions
}

type queryExecutionOptions struct {
	StepLineStore   chat.StepLineStore
	CompletionStore chat.Store
	HiddenRun       bool
	QueryMetadata   map[string]any
	BTWID           string
	ParentChatID    string
}

func (s *Server) resolvedQueryExecution(prepared preparedQuery) queryExecutionOptions {
	if prepared.Execution == nil {
		return queryExecutionOptions{
			StepLineStore:   s.deps.Chats,
			CompletionStore: s.deps.Chats,
		}
	}
	resolved := *prepared.Execution
	if resolved.StepLineStore == nil {
		resolved.StepLineStore = s.deps.Chats
	}
	return resolved
}

type statusError = runtimetypes.RequestError

type queryReleaseFunc = func()

func releaseQuery(release queryReleaseFunc) {
	if release != nil {
		release()
	}
}

func decodeQueryRequest(r *http.Request) (api.QueryRequest, error) {
	var req api.QueryRequest
	if err := decodeJSON(r, &req); err != nil {
		if errors.Is(err, api.ErrRequiredSkillKeysRemoved) {
			const code = "required_skill_keys_removed"
			return api.QueryRequest{}, &statusError{
				Status:  http.StatusBadRequest,
				Code:    code,
				Message: api.RequiredSkillKeysRemovedMessage,
				Data: map[string]any{
					"error": map[string]any{"code": code, "message": api.RequiredSkillKeysRemovedMessage},
				},
			}
		}
		message := "invalid request body"
		if strings.Contains(err.Error(), api.ReferenceSandboxPathRemovedMessage) {
			message = api.ReferenceSandboxPathRemovedMessage
		}
		return api.QueryRequest{}, &statusError{Status: http.StatusBadRequest, Message: message}
	}
	return req, nil
}

func chatAgentMode(agentDef catalog.AgentDefinition, orchestratedTeam bool) string {
	if orchestratedTeam {
		return "TEAM"
	}
	return catalog.AgentModeForAPI(agentDef.Mode)
}

// Reference-only follow-ups are validated by the runtime.
