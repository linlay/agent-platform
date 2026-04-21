package tools

import (
	"strings"

	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/skills"
)

func (t *RuntimeToolExecutor) invokeSkillCandidateWrite(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.skillCandidates == nil {
		return ToolExecutionResult{Output: "skill candidate store not configured", Error: "skill_candidate_store_not_configured", ExitCode: -1}, nil
	}
	procedure := strings.TrimSpace(stringArg(args, "procedure"))
	if procedure == "" {
		return ToolExecutionResult{Output: "procedure must not be blank", Error: "missing_procedure", ExitCode: -1}, nil
	}
	input := skills.CandidateInput{
		SourceKind: "tool",
		Title:      strings.TrimSpace(stringArg(args, "title")),
		Summary:    strings.TrimSpace(stringArg(args, "summary")),
		Procedure:  procedure,
		Category:   strings.TrimSpace(stringArg(args, "category")),
		Confidence: floatArg(args, "confidence"),
		Tags:       normalizeMemoryTags(stringListArg(args, "tags")),
	}
	if execCtx != nil {
		input.AgentKey = strings.TrimSpace(execCtx.Session.AgentKey)
		input.ChatID = strings.TrimSpace(execCtx.Session.ChatID)
		input.RunID = strings.TrimSpace(execCtx.Session.RunID)
	}
	candidate, err := t.skillCandidates.Write(input)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{"candidate": candidate}), nil
}

func (t *RuntimeToolExecutor) invokeSkillCandidateList(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.skillCandidates == nil {
		return ToolExecutionResult{Output: "skill candidate store not configured", Error: "skill_candidate_store_not_configured", ExitCode: -1}, nil
	}
	agentKey := strings.TrimSpace(stringArg(args, "agentKey"))
	if agentKey == "" && execCtx != nil {
		agentKey = strings.TrimSpace(execCtx.Session.AgentKey)
	}
	limit := int(int64Arg(args, "limit"))
	items, err := t.skillCandidates.List(agentKey, limit)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	return structuredResult(map[string]any{"count": len(items), "results": items}), nil
}
