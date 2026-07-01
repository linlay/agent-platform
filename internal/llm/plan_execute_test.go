package llm

import (
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	contracts "agent-platform/internal/contracts"
)

func TestPlanStageToolsDefaultsToPlanAddTasksOnly(t *testing.T) {
	stream := &planPipelineStream{
		session: contracts.QuerySession{
			ToolNames: []string{"datetime", "memory_search"},
		},
	}

	if got, want := stream.planStageTools(), []string{"plan_add_tasks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestPlanStageToolsPreservesExplicitPlanToolsWithoutSessionFallback(t *testing.T) {
	stream := &planPipelineStream{
		session: contracts.QuerySession{
			ToolNames: []string{"memory_search", "datetime"},
		},
		settings: contracts.PlanExecuteSettings{
			Plan: contracts.StageSettings{
				Tools: []string{"mock.plan.inspect", "datetime"},
			},
		},
	}

	if got, want := stream.planStageTools(), []string{"mock.plan.inspect", "datetime", "plan_add_tasks"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestPlanStagePostToolHookStopsAfterTasksCreated(t *testing.T) {
	stream := &planPipelineStream{
		execCtx: &contracts.ExecutionContext{
			PlanState: &contracts.PlanRuntimeState{},
		},
	}

	if got := stream.planStagePostToolHook("datetime", "tool_1"); got != contracts.PostToolContinue {
		t.Fatalf("non-plan tool hook=%v want %v", got, contracts.PostToolContinue)
	}
	if got := stream.planStagePostToolHook("plan_add_tasks", "tool_1"); got != contracts.PostToolContinue {
		t.Fatalf("empty plan hook=%v want %v", got, contracts.PostToolContinue)
	}

	stream.execCtx.PlanState.Tasks = []contracts.PlanTask{{TaskID: "task_1", Description: "first task"}}
	if got := stream.planStagePostToolHook("plan_add_tasks", "tool_1"); got != contracts.PostToolStop {
		t.Fatalf("created-plan hook=%v want %v", got, contracts.PostToolStop)
	}
}

func TestPlanExecuteUsesGlobalPromptTemplates(t *testing.T) {
	stream := &planPipelineStream{
		engine: &LLMAgentEngine{cfg: config.Config{
			Prompts: config.PromptsConfig{
				PlanExecute: config.PlanExecutePromptsConfig{
					TaskExecutionPromptTemplate: "global task {{task_id}} {{task_description}}",
					PlanUserPromptTemplate:      "global plan {{plan_prompt}} {{execute_tool_descriptions}} {{plan_callable_tool_descriptions}} {{user_request}}",
					SummaryUserPromptTemplate:   "global summary {{original_request}} {{task_results}}",
				},
			},
		}},
		req: api.QueryRequest{Message: "do work"},
		execCtx: &contracts.ExecutionContext{
			PlanState: &contracts.PlanRuntimeState{
				Tasks: []contracts.PlanTask{{TaskID: "task_1", Status: "completed", Description: "first"}},
			},
		},
	}
	if got := stream.taskTemplate(); got != "global task {{task_id}} {{task_description}}" {
		t.Fatalf("expected global task template, got %q", got)
	}
	planPrompt := stream.renderPlanUserPrompt("stage plan", "exec tools", "plan tools")
	if planPrompt != "global plan stage plan exec tools plan tools do work" {
		t.Fatalf("unexpected rendered plan prompt %q", planPrompt)
	}
	summaryPrompt := stream.renderSummaryUserPrompt()
	if summaryPrompt != "global summary do work - task_1 | completed | first" {
		t.Fatalf("unexpected rendered summary prompt %q", summaryPrompt)
	}
}

func TestPlanExecuteAgentTaskTemplateOverridesGlobal(t *testing.T) {
	stream := &planPipelineStream{
		engine: &LLMAgentEngine{cfg: config.Config{
			Prompts: config.PromptsConfig{
				PlanExecute: config.PlanExecutePromptsConfig{
					TaskExecutionPromptTemplate: "global task",
				},
			},
		}},
		settings: contracts.PlanExecuteSettings{
			TaskExecutionPrompt: "agent task",
		},
	}
	if got := stream.taskTemplate(); got != "agent task" {
		t.Fatalf("expected agent task template to win, got %q", got)
	}
}

func TestNonSystemMessagesFiltersSystemMessages(t *testing.T) {
	messages := []openAIMessage{
		{Role: "system", Content: "first system"},
		{Role: "user", Content: "first user"},
		{Role: " system ", Content: "spaced system"},
		{Role: "assistant", Content: "assistant reply"},
		{Role: "", Content: "empty role"},
	}

	got := nonSystemMessages(messages)
	want := []openAIMessage{
		{Role: "user", Content: "first user"},
		{Role: "assistant", Content: "assistant reply"},
		{Role: "", Content: "empty role"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nonSystemMessages()=%#v want %#v", got, want)
	}
}
