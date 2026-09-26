package query

import (
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

func interruptRequestForQuery(req runtimetypes.QueryCommand, source string, reason string, detail string) queryinput.InterruptRequest {
	return interruptRequestWithCause(queryinput.InterruptRequest{
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
		RunID:     req.RunID,
		AgentKey:  req.AgentKey,
		TeamID:    req.TeamID,
	}, source, reason, detail)
}

func interruptRequestWithCause(req queryinput.InterruptRequest, source string, reason string, detail string) queryinput.InterruptRequest {
	req.InterruptSource = strings.TrimSpace(source)
	req.InterruptReason = strings.TrimSpace(reason)
	req.InterruptDetail = strings.TrimSpace(detail)
	return req
}

func httpAPIUserInterruptRequest(req queryinput.InterruptRequest) queryinput.InterruptRequest {
	detail := firstNonBlank(req.Message, "interrupt requested by HTTP API")
	return interruptRequestWithCause(req, contracts.InterruptSourceHTTPAPI, contracts.InterruptReasonUserCancelled, detail)
}

func serverSetupInterruptRequest(req runtimetypes.QueryCommand, reason string, detail string) queryinput.InterruptRequest {
	return interruptRequestForQuery(req, contracts.InterruptSourceServerSetup, reason, detail)
}
