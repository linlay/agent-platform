package tools

import (
	. "agent-platform/internal/contracts"
	"agent-platform/internal/memory"
	"errors"
	"fmt"
)

func (t *RuntimeToolExecutor) memoryAllowed(execCtx *ExecutionContext) error {
	if t.memory == nil || !t.cfg.Memory.Enabled || execCtx == nil || !execCtx.Session.AgentHasMemoryConfig {
		return fmt.Errorf("personal memory is not enabled for this agent")
	}
	return nil
}

func memoryResult(value any, err error) (ToolExecutionResult, error) {
	if err == nil {
		switch v := value.(type) {
		case memory.Document:
			return structuredResult(map[string]any{"document": v}), nil
		case memory.SearchResult:
			return structuredResult(map[string]any{"matches": v.Matches, "nextBefore": v.NextBefore, "maxMatches": v.MaxMatches, "searchedDailyFiles": v.SearchedDailyFiles}), nil
		case map[string]any:
			return structuredResult(v), nil
		default:
			return structuredResult(map[string]any{"result": v}), nil
		}
	}
	code := "memory_error"
	if errors.Is(err, memory.ErrConflict) {
		code = "memory_conflict"
	}
	if errors.Is(err, memory.ErrInvalid) {
		code = "memory_invalid_document"
	}
	return ToolExecutionResult{Output: err.Error(), Error: code, ExitCode: -1}, nil
}

func (t *RuntimeToolExecutor) invokeMemoryRead(_ string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := t.memoryAllowed(execCtx); err != nil {
		return memoryResult(nil, err)
	}
	kind, date := stringArg(args, "kind"), stringArg(args, "date")
	if kind == "daily" && date == "" {
		date = t.memory.Today()
	}
	d, err := t.memory.Read(kind, date)
	return memoryResult(d, err)
}

func (t *RuntimeToolExecutor) invokeMemorySearch(_ string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := t.memoryAllowed(execCtx); err != nil {
		return memoryResult(nil, err)
	}
	before := stringArg(args, "before")
	dates, err := t.memory.Dates(before, 200)
	if err != nil {
		return memoryResult(nil, err)
	}
	next := ""
	if len(dates) == 200 {
		next = dates[len(dates)-1]
	}
	query := stringArg(args, "query")
	if query == "" {
		return structuredResult(map[string]any{"dates": dates, "nextBefore": next, "today": t.memory.Today()}), nil
	}
	result, err := t.memory.SearchPage(query, before)
	return memoryResult(result, err)
}

func (t *RuntimeToolExecutor) invokeMemoryWrite(_ string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if err := t.memoryAllowed(execCtx); err != nil {
		return memoryResult(nil, err)
	}
	if !OrdinaryNativeRoot(execCtx.Session) || execCtx.Session.RunOrigin != nil || IsReadOnlyToolExecutionPolicy(execCtx.Session.ToolExecutionPolicy) || IsReadOnlyToolExecutionPolicy(execCtx.ToolExecutionPolicy) {
		return memoryResult(nil, fmt.Errorf("memory writes require an ordinary native main root run"))
	}
	kind, date := stringArg(args, "kind"), stringArg(args, "date")
	if kind == "daily" && date == "" {
		date = t.memory.Today()
	}
	content, hasContent := args["content"].(string)
	base := stringArg(args, "revision")
	operation := stringArg(args, "operation")
	if (operation == "save" || operation == "append") && !hasContent {
		return memoryResult(nil, memory.ErrInvalid)
	}
	var d memory.Document
	var err error
	switch operation {
	case "save":
		d, err = t.memory.Save(kind, date, content, base)
	case "append":
		if kind != "daily" {
			err = memory.ErrInvalid
		} else {
			d, err = t.memory.Append(date, content, base)
		}
	case "delete":
		d, err = t.memory.Delete(kind, date, base)
	default:
		err = memory.ErrInvalid
	}
	return memoryResult(d, err)
}
