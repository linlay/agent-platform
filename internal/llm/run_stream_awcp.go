package llm

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/awcp"
	"agent-platform/internal/contracts"
	"agent-platform/internal/tools"
)

const (
	desktopCdpToolName        = "desktop_cdp"
	desktopAwcpSnapshotMethod = "AWCP.getSnapshot"
	desktopAwcpInvokeMethod   = "AWCP.invoke"
	awcpMaxSchemaDepth        = awcp.MaxSchemaDepth
)

type awcpActionConstraint struct {
	action      string
	description string
	example     map[string]any
	inputSchema map[string]any
}

type awcpRunConstraint struct {
	stoppedReason    string
	generation       uint64
	revision         string
	actions          []awcpActionConstraint
	staleRecoveries  int
	formatRecoveries int
	recoveryRunSeq   int
	inputRecoveries  int
	totalRecoveries  int
	pendingMethod    string
	pendingAction    string
	failedInputs     map[[32]byte]struct{}
}

func (s *llmRunStream) observeDesktopToolResult(invocation *preparedToolInvocation, result contracts.ToolExecutionResult) {
	if s == nil || invocation == nil || s.awcpConstraint.stoppedReason != "" {
		return
	}
	if result.Error == "awcp_recovery_required" {
		return
	}
	if strings.TrimSpace(invocation.toolName) != desktopCdpToolName {
		return
	}
	switch strings.TrimSpace(stringMapValue(invocation.args, "method")) {
	case desktopAwcpSnapshotMethod:
		s.observeDesktopAwcpSnapshotResult(result, invocation.modelRunSeq)
	case desktopAwcpInvokeMethod:
		s.observeDesktopAwcpResult(result, invocation)
	}
}

func (s *llmRunStream) validateAwcpDesktopCdpCall(toolName string, args map[string]any) error {
	if s == nil || strings.TrimSpace(toolName) != desktopCdpToolName {
		return nil
	}
	if err := tools.ValidateDesktopAwcpCall(args); err != nil {
		return err
	}
	switch strings.TrimSpace(stringMapValue(args, "method")) {
	case desktopAwcpSnapshotMethod:
		return nil
	case desktopAwcpInvokeMethod:
		action, _, err := tools.DesktopAwcpInvocation(args)
		if err != nil {
			return err
		}
		binding := s.awcpRequest
		if binding == nil || binding.owner != &s.awcpConstraint || binding.revision == "" {
			return fmt.Errorf("AWCP.getSnapshot must succeed before the model request producing AWCP.invoke")
		}
		if binding.generation != s.awcpConstraint.generation || binding.revision != s.awcpConstraint.revision {
			return fmt.Errorf("AWCP.invoke request snapshot is no longer active; discover again")
		}
		for _, candidate := range binding.actions {
			if candidate.action == action {
				return nil
			}
		}
		return fmt.Errorf("AWCP.invoke action was not exposed to this model request")
	}
	return nil
}

func (s *llmRunStream) invalidateAwcpForDesktopCdpCall(toolName string, args map[string]any) {
	if s == nil || strings.TrimSpace(toolName) != desktopCdpToolName {
		return
	}
	method := strings.TrimSpace(stringMapValue(args, "method"))
	switch method {
	case "Page.navigate", "Page.reload", "Page.bringToFront", "Target.closeTarget":
		s.clearAwcpConstraint()
	}
}

func (s *llmRunStream) observeDesktopAwcpSnapshotResult(result contracts.ToolExecutionResult, runSeq int) {
	if result.ExitCode != 0 || strings.TrimSpace(result.Error) != "" {
		s.clearAwcpConstraint()
		// Only a trusted Desktop absence result permits ordinary UI routing.
		details := anyMap(result.Structured["details"])
		if result.Error == "desktop_cdp_client_rejected" && stringMapValue(details, "clientErrorType") == "awcp_protocol_unavailable" {
			s.resetAwcpAttempt()
			return
		}
		s.stopAwcp("snapshot_failed")
		return
	}
	response := anyMap(result.Structured["response"])
	if ok, valid := response["ok"].(bool); !valid || !ok || strings.TrimSpace(stringMapValue(response, "method")) != desktopAwcpSnapshotMethod {
		s.stopAwcp("invalid_snapshot")
		return
	}
	snapshot := map[string]any{"revision": response["revision"], "actions": response["actions"]}
	revision, actions, err := validateAwcpSnapshot(snapshot)
	if err != nil {
		s.stopAwcp("invalid_snapshot")
		return
	}
	s.applyAwcpConstraint(revision, actions)
	if s.awcpConstraint.staleRecoveries > 0 {
		s.awcpConstraint.pendingMethod = desktopAwcpInvokeMethod
	} else {
		s.resetAwcpAttemptAfterSuccess(runSeq)
	}
}

func (s *llmRunStream) observeDesktopAwcpResult(result contracts.ToolExecutionResult, invocation *preparedToolInvocation) {
	response := anyMap(result.Structured["response"])
	if ok, valid := response["ok"].(bool); valid && ok && result.ExitCode == 0 && strings.TrimSpace(result.Error) == "" {
		s.resetAwcpAttemptAfterSuccess(invocation.modelRunSeq)
		return
	}
	reason := awcpPreflightReason(result)
	if reason == "input_schema_mismatch" {
		if fingerprint, ok := awcpInputFingerprint(invocation.awcpBinding, invocation.args); ok {
			if s.awcpConstraint.failedInputs == nil {
				s.awcpConstraint.failedInputs = make(map[[32]byte]struct{})
			}
			s.awcpConstraint.failedInputs[fingerprint] = struct{}{}
		}
	}
	if reason == "stale_snapshot" || reason == "input_schema_mismatch" {
		if s.awcpConstraint.pendingMethod != "" && invocation.modelRunSeq <= s.awcpConstraint.recoveryRunSeq {
			if reason == "stale_snapshot" && invocation.modelRunSeq == s.awcpConstraint.recoveryRunSeq {
				s.clearAwcpConstraint()
				s.awcpConstraint.pendingMethod = desktopAwcpSnapshotMethod
				if s.awcpConstraint.staleRecoveries == 0 {
					s.awcpConstraint.staleRecoveries = 1
				}
			}
			return
		}
		if s.awcpConstraint.pendingMethod == "" {
			s.awcpConstraint.pendingAction, _, _ = tools.DesktopAwcpInvocation(invocation.args)
		}
		s.awcpConstraint.recoveryRunSeq = invocation.modelRunSeq
	}
	switch reason {
	case "stale_snapshot":
		s.clearAwcpConstraint()
		s.allowAwcpCorrection(&s.awcpConstraint.staleRecoveries, desktopAwcpSnapshotMethod)
		return
	case "input_schema_mismatch":
		s.allowAwcpCorrection(&s.awcpConstraint.inputRecoveries, desktopAwcpInvokeMethod)
		return
	}
	s.stopAwcp("invocation_failed")
}

func (s *llmRunStream) unchangedAwcpInputError(toolName string, args map[string]any) bool {
	if s == nil || awcpMethod(toolName, args) != desktopAwcpInvokeMethod || len(s.awcpConstraint.failedInputs) == 0 {
		return false
	}
	fingerprint, ok := awcpInputFingerprint(s.awcpRequest, args)
	if !ok {
		return false
	}
	_, repeated := s.awcpConstraint.failedInputs[fingerprint]
	return repeated
}

func awcpInputFingerprint(binding *awcpRequestBinding, args map[string]any) ([32]byte, bool) {
	if binding == nil || binding.revision == "" {
		return [32]byte{}, false
	}
	action, input, err := tools.DesktopAwcpInvocation(args)
	if err != nil {
		return [32]byte{}, false
	}
	encoded, err := json.Marshal(map[string]any{"revision": binding.revision, "action": action, "args": input})
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(encoded), true
}

func (s *llmRunStream) stopAwcp(reason string) {
	if s == nil || s.awcpConstraint.stoppedReason != "" {
		return
	}
	s.awcpConstraint.stoppedReason = reason
	s.clearAwcpConstraint()
}

func (s *llmRunStream) awcpStopNotice() string {
	if s.awcpConstraint.stoppedReason == "" {
		return ""
	}
	return "AWCP is stopped for this Run (" + s.awcpConstraint.stoppedReason + "). Do not invoke or rediscover AWCP. Other authorized tasks may continue; do not repeat the failed operation through DOM or another transport. Explain its actual execution status and any instancePath, expectedType and actualType from the error; do not infer missing discovery or invent schema requirements."
}

func (s *llmRunStream) applyAwcpConstraint(revision string, actions []awcpActionConstraint) {
	if s == nil {
		return
	}
	s.awcpConstraint.generation++
	s.awcpConstraint.revision = revision
	s.awcpConstraint.actions = cloneAwcpActions(actions)
}

func (s *llmRunStream) clearAwcpConstraint() {
	if s == nil {
		return
	}
	s.awcpConstraint.generation++
	s.awcpConstraint.revision = ""
	s.awcpConstraint.actions = nil
}

func cloneAwcpActions(actions []awcpActionConstraint) []awcpActionConstraint {
	out := append([]awcpActionConstraint(nil), actions...)
	for index := range out {
		out[index].example = cloneToolSchemaMap(out[index].example)
		out[index].inputSchema = cloneToolSchemaMap(out[index].inputSchema)
	}
	return out
}

func validateAwcpSnapshot(snapshot map[string]any) (string, []awcpActionConstraint, error) {
	revision, descriptors, err := awcp.ParseSnapshot(snapshot)
	if err != nil {
		return "", nil, err
	}
	actions := make([]awcpActionConstraint, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if _, err := relocateAwcpInputSchema(descriptor.InputSchema, "#"); err != nil {
			return "", nil, fmt.Errorf("AWCP Action %s inputSchema: %w", descriptor.Name, err)
		}
		actions = append(actions, awcpActionConstraint{
			action: descriptor.Name, description: descriptor.Description,
			example: cloneToolSchemaMap(descriptor.Example), inputSchema: cloneToolSchemaMap(descriptor.InputSchema),
		})
	}
	return revision, actions, nil
}

func anyMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func stringMapValue(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func (s *llmRunStream) validateAwcpInvocationBinding(invocation *preparedToolInvocation) error {
	if awcpMethod(invocation.toolName, invocation.args) != desktopAwcpInvokeMethod {
		return nil
	}
	binding := invocation.awcpBinding
	if binding == nil || binding.owner != &s.awcpConstraint || binding.revision == "" || binding.generation != s.awcpConstraint.generation || binding.revision != s.awcpConstraint.revision {
		return fmt.Errorf("AWCP invocation snapshot expired or was never bound to its model request")
	}
	return nil
}

func (s *llmRunStream) bindAwcpExecutionContext(invocation *preparedToolInvocation, execCtx *contracts.ExecutionContext) {
	execCtx.DesktopAwcpRevision = ""
	if invocation.awcpBinding != nil && s.validateAwcpInvocationBinding(invocation) == nil {
		execCtx.DesktopAwcpRevision = invocation.awcpBinding.revision
	}
}
