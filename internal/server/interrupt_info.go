package server

import (
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func interruptRequestWithCause(req api.InterruptRequest, source string, reason string, detail string) api.InterruptRequest {
	req.InterruptSource = strings.TrimSpace(source)
	req.InterruptReason = strings.TrimSpace(reason)
	req.InterruptDetail = strings.TrimSpace(detail)
	return req
}

func interruptRequestForQuery(req api.QueryRequest, source string, reason string, detail string) api.InterruptRequest {
	return interruptRequestWithCause(api.InterruptRequest{
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
		RunID:     req.RunID,
		AgentKey:  req.AgentKey,
	}, source, reason, detail)
}

func serverSetupInterruptRequest(req api.QueryRequest, reason string, detail string) api.InterruptRequest {
	return interruptRequestForQuery(req, contracts.InterruptSourceServerSetup, reason, detail)
}

func httpAPIUserInterruptRequest(req api.InterruptRequest) api.InterruptRequest {
	detail := firstNonBlank(req.Message, "interrupt requested by HTTP API")
	return interruptRequestWithCause(req, contracts.InterruptSourceHTTPAPI, contracts.InterruptReasonUserCancelled, detail)
}

func wsAPIUserInterruptRequest(req api.InterruptRequest) api.InterruptRequest {
	if strings.TrimSpace(req.InterruptSource) == contracts.InterruptSourceProxyWS &&
		strings.TrimSpace(req.InterruptReason) == contracts.InterruptReasonProxyInterrupt {
		if strings.TrimSpace(req.InterruptDetail) == "" {
			req.InterruptDetail = "proxy interrupt forwarded"
		}
		return req
	}
	detail := firstNonBlank(req.Message, "interrupt requested by websocket API")
	return interruptRequestWithCause(req, contracts.InterruptSourceWSAPI, contracts.InterruptReasonUserCancelled, detail)
}

func proxyWSInterruptRequest(req api.InterruptRequest) api.InterruptRequest {
	return interruptRequestWithCause(req, contracts.InterruptSourceProxyWS, contracts.InterruptReasonProxyInterrupt, "proxy interrupt forwarded")
}
