package llm

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"agent-platform/internal/contracts"
)

const (
	desktopAwcpToolName      = "desktop_awcp"
	desktopCdpToolName       = "desktop_cdp"
	awcpMaxActions           = 128
	awcpMaxNameLength        = 128
	awcpMaxDescriptionLength = 2048
	awcpMaxSnapshotBytes     = 256 * 1024
	awcpMaxSchemaDepth       = 20
	awcpFailureFinalPrompt   = "The AWCP call failed and must not be retried. Do not call any tools or fall back to DOM interaction. Briefly explain the failure from the immediately preceding tool result and, when useful, tell the user what must change before trying again."
	awcpFailureFinalFallback = "The AWCP action failed and cannot be retried in this run. Review the preceding tool error and correct the page action or its arguments before trying again."
)

var awcpActionNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

type awcpActionConstraint struct {
	action      string
	inputSchema map[string]any
}

type awcpRunConstraint struct {
	targetID        string
	revision        string
	actions         []awcpActionConstraint
	staleRecoveries int
}

func (s *llmRunStream) observeDesktopToolResult(invocation *preparedToolInvocation, result contracts.ToolExecutionResult) {
	if s == nil || invocation == nil {
		return
	}
	switch strings.TrimSpace(invocation.toolName) {
	case desktopCdpToolName:
		s.observeDesktopCdpResult(invocation, result)
	case desktopAwcpToolName:
		s.observeDesktopAwcpResult(result)
	}
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

func (s *llmRunStream) observeDesktopCdpResult(invocation *preparedToolInvocation, result contracts.ToolExecutionResult) {
	if strings.TrimSpace(stringMapValue(invocation.args, "method")) != "Runtime.evaluate" || result.ExitCode != 0 || strings.TrimSpace(result.Error) != "" {
		return
	}
	targetID := strings.TrimSpace(stringMapValue(invocation.args, "targetId"))
	if targetID == "" {
		return
	}
	response := anyMap(result.Structured["response"])
	if ok, valid := response["ok"].(bool); !valid || !ok || strings.TrimSpace(stringMapValue(response, "method")) != "Runtime.evaluate" {
		return
	}
	cdpResult := anyMap(response["result"])
	remoteObject := anyMap(cdpResult["result"])
	value := anyMap(remoteObject["value"])
	if ok, valid := value["ok"].(bool); !valid || !ok {
		return
	}
	snapshot := anyMap(value["snapshot"])
	revision, actions, valid := validateAwcpSnapshot(snapshot)
	if !valid || len(actions) == 0 {
		return
	}
	s.applyAwcpConstraint(targetID, revision, actions)
}

func (s *llmRunStream) observeDesktopAwcpResult(result contracts.ToolExecutionResult) {
	response := anyMap(result.Structured["response"])
	if ok, valid := response["ok"].(bool); valid && ok && result.ExitCode == 0 && strings.TrimSpace(result.Error) == "" {
		return
	}
	errorCode := strings.TrimSpace(stringMapValue(anyMap(response["error"]), "code"))
	if errorCode == "stale_snapshot" && strings.TrimSpace(result.Error) == "" {
		s.awcpConstraint.staleRecoveries++
		s.clearAwcpConstraint()
		if s.awcpConstraint.staleRecoveries <= 1 {
			return
		}
	}
	s.queueAwcpFinalAnswer()
}

func (s *llmRunStream) queueAwcpFinalAnswer() {
	if s == nil || s.forcedFinalAnswer != "" {
		return
	}
	s.forcedFinalAnswer = awcpFailureFinalPrompt
	s.toolSpecs = nil
	s.toolChoice = "none"
}

func (s *llmRunStream) applyAwcpConstraint(targetID, revision string, actions []awcpActionConstraint) {
	if s == nil {
		return
	}
	for index := range s.toolSpecs {
		if strings.TrimSpace(s.toolSpecs[index].Function.Name) != desktopAwcpToolName {
			continue
		}
		branches := make([]any, 0, len(actions))
		for _, descriptor := range actions {
			branches = append(branches, map[string]any{
				"type":                 "object",
				"required":             []any{"revision", "action", "args"},
				"additionalProperties": false,
				"properties": map[string]any{
					"revision": map[string]any{"type": "string", "const": revision},
					"action":   map[string]any{"type": "string", "const": descriptor.action},
					"args":     cloneToolSchemaMap(descriptor.inputSchema),
				},
			})
		}
		s.toolSpecs[index].Function.Parameters = map[string]any{"oneOf": branches}
		s.awcpConstraint.targetID = targetID
		s.awcpConstraint.revision = revision
		s.awcpConstraint.actions = cloneAwcpActions(actions)
		return
	}
}

func (s *llmRunStream) clearAwcpConstraint() {
	if s == nil {
		return
	}
	for index := range s.toolSpecs {
		if strings.TrimSpace(s.toolSpecs[index].Function.Name) == desktopAwcpToolName {
			s.toolSpecs[index].Function.Parameters = s.desktopAwcpBaseParameters()
			break
		}
	}
	s.awcpConstraint.targetID = ""
	s.awcpConstraint.revision = ""
	s.awcpConstraint.actions = nil
}

func (s *llmRunStream) desktopAwcpBaseParameters() map[string]any {
	if s != nil && s.engine != nil && s.engine.tools != nil {
		definitions := mergeToolDefinitions(s.engine.tools.Definitions(), s.session.ModeToolDefinitions)
		for _, definition := range definitions {
			if normalizedToolDefinitionName(definition) == desktopAwcpToolName {
				return cloneToolSchemaMap(definition.Parameters)
			}
		}
	}
	return map[string]any{
		"type":                 "object",
		"required":             []any{"revision", "action", "args"},
		"additionalProperties": false,
		"properties": map[string]any{
			"revision": map[string]any{"type": "string", "minLength": float64(1), "maxLength": float64(128)},
			"action": map[string]any{
				"type":      "string",
				"minLength": float64(1),
				"maxLength": float64(128),
				"pattern":   `^[a-z0-9]+(?:-[a-z0-9]+)*(?:\.[a-z0-9]+(?:-[a-z0-9]+)*)*$`,
			},
			"args": map[string]any{"type": "object", "additionalProperties": true},
		},
	}
}

func cloneAwcpActions(actions []awcpActionConstraint) []awcpActionConstraint {
	cloned := make([]awcpActionConstraint, len(actions))
	for index, action := range actions {
		cloned[index] = awcpActionConstraint{action: action.action, inputSchema: cloneToolSchemaMap(action.inputSchema)}
	}
	return cloned
}

func cloneOpenAIToolSpecsForAwcpProfile(specs []openAIToolSpec, baseParameters map[string]any) []openAIToolSpec {
	cloned := make([]openAIToolSpec, len(specs))
	for index, spec := range specs {
		cloned[index] = spec
		cloned[index].Function.Parameters = cloneToolSchemaMap(spec.Function.Parameters)
		if strings.TrimSpace(spec.Function.Name) == desktopAwcpToolName {
			cloned[index].Function.Parameters = cloneToolSchemaMap(baseParameters)
		}
	}
	return cloned
}

func validateAwcpSnapshot(snapshot map[string]any) (string, []awcpActionConstraint, bool) {
	if !hasExactKeys(snapshot, "actions", "revision") {
		return "", nil, false
	}
	revision, ok := snapshot["revision"].(string)
	if !ok || revision == "" || utf16StringLength(revision) > awcpMaxNameLength {
		return "", nil, false
	}
	rawActions, ok := snapshot["actions"].([]any)
	if !ok || len(rawActions) > awcpMaxActions {
		return "", nil, false
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > awcpMaxSnapshotBytes {
		return "", nil, false
	}
	actions := make([]awcpActionConstraint, 0, len(rawActions))
	previous := ""
	for _, rawAction := range rawActions {
		descriptor := anyMap(rawAction)
		if !hasExactKeys(descriptor, "action", "description", "inputSchema") && !hasExactKeys(descriptor, "action", "description", "inputSchema", "outputSchema") {
			return "", nil, false
		}
		action, actionOK := descriptor["action"].(string)
		description, descriptionOK := descriptor["description"].(string)
		inputSchema, schemaOK := descriptor["inputSchema"].(map[string]any)
		if !actionOK || action == "" || utf16StringLength(action) > awcpMaxNameLength || !awcpActionNamePattern.MatchString(action) || action <= previous {
			return "", nil, false
		}
		if !descriptionOK || strings.TrimSpace(description) == "" || utf16StringLength(description) > awcpMaxDescriptionLength {
			return "", nil, false
		}
		if !schemaOK || inputSchema == nil || !validAwcpJSONTree(inputSchema, 0, awcpMaxSchemaDepth) {
			return "", nil, false
		}
		if outputSchema, present := descriptor["outputSchema"]; present {
			output, outputOK := outputSchema.(map[string]any)
			if !outputOK || output == nil || !validAwcpJSONTree(output, 0, awcpMaxSchemaDepth) {
				return "", nil, false
			}
		}
		previous = action
		actions = append(actions, awcpActionConstraint{action: action, inputSchema: cloneToolSchemaMap(inputSchema)})
	}
	return revision, actions, true
}

func validAwcpJSONTree(value any, depth, maximumDepth int) bool {
	if depth > maximumDepth {
		return false
	}
	switch typed := value.(type) {
	case nil, bool, string:
		return true
	case float64:
		return !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		parsed, err := typed.Float64()
		return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case []any:
		for _, item := range typed {
			if !validAwcpJSONTree(item, depth+1, maximumDepth) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range typed {
			if !validAwcpJSONTree(item, depth+1, maximumDepth) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func hasExactKeys(value map[string]any, expected ...string) bool {
	if len(value) != len(expected) {
		return false
	}
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sortedExpected := append([]string(nil), expected...)
	sort.Strings(sortedExpected)
	for index := range actual {
		if actual[index] != sortedExpected[index] {
			return false
		}
	}
	return true
}

func anyMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func stringMapValue(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func utf16StringLength(value string) int {
	return len(utf16.Encode([]rune(value)))
}
