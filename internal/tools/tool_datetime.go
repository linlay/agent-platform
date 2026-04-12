package tools

import "time"

func (t *RuntimeToolExecutor) invokeDateTime(args map[string]any) ToolExecutionResult {
	payload, err := buildDateTimePayload(args, time.Now())
	if err != nil {
		return ToolExecutionResult{Output: err.Error(), Error: "invalid_datetime_arguments", ExitCode: -1}
	}
	return structuredResult(payload)
}
