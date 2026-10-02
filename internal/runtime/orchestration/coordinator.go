package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/catalogview"
	"agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type Coordinator struct {
	RunCtx            context.Context
	Request           runtimetypes.QueryCommand
	Session           contracts.QuerySession
	Summary           chat.Summary
	Agent             runtimetypes.Engine
	Registry          catalog.Registry
	TeamSnapshot      *catalog.TeamSnapshot
	BuildQuerySession func(context.Context, runtimetypes.QueryCommand, chat.Summary, catalog.AgentDefinition, session.Options) (contracts.QuerySession, error)
	Chats             chat.Store
	ResourceBaseURL   string
	ResourceTickets   proxy.TicketIssuer
	PrepareSystemInit func(runtimetypes.QueryCommand, *contracts.QuerySession, bool) (*chat.QueryLineSystem, error)
	SystemInitMu      sync.Mutex
	Mapper            contracts.StreamDeltaMapper
	EmitDelta         func(contracts.AgentDelta)
	EmitInputs        func(...stream.StreamInput)
	CurrentLiveSeq    func() int64
	TaskCounter       int
	TeamAwaitCounter  int
}

func (o *Coordinator) Run(mainStream contracts.AgentStream) (bool, bool, error) {
	result, err := Run(mainStream, o)
	return result.StreamFailed, result.StreamInterrupted, err
}

func (o *Coordinator) Emit(delta contracts.AgentDelta) {
	o.EmitDelta(delta)
}

func (o *Coordinator) HandleSubAgents(mainStream contracts.AgentStream, invoke contracts.DeltaInvokeSubAgents) error {
	return o.HandleSubAgentBatch(mainStream, invoke)
}

func (o *Coordinator) HandleTeamDispatch(mainStream contracts.AgentStream, dispatch contracts.DeltaTeamDispatch) (bool, error) {
	return o.dispatchTeam(mainStream, dispatch)
}

type ChildTaskResult struct {
	Index       int    `json:"-"`
	TaskID      string `json:"taskId"`
	TaskName    string `json:"taskName"`
	SubAgentKey string `json:"subAgentKey"`
	Status      string `json:"status"`
	Text        string `json:"text"`
	Error       string `json:"error,omitempty"`
	ErrorCode   string `json:"errorCode,omitempty"`
}

type teamDelegateMemberResult struct {
	AgentKey  string `json:"agentKey"`
	TaskName  string `json:"taskName,omitempty"`
	Status    string `json:"status"`
	Content   string `json:"content,omitempty"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type TeamDelegateToolResult struct {
	Results []teamDelegateMemberResult `json:"results"`
}

type ChildRouteEvent struct {
	Input        stream.StreamInput
	Result       *ChildTaskResult
	Awaiting     *TeamChildAwaiting
	AwaitingDone string
}

type PreparedSubTask struct {
	Spec         contracts.SubAgentTaskSpec
	AgentDef     catalog.AgentDefinition
	TaskID       string
	RequestID    string
	SubTaskID    string
	MainToolID   string
	TeamID       string
	Presentation string
}

type ChildRunOptions struct {
	InheritOriginalContext bool
	IncludeHistory         bool
	Presentation           string
	SuppressFinalDuplicate bool
	RunControl             *contracts.RunControl
}

type TeamChildAwaiting struct {
	Task     PreparedSubTask
	Control  *contracts.RunControl
	Ask      stream.AwaitAsk
	RawID    string
	PublicID string
}

type TeamMergedHITLBatch struct {
	Orchestrator *Coordinator
	Tasks        []PreparedSubTask
	Enabled      bool
	WaveSize     int
	Controls     map[string]*contracts.RunControl
	Waiting      map[string]*TeamChildAwaiting
	Completed    map[string]bool
}

func (o *Coordinator) HandleSubAgentBatch(mainStream contracts.AgentStream, invoke contracts.DeltaInvokeSubAgents) error {
	main, ok := mainStream.(contracts.OrchestratableAgentStream)
	if !ok {
		return fmt.Errorf("main agent stream does not support sub-agent orchestration")
	}
	if o.Registry == nil || o.BuildQuerySession == nil || o.Mapper == nil {
		o.InjectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is not configured")
		return nil
	}
	if o.Session.TeamRuntime != nil {
		o.InjectMainToolError(main, invoke.MainToolID, "TEAM coordinators must use agent_delegate instead of agent_invoke")
		return nil
	}
	if !o.Session.ModeCapabilities.InvokeChildren {
		o.InjectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is only supported for GENERAL/ONESHOT/CODER main agents")
		return nil
	}
	if len(invoke.Tasks) < 1 || len(invoke.Tasks) > contracts.MaxInvokeAgentTasks {
		o.InjectMainToolError(main, invoke.MainToolID, fmt.Sprintf("invalid agent_invoke call: tasks must contain between 1 and %d items", contracts.MaxInvokeAgentTasks))
		return nil
	}
	for _, task := range invoke.Tasks {
		subAgentKey := strings.TrimSpace(task.SubAgentKey)
		if subAgentKey == "" || strings.TrimSpace(task.TaskText) == "" {
			o.InjectMainToolError(main, invoke.MainToolID, "invalid agent_invoke call: every task requires subAgentKey and task")
			return nil
		}
		if SameAgentKey(subAgentKey, o.Session.AgentKey) {
			o.InjectMainToolError(main, invoke.MainToolID, "agent_invoke cannot target the current agent")
			return nil
		}
	}
	if strings.TrimSpace(o.Session.TeamID) != "" && o.TeamSnapshot == nil {
		resolved, found := catalogview.ResolveTeam(o.Registry, o.Session.TeamID)
		if !found {
			o.InjectMainToolError(main, invoke.MainToolID, fmt.Sprintf("team is unavailable: %s", o.Session.TeamID))
			return nil
		}
		o.TeamSnapshot = &resolved
	}
	prepared := make([]PreparedSubTask, 0, len(invoke.Tasks))
	for _, task := range invoke.Tasks {
		subAgentKey := strings.TrimSpace(task.SubAgentKey)
		taskText := strings.TrimSpace(task.TaskText)
		taskName := strings.TrimSpace(task.TaskName)
		if taskName == "" {
			taskName = subAgentKey
		}
		var agentDef catalog.AgentDefinition
		var found bool
		if o.TeamSnapshot != nil {
			if !o.TeamSnapshot.HasAgent(subAgentKey) {
				message := fmt.Sprintf("sub-agent %q is not in team %q", subAgentKey, o.TeamSnapshot.TeamID)
				if o.TeamSnapshot.DeclaresAgent(subAgentKey) {
					message = fmt.Sprintf("sub-agent %q is unavailable in team %q", subAgentKey, o.TeamSnapshot.TeamID)
				}
				o.InjectMainToolError(main, invoke.MainToolID, message)
				return nil
			}
			agentDef, found = o.TeamSnapshot.AgentDefinition(subAgentKey)
			if !found {
				o.InjectMainToolError(main, invoke.MainToolID, fmt.Sprintf("sub-agent %q is unavailable in team %q", subAgentKey, o.TeamSnapshot.TeamID))
				return nil
			}
		} else {
			agentDef, found = o.Registry.AgentDefinition(subAgentKey)
			if !found {
				o.InjectMainToolError(main, invoke.MainToolID, fmt.Sprintf("sub-agent not found: %s", subAgentKey))
				return nil
			}
		}
		if !catalog.AgentUsesACPCoderBackend(agentDef) && !session.ResolvedModeCapabilities(agentDef).RunAsChild {
			o.InjectMainToolError(main, invoke.MainToolID, "sub-agent must be GENERAL/ONESHOT/CODER/KBASE/PROXY")
			return nil
		}
		if !catalog.AgentInvocable(agentDef) {
			o.InjectMainToolError(main, invoke.MainToolID, "sub-agent is not invocable")
			return nil
		}
		if ContainsInvokeAgentsTool(agentDef.Tools) {
			o.InjectMainToolError(main, invoke.MainToolID, "nested sub-agent invocation is not allowed")
			return nil
		}
		o.TaskCounter++
		taskIndex := o.TaskCounter
		parentReqID := strings.TrimSpace(o.Session.RequestID)
		requestID := fmt.Sprintf("sub_%d", taskIndex)
		if parentReqID != "" {
			requestID = fmt.Sprintf("%s_sub_%d", parentReqID, taskIndex)
		}
		subTaskID := fmt.Sprintf("sub_%d", taskIndex)
		prepared = append(prepared, PreparedSubTask{
			Spec: contracts.SubAgentTaskSpec{
				SubAgentKey: subAgentKey,
				TaskText:    taskText,
				TaskName:    taskName,
				Files:       append([]string(nil), task.Files...),
			},
			AgentDef:  agentDef,
			TaskID:    fmt.Sprintf("%s_t_%d", strings.TrimSpace(o.Session.RunID), taskIndex),
			RequestID: requestID,
			SubTaskID: subTaskID,
		})
	}

	for index := range prepared {
		prepared[index].MainToolID = invoke.MainToolID
		if o.Session.TeamRuntime != nil {
			prepared[index].TeamID = o.Session.TeamID
			prepared[index].Presentation = "task"
		}
	}

	for _, task := range prepared {
		o.EmitDelta(contracts.DeltaTaskLifecycle{
			Kind:         "start",
			TaskID:       task.TaskID,
			RunID:        o.Session.RunID,
			TaskName:     task.Spec.TaskName,
			Description:  task.Spec.TaskText,
			SubAgentKey:  task.Spec.SubAgentKey,
			MainToolID:   invoke.MainToolID,
			TeamID:       task.TeamID,
			Presentation: task.Presentation,
		})
	}

	var principal *contracts.AuthIdentity
	if strings.TrimSpace(o.Session.Subject) != "" {
		principal = &contracts.AuthIdentity{Subject: o.Session.Subject}
	}

	results := make([]ChildTaskResult, len(prepared))
	routedCh := make(chan ChildRouteEvent, 32)
	teamMaxParallel := len(prepared)
	var teamSem chan struct{}
	if o.Session.TeamRuntime != nil {
		teamMaxParallel = o.Session.TeamRuntime.MaxParallel
		if teamMaxParallel < 1 || teamMaxParallel > len(prepared) {
			teamMaxParallel = len(prepared)
		}
		teamSem = make(chan struct{}, teamMaxParallel)
	}
	hitlBatch := NewTeamMergedHITLBatch(o, prepared, o.Session.TeamRuntime != nil, teamMaxParallel)
	var wg sync.WaitGroup

	for index, task := range prepared {
		wg.Add(1)
		go func(index int, task PreparedSubTask) {
			defer wg.Done()
			if teamSem != nil {
				select {
				case teamSem <- struct{}{}:
					defer func() { <-teamSem }()
				case <-o.RunCtx.Done():
					routedCh <- ChildRouteEvent{Result: &ChildTaskResult{Index: index, TaskID: task.TaskID, TaskName: task.Spec.TaskName, SubAgentKey: task.Spec.SubAgentKey, Status: "cancelled", Text: "Team member interrupted"}}
					return
				}
			}
			options := ChildRunOptions{RunControl: hitlBatch.ControlFor(task)}
			routedCh <- ChildRouteEvent{Result: o.RunChildTaskWithOptions(index, task, principal, func(input stream.StreamInput) {
				if event, captured := hitlBatch.Capture(task, input); captured {
					routedCh <- event
					return
				}
				routedCh <- ChildRouteEvent{Input: input}
			}, options)}
		}(index, task)
	}
	go func() {
		wg.Wait()
		close(routedCh)
	}()

	for routed := range routedCh {
		if routed.Input != nil && o.EmitInputs != nil {
			o.EmitInputs(routed.Input)
		}
		if routed.Result == nil {
			hitlBatch.Observe(routed)
			continue
		}
		results[routed.Result.Index] = *routed.Result
		task := prepared[routed.Result.Index]
		terminalKind := "complete"
		if routed.Result.Status == "failed" {
			terminalKind = "error"
		} else if routed.Result.Status == "cancelled" {
			terminalKind = "cancel"
		}
		lifecycle := contracts.DeltaTaskLifecycle{
			Kind:         terminalKind,
			TaskID:       routed.Result.TaskID,
			SubAgentKey:  routed.Result.SubAgentKey,
			TeamID:       task.TeamID,
			Presentation: task.Presentation,
		}
		if terminalKind == "error" {
			lifecycle.Error = apperrors.Payload(
				apperrors.Code(FirstNonEmpty(routed.Result.ErrorCode, string(apperrors.CodeSubAgentFailed))),
				FirstNonEmpty(routed.Result.Error, routed.Result.Text),
				apperrors.WithScope(apperrors.ScopeTask),
				apperrors.WithCategory(apperrors.CategorySystem),
			)
		}
		o.EmitDelta(lifecycle)
		hitlBatch.Observe(routed)
	}

	aggregated, err := json.Marshal(results)
	if err != nil {
		o.InjectMainToolError(main, invoke.MainToolID, err.Error())
		return nil
	}
	anyFailed := false
	for _, result := range results {
		if result.Status != "completed" {
			anyFailed = true
			break
		}
	}
	_ = main.InjectToolResult(invoke.MainToolID, string(aggregated), anyFailed)
	return nil
}

func (o *Coordinator) dispatchTeam(mainStream contracts.AgentStream, dispatch contracts.DeltaTeamDispatch) (bool, error) {
	main, ok := mainStream.(contracts.OrchestratableAgentStream)
	if !ok {
		return false, fmt.Errorf("TEAM coordinator stream does not support orchestration")
	}
	if o.TeamSnapshot == nil {
		o.InjectMainToolError(main, dispatch.MainToolID, "TEAM snapshot is unavailable")
		return false, nil
	}
	if len(dispatch.Tasks) == 0 || len(dispatch.Tasks) > len(o.TeamSnapshot.ValidAgentKeys) {
		o.InjectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("agent_delegate tasks must contain between 1 and %d Team members", len(o.TeamSnapshot.ValidAgentKeys)))
		return false, nil
	}

	prepared := make([]PreparedSubTask, 0, len(dispatch.Tasks))
	seenMembers := make(map[string]struct{}, len(dispatch.Tasks))
	for _, spec := range dispatch.Tasks {
		memberKey := strings.TrimSpace(spec.SubAgentKey)
		if SameAgentKey(memberKey, o.Session.AgentKey) {
			o.InjectMainToolError(main, dispatch.MainToolID, "agent_delegate cannot target the current agent")
			return false, nil
		}
		lookupKey := strings.ToLower(memberKey)
		if _, duplicate := seenMembers[lookupKey]; duplicate {
			o.InjectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q may only appear once in agent_delegate", memberKey))
			return false, nil
		}
		seenMembers[lookupKey] = struct{}{}
		if !o.TeamSnapshot.HasAgent(memberKey) {
			o.InjectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q is unavailable in Team %q", memberKey, o.TeamSnapshot.TeamID))
			return false, nil
		}
		def, found := o.TeamSnapshot.AgentDefinition(memberKey)
		if !found {
			o.InjectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q is unavailable in Team %q", memberKey, o.TeamSnapshot.TeamID))
			return false, nil
		}
		if !catalog.AgentUsesACPCoderBackend(def) && !session.ResolvedModeCapabilities(def).RunAsChild {
			o.InjectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q cannot run as a Team child", memberKey))
			return false, nil
		}
		if ContainsInvokeAgentsTool(def.Tools) {
			o.InjectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q cannot invoke nested sub-agents", memberKey))
			return false, nil
		}
		o.TaskCounter++
		index := o.TaskCounter
		requestID := fmt.Sprintf("%s_team_%d", FirstNonEmpty(o.Session.RequestID, o.Session.RunID), index)
		taskName := strings.TrimSpace(spec.TaskName)
		if taskName == "" {
			taskName = FirstNonEmpty(def.Name, memberKey)
		}
		taskText := strings.TrimSpace(spec.TaskText)
		if taskText == "" {
			taskText = o.Request.Message
		}
		prepared = append(prepared, PreparedSubTask{
			Spec: contracts.SubAgentTaskSpec{
				SubAgentKey: memberKey,
				TaskText:    taskText,
				TaskName:    taskName,
				Files:       append([]string(nil), spec.Files...),
			},
			AgentDef:     def,
			TaskID:       fmt.Sprintf("%s_team_t_%d", strings.TrimSpace(o.Session.RunID), index),
			RequestID:    requestID,
			SubTaskID:    fmt.Sprintf("team_%d", index),
			MainToolID:   dispatch.MainToolID,
			TeamID:       o.TeamSnapshot.TeamID,
			Presentation: "task",
		})
	}

	for _, task := range prepared {
		o.EmitDelta(contracts.DeltaTaskLifecycle{
			Kind:         "start",
			TaskID:       task.TaskID,
			RunID:        o.Session.RunID,
			TaskName:     task.Spec.TaskName,
			SubAgentKey:  task.Spec.SubAgentKey,
			MainToolID:   dispatch.MainToolID,
			TeamID:       o.TeamSnapshot.TeamID,
			Presentation: "task",
		})
	}

	var principal *contracts.AuthIdentity
	if strings.TrimSpace(o.Session.Subject) != "" {
		principal = &contracts.AuthIdentity{Subject: o.Session.Subject}
	}
	maxParallel := o.TeamSnapshot.Orchestrator.MaxParallel
	if maxParallel < 1 || maxParallel > contracts.MaxInvokeAgentTasks {
		maxParallel = contracts.MaxInvokeAgentTasks
	}
	sem := make(chan struct{}, maxParallel)
	results := make([]ChildTaskResult, len(prepared))
	routedCh := make(chan ChildRouteEvent, 32)
	hitlBatch := NewTeamMergedHITLBatch(o, prepared, true, maxParallel)
	var wg sync.WaitGroup
	for index, task := range prepared {
		wg.Add(1)
		go func(index int, task PreparedSubTask) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-o.RunCtx.Done():
				routedCh <- ChildRouteEvent{Result: &ChildTaskResult{Index: index, TaskID: task.TaskID, TaskName: task.Spec.TaskName, SubAgentKey: task.Spec.SubAgentKey, Status: "cancelled", Text: "Team member interrupted"}}
				return
			}
			options := ChildRunOptions{InheritOriginalContext: true, IncludeHistory: true, Presentation: "task", SuppressFinalDuplicate: true, RunControl: hitlBatch.ControlFor(task)}
			routedCh <- ChildRouteEvent{Result: o.RunChildTaskWithOptions(index, task, principal, func(input stream.StreamInput) {
				if event, captured := hitlBatch.Capture(task, input); captured {
					routedCh <- event
					return
				}
				routedCh <- ChildRouteEvent{Input: RouteTeamChildStreamInput(o.Session.RunID, o.TeamSnapshot.TeamID, task, input, options)}
			}, options)}
		}(index, task)
	}
	go func() {
		wg.Wait()
		close(routedCh)
	}()
	for routed := range routedCh {
		if routed.Input != nil && o.EmitInputs != nil {
			o.EmitInputs(routed.Input)
		}
		if routed.Result == nil {
			hitlBatch.Observe(routed)
			continue
		}
		results[routed.Result.Index] = *routed.Result
		task := prepared[routed.Result.Index]
		terminalKind := "complete"
		if routed.Result.Status == "failed" {
			terminalKind = "error"
		} else if routed.Result.Status == "cancelled" {
			terminalKind = "cancel"
		}
		lifecycle := contracts.DeltaTaskLifecycle{Kind: terminalKind, TaskID: routed.Result.TaskID, SubAgentKey: routed.Result.SubAgentKey, TeamID: task.TeamID, Presentation: task.Presentation}
		if terminalKind == "error" {
			lifecycle.Error = apperrors.Payload(
				apperrors.Code(FirstNonEmpty(routed.Result.ErrorCode, string(apperrors.CodeTeamMemberFailed))),
				FirstNonEmpty(routed.Result.Error, routed.Result.Text),
				apperrors.WithScope(apperrors.ScopeTask),
				apperrors.WithCategory(apperrors.CategorySystem),
			)
		}
		o.EmitDelta(lifecycle)
		hitlBatch.Observe(routed)
	}

	toolResults := make([]teamDelegateMemberResult, 0, len(results))
	anyFailed := false
	for _, result := range results {
		item := teamDelegateMemberResult{
			AgentKey:  result.SubAgentKey,
			TaskName:  result.TaskName,
			Status:    result.Status,
			Content:   result.Text,
			Error:     result.Error,
			ErrorCode: result.ErrorCode,
		}
		if result.Status != "completed" {
			anyFailed = true
			item.Content = ""
			if strings.TrimSpace(item.Error) == "" {
				item.Error = result.Text
			}
		}
		toolResults = append(toolResults, item)
	}
	aggregated, err := json.Marshal(TeamDelegateToolResult{Results: toolResults})
	if err != nil {
		o.InjectMainToolError(main, dispatch.MainToolID, err.Error())
		return false, nil
	}
	if !main.InjectToolResult(dispatch.MainToolID, string(aggregated), anyFailed) {
		return false, fmt.Errorf("TEAM coordinator rejected dispatch result")
	}
	return false, nil
}

func NewTeamMergedHITLBatch(o *Coordinator, tasks []PreparedSubTask, enabled bool, maxParallel int) *TeamMergedHITLBatch {
	if maxParallel < 1 || maxParallel > len(tasks) {
		maxParallel = len(tasks)
	}
	batch := &TeamMergedHITLBatch{
		Orchestrator: o,
		Tasks:        append([]PreparedSubTask(nil), tasks...),
		WaveSize:     maxParallel,
		Controls:     map[string]*contracts.RunControl{},
		Waiting:      map[string]*TeamChildAwaiting{},
		Completed:    map[string]bool{},
	}
	if !enabled || o == nil || contracts.RunControlFromContext(o.RunCtx) == nil {
		return batch
	}
	batch.Enabled = true
	for _, task := range tasks {
		control := contracts.NewRunControl(o.RunCtx, task.TaskID)
		control.SetInitialAccessLevel(o.Session.AccessLevel)
		batch.Controls[task.TaskID] = control
	}
	return batch
}

func (b *TeamMergedHITLBatch) ControlFor(task PreparedSubTask) *contracts.RunControl {
	if b == nil || !b.Enabled {
		return nil
	}
	return b.Controls[task.TaskID]
}

func (b *TeamMergedHITLBatch) Capture(task PreparedSubTask, input stream.StreamInput) (ChildRouteEvent, bool) {
	if b == nil || !b.Enabled {
		return ChildRouteEvent{}, false
	}
	switch value := input.(type) {
	case stream.AwaitAsk:
		rawID := rawAwaitingIDForTask(task.TaskID, value.AwaitingID)
		publicID := NamespaceChildID(task.TaskID, rawID)
		if publicID == "" || rawID == "" {
			return ChildRouteEvent{}, false
		}
		return ChildRouteEvent{Awaiting: &TeamChildAwaiting{
			Task:     task,
			Control:  b.Controls[task.TaskID],
			Ask:      value,
			RawID:    rawID,
			PublicID: publicID,
		}}, true
	case stream.RequestSubmit:
		// The Team-level request.submit is the sole public submit event. Child
		// submits remain local to their isolated controls.
		return ChildRouteEvent{}, true
	case stream.AwaitingAnswer:
		rawID := rawAwaitingIDForTask(task.TaskID, value.AwaitingID)
		return ChildRouteEvent{AwaitingDone: NamespaceChildID(task.TaskID, rawID)}, true
	default:
		return ChildRouteEvent{}, false
	}
}

func (b *TeamMergedHITLBatch) Observe(event ChildRouteEvent) {
	if b == nil || !b.Enabled {
		return
	}
	if event.Awaiting != nil {
		b.Waiting[event.Awaiting.Task.TaskID] = event.Awaiting
	}
	if doneID := strings.TrimSpace(event.AwaitingDone); doneID != "" {
		for taskID, pending := range b.Waiting {
			if pending != nil && pending.PublicID == doneID {
				delete(b.Waiting, taskID)
				break
			}
		}
	}
	if event.Result != nil {
		b.Completed[event.Result.TaskID] = true
		delete(b.Waiting, event.Result.TaskID)
	}
	readyWave := b.WaveSize > 0 && len(b.Waiting) >= b.WaveSize
	allSettled := len(b.Completed)+len(b.Waiting) == len(b.Tasks)
	if len(b.Waiting) == 0 || (!readyWave && !allSettled) {
		return
	}
	b.ResolveWaiting()
}

func (b *TeamMergedHITLBatch) ResolveWaiting() {
	if b == nil || !b.Enabled || b.Orchestrator == nil {
		return
	}
	pending := make([]*TeamChildAwaiting, 0, len(b.Waiting))
	for _, task := range b.Tasks {
		if item := b.Waiting[task.TaskID]; item != nil {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return
	}
	for _, item := range pending {
		delete(b.Waiting, item.Task.TaskID)
	}

	o := b.Orchestrator
	parentControl := contracts.RunControlFromContext(o.RunCtx)
	if parentControl == nil {
		return
	}
	o.TeamAwaitCounter++
	mergedID := fmt.Sprintf("%s_team_await_%d", strings.TrimSpace(o.Session.RunID), o.TeamAwaitCounter)
	forms, routes, timeoutSeconds := TeamMergedAwaitingDefinition(pending)
	var approvals []any
	for _, item := range pending {
		if item != nil {
			approvals = append(approvals, item.Ask.Approvals...)
		}
	}
	summaries, truncated := contracts.SummarizeApprovals(approvals)
	parentControl.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID:         mergedID,
		Summaries:          summaries,
		SummariesTruncated: truncated,
		Mode:               "form",
		ItemCount:          len(routes),
		Routes:             routes,
		NoTimeout:          timeoutSeconds == 0,
		Timeout:            timeoutSeconds,
	})
	parentControl.TransitionState(contracts.RunLoopStateWaitingSubmit)
	if o.EmitInputs != nil {
		o.EmitInputs(stream.AwaitAsk{
			AwaitingID:   mergedID,
			Mode:         "form",
			Timeout:      timeoutSeconds,
			RunID:        o.Session.RunID,
			ViewportType: "html",
			ViewportKey:  "team-hitl",
			Forms:        forms,
		})
	}

	startedAt := time.Now()
	wait := time.Duration(timeoutSeconds) * time.Second
	result, err := parentControl.AwaitSubmitWithTimeout(o.RunCtx, mergedID, wait)
	if err != nil {
		if errors.Is(err, contracts.ErrRunInterrupted) || errors.Is(err, context.Canceled) {
			return
		}
		if o.EmitInputs != nil {
			o.EmitInputs(stream.AwaitingAnswer{
				AwaitingID: mergedID,
				Answer:     contracts.AwaitingTimeoutAnswer("form", timeoutSeconds, int64(time.Since(startedAt).Seconds())),
			})
		}
		TeamDistributeMergedSubmit(pending, nil, queryinput.SubmitRequest{RunID: o.Session.RunID, TeamID: o.Session.TeamID})
		parentControl.TransitionState(contracts.RunLoopStateToolExecuting)
		return
	}

	if o.EmitInputs != nil {
		o.EmitInputs(stream.RequestSubmit{
			RequestID:  o.Session.RequestID,
			ChatID:     o.Session.ChatID,
			RunID:      o.Session.RunID,
			AwaitingID: mergedID,
			SubmitID:   result.Request.SubmitID,
			Params:     result.Request.Params,
		})
		o.EmitInputs(stream.AwaitingAnswer{
			AwaitingID: mergedID,
			Answer:     TeamMergedAwaitingAnswer(result.Request.Params, result.Request.SubmitID),
		})
	}
	TeamDistributeMergedSubmit(pending, result.Request.Params, result.Request)
	parentControl.TransitionState(contracts.RunLoopStateToolExecuting)
}

func TeamMergedAwaitingDefinition(pending []*TeamChildAwaiting) ([]any, []contracts.AwaitingSubmitRoute, int64) {
	forms := make([]any, 0, len(pending))
	routes := make([]contracts.AwaitingSubmitRoute, 0, len(pending))
	var timeoutSeconds int64
	for _, item := range pending {
		if item == nil {
			continue
		}
		definition := map[string]any{
			"taskId":     item.Task.TaskID,
			"awaitingId": item.RawID,
			"mode":       item.Ask.Mode,
		}
		if item.Ask.View != nil {
			definition["view"] = item.Ask.View.Map()
		}
		if item.Ask.ViewError != "" {
			definition["viewError"] = item.Ask.ViewError
		}
		if len(item.Ask.Questions) > 0 {
			definition["questions"] = append([]any(nil), item.Ask.Questions...)
		}
		if len(item.Ask.Approvals) > 0 {
			definition["approvals"] = append([]any(nil), item.Ask.Approvals...)
		}
		if len(item.Ask.Forms) > 0 {
			definition["forms"] = append([]any(nil), item.Ask.Forms...)
		}
		if len(item.Ask.Planning) > 0 {
			definition["planning"] = contracts.CloneMap(item.Ask.Planning)
		}
		forms = append(forms, map[string]any{
			"id":         item.PublicID,
			"title":      FirstNonEmpty(item.Task.Spec.TaskName, item.Task.Spec.SubAgentKey),
			"taskId":     item.Task.TaskID,
			"awaitingId": item.RawID,
			"mode":       item.Ask.Mode,
			"form":       definition,
		})
		routes = append(routes, contracts.AwaitingSubmitRoute{
			FieldID:    item.PublicID,
			TaskID:     item.Task.TaskID,
			AwaitingID: item.RawID,
			Mode:       item.Ask.Mode,
			ItemCount:  TeamAwaitingItemCount(item.Ask),
			Questions:  append([]any(nil), item.Ask.Questions...),
		})
		if item.Ask.Timeout > 0 && (timeoutSeconds == 0 || item.Ask.Timeout < timeoutSeconds) {
			timeoutSeconds = item.Ask.Timeout
		}
	}
	return forms, routes, timeoutSeconds
}

func TeamAwaitingItemCount(ask stream.AwaitAsk) int {
	switch strings.ToLower(strings.TrimSpace(ask.Mode)) {
	case "question":
		return len(ask.Questions)
	case "approval":
		return len(ask.Approvals)
	case "form":
		return len(ask.Forms)
	case "planning":
		if len(ask.Planning) > 0 {
			return 1
		}
	}
	return 0
}

func TeamDistributeMergedSubmit(pending []*TeamChildAwaiting, merged queryinput.SubmitParams, parent queryinput.SubmitRequest) {
	items, _ := queryinput.DecodeSubmitParams(merged)
	for index, child := range pending {
		if child == nil || child.Control == nil {
			continue
		}
		var params queryinput.SubmitParams
		if index < len(items) {
			params = TeamChildSubmitParams(child.Ask, items[index])
		}
		child.Control.ResolveSubmit(queryinput.SubmitRequest{
			ChatID:     parent.ChatID,
			RunID:      parent.RunID,
			AgentKey:   child.Task.Spec.SubAgentKey,
			TeamID:     parent.TeamID,
			AwaitingID: child.RawID,
			SubmitID:   parent.SubmitID,
			Locale:     parent.Locale,
			Params:     params,
		})
	}
}

func TeamChildSubmitParams(ask stream.AwaitAsk, item map[string]any) queryinput.SubmitParams {
	decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
	if decision == "approve" {
		form := contracts.AnyMapNode(item["form"])
		params, err := queryinput.EncodeSubmitParams(form["params"])
		if err == nil {
			return params
		}
		return nil
	}
	return TeamRejectedChildParams(ask)
}

func TeamRejectedChildParams(ask stream.AwaitAsk) queryinput.SubmitParams {
	var items []map[string]any
	appendRejected := func(raw []any) {
		for _, value := range raw {
			definition := contracts.AnyMapNode(value)
			item := map[string]any{"decision": "reject"}
			if id := strings.TrimSpace(contracts.AnyStringNode(definition["id"])); id != "" {
				item["id"] = id
			}
			items = append(items, item)
		}
	}
	switch strings.ToLower(strings.TrimSpace(ask.Mode)) {
	case "approval":
		appendRejected(ask.Approvals)
	case "form":
		appendRejected(ask.Forms)
	case "planning":
		item := map[string]any{"decision": "reject"}
		if id := strings.TrimSpace(contracts.AnyStringNode(ask.Planning["id"])); id != "" {
			item["id"] = id
		}
		items = append(items, item)
	default:
		return nil
	}
	params, err := queryinput.EncodeSubmitParams(items)
	if err != nil {
		return nil
	}
	return params
}

func TeamMergedAwaitingAnswer(params queryinput.SubmitParams, submitID string) map[string]any {
	items, _ := queryinput.DecodeSubmitParams(params)
	if len(items) == 0 {
		return contracts.AwaitingErrorAnswer("form", "user_dismissed", "用户关闭等待项")
	}
	forms := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{
			"id":       strings.TrimSpace(contracts.AnyStringNode(item["id"])),
			"decision": strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"]))),
		}
		if form := contracts.AnyMapNode(item["form"]); len(form) > 0 {
			entry["form"] = form
		}
		forms = append(forms, entry)
	}
	answer := map[string]any{"mode": "form", "status": "answered", "forms": forms}
	if strings.TrimSpace(submitID) != "" {
		answer["submitId"] = strings.TrimSpace(submitID)
	}
	return answer
}

func (o *Coordinator) RunChildTaskWithOptions(index int, task PreparedSubTask, principal *contracts.AuthIdentity, route func(stream.StreamInput), options ChildRunOptions) *ChildTaskResult {
	result := &ChildTaskResult{
		Index:       index,
		TaskID:      task.TaskID,
		TaskName:    task.Spec.TaskName,
		SubAgentKey: task.Spec.SubAgentKey,
		Status:      "completed",
	}

	leasedDef, release, ok := catalogview.AcquireAgent(o.Registry, task.Spec.SubAgentKey)
	if !ok {
		result.Status = "failed"
		result.Error = "Agent runtime is unavailable"
		result.Text = result.Error
		return result
	}
	defer release()
	if o.Session.TeamID == "" {
		task.AgentDef = leasedDef
	}

	if catalog.AgentUsesACPCoderBackend(task.AgentDef) {
		result.Status = "failed"
		result.Text = "ACP CODER sub-agent is not supported"
		result.Error = result.Text
		return result
	}

	subReq := runtimetypes.QueryCommand{
		RequestID:   task.RequestID,
		RunID:       o.Session.RunID,
		ChatID:      o.Session.ChatID,
		AgentKey:    task.Spec.SubAgentKey,
		TeamID:      o.Session.TeamID,
		Role:        queryinput.QueryRoleUser,
		Message:     task.Spec.TaskText,
		AccessLevel: o.Session.AccessLevel,
	}
	if options.InheritOriginalContext {
		subReq.Role = o.Request.Role
		if strings.TrimSpace(subReq.Role) == "" {
			subReq.Role = queryinput.QueryRoleUser
		}
		subReq.Scene = o.Request.Scene
	}
	baseReferences := []runtimetypes.Reference(nil)
	if options.InheritOriginalContext {
		baseReferences = append(baseReferences, o.Request.References...)
	}
	if len(task.Spec.Files) > 0 || len(baseReferences) > 0 {
		references, err := proxy.PrepareReferences(o.Chats, o.ResourceTickets, proxy.ReferenceOptions{
			ChatID:          subReq.ChatID,
			RunID:           subReq.RunID,
			Subject:         o.Session.Subject,
			ResourceBaseURL: o.ResourceBaseURL,
			WorkspaceRoot:   o.Session.WorkspaceRoot,
			References:      baseReferences,
			Files:           task.Spec.Files,
		})
		if err != nil {
			result.Status = "failed"
			result.Text = err.Error()
			result.Error = err.Error()
			return result
		}
		subReq.References = DeduplicateTeamReferences(references)
	} else {
		subReq.References = DeduplicateTeamReferences(baseReferences)
	}

	childRunCtx := o.RunCtx
	if options.RunControl != nil {
		childRunCtx = contracts.WithRunControl(options.RunControl.Context(), options.RunControl)
	}
	subSession, err := o.BuildQuerySession(childRunCtx, subReq, o.Summary, task.AgentDef, session.Options{
		Created:           false,
		IncludeHistory:    options.IncludeHistory,
		IncludeMemory:     false,
		AllowInvokeAgents: false,
		SubTaskID:         task.SubTaskID,
		Identity:          principal,
		TeamHistoryAgentKey: func() string {
			if options.InheritOriginalContext {
				return task.Spec.SubAgentKey
			}
			return ""
		}(),
	})
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	subSession.WebClientTarget = o.Session.WebClientTarget
	if len(subSession.RuntimeContext.References) > 0 {
		subReq.References = subSession.RuntimeContext.References
	}
	o.WriteChildTaskQueryAndSystem(subReq, &subSession, task)

	if session.IsProxyAgentMode(task.AgentDef.Mode) {
		return o.RunProxyChildTask(result, subReq, subSession.WorkspaceRoot, task, route)
	}

	subStream, err := o.Agent.Stream(childRunCtx, subReq, subSession)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	defer subStream.Close()

	childMapper := o.Mapper.CloneIsolated(task.TaskID, o.Session.ChatID)
	if childMapper == nil {
		result.Status = "failed"
		result.Text = "sub-agent delta mapper is unavailable"
		result.Error = result.Text
		return result
	}

	sawContent := false
	for {
		delta, nextErr := subStream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if contracts.IsRunInterrupted(nextErr) {
			result.Status = "cancelled"
			result.Text = "sub-agent interrupted"
			return result
		}
		if nextErr != nil {
			result.Status = "failed"
			result.Text = nextErr.Error()
			result.Error = nextErr.Error()
			return result
		}

		switch value := delta.(type) {
		case contracts.DeltaInvokeSubAgents:
			result.Status = "failed"
			result.Text = "nested sub-agent invocation is not allowed"
			result.Error = result.Text
			return result
		case contracts.DeltaFinishReason, contracts.DeltaRunCancel:
			continue
		case contracts.DeltaError:
			result.Status = "failed"
			result.Text = ErrorMessage(value.Error)
			result.Error = result.Text
			return result
		default:
			for _, input := range childMapper.Map(delta) {
				if _, ok := input.(stream.ContentDelta); ok {
					sawContent = true
				}
				route(RouteChildStreamInput(o.Session.RunID, task.TaskID, input))
			}
		}
	}

	child, ok := subStream.(contracts.OrchestratableAgentStream)
	if !ok {
		result.Status = "failed"
		result.Text = "sub-agent stream does not expose final assistant content"
		result.Error = result.Text
		return result
	}
	text, ok := child.FinalAssistantContent()
	if !ok || strings.TrimSpace(text) == "" {
		result.Status = "failed"
		result.Text = "sub-agent produced no final assistant content"
		result.Error = result.Text
		return result
	}
	if !options.SuppressFinalDuplicate || !sawContent {
		route(RouteChildStreamInput(o.Session.RunID, task.TaskID, stream.ContentDelta{
			ContentID: task.TaskID + ":final",
			TaskID:    task.TaskID,
			Delta:     text,
		}))
	}
	result.Text = text
	return result
}

func (o *Coordinator) WriteChildTaskQueryAndSystem(subReq runtimetypes.QueryCommand, subSession *contracts.QuerySession, task PreparedSubTask) {
	if o.Chats == nil {
		return
	}
	var system *chat.QueryLineSystem
	if subSession != nil && o.PrepareSystemInit != nil {
		o.SystemInitMu.Lock()
		defer o.SystemInitMu.Unlock()
		system, _ = o.PrepareSystemInit(subReq, subSession, false)
	}
	var liveSeq int64
	if o.CurrentLiveSeq != nil {
		liveSeq = o.CurrentLiveSeq()
	}
	_ = o.Chats.AppendQueryLine(o.Summary.ChatID, chat.QueryLine{
		Type:         "query",
		ChatID:       o.Summary.ChatID,
		RunID:        o.Session.RunID,
		UpdatedAt:    time.Now().UnixMilli(),
		LiveSeq:      liveSeq,
		TaskID:       task.TaskID,
		TaskName:     task.Spec.TaskName,
		TaskToolID:   task.MainToolID,
		SubAgentKey:  task.Spec.SubAgentKey,
		TeamID:       task.TeamID,
		Presentation: task.Presentation,
		Query: map[string]any{
			"message":   task.Spec.TaskText,
			"agentKey":  task.Spec.SubAgentKey,
			"chatId":    o.Summary.ChatID,
			"runId":     o.Session.RunID,
			"requestId": task.RequestID,
			"role":      FirstNonEmpty(subReq.Role, queryinput.QueryRoleUser),
		},
		Messages: CurrentMessagesFromSession(subSession),
		System:   system,
	})
}

func CurrentMessagesFromSession(session *contracts.QuerySession) []map[string]any {
	if session == nil {
		return nil
	}
	return session.CurrentMessages
}

func (o *Coordinator) InjectMainToolError(main contracts.OrchestratableAgentStream, toolID string, message string) {
	_ = main.InjectToolResult(toolID, message, true)
}

func ContainsInvokeAgentsTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		if strings.EqualFold(strings.TrimSpace(toolName), contracts.InvokeAgentsToolName) {
			return true
		}
	}
	return false
}

func SameAgentKey(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && left == right
}

func RouteChildStreamInput(parentRunID string, taskID string, input stream.StreamInput) stream.StreamInput {
	switch value := input.(type) {
	case stream.ReasoningDelta:
		value.TaskID = taskID
		return value
	case stream.ContentDelta:
		value.TaskID = taskID
		return value
	case stream.ToolArgs:
		value.TaskID = taskID
		value.ToolID = NamespaceChildID(taskID, value.ToolID)
		if value.AwaitAsk != nil {
			awaitCopy := *value.AwaitAsk
			awaitCopy.RunID = FirstNonEmpty(parentRunID, awaitCopy.RunID)
			awaitCopy.TaskID = taskID
			awaitCopy.AwaitingID = NamespaceChildID(taskID, awaitCopy.AwaitingID)
			value.AwaitAsk = &awaitCopy
		}
		return value
	case stream.ToolWait:
		value.TaskID = taskID
		value.ToolID = NamespaceChildID(taskID, value.ToolID)
		return value
	case stream.ToolEnd:
		value.ToolID = NamespaceChildID(taskID, value.ToolID)
		return value
	case stream.ToolResult:
		value.ToolID = NamespaceChildID(taskID, value.ToolID)
		return value
	case stream.SourcePublish:
		value.TaskID = taskID
		value.PublishID = NamespaceChildID(taskID, value.PublishID)
		value.ToolID = NamespaceChildID(taskID, value.ToolID)
		return value
	case stream.ArtifactPublish:
		value.TaskID = taskID
		value.ToolID = NamespaceChildID(taskID, value.ToolID)
		return value
	case stream.AwaitAsk:
		value.RunID = FirstNonEmpty(parentRunID, value.RunID)
		value.TaskID = taskID
		value.AwaitingID = NamespaceChildID(taskID, value.AwaitingID)
		return value
	case stream.RequestSubmit:
		value.RunID = FirstNonEmpty(parentRunID, value.RunID)
		value.TaskID = taskID
		value.AwaitingID = NamespaceChildID(taskID, value.AwaitingID)
		return value
	case stream.AwaitingAnswer:
		value.TaskID = taskID
		value.AwaitingID = NamespaceChildID(taskID, value.AwaitingID)
		return value
	case stream.InputDebugLLMChat:
		value.TaskID = taskID
		return value
	case stream.InputLLMRequest:
		value.TaskID = taskID
		return value
	case stream.InputUsageSnapshot:
		value.TaskID = taskID
		return value
	case stream.InputRunActivity:
		value.TaskID = taskID
		return value
	case stream.ModelTurnCommit:
		value.TaskID = taskID
		return value
	case stream.ModelTurnDiscard:
		value.TaskID = taskID
		value.ToolIDs = NamespaceChildIDs(taskID, value.ToolIDs)
		return value
	default:
		return input
	}
}

func RouteTeamChildStreamInput(_ string, teamID string, task PreparedSubTask, input stream.StreamInput, options ChildRunOptions) stream.StreamInput {
	switch value := input.(type) {
	case stream.ToolArgs:
		value.TaskID = task.TaskID
		value.ToolID = NamespaceChildID(task.TaskID, value.ToolID)
		if value.AwaitAsk != nil {
			awaitCopy := *value.AwaitAsk
			awaitCopy.TaskID = task.TaskID
			awaitCopy.AwaitingID = NamespaceChildID(task.TaskID, awaitCopy.AwaitingID)
			value.AwaitAsk = &awaitCopy
		}
		return value
	case stream.ToolWait:
		value.TaskID = task.TaskID
		value.ToolID = NamespaceChildID(task.TaskID, value.ToolID)
		return value
	case stream.ToolEnd:
		value.ToolID = NamespaceChildID(task.TaskID, value.ToolID)
		return value
	case stream.ToolResult:
		value.ToolID = NamespaceChildID(task.TaskID, value.ToolID)
		return value
	case stream.ArtifactPublish:
		value.TaskID = task.TaskID
		value.ToolID = NamespaceChildID(task.TaskID, value.ToolID)
		return value
	case stream.ContentDelta:
		value.ActorType = "agent"
		value.TeamID = strings.TrimSpace(teamID)
		value.AgentKey = strings.TrimSpace(task.Spec.SubAgentKey)
		value.Presentation = FirstNonEmpty(options.Presentation, "task")
		return value
	case stream.InputLLMRequest:
		value.ActorType = "agent"
		value.TeamID = strings.TrimSpace(teamID)
		value.AgentKey = strings.TrimSpace(task.Spec.SubAgentKey)
		value.Presentation = FirstNonEmpty(options.Presentation, "task")
		return value
	default:
		return input
	}
}

func NamespaceChildID(taskID string, rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return ""
	}
	return taskID + ":" + rawID
}

func NamespaceChildIDs(taskID string, rawIDs []string) []string {
	if len(rawIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(rawIDs))
	for _, rawID := range rawIDs {
		if namespaced := NamespaceChildID(taskID, rawID); namespaced != "" {
			out = append(out, namespaced)
		}
	}
	return out
}

func DeduplicateTeamReferences(references []runtimetypes.Reference) []runtimetypes.Reference {
	runtimeReferences := references
	deduplicated := DeduplicateReferences(runtimeReferences)
	return deduplicated
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ErrorMessage(payload map[string]any) string {
	if payload == nil {
		return "sub-agent failed"
	}
	if message := FirstPayloadString(payload, "message", "error", "reason", "detail", "msg"); message != "" {
		return message
	}
	for _, key := range []string{"error", "rawEvent"} {
		if nested, ok := payload[key].(map[string]any); ok {
			if message := FirstPayloadString(nested, "message", "error", "reason", "detail", "msg"); message != "" {
				return message
			}
		}
	}
	if data, err := json.Marshal(payload); err == nil && len(data) > 0 {
		return "sub-agent failed: " + string(data)
	}
	return "sub-agent failed"
}

func FirstPayloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := payload[key].(string)
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func rawAwaitingIDForTask(taskID string, awaitingID string) string {
	taskID = strings.TrimSpace(taskID)
	awaitingID = strings.TrimSpace(awaitingID)
	if taskID == "" || awaitingID == "" {
		return awaitingID
	}
	prefix := taskID + ":"
	if !strings.HasPrefix(awaitingID, prefix) {
		return awaitingID
	}
	return strings.TrimSpace(strings.TrimPrefix(awaitingID, prefix))
}

func (o *Coordinator) RunProxyChildTask(result *ChildTaskResult, command runtimetypes.QueryCommand, workspace string, task PreparedSubTask, route func(stream.StreamInput)) *ChildTaskResult {
	child := proxy.RunChild(o.RunCtx, o.Session.RunID, task.TaskID, command, workspace, task.AgentDef.ProxyConfig, route)
	result.Status, result.Text, result.Error, result.ErrorCode = child.Status, child.Text, child.Error, child.ErrorCode
	return result
}
