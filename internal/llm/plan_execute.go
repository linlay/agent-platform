package llm

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/plantasks"
)

const defaultTaskExecutionPromptTemplate = `Task list:
{{task_list}}
Current task ID: {{task_id}}
Current task description: {{task_description}}
Execution rules:
1) Call at most one tool per round.
2) You may call any available tool as needed.
3) Before finishing this task, you MUST call plan_update_task to update its status.`

const defaultPlanUserPromptTemplate = `{{plan_prompt}}

{{execute_tool_descriptions}}

{{plan_callable_tool_descriptions}}

Create an execution plan for the user's request. You MUST call plan_add_tasks before the stage finishes.

User request:
{{user_request}}`

const defaultPlanSummarySystemPrompt = `Summarize the completed plan execution for the user.`

const defaultPlanSummaryUserPromptTemplate = `Please provide a final summary of the completed plan.

Original request:
{{original_request}}

Task results:
{{task_results}}`

type planPipelineStream struct {
	engine  *LLMAgentEngine
	ctx     context.Context
	req     api.QueryRequest
	session QuerySession
	execCtx *ExecutionContext

	settings      PlanExecuteSettings
	pending       []AgentDelta
	current       AgentStream
	taskIndex     int
	planDone      bool
	summaryDone   bool
	completed     bool
	closed        bool
	taskLifecycle bool

	executeMessages []openAIMessage // accumulated messages across rounds for summary
}

func newPlanPipelineStream(engine *LLMAgentEngine, ctx context.Context, req api.QueryRequest, session QuerySession) (AgentStream, error) {
	settings := resolvePlanExecuteRuntimeSettings(session, engine.cfg.Defaults.Plan.MaxSteps, engine.cfg.Defaults.Plan.MaxWorkRoundsPerTask)
	execCtx := &ExecutionContext{
		Request:             req,
		Session:             session,
		RunControl:          RunControlFromContext(ctx),
		Budget:              NormalizeBudget(session.ResolvedBudget),
		StageSettings:       settings,
		RunLoopState:        RunLoopStateIdle,
		ToolExecutionPolicy: session.ToolExecutionPolicy,
		PlanState: &PlanRuntimeState{
			PlanID: session.RunID + "_plan",
		},
	}
	stream := &planPipelineStream{
		engine:   engine,
		ctx:      ctx,
		req:      req,
		session:  session,
		execCtx:  execCtx,
		settings: settings,
		pending: []AgentDelta{
			DeltaStageMarker{Stage: "plan"},
			// Java parity: do NOT emit an empty plan.update at initialization.
			// The first plan.update is emitted in afterStageEOF after
			// plan_add_tasks has populated PlanState.Tasks.
		},
	}
	return stream, nil
}

func (s *planPipelineStream) Next() (AgentDelta, error) {
	for {
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			return event, nil
		}
		if s.completed {
			return nil, io.EOF
		}
		if s.current == nil {
			if err := s.advance(); err != nil {
				return nil, err
			}
			continue
		}
		event, err := s.current.Next()
		if err == io.EOF {
			// Capture accumulated messages before closing (for summary stage context)
			if llmStream, ok := s.current.(*llmRunStream); ok && s.taskLifecycle {
				s.executeMessages = append(s.executeMessages, nonSystemMessages(llmStream.AccumulatedMessages())...)
			}
			_ = s.current.Close()
			s.current = nil
			if err := s.afterStageEOF(); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		return event, nil
	}
}

func (s *planPipelineStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.current != nil {
		return s.current.Close()
	}
	return nil
}

func (s *planPipelineStream) advance() error {
	if !s.planDone {
		return s.startPlanStage()
	}
	if s.taskIndex < len(s.execCtx.PlanState.Tasks) {
		return s.advanceTaskExecution()
	}
	if !s.summaryDone {
		return s.startSummaryStage()
	}
	s.completed = true
	return nil
}

// advanceTaskExecution starts execution for the current task.
// The llmRunStream handles the multi-turn tool execution loop internally.
// After the stream ends, afterStageEOF checks task status.
func (s *planPipelineStream) advanceTaskExecution() error {
	task := &s.execCtx.PlanState.Tasks[s.taskIndex]

	if task.Status == "" || task.Status == "init" {
		task.Status = "in_progress"
	}
	s.execCtx.PlanState.ActiveTaskID = task.TaskID
	s.pending = append(s.pending,
		DeltaStageMarker{Stage: fmt.Sprintf("execute-task-%d", s.taskIndex+1)},
	)
	s.appendPendingSystemInitQuery("plan-execute:execute", "execute")
	s.pending = append(s.pending, DeltaTaskLifecycle{
		Kind:        "start",
		TaskID:      task.TaskID,
		RunID:       s.session.RunID,
		TaskName:    task.TaskID,
		Description: task.Description,
	})

	return s.startTaskStream(task)
}

func (s *planPipelineStream) startTaskStream(task *PlanTask) error {
	beforeStatus := NormalizePlanTaskStatus(task.Status)

	// Build messages from accumulated executeMessages (Java: context.executeMessages() is shared
	// across all tasks, so each task sees previous tasks' conversation + steers).
	taskPrompt := renderTemplate(s.taskTemplate(), map[string]string{
		"task_list":        formatTaskList(s.execCtx.PlanState.Tasks),
		"task_id":          task.TaskID,
		"task_description": task.Description,
	})
	messages := make([]openAIMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.settings.Execute.PrimaryPrompt()
	if systemPrompt == "" {
		systemPrompt = "Execute the current task."
	}
	messages = append(messages, openAIMessage{Role: "system", Content: systemPrompt})
	messages = append(messages, s.executeMessages...)
	messages = append(messages, openAIMessage{Role: "user", Content: taskPrompt})

	req := s.req
	req.Message = taskPrompt
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, s.sessionForStage(s.settings.Execute, s.executeStageTools()), true, runStreamOptions{
		ExecCtx:   s.execCtx,
		Messages:  messages, // Carry full execute history including steers
		ToolNames: s.executeStageTools(),
		ModelKey:  s.resolveStageModelKey(s.settings.Execute),
		MaxSteps:  s.settings.MaxWorkRoundsPerTask,
		Stage:     fmt.Sprintf("execute-step-%d", s.taskIndex+1),
		PostToolHook: func(toolName string, toolID string) PostToolHookResult {
			if !isPlanTool(toolName) {
				return PostToolContinue
			}
			afterStatus := NormalizePlanTaskStatus(task.Status)
			if afterStatus != beforeStatus && isTerminalPlanStatus(afterStatus) {
				return PostToolStop
			}
			return PostToolContinue
		},
	})
	if err != nil {
		return err
	}
	s.current = stream
	s.taskLifecycle = true
	return nil
}

func (s *planPipelineStream) afterStageEOF() error {
	if !s.planDone {
		s.planDone = true
		if len(s.execCtx.PlanState.Tasks) == 0 {
			s.pending = append(s.pending, DeltaError{
				Error: NewErrorPayload(
					"plan_not_created",
					"planning stage did not create any tasks via plan_add_tasks",
					ErrorScopeRun,
					ErrorCategorySystem,
					nil,
				),
			})
			s.summaryDone = true
			s.completed = true
			return nil
		}
		return nil
	}

	if s.taskLifecycle {
		s.taskLifecycle = false
		task := &s.execCtx.PlanState.Tasks[s.taskIndex]
		finalStatus := NormalizePlanTaskStatus(task.Status)

		if isTerminalPlanStatus(finalStatus) {
			s.emitTaskTerminal(task, finalStatus)
			if finalStatus == "failed" {
				// Failed tasks stop the entire plan (Java behaviour)
				s.taskIndex = len(s.execCtx.PlanState.Tasks)
			}
			return nil
		}

		// Task did not reach terminal status — mark as failed
		s.emitTaskFailure(task, "task execution did not reach a terminal status")
		return nil
	}

	// Summary stage finished
	s.summaryDone = true
	s.completed = true
	return nil
}

func (s *planPipelineStream) emitTaskTerminal(task *PlanTask, status string) {
	switch status {
	case "completed":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "complete", TaskID: task.TaskID})
	case "canceled":
		s.pending = append(s.pending, DeltaTaskLifecycle{Kind: "cancel", TaskID: task.TaskID})
	case "failed":
		s.pending = append(s.pending, DeltaTaskLifecycle{
			Kind:   "error",
			TaskID: task.TaskID,
			Error: NewErrorPayload("task_failed", "Task status updated to failed",
				ErrorScopeTask, ErrorCategorySystem, nil),
		})
	}
	s.taskIndex++
	s.execCtx.PlanState.ActiveTaskID = ""
}

func (s *planPipelineStream) emitTaskFailure(task *PlanTask, message string) {
	task.Status = "failed"
	s.execCtx.PlanState.ActiveTaskID = ""
	s.persistPlanTasksSnapshot()
	s.pending = append(s.pending, DeltaPlanUpdate{
		PlanID: s.execCtx.PlanState.PlanID,
		ChatID: s.session.ChatID,
		Plan:   PlanTasksArray(s.execCtx.PlanState),
	})
	s.pending = append(s.pending, DeltaTaskLifecycle{
		Kind:   "error",
		TaskID: task.TaskID,
		Error: NewErrorPayload("task_execution_error", message,
			ErrorScopeTask, ErrorCategorySystem, map[string]any{"taskId": task.TaskID}),
	})
	s.taskIndex++
}

func (s *planPipelineStream) persistPlanTasksSnapshot() {
	if s == nil || s.execCtx == nil || IsReadOnlyToolExecutionPolicy(s.execCtx.ToolExecutionPolicy) {
		return
	}
	if path, err := plantasks.PersistExecutionContext("", s.execCtx); err != nil {
		log.Printf("[llm][plan] write plan task snapshot failed runId=%s path=%s err=%v", s.session.RunID, path, err)
	}
}

func (s *planPipelineStream) startPlanStage() error {
	planPrompt := s.settings.Plan.PrimaryPrompt()
	executeToolDesc := s.buildExecuteToolDescriptions()
	planCallableDesc := s.buildPlanCallableToolDescriptions()

	req := s.req
	req.Message = s.renderPlanUserPrompt(planPrompt, executeToolDesc, planCallableDesc)
	stageSession := s.sessionForStage(s.settings.Plan, s.planStageTools())
	stageSession.CurrentMessages = s.engine.buildCurrentMessagesForRequest(req, stageSession, false)
	stream, err := s.engine.newRunStreamWithOptions(s.ctx, req, stageSession, true, runStreamOptions{
		ExecCtx:      s.execCtx,
		ToolNames:    s.planStageTools(),
		ModelKey:     s.resolveStageModelKey(s.settings.Plan),
		MaxSteps:     minPositive(s.settings.MaxSteps, 6),
		Stage:        "plan",
		PostToolHook: s.planStagePostToolHook,
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *planPipelineStream) startSummaryStage() error {
	s.pending = append(s.pending, DeltaStageMarker{Stage: "summary"})
	s.appendPendingSystemInitQuery("plan-execute:summary", "summary")

	// Build summary messages from accumulated execute history (Java: context.executeMessages())
	summaryMessages := make([]openAIMessage, 0, len(s.executeMessages)+2)
	systemPrompt := s.settings.Summary.PrimaryPrompt()
	if systemPrompt == "" && s.engine != nil {
		systemPrompt = strings.TrimSpace(s.engine.cfg.Prompts.PlanExecute.SummarySystemPrompt)
	}
	if systemPrompt == "" {
		systemPrompt = defaultPlanSummarySystemPrompt
	}
	summaryMessages = append(summaryMessages, openAIMessage{Role: "system", Content: systemPrompt})
	summaryMessages = append(summaryMessages, s.executeMessages...)
	summaryMessages = append(summaryMessages, openAIMessage{
		Role:    "user",
		Content: s.renderSummaryUserPrompt(),
	})

	stream, err := s.engine.newRunStreamWithOptions(s.ctx, s.req, s.sessionForStage(s.settings.Summary, nil), false, runStreamOptions{
		ExecCtx:   s.execCtx,
		Messages:  summaryMessages,
		ToolNames: nil,
		ModelKey:  s.resolveStageModelKey(s.settings.Summary),
		MaxSteps:  1,
		Stage:     "summary",
	})
	if err != nil {
		return err
	}
	s.current = stream
	return nil
}

func (s *planPipelineStream) appendPendingSystemInitQuery(cacheKey string, stage string) {
	if s == nil {
		return
	}
	system := takePendingSystemPayload(&s.session, cacheKey)
	if len(system) == 0 {
		return
	}
	s.pending = append(s.pending, DeltaSyntheticQuery{
		ChatID: s.session.ChatID,
		Role:   api.QueryRoleSystem,
		System: system,
		Kind:   "system-init",
		Stage:  stage,
		Hidden: true,
	})
}

// buildExecuteToolDescriptions returns a prompt section describing execute-stage
// tools for reference during planning (Java: augmentPlanStageWithToolPrompts).
// Output format matches Java backendToolDescriptionSection: "- name: description".
func (s *planPipelineStream) buildExecuteToolDescriptions() string {
	tools := s.executeStageTools()
	if len(tools) == 0 {
		return ""
	}
	descByName := s.toolDescriptionsByName()
	var lines []string
	for _, toolName := range tools {
		if isPlanTool(toolName) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(toolName)), "plan_") {
			continue // skip plan tools in reference section
		}
		desc := strings.TrimSpace(descByName[strings.ToLower(toolName)])
		if desc == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", toolName, desc))
	}
	if len(lines) == 0 {
		return ""
	}
	return "以下是执行阶段可用工具说明（当前是规划阶段，仅供参考，不允许调用）:\n" + strings.Join(lines, "\n")
}

func (s *planPipelineStream) buildPlanCallableToolDescriptions() string {
	descByName := s.toolDescriptionsByName()
	desc := strings.TrimSpace(descByName["plan_add_tasks"])
	if desc == "" {
		return "当前规划阶段可调用工具（必须调用 plan_add_tasks 创建计划）:\n- plan_add_tasks"
	}
	return "当前规划阶段可调用工具（必须调用 plan_add_tasks 创建计划）:\n- plan_add_tasks: " + desc
}

func (s *planPipelineStream) toolDescriptionsByName() map[string]string {
	defs := s.engine.tools.Definitions()
	out := make(map[string]string, len(defs))
	for _, def := range defs {
		name := strings.ToLower(strings.TrimSpace(def.Name))
		if name == "" {
			continue
		}
		out[name] = def.Description
	}
	return out
}

func (s *planPipelineStream) sessionForStage(stage StageSettings, toolNames []string) QuerySession {
	session := s.session
	if modelKey := s.resolveStageModelKey(stage); modelKey != "" {
		session.ModelKey = modelKey
	}
	if toolNames != nil {
		session.ToolNames = append([]string(nil), toolNames...)
	}
	return session
}

func (s *planPipelineStream) resolveStageModelKey(stage StageSettings) string {
	if strings.TrimSpace(stage.ModelKey) != "" {
		return strings.TrimSpace(stage.ModelKey)
	}
	return s.session.ModelKey
}

func (s *planPipelineStream) planStageTools() []string {
	if len(s.settings.Plan.Tools) > 0 {
		return appendUniqueTools(s.settings.Plan.Tools, "plan_add_tasks")
	}
	return []string{"plan_add_tasks"}
}

func (s *planPipelineStream) planStagePostToolHook(toolName string, _ string) PostToolHookResult {
	if !isPlanTool(toolName) {
		return PostToolContinue
	}
	if s.execCtx != nil && s.execCtx.PlanState != nil && len(s.execCtx.PlanState.Tasks) > 0 {
		return PostToolStop
	}
	return PostToolContinue
}

func (s *planPipelineStream) executeStageTools() []string {
	tools := stageToolsOrDefault(s.settings.Execute, s.session.ToolNames)
	// plan_update_task for status updates, no plan_get_tasks (per Zhang Qian's feedback)
	return appendUniqueTools(tools, "plan_update_task")
}

func isTerminalPlanStatus(status string) bool {
	switch status {
	case "completed", "canceled", "failed":
		return true
	default:
		return false
	}
}

func stageToolsOrDefault(stage StageSettings, fallback []string) []string {
	if len(stage.Tools) > 0 {
		return append([]string(nil), stage.Tools...)
	}
	return append([]string(nil), fallback...)
}

func appendUniqueTools(base []string, extra ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	for _, item := range append(base, extra...) {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func nonSystemMessages(msgs []openAIMessage) []openAIMessage {
	out := make([]openAIMessage, 0, len(msgs))
	for _, msg := range msgs {
		if strings.TrimSpace(msg.Role) != "system" {
			out = append(out, msg)
		}
	}
	return out
}

func (s *planPipelineStream) taskTemplate() string {
	if strings.TrimSpace(s.settings.TaskExecutionPrompt) != "" {
		return s.settings.TaskExecutionPrompt
	}
	if s.engine != nil && strings.TrimSpace(s.engine.cfg.Prompts.PlanExecute.TaskExecutionPromptTemplate) != "" {
		return strings.TrimSpace(s.engine.cfg.Prompts.PlanExecute.TaskExecutionPromptTemplate)
	}
	return defaultTaskExecutionPromptTemplate
}

func defaultTaskTemplate(settings PlanExecuteSettings) string {
	if strings.TrimSpace(settings.TaskExecutionPrompt) != "" {
		return settings.TaskExecutionPrompt
	}
	return defaultTaskExecutionPromptTemplate
}

func (s *planPipelineStream) renderPlanUserPrompt(planPrompt string, executeToolDesc string, planCallableDesc string) string {
	template := defaultPlanUserPromptTemplate
	if s.engine != nil && strings.TrimSpace(s.engine.cfg.Prompts.PlanExecute.PlanUserPromptTemplate) != "" {
		template = strings.TrimSpace(s.engine.cfg.Prompts.PlanExecute.PlanUserPromptTemplate)
	}
	return strings.TrimSpace(renderTemplate(template, map[string]string{
		"plan_prompt":                     strings.TrimSpace(planPrompt),
		"execute_tool_descriptions":       strings.TrimSpace(executeToolDesc),
		"plan_callable_tool_descriptions": strings.TrimSpace(planCallableDesc),
		"user_request":                    s.req.Message,
	}))
}

func (s *planPipelineStream) renderSummaryUserPrompt() string {
	template := defaultPlanSummaryUserPromptTemplate
	if s.engine != nil && strings.TrimSpace(s.engine.cfg.Prompts.PlanExecute.SummaryUserPromptTemplate) != "" {
		template = strings.TrimSpace(s.engine.cfg.Prompts.PlanExecute.SummaryUserPromptTemplate)
	}
	return strings.TrimSpace(renderTemplate(template, map[string]string{
		"original_request": s.req.Message,
		"task_results":     formatTaskList(s.execCtx.PlanState.Tasks),
	}))
}

func renderTemplate(template string, values map[string]string) string {
	result := template
	for key, value := range values {
		result = strings.ReplaceAll(result, "{{"+key+"}}", value)
		result = strings.ReplaceAll(result, "{{ "+key+" }}", value)
	}
	return result
}

func formatTaskList(tasks []PlanTask) string {
	if len(tasks) == 0 {
		return "- (empty)"
	}
	lines := make([]string, 0, len(tasks))
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("- %s | %s | %s", task.TaskID, task.Status, task.Description))
	}
	return strings.Join(lines, "\n")
}

func minPositive(value int, fallback int) int {
	if value > 0 && value < fallback {
		return value
	}
	if fallback > 0 {
		return fallback
	}
	return value
}
