package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/tools"
)

const awcpMaxRunCorrections = 8

func awcpMethod(toolName string, args map[string]any) string {
	if strings.TrimSpace(toolName) != desktopCdpToolName {
		return ""
	}
	method := strings.TrimSpace(stringMapValue(args, "method"))
	if method == desktopAwcpSnapshotMethod || method == desktopAwcpInvokeMethod {
		return method
	}
	return ""
}

// Recovery constrains AWCP only. Other tools retain their own authorization,
// approval and execution policy; page routing belongs to the skill.
func (s *llmRunStream) awcpRecoveryGate(toolName string, args map[string]any) error {
	method := awcpMethod(toolName, args)
	if method == "" {
		return nil
	}
	if s.awcpConstraint.stoppedReason != "" {
		return fmt.Errorf("%s", s.awcpStopNotice())
	}
	pending := s.awcpConstraint.pendingMethod
	if pending == "" {
		return nil
	}
	if method != pending {
		return fmt.Errorf("AWCP attempt is unresolved; only %s may continue this AWCP attempt. Other authorized tasks may continue, but do not substitute DOM or another transport for the failed operation", pending)
	}
	if pending == desktopAwcpInvokeMethod && s.awcpConstraint.pendingAction != "" {
		action, _, _ := tools.DesktopAwcpInvocation(args)
		if action != s.awcpConstraint.pendingAction {
			return fmt.Errorf("correct the pending AWCP Action %q; do not substitute another Action", s.awcpConstraint.pendingAction)
		}
	}
	return nil
}

func (s *llmRunStream) awcpInvocationRecoveryGate(invocation *preparedToolInvocation) error {
	// A prepared sibling is not a correction attempt. Binding validation
	// still rejects invocations invalidated by discovery or page changes.
	if s.awcpConstraint.stoppedReason == "" && s.awcpConstraint.pendingMethod != "" &&
		invocation.modelRunSeq == s.awcpConstraint.recoveryRunSeq &&
		awcpMethod(invocation.toolName, invocation.args) != "" {
		return nil
	}
	return s.awcpRecoveryGate(invocation.toolName, invocation.args)
}

func (s *llmRunStream) noteAwcpArgumentRejection(toolName string, args map[string]any) {
	method := awcpMethod(toolName, args)
	if method == "" || s.awcpConstraint.stoppedReason != "" {
		return
	}
	if s.awcpConstraint.pendingMethod != "" {
		if s.awcpConstraint.recoveryRunSeq == s.runLLMChatCompletionCount {
			return
		}
		// Only a new model response can be a correction attempt. Keep the
		// first rejected target instead of adopting a later invalid call.
		s.awcpConstraint.recoveryRunSeq = s.runLLMChatCompletionCount
		s.allowAwcpCorrection(&s.awcpConstraint.formatRecoveries, s.awcpConstraint.pendingMethod)
		return
	}
	if method == desktopAwcpSnapshotMethod || s.awcpConstraint.revision == "" {
		method = desktopAwcpSnapshotMethod
		s.clearAwcpConstraint()
	} else {
		// The input value may be invalid, but a unique discovered key is
		// sufficient to identify which Action must be corrected.
		s.awcpConstraint.pendingAction = s.discoveredAwcpActionKey(args)
	}
	s.awcpConstraint.recoveryRunSeq = s.runLLMChatCompletionCount
	s.allowAwcpCorrection(&s.awcpConstraint.formatRecoveries, method)
}

// Read only a complete top-level method token for recovery accounting. This
// never constructs invocation arguments from a malformed JSON document.
func (s *llmRunStream) noteAwcpMalformedArguments(toolName, raw string) bool {
	if strings.TrimSpace(toolName) != desktopCdpToolName {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		if key == "method" {
			var method string
			if json.Unmarshal(value, &method) == nil {
				s.noteAwcpArgumentRejection(toolName, map[string]any{"method": method})
				return awcpMethod(toolName, map[string]any{"method": method}) != ""
			}
			return false
		}
	}
	return false
}

func (s *llmRunStream) discoveredAwcpActionKey(args map[string]any) string {
	actions := anyMap(anyMap(args["params"])["action"])
	if len(actions) != 1 {
		return ""
	}
	for _, candidate := range s.awcpConstraint.actions {
		if _, exists := actions[candidate.action]; exists {
			return candidate.action
		}
	}
	return ""
}

func (s *llmRunStream) allowAwcpCorrection(counter *int, method string) {
	*counter++
	s.awcpConstraint.totalRecoveries++
	if *counter > 1 || s.awcpConstraint.totalRecoveries > awcpMaxRunCorrections {
		s.stopAwcp("correction_limit")
		return
	}
	s.awcpConstraint.pendingMethod = method
}

// A success generated before the error was returned cannot resolve it.
// Keep its result, but wait for a successful call from a later model response.
func (s *llmRunStream) resetAwcpAttemptAfterSuccess(runSeq int) {
	if s.awcpConstraint.stoppedReason != "" {
		return
	}
	if s.awcpConstraint.pendingMethod != "" && runSeq <= s.awcpConstraint.recoveryRunSeq {
		return
	}
	s.resetAwcpAttempt()
}

// Rediscovery during stale recovery does NOT reset the correction episode.
func (s *llmRunStream) resetAwcpAttempt() {
	s.awcpConstraint.formatRecoveries = 0
	s.awcpConstraint.recoveryRunSeq = 0
	s.awcpConstraint.inputRecoveries = 0
	s.awcpConstraint.staleRecoveries = 0
	s.awcpConstraint.pendingMethod = ""
	s.awcpConstraint.pendingAction = ""
}

func awcpPreflightReason(result contracts.ToolExecutionResult) string {
	if result.Error != "desktop_cdp_client_rejected" || result.ExitCode == 0 {
		return ""
	}
	details := anyMap(result.Structured["details"])
	if stringMapValue(details, "clientErrorType") != "awcp_preflight_rejected" ||
		details["executionStarted"] != false || stringMapValue(details, "stage") != "desktop_preflight" {
		return ""
	}
	return stringMapValue(details, "reason")
}

func awcpGateResult(err error) contracts.ToolExecutionResult {
	return contracts.ToolExecutionResult{
		Error: "awcp_recovery_required", ExitCode: -1, Output: err.Error(),
		Structured: map[string]any{"executionStarted": false, "stage": "platform_parse", "reason": "awcp_recovery_required"},
	}
}
