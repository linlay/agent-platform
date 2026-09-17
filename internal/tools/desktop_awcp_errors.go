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
	if (requestType != desktopAwcpSnapshotAction && requestType != desktopAwcpInvokeAction) || frame.Type != "awcp_preflight_rejected" {
		return details
	}
	var data struct {
		Stage            string `json:"stage"`
		ExecutionStarted *bool  `json:"executionStarted"`
		Reason           string `json:"reason"`
		Violations       []struct {
			InstancePath string `json:"instancePath"`
			Keyword      string `json:"keyword"`
			ExpectedType string `json:"expectedType"`
			ActualType   string `json:"actualType"`
		} `json:"violations"`
	}
	// Desktop sends host error details directly as AGW frame.data. Do not
	// reuse ordinary CDP's nested data.details metadata envelope here.
	if json.Unmarshal(frame.Data, &data) != nil || data.Stage != "desktop_preflight" || data.ExecutionStarted == nil || *data.ExecutionStarted {
		return details
	}
	switch data.Reason {
	case "input_schema_mismatch", "stale_snapshot", "action_not_found", "discovery_required", "page_changed":
	default:
		return details
	}
	details["stage"] = "desktop_preflight"
	details["executionStarted"] = false
	details["reason"] = data.Reason
	violations := make([]map[string]any, 0, 8)
	for _, violation := range data.Violations {
		if len(violations) == 8 {
			break
		}
		if len(violation.InstancePath) > 256 || len(violation.Keyword) > 64 {
			continue
		}
		item := map[string]any{"instancePath": violation.InstancePath, "keyword": violation.Keyword}
		if violation.Keyword == "type" {
			if awcpViolationType(violation.ExpectedType, false) {
				item["expectedType"] = violation.ExpectedType
			}
			if awcpViolationType(violation.ActualType, true) {
				item["actualType"] = violation.ActualType
			}
		}
		violations = append(violations, item)
	}
	if len(violations) > 0 {
		details["violations"] = violations
	}
	return details
}

func awcpViolationType(value string, allowMissing bool) bool {
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	case "missing":
		return allowMissing
	default:
		return false
	}
}
