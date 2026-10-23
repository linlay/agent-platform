package resultsubmit

import (
	"context"
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
)

const (
	CodeInvalid          = "result_invalid"
	CodeAlreadySubmitted = "result_already_submitted"
)

// ToolHandler checks a result_submit call. Outside /api/execute it checks only
// the status/output/reason shape; with a ResultContract it also checks the
// output against the caller's format and hands the accepted submission to the
// Runtime. The output is the caller's arbitrary structure: strings in it,
// @chat paths included, are not interpreted.
type ToolHandler struct{}

func NewToolHandler() *ToolHandler { return &ToolHandler{} }

func (*ToolHandler) ToolNames() []string { return []string{ToolName} }

func (*ToolHandler) Invoke(_ context.Context, _ string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	var session contracts.QuerySession
	if execCtx != nil {
		session = execCtx.Session
	}
	submission, err := parseSubmission(args)
	if err != nil {
		return failure(CodeInvalid, err.Error()), nil
	}
	contract := session.ResultContract
	if contract == nil {
		return accepted(submission), nil
	}
	if contract.Submission() != nil {
		return failure(CodeAlreadySubmitted, "a result was already accepted for this Run"), nil
	}
	if submission.Status == contracts.ResultStatusSuccess {
		if err := validateOutput(contract.OutputSchema, submission.Output); err != nil {
			return failure(CodeInvalid, err.Error()), nil
		}
	}
	if !contract.Accept(submission) {
		return failure(CodeAlreadySubmitted, "a result was already accepted for this Run"), nil
	}
	return accepted(submission), nil
}

func parseSubmission(args map[string]any) (contracts.ResultSubmission, error) {
	for key := range args {
		if key != "status" && key != "output" && key != "reason" {
			return contracts.ResultSubmission{}, fmt.Errorf("unknown field %s; result_submit takes status, output and reason", key)
		}
	}
	status, _ := args["status"].(string)
	_, hasOutput := args["output"]
	_, hasReason := args["reason"]
	switch status {
	case contracts.ResultStatusSuccess:
		if hasReason {
			return contracts.ResultSubmission{}, fmt.Errorf("reason must be omitted when status is success")
		}
		output, ok := args["output"].(map[string]any)
		if !ok {
			return contracts.ResultSubmission{}, fmt.Errorf("output is required as an object when status is success")
		}
		return contracts.ResultSubmission{Status: status, Output: output}, nil
	case contracts.ResultStatusFailure:
		if hasOutput {
			return contracts.ResultSubmission{}, fmt.Errorf("output must be omitted when status is failure")
		}
		reason, _ := args["reason"].(string)
		if strings.TrimSpace(reason) == "" {
			return contracts.ResultSubmission{}, fmt.Errorf("reason is required as a non-empty string when status is failure")
		}
		return contracts.ResultSubmission{Status: status, Reason: reason}, nil
	default:
		return contracts.ResultSubmission{}, fmt.Errorf("status must be success or failure")
	}
}

func validateOutput(schema map[string]any, output map[string]any) error {
	resolved, err := CompileOutputSchema(schema)
	if err != nil {
		return err
	}
	if err := resolved.Validate(jsonValue(output)); err != nil {
		return fmt.Errorf("output does not match the required format: %v", err)
	}
	return nil
}

func accepted(submission contracts.ResultSubmission) contracts.ToolExecutionResult {
	data := map[string]any{"accepted": true, "status": submission.Status}
	return contracts.ToolExecutionResult{Structured: data, Output: contracts.CompactToolModelOutput(data, "")}
}

func failure(code, message string) contracts.ToolExecutionResult {
	data := map[string]any{"error": code, "message": message}
	return contracts.ToolExecutionResult{Structured: data, Output: contracts.CompactToolModelOutput(data, ""), Error: code, ExitCode: -1}
}
