package server

import (
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
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
	SideID          string
	ParentChatID    string
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
		if errors.Is(err, queryinput.ErrUnknownField) {
			const code = "unknown_field"
			return api.QueryRequest{}, &statusError{Status: http.StatusBadRequest, Code: code, Message: err.Error(), Data: map[string]any{"error": map[string]any{"code": code, "message": err.Error()}}}
		}
		message := "invalid request body"
		if strings.Contains(err.Error(), api.ReferenceSandboxPathRemovedMessage) {
			message = api.ReferenceSandboxPathRemovedMessage
		}
		return api.QueryRequest{}, &statusError{Status: http.StatusBadRequest, Message: message}
	}
	return req, nil
}

// Reference-only follow-ups are validated by the runtime.
