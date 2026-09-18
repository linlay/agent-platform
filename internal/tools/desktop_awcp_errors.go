package tools

import (
	"encoding/json"

	"agent-platform/internal/contracts"
)

// Ordinary CDP keeps its existing error projection. Execution evidence is only
// accepted from the dedicated Desktop AWCP preflight error frame, never from a
// page response or arbitrary error.details fields.
func desktopAwcpRejectionDetails(requestType string, frame contracts.ClientResponseFrame) map[string]any {
	details := desktopClientRejectionDetails(frame)
	if (requestType != desktopAwcpManualAction && requestType != desktopAwcpInvokeAction) || frame.Type != "awcp_preflight_rejected" {
		return details
	}
	var data struct {
		Stage            string `json:"stage"`
		ExecutionStarted *bool  `json:"executionStarted"`
		Reason           string `json:"reason"`
	}
	// Desktop sends host error details directly as AGW frame.data. Do not
	// reuse ordinary CDP's nested data.details metadata envelope here.
	if json.Unmarshal(frame.Data, &data) != nil || data.Stage != "desktop_preflight" || data.ExecutionStarted == nil || *data.ExecutionStarted {
		return details
	}
	switch data.Reason {
	case "stale_revision", "action_not_found", "manual_required", "page_changed":
	default:
		return details
	}
	details["stage"] = "desktop_preflight"
	details["executionStarted"] = false
	details["reason"] = data.Reason
	return details
}
