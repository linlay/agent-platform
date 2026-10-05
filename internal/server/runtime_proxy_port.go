package server

import (
	"context"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/proxy"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/ws"
)

// RuntimeProxyPort adapts routing and control protocols; both async and blocking
// Proxy calls use the same Runtime executor.
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
	return p.Server.proxyExecutor().Start(prepared, registered, bus, wait)
}
func (p RuntimeProxyPort) Execute(prepared runtimetypes.PreparedQuery, registered runtimetypes.RegisteredRun, bus *stream.RunEventBus) (runtimetypes.QueryResult, error) {
	return p.Server.proxyExecutor().ExecuteBlocking(prepared, registered, bus)
}
func (s *Server) RuntimeResourceTickets() proxy.TicketIssuer { return s.ticketService }
func trustedQueryCommand(ctx context.Context, req api.QueryRequest) runtimetypes.QueryCommand {
	cmd := queryCommandFromAPI(req)
	cmd.Identity = buildAuthIdentity(PrincipalFromContext(ctx))
	if cmd.Identity != nil {
		cmd.Caller = runtimetypes.Caller{Subject: cmd.Identity.Subject, DeviceID: cmd.Identity.DeviceID, Scope: cmd.Identity.Scope}
	}
	_, cmd.TrustedGateway = ws.GatewayFromContext(ctx)
	return cmd
}
