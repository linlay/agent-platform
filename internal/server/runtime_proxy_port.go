package server

import (
	"context"
	"encoding/json"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/proxy"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/ws"
)

// RuntimeProxyPort preserves the existing root Proxy wire drivers during R16.
// Admission, native execution and recovery are owned by Runtime.
type RuntimeProxyPort struct{ Server *Server }

func (p RuntimeProxyPort) Configure(def *catalog.AgentDefinition) *runtimetypes.RequestError {
	return p.Server.applyProxyRoutingConfig(def)
}
func (p RuntimeProxyPort) Models(key string) ([]queryinput.CoderModelOption, error, bool) {
	return p.Server.listACPCoderModelOptions(key)
}
func (p RuntimeProxyPort) Submit(req queryinput.SubmitRequest) (queryinput.SubmitResponse, *runtimetypes.RequestError, bool) {
	return p.Server.forwardProxySubmit(req)
}
func (p RuntimeProxyPort) Steer(req queryinput.SteerRequest) (queryinput.SteerResponse, *runtimetypes.RequestError, bool) {
	return p.Server.forwardProxySteer(req)
}
func (p RuntimeProxyPort) Interrupt(req queryinput.InterruptRequest) (queryinput.InterruptResponse, *runtimetypes.RequestError, bool) {
	return p.Server.forwardProxyInterrupt(req)
}
func (p RuntimeProxyPort) AccessLevel(req queryinput.AccessLevelRequest) (queryinput.AccessLevelResponse, *runtimetypes.RequestError, bool) {
	return p.Server.forwardProxyAccessLevel(req)
}
func (p RuntimeProxyPort) Start(prepared runtimetypes.PreparedQuery, registered runtimetypes.RegisteredRun, bus *stream.RunEventBus, wait bool) error {
	input := proxyPreparedQuery(prepared)
	state := registeredQueryRun(registered)
	if wait {
		return p.Server.startPreparedProxyRunAndWait(input, state, bus)
	}
	p.Server.startPreparedProxyRun(input, state, bus)
	return nil
}
func (p RuntimeProxyPort) Execute(ctx context.Context, prepared runtimetypes.PreparedQuery, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	capture := &internalQueryCapture{hooks: InternalQueryHooks{OnRunStarted: hooks.OnRunStarted}}
	ctx = withInternalQueryCapture(ctx, capture)
	response := newQueryResponseBuffer()
	if prepared.Req.Stream != nil && !*prepared.Req.Stream {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/api/query", nil)
		p.Server.handleProxyQueryNonStream(response, request, proxyPreparedQuery(prepared))
	} else {
		p.Server.executePreparedProxyCompatibility(response, ctx, proxyPreparedQuery(prepared))
	}
	result := capture.result(response.status, response.body.String())
	out := runtimetypes.QueryResult{Completion: result.Completion, ErrorMessage: result.ErrorMessage}
	if value := capture.responseResult; value != nil {
		out.Content = value.AssistantText
		out.FullText = value.FullText
		out.Usage = value.Usage
		out.FinishReason = value.FinishReason
		out.ErrorPayload = value.ErrorPayload
	}
	out.ChatID = prepared.Req.ChatID
	out.RunID = prepared.Req.RunID
	if result.StatusCode != http.StatusOK {
		var body api.ApiResponse[any]
		if err := json.Unmarshal(response.body.Bytes(), &body); err == nil {
			return out, &runtimetypes.RequestError{Status: result.StatusCode, Message: body.Msg, Data: body.Data}
		}
		return out, &runtimetypes.RequestError{Status: result.StatusCode, Message: summarizeRuntimeQueryBody(result.Body)}
	}
	return out, nil
}
func (s *Server) RuntimeResourceTickets() proxy.TicketIssuer { return s.ticketService }
func proxyPreparedQuery(p runtimetypes.PreparedQuery) preparedQuery {
	out := preparedQuery{Req: queryRequestFromRuntime(p.Req), Summary: p.Summary, Created: p.Created, AgentDef: p.AgentDef, TeamSnapshot: p.TeamSnapshot, Session: p.Session, SystemInitLine: p.SystemInitLine, ResourceBaseURL: p.ResourceBaseURL, Release: p.Release, ContinueRun: p.ContinueRun, InitialSeq: p.InitialSeq, SyntheticBootstrap: p.SyntheticBootstrap}
	if p.Execution != nil {
		e := queryExecutionOptions(*p.Execution)
		out.Execution = &e
	}
	return out
}

func trustedQueryCommand(ctx context.Context, req api.QueryRequest) runtimetypes.QueryCommand {
	cmd := queryCommandFromAPI(req)
	cmd.Identity = buildAuthIdentity(PrincipalFromContext(ctx))
	if cmd.Identity != nil {
		cmd.Caller = runtimetypes.Caller{Subject: cmd.Identity.Subject, DeviceID: cmd.Identity.DeviceID, Scope: cmd.Identity.Scope}
	}
	_, cmd.TrustedGateway = ws.GatewayFromContext(ctx)
	return cmd
}
