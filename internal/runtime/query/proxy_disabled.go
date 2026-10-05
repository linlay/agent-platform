package query

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type unavailableProxy struct{}

func (unavailableProxy) Configure(def *catalog.AgentDefinition) *runtimetypes.RequestError {
	if def != nil && session.IsProxyRoutedAgent(*def) {
		return &runtimetypes.RequestError{Status: 503, Message: "proxy protocol adapter unavailable"}
	}
	return nil
}
func (unavailableProxy) Models(string) ([]queryinput.CoderModelOption, error, bool) {
	return nil, nil, false
}
func (unavailableProxy) Start(runtimetypes.PreparedQuery, runtimetypes.RegisteredRun, *stream.RunEventBus, bool) error {
	return ErrNotConfigured
}
func (unavailableProxy) Execute(runtimetypes.PreparedQuery, runtimetypes.RegisteredRun, *stream.RunEventBus) (runtimetypes.QueryResult, error) {
	return runtimetypes.QueryResult{}, ErrNotConfigured
}
func (unavailableProxy) Submit(queryinput.SubmitRequest) (queryinput.SubmitResponse, *runtimetypes.RequestError, bool) {
	return queryinput.SubmitResponse{}, nil, false
}
func (unavailableProxy) Steer(queryinput.SteerRequest) (queryinput.SteerResponse, *runtimetypes.RequestError, bool) {
	return queryinput.SteerResponse{}, nil, false
}
func (unavailableProxy) Interrupt(queryinput.InterruptRequest) (queryinput.InterruptResponse, *runtimetypes.RequestError, bool) {
	return queryinput.InterruptResponse{}, nil, false
}
func (unavailableProxy) AccessLevel(queryinput.AccessLevelRequest) (queryinput.AccessLevelResponse, *runtimetypes.RequestError, bool) {
	return queryinput.AccessLevelResponse{}, nil, false
}
