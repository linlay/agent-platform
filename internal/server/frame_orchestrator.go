package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

type frameOrchestrator struct {
	runCtx            context.Context
	request           api.QueryRequest
	session           contracts.QuerySession
	summary           chat.Summary
	agent             contracts.AgentEngine
	registry          catalog.Registry
	teamSnapshot      *catalog.TeamSnapshot
	buildQuerySession func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error)
	chats             chat.Store
	resourceBaseURL   string
	resourceTickets   *ResourceTicketService
	prepareSystemInit func(api.QueryRequest, *contracts.QuerySession, bool) (*chat.QueryLineSystem, error)
	systemInitMu      sync.Mutex
	mapper            contracts.StreamDeltaMapper
	emitDelta         func(contracts.AgentDelta)
	emitInputs        func(...stream.StreamInput)
	nextLiveSeq       func() int64
	taskCounter       int
	teamAwaitCounter  int
}

func (o *frameOrchestrator) Run(mainStream contracts.AgentStream) (bool, bool, error) {
	for {
		delta, nextErr := mainStream.Next()
		if errors.Is(nextErr, io.EOF) {
			return false, false, nil
		}
		if contracts.IsRunInterrupted(nextErr) {
			return false, true, nil
		}
		if nextErr != nil {
			return true, false, nextErr
		}

		switch value := delta.(type) {
		case contracts.DeltaInvokeSubAgents:
			if err := o.handleSubAgentBatch(mainStream, value); err != nil {
				return true, false, err
			}
		case contracts.DeltaTeamDispatch:
			terminal, err := o.handleTeamDispatch(mainStream, value)
			if err != nil {
				return true, false, err
			}
			if terminal {
				return false, false, nil
			}
		default:
			o.emitDelta(delta)
		}
	}
}

type childTaskResult struct {
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

type teamDelegateToolResult struct {
	Results []teamDelegateMemberResult `json:"results"`
}

type childRouteEvent struct {
	input        stream.StreamInput
	result       *childTaskResult
	awaiting     *teamChildAwaiting
	awaitingDone string
}

type preparedSubTask struct {
	spec         contracts.SubAgentTaskSpec
	agentDef     catalog.AgentDefinition
	taskID       string
	requestID    string
	subTaskID    string
	mainToolID   string
	teamID       string
	presentation string
}

type childRunOptions struct {
	InheritOriginalContext bool
	IncludeHistory         bool
	Presentation           string
	SuppressFinalDuplicate bool
	RunControl             *contracts.RunControl
}

type teamChildAwaiting struct {
	Task     preparedSubTask
	Control  *contracts.RunControl
	Ask      stream.AwaitAsk
	RawID    string
	PublicID string
}

type teamMergedHITLBatch struct {
	orchestrator *frameOrchestrator
	tasks        []preparedSubTask
	enabled      bool
	waveSize     int
	controls     map[string]*contracts.RunControl
	waiting      map[string]*teamChildAwaiting
	completed    map[string]bool
}

func (o *frameOrchestrator) handleSubAgentBatch(mainStream contracts.AgentStream, invoke contracts.DeltaInvokeSubAgents) error {
	main, ok := mainStream.(contracts.OrchestratableAgentStream)
	if !ok {
		return fmt.Errorf("main agent stream does not support sub-agent orchestration")
	}
	if o.registry == nil || o.buildQuerySession == nil || o.mapper == nil {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is not configured")
		return nil
	}
	if o.session.TeamRuntime != nil {
		o.injectMainToolError(main, invoke.MainToolID, "TEAM coordinators must use agent_delegate instead of agent_invoke")
		return nil
	}
	if !o.session.ModeCapabilities.InvokeChildren {
		o.injectMainToolError(main, invoke.MainToolID, "sub-agent orchestration is only supported for REACT/ONESHOT/CODER main agents")
		return nil
	}
	if len(invoke.Tasks) < 1 || len(invoke.Tasks) > contracts.MaxInvokeAgentTasks {
		o.injectMainToolError(main, invoke.MainToolID, fmt.Sprintf("invalid agent_invoke call: tasks must contain between 1 and %d items", contracts.MaxInvokeAgentTasks))
		return nil
	}
	if strings.TrimSpace(o.session.TeamID) != "" && o.teamSnapshot == nil {
		resolved, found := resolveCatalogTeam(o.registry, o.session.TeamID)
		if !found {
			o.injectMainToolError(main, invoke.MainToolID, fmt.Sprintf("team is unavailable: %s", o.session.TeamID))
			return nil
		}
		o.teamSnapshot = &resolved
	}
	prepared := make([]preparedSubTask, 0, len(invoke.Tasks))
	for _, task := range invoke.Tasks {
		subAgentKey := strings.TrimSpace(task.SubAgentKey)
		taskText := strings.TrimSpace(task.TaskText)
		taskName := strings.TrimSpace(task.TaskName)
		if taskName == "" {
			taskName = subAgentKey
		}
		if subAgentKey == "" || taskText == "" {
			o.injectMainToolError(main, invoke.MainToolID, "invalid agent_invoke call: every task requires subAgentKey and task")
			return nil
		}
		var agentDef catalog.AgentDefinition
		var found bool
		if o.teamSnapshot != nil {
			if !o.teamSnapshot.HasAgent(subAgentKey) {
				message := fmt.Sprintf("sub-agent %q is not in team %q", subAgentKey, o.teamSnapshot.TeamID)
				if o.teamSnapshot.DeclaresAgent(subAgentKey) {
					message = fmt.Sprintf("sub-agent %q is unavailable in team %q", subAgentKey, o.teamSnapshot.TeamID)
				}
				o.injectMainToolError(main, invoke.MainToolID, message)
				return nil
			}
			agentDef, found = o.teamSnapshot.AgentDefinition(subAgentKey)
			if !found {
				o.injectMainToolError(main, invoke.MainToolID, fmt.Sprintf("sub-agent %q is unavailable in team %q", subAgentKey, o.teamSnapshot.TeamID))
				return nil
			}
		} else {
			agentDef, found = o.registry.AgentDefinition(subAgentKey)
			if !found {
				o.injectMainToolError(main, invoke.MainToolID, fmt.Sprintf("sub-agent not found: %s", subAgentKey))
				return nil
			}
		}
		if !catalog.AgentUsesACPCoderBackend(agentDef) && !resolvedModeCapabilities(agentDef).RunAsChild {
			o.injectMainToolError(main, invoke.MainToolID, "sub-agent must be REACT/ONESHOT/CODER/KBASE/PROXY")
			return nil
		}
		if !catalog.AgentInvocable(agentDef) {
			o.injectMainToolError(main, invoke.MainToolID, "sub-agent is not invocable")
			return nil
		}
		if containsInvokeAgentsTool(agentDef.Tools) {
			o.injectMainToolError(main, invoke.MainToolID, "nested sub-agent invocation is not allowed")
			return nil
		}
		o.taskCounter++
		taskIndex := o.taskCounter
		parentReqID := strings.TrimSpace(o.session.RequestID)
		requestID := fmt.Sprintf("sub_%d", taskIndex)
		if parentReqID != "" {
			requestID = fmt.Sprintf("%s_sub_%d", parentReqID, taskIndex)
		}
		subTaskID := fmt.Sprintf("sub_%d", taskIndex)
		prepared = append(prepared, preparedSubTask{
			spec: contracts.SubAgentTaskSpec{
				SubAgentKey: subAgentKey,
				TaskText:    taskText,
				TaskName:    taskName,
				Files:       append([]string(nil), task.Files...),
			},
			agentDef:  agentDef,
			taskID:    fmt.Sprintf("%s_t_%d", strings.TrimSpace(o.session.RunID), taskIndex),
			requestID: requestID,
			subTaskID: subTaskID,
		})
	}

	for index := range prepared {
		prepared[index].mainToolID = invoke.MainToolID
		if o.session.TeamRuntime != nil {
			prepared[index].teamID = o.session.TeamID
			prepared[index].presentation = "task"
		}
	}

	for _, task := range prepared {
		o.emitDelta(contracts.DeltaTaskLifecycle{
			Kind:         "start",
			TaskID:       task.taskID,
			RunID:        o.session.RunID,
			TaskName:     task.spec.TaskName,
			Description:  task.spec.TaskText,
			SubAgentKey:  task.spec.SubAgentKey,
			MainToolID:   invoke.MainToolID,
			TeamID:       task.teamID,
			Presentation: task.presentation,
		})
	}

	var principal *Principal
	if strings.TrimSpace(o.session.Subject) != "" {
		principal = &Principal{Subject: o.session.Subject}
	}

	results := make([]childTaskResult, len(prepared))
	routedCh := make(chan childRouteEvent, 32)
	teamMaxParallel := len(prepared)
	var teamSem chan struct{}
	if o.session.TeamRuntime != nil {
		teamMaxParallel = o.session.TeamRuntime.MaxParallel
		if teamMaxParallel < 1 || teamMaxParallel > len(prepared) {
			teamMaxParallel = len(prepared)
		}
		teamSem = make(chan struct{}, teamMaxParallel)
	}
	hitlBatch := newTeamMergedHITLBatch(o, prepared, o.session.TeamRuntime != nil, teamMaxParallel)
	var wg sync.WaitGroup

	for index, task := range prepared {
		wg.Add(1)
		go func(index int, task preparedSubTask) {
			defer wg.Done()
			if teamSem != nil {
				select {
				case teamSem <- struct{}{}:
					defer func() { <-teamSem }()
				case <-o.runCtx.Done():
					routedCh <- childRouteEvent{result: &childTaskResult{Index: index, TaskID: task.taskID, TaskName: task.spec.TaskName, SubAgentKey: task.spec.SubAgentKey, Status: "cancelled", Text: "Team member interrupted"}}
					return
				}
			}
			options := childRunOptions{RunControl: hitlBatch.controlFor(task)}
			routedCh <- childRouteEvent{result: o.runChildTaskWithOptions(index, task, principal, func(input stream.StreamInput) {
				if event, captured := hitlBatch.capture(task, input); captured {
					routedCh <- event
					return
				}
				routedCh <- childRouteEvent{input: input}
			}, options)}
		}(index, task)
	}
	go func() {
		wg.Wait()
		close(routedCh)
	}()

	for routed := range routedCh {
		if routed.input != nil && o.emitInputs != nil {
			o.emitInputs(routed.input)
		}
		if routed.result == nil {
			hitlBatch.observe(routed)
			continue
		}
		results[routed.result.Index] = *routed.result
		task := prepared[routed.result.Index]
		terminalKind := "complete"
		if routed.result.Status == "failed" {
			terminalKind = "error"
		} else if routed.result.Status == "cancelled" {
			terminalKind = "cancel"
		}
		lifecycle := contracts.DeltaTaskLifecycle{
			Kind:         terminalKind,
			TaskID:       routed.result.TaskID,
			SubAgentKey:  routed.result.SubAgentKey,
			TeamID:       task.teamID,
			Presentation: task.presentation,
		}
		if terminalKind == "error" {
			lifecycle.Error = apperrors.Payload(
				apperrors.Code(firstNonEmpty(routed.result.ErrorCode, string(apperrors.CodeSubAgentFailed))),
				firstNonEmpty(routed.result.Error, routed.result.Text),
				apperrors.WithScope(apperrors.ScopeTask),
				apperrors.WithCategory(apperrors.CategorySystem),
			)
		}
		o.emitDelta(lifecycle)
		hitlBatch.observe(routed)
	}

	aggregated, err := json.Marshal(results)
	if err != nil {
		o.injectMainToolError(main, invoke.MainToolID, err.Error())
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

func (o *frameOrchestrator) handleTeamDispatch(mainStream contracts.AgentStream, dispatch contracts.DeltaTeamDispatch) (bool, error) {
	main, ok := mainStream.(contracts.OrchestratableAgentStream)
	if !ok {
		return false, fmt.Errorf("TEAM coordinator stream does not support orchestration")
	}
	if o.teamSnapshot == nil {
		o.injectMainToolError(main, dispatch.MainToolID, "TEAM snapshot is unavailable")
		return false, nil
	}
	if len(dispatch.Tasks) == 0 || len(dispatch.Tasks) > len(o.teamSnapshot.ValidAgentKeys) {
		o.injectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("agent_delegate tasks must contain between 1 and %d Team members", len(o.teamSnapshot.ValidAgentKeys)))
		return false, nil
	}

	prepared := make([]preparedSubTask, 0, len(dispatch.Tasks))
	seenMembers := make(map[string]struct{}, len(dispatch.Tasks))
	for _, spec := range dispatch.Tasks {
		memberKey := strings.TrimSpace(spec.SubAgentKey)
		lookupKey := strings.ToLower(memberKey)
		if _, duplicate := seenMembers[lookupKey]; duplicate {
			o.injectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q may only appear once in agent_delegate", memberKey))
			return false, nil
		}
		seenMembers[lookupKey] = struct{}{}
		if !o.teamSnapshot.HasAgent(memberKey) {
			o.injectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q is unavailable in Team %q", memberKey, o.teamSnapshot.TeamID))
			return false, nil
		}
		def, found := o.teamSnapshot.AgentDefinition(memberKey)
		if !found {
			o.injectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q is unavailable in Team %q", memberKey, o.teamSnapshot.TeamID))
			return false, nil
		}
		if !catalog.AgentUsesACPCoderBackend(def) && !resolvedModeCapabilities(def).RunAsChild {
			o.injectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q cannot run as a Team child", memberKey))
			return false, nil
		}
		if containsInvokeAgentsTool(def.Tools) {
			o.injectMainToolError(main, dispatch.MainToolID, fmt.Sprintf("member %q cannot invoke nested sub-agents", memberKey))
			return false, nil
		}
		o.taskCounter++
		index := o.taskCounter
		requestID := fmt.Sprintf("%s_team_%d", firstNonEmpty(o.session.RequestID, o.session.RunID), index)
		taskName := strings.TrimSpace(spec.TaskName)
		if taskName == "" {
			taskName = firstNonEmpty(def.Name, memberKey)
		}
		taskText := strings.TrimSpace(spec.TaskText)
		if taskText == "" {
			taskText = o.request.Message
		}
		prepared = append(prepared, preparedSubTask{
			spec: contracts.SubAgentTaskSpec{
				SubAgentKey: memberKey,
				TaskText:    taskText,
				TaskName:    taskName,
				Files:       append([]string(nil), spec.Files...),
			},
			agentDef:     def,
			taskID:       fmt.Sprintf("%s_team_t_%d", strings.TrimSpace(o.session.RunID), index),
			requestID:    requestID,
			subTaskID:    fmt.Sprintf("team_%d", index),
			mainToolID:   dispatch.MainToolID,
			teamID:       o.teamSnapshot.TeamID,
			presentation: "task",
		})
	}

	for _, task := range prepared {
		o.emitDelta(contracts.DeltaTaskLifecycle{
			Kind:         "start",
			TaskID:       task.taskID,
			RunID:        o.session.RunID,
			TaskName:     task.spec.TaskName,
			SubAgentKey:  task.spec.SubAgentKey,
			MainToolID:   dispatch.MainToolID,
			TeamID:       o.teamSnapshot.TeamID,
			Presentation: "task",
		})
	}

	var principal *Principal
	if strings.TrimSpace(o.session.Subject) != "" {
		principal = &Principal{Subject: o.session.Subject}
	}
	maxParallel := o.teamSnapshot.Orchestrator.MaxParallel
	if maxParallel < 1 || maxParallel > contracts.MaxInvokeAgentTasks {
		maxParallel = contracts.MaxInvokeAgentTasks
	}
	sem := make(chan struct{}, maxParallel)
	results := make([]childTaskResult, len(prepared))
	routedCh := make(chan childRouteEvent, 32)
	hitlBatch := newTeamMergedHITLBatch(o, prepared, true, maxParallel)
	var wg sync.WaitGroup
	for index, task := range prepared {
		wg.Add(1)
		go func(index int, task preparedSubTask) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-o.runCtx.Done():
				routedCh <- childRouteEvent{result: &childTaskResult{Index: index, TaskID: task.taskID, TaskName: task.spec.TaskName, SubAgentKey: task.spec.SubAgentKey, Status: "cancelled", Text: "Team member interrupted"}}
				return
			}
			options := childRunOptions{InheritOriginalContext: true, IncludeHistory: true, Presentation: "task", SuppressFinalDuplicate: true, RunControl: hitlBatch.controlFor(task)}
			routedCh <- childRouteEvent{result: o.runChildTaskWithOptions(index, task, principal, func(input stream.StreamInput) {
				if event, captured := hitlBatch.capture(task, input); captured {
					routedCh <- event
					return
				}
				routedCh <- childRouteEvent{input: routeTeamChildStreamInput(o.session.RunID, o.teamSnapshot.TeamID, task, input, options)}
			}, options)}
		}(index, task)
	}
	go func() {
		wg.Wait()
		close(routedCh)
	}()
	for routed := range routedCh {
		if routed.input != nil && o.emitInputs != nil {
			o.emitInputs(routed.input)
		}
		if routed.result == nil {
			hitlBatch.observe(routed)
			continue
		}
		results[routed.result.Index] = *routed.result
		task := prepared[routed.result.Index]
		terminalKind := "complete"
		if routed.result.Status == "failed" {
			terminalKind = "error"
		} else if routed.result.Status == "cancelled" {
			terminalKind = "cancel"
		}
		lifecycle := contracts.DeltaTaskLifecycle{Kind: terminalKind, TaskID: routed.result.TaskID, SubAgentKey: routed.result.SubAgentKey, TeamID: task.teamID, Presentation: task.presentation}
		if terminalKind == "error" {
			lifecycle.Error = apperrors.Payload(
				apperrors.Code(firstNonEmpty(routed.result.ErrorCode, string(apperrors.CodeTeamMemberFailed))),
				firstNonEmpty(routed.result.Error, routed.result.Text),
				apperrors.WithScope(apperrors.ScopeTask),
				apperrors.WithCategory(apperrors.CategorySystem),
			)
		}
		o.emitDelta(lifecycle)
		hitlBatch.observe(routed)
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
	aggregated, err := json.Marshal(teamDelegateToolResult{Results: toolResults})
	if err != nil {
		o.injectMainToolError(main, dispatch.MainToolID, err.Error())
		return false, nil
	}
	if !main.InjectToolResult(dispatch.MainToolID, string(aggregated), anyFailed) {
		return false, fmt.Errorf("TEAM coordinator rejected dispatch result")
	}
	if optional, ok := mainStream.(contracts.OptionalToolAgentStream); ok {
		optional.AllowOptionalTools()
	}
	return false, nil
}

func newTeamMergedHITLBatch(o *frameOrchestrator, tasks []preparedSubTask, enabled bool, maxParallel int) *teamMergedHITLBatch {
	if maxParallel < 1 || maxParallel > len(tasks) {
		maxParallel = len(tasks)
	}
	batch := &teamMergedHITLBatch{
		orchestrator: o,
		tasks:        append([]preparedSubTask(nil), tasks...),
		waveSize:     maxParallel,
		controls:     map[string]*contracts.RunControl{},
		waiting:      map[string]*teamChildAwaiting{},
		completed:    map[string]bool{},
	}
	if !enabled || o == nil || contracts.RunControlFromContext(o.runCtx) == nil {
		return batch
	}
	batch.enabled = true
	for _, task := range tasks {
		control := contracts.NewRunControl(o.runCtx, task.taskID)
		control.SetInitialAccessLevel(o.session.AccessLevel)
		batch.controls[task.taskID] = control
	}
	return batch
}

func (b *teamMergedHITLBatch) controlFor(task preparedSubTask) *contracts.RunControl {
	if b == nil || !b.enabled {
		return nil
	}
	return b.controls[task.taskID]
}

func (b *teamMergedHITLBatch) capture(task preparedSubTask, input stream.StreamInput) (childRouteEvent, bool) {
	if b == nil || !b.enabled {
		return childRouteEvent{}, false
	}
	switch value := input.(type) {
	case stream.AwaitAsk:
		rawID := rawAwaitingIDForTask(task.taskID, value.AwaitingID)
		publicID := namespaceChildID(task.taskID, rawID)
		if publicID == "" || rawID == "" {
			return childRouteEvent{}, false
		}
		return childRouteEvent{awaiting: &teamChildAwaiting{
			Task:     task,
			Control:  b.controls[task.taskID],
			Ask:      value,
			RawID:    rawID,
			PublicID: publicID,
		}}, true
	case stream.RequestSubmit:
		// The Team-level request.submit is the sole public submit event. Child
		// submits remain local to their isolated controls.
		return childRouteEvent{}, true
	case stream.AwaitingAnswer:
		rawID := rawAwaitingIDForTask(task.taskID, value.AwaitingID)
		return childRouteEvent{awaitingDone: namespaceChildID(task.taskID, rawID)}, true
	default:
		return childRouteEvent{}, false
	}
}

func (b *teamMergedHITLBatch) observe(event childRouteEvent) {
	if b == nil || !b.enabled {
		return
	}
	if event.awaiting != nil {
		b.waiting[event.awaiting.Task.taskID] = event.awaiting
	}
	if doneID := strings.TrimSpace(event.awaitingDone); doneID != "" {
		for taskID, pending := range b.waiting {
			if pending != nil && pending.PublicID == doneID {
				delete(b.waiting, taskID)
				break
			}
		}
	}
	if event.result != nil {
		b.completed[event.result.TaskID] = true
		delete(b.waiting, event.result.TaskID)
	}
	readyWave := b.waveSize > 0 && len(b.waiting) >= b.waveSize
	allSettled := len(b.completed)+len(b.waiting) == len(b.tasks)
	if len(b.waiting) == 0 || (!readyWave && !allSettled) {
		return
	}
	b.resolveWaiting()
}

func (b *teamMergedHITLBatch) resolveWaiting() {
	if b == nil || !b.enabled || b.orchestrator == nil {
		return
	}
	pending := make([]*teamChildAwaiting, 0, len(b.waiting))
	for _, task := range b.tasks {
		if item := b.waiting[task.taskID]; item != nil {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return
	}
	for _, item := range pending {
		delete(b.waiting, item.Task.taskID)
	}

	o := b.orchestrator
	parentControl := contracts.RunControlFromContext(o.runCtx)
	if parentControl == nil {
		return
	}
	o.teamAwaitCounter++
	mergedID := fmt.Sprintf("%s_team_await_%d", strings.TrimSpace(o.session.RunID), o.teamAwaitCounter)
	forms, routes, timeoutSeconds := teamMergedAwaitingDefinition(pending)
	parentControl.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: mergedID,
		Mode:       "form",
		ItemCount:  len(routes),
		Routes:     routes,
		NoTimeout:  timeoutSeconds == 0,
		Timeout:    timeoutSeconds,
	})
	parentControl.TransitionState(contracts.RunLoopStateWaitingSubmit)
	if o.emitInputs != nil {
		o.emitInputs(stream.AwaitAsk{
			AwaitingID:   mergedID,
			Mode:         "form",
			Timeout:      timeoutSeconds,
			RunID:        o.session.RunID,
			ViewportType: "html",
			ViewportKey:  "team-hitl",
			Forms:        forms,
		})
	}

	startedAt := time.Now()
	wait := time.Duration(timeoutSeconds) * time.Second
	result, err := parentControl.AwaitSubmitWithTimeout(o.runCtx, mergedID, wait)
	if err != nil {
		if errors.Is(err, contracts.ErrRunInterrupted) || errors.Is(err, context.Canceled) {
			return
		}
		if o.emitInputs != nil {
			o.emitInputs(stream.AwaitingAnswer{
				AwaitingID: mergedID,
				Answer:     contracts.AwaitingTimeoutAnswer("form", timeoutSeconds, int64(time.Since(startedAt).Seconds())),
			})
		}
		teamDistributeMergedSubmit(pending, nil, api.SubmitRequest{RunID: o.session.RunID, TeamID: o.session.TeamID})
		parentControl.TransitionState(contracts.RunLoopStateToolExecuting)
		return
	}

	if o.emitInputs != nil {
		o.emitInputs(stream.RequestSubmit{
			RequestID:  o.session.RequestID,
			ChatID:     o.session.ChatID,
			RunID:      o.session.RunID,
			AwaitingID: mergedID,
			SubmitID:   result.Request.SubmitID,
			Params:     result.Request.Params,
		})
		o.emitInputs(stream.AwaitingAnswer{
			AwaitingID: mergedID,
			Answer:     teamMergedAwaitingAnswer(result.Request.Params, result.Request.SubmitID),
		})
	}
	teamDistributeMergedSubmit(pending, result.Request.Params, result.Request)
	parentControl.TransitionState(contracts.RunLoopStateToolExecuting)
}

func teamMergedAwaitingDefinition(pending []*teamChildAwaiting) ([]any, []contracts.AwaitingSubmitRoute, int64) {
	forms := make([]any, 0, len(pending))
	routes := make([]contracts.AwaitingSubmitRoute, 0, len(pending))
	var timeoutSeconds int64
	for _, item := range pending {
		if item == nil {
			continue
		}
		definition := map[string]any{
			"taskId":     item.Task.taskID,
			"awaitingId": item.RawID,
			"mode":       item.Ask.Mode,
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
			"title":      firstNonEmpty(item.Task.spec.TaskName, item.Task.spec.SubAgentKey),
			"taskId":     item.Task.taskID,
			"awaitingId": item.RawID,
			"mode":       item.Ask.Mode,
			"form":       definition,
		})
		routes = append(routes, contracts.AwaitingSubmitRoute{
			FieldID:    item.PublicID,
			TaskID:     item.Task.taskID,
			AwaitingID: item.RawID,
			Mode:       item.Ask.Mode,
			ItemCount:  teamAwaitingItemCount(item.Ask),
			Questions:  append([]any(nil), item.Ask.Questions...),
		})
		if item.Ask.Timeout > 0 && (timeoutSeconds == 0 || item.Ask.Timeout < timeoutSeconds) {
			timeoutSeconds = item.Ask.Timeout
		}
	}
	return forms, routes, timeoutSeconds
}

func teamAwaitingItemCount(ask stream.AwaitAsk) int {
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

func teamDistributeMergedSubmit(pending []*teamChildAwaiting, merged api.SubmitParams, parent api.SubmitRequest) {
	items, _ := api.DecodeSubmitParams(merged)
	for index, child := range pending {
		if child == nil || child.Control == nil {
			continue
		}
		var params api.SubmitParams
		if index < len(items) {
			params = teamChildSubmitParams(child.Ask, items[index])
		}
		child.Control.ResolveSubmit(api.SubmitRequest{
			ChatID:     parent.ChatID,
			RunID:      parent.RunID,
			AgentKey:   child.Task.spec.SubAgentKey,
			TeamID:     parent.TeamID,
			AwaitingID: child.RawID,
			SubmitID:   parent.SubmitID,
			Locale:     parent.Locale,
			Params:     params,
		})
	}
}

func teamChildSubmitParams(ask stream.AwaitAsk, item map[string]any) api.SubmitParams {
	decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
	if decision == "approve" {
		form := contracts.AnyMapNode(item["form"])
		params, err := api.EncodeSubmitParams(form["params"])
		if err == nil {
			return params
		}
		return nil
	}
	return teamRejectedChildParams(ask)
}

func teamRejectedChildParams(ask stream.AwaitAsk) api.SubmitParams {
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
	params, err := api.EncodeSubmitParams(items)
	if err != nil {
		return nil
	}
	return params
}

func teamMergedAwaitingAnswer(params api.SubmitParams, submitID string) map[string]any {
	items, _ := api.DecodeSubmitParams(params)
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

func (o *frameOrchestrator) runChildTask(index int, task preparedSubTask, principal *Principal, route func(stream.StreamInput)) *childTaskResult {
	return o.runChildTaskWithOptions(index, task, principal, route, childRunOptions{})
}

func (o *frameOrchestrator) runChildTaskWithOptions(index int, task preparedSubTask, principal *Principal, route func(stream.StreamInput), options childRunOptions) *childTaskResult {
	result := &childTaskResult{
		Index:       index,
		TaskID:      task.taskID,
		TaskName:    task.spec.TaskName,
		SubAgentKey: task.spec.SubAgentKey,
		Status:      "completed",
	}

	if catalog.AgentUsesACPCoderBackend(task.agentDef) {
		result.Status = "failed"
		result.Text = "ACP CODER sub-agent is not supported"
		result.Error = result.Text
		return result
	}

	subReq := api.QueryRequest{
		RequestID:   task.requestID,
		RunID:       o.session.RunID,
		ChatID:      o.session.ChatID,
		AgentKey:    task.spec.SubAgentKey,
		TeamID:      o.session.TeamID,
		Role:        api.QueryRoleUser,
		Message:     task.spec.TaskText,
		AccessLevel: o.session.AccessLevel,
	}
	if options.InheritOriginalContext {
		subReq.Role = o.request.Role
		if strings.TrimSpace(subReq.Role) == "" {
			subReq.Role = api.QueryRoleUser
		}
		subReq.Scene = o.request.Scene
	}
	baseReferences := []api.Reference(nil)
	if options.InheritOriginalContext {
		baseReferences = append(baseReferences, o.request.References...)
	}
	if len(task.spec.Files) > 0 {
		references, err := prepareProxyReferences(o.chats, o.resourceTickets, proxyReferenceOptions{
			ChatID:          subReq.ChatID,
			RunID:           subReq.RunID,
			Subject:         o.session.Subject,
			ResourceBaseURL: o.resourceBaseURL,
			References:      baseReferences,
			Files:           task.spec.Files,
		})
		if err != nil {
			result.Status = "failed"
			result.Text = err.Error()
			result.Error = err.Error()
			return result
		}
		subReq.References = deduplicateTeamReferences(references)
	} else {
		subReq.References = deduplicateTeamReferences(baseReferences)
	}

	childRunCtx := o.runCtx
	if options.RunControl != nil {
		childRunCtx = contracts.WithRunControl(options.RunControl.Context(), options.RunControl)
	}
	subSession, err := o.buildQuerySession(childRunCtx, subReq, o.summary, task.agentDef, querySessionBuildOptions{
		Created:           false,
		IncludeHistory:    options.IncludeHistory,
		IncludeMemory:     false,
		AllowInvokeAgents: false,
		SubTaskID:         task.subTaskID,
		Principal:         principal,
		TeamHistoryAgentKey: func() string {
			if options.InheritOriginalContext {
				return task.spec.SubAgentKey
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
	if len(subSession.RuntimeContext.References) > 0 {
		subReq.References = subSession.RuntimeContext.References
	}
	o.writeChildTaskQueryAndSystem(subReq, &subSession, task)

	if isProxyAgentMode(task.agentDef.Mode) {
		return o.runProxyChildTask(result, subReq, subSession.WorkspaceRoot, task, route)
	}

	subStream, err := o.agent.Stream(childRunCtx, subReq, subSession)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	defer subStream.Close()

	childMapper := o.mapper.CloneIsolated(task.taskID, o.session.ChatID)
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
			result.Text = errorMessage(value.Error)
			result.Error = result.Text
			return result
		default:
			for _, input := range childMapper.Map(delta) {
				if _, ok := input.(stream.ContentDelta); ok {
					sawContent = true
				}
				route(routeChildStreamInput(o.session.RunID, task.taskID, input))
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
		route(routeChildStreamInput(o.session.RunID, task.taskID, stream.ContentDelta{
			ContentID: task.taskID + ":final",
			TaskID:    task.taskID,
			Delta:     text,
		}))
	}
	result.Text = text
	return result
}

func (o *frameOrchestrator) runProxyChildTask(result *childTaskResult, subReq api.QueryRequest, workspaceRoot string, task preparedSubTask, route func(stream.StreamInput)) *childTaskResult {
	proxy := task.agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		result.Status = "failed"
		result.Text = "PROXY sub-agent missing proxyConfig.baseUrl"
		result.Error = result.Text
		return result
	}

	targetURL := strings.TrimRight(proxy.BaseURL, "/") + "/api/query"
	payload := map[string]any{
		"agentKey":   proxyAgentKey(proxy, subReq.AgentKey),
		"message":    subReq.Message,
		"references": subReq.References,
	}
	if chatID := strings.TrimSpace(proxy.ChatID); chatID != "" {
		payload["chatId"] = chatID
	}
	if params := proxyForwardParams(subReq, workspaceRoot); params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}

	client := &http.Client{Timeout: proxyRequestTimeout(proxy)}
	req, err := http.NewRequestWithContext(o.runCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if strings.TrimSpace(proxy.Token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(proxy.Token))
	}

	resp, err := client.Do(req)
	if err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		result.Status = "failed"
		result.Text = fmt.Sprintf("PROXY sub-agent returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		result.Error = result.Text
		return result
	}

	var assistantText strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		event, ok, decodeErr := parseProxySSEDataLineAt(scanner.Text())
		if decodeErr != nil {
			result.Status = "failed"
			result.Text = timeContractViolationMessage
			result.Error = decodeErr.Error()
			result.ErrorCode = "time_contract_violation"
			return result
		}
		if !ok {
			continue
		}
		switch event.Type {
		case "content.delta":
			delta, _ := event.Payload["delta"].(string)
			if delta == "" {
				continue
			}
			assistantText.WriteString(delta)
			contentID, _ := event.Payload["contentId"].(string)
			if strings.TrimSpace(contentID) == "" {
				contentID = task.taskID + ":proxy"
			} else {
				contentID = namespaceChildID(task.taskID, contentID)
			}
			route(routeChildStreamInput(o.session.RunID, task.taskID, stream.ContentDelta{
				ContentID: contentID,
				TaskID:    task.taskID,
				Delta:     delta,
			}))
		case "run.error":
			result.Status = "failed"
			result.Text = errorMessage(event.Payload)
			result.Error = result.Text
			return result
		case "run.cancel":
			result.Status = "cancelled"
			result.Text = "sub-agent cancelled"
			return result
		case "run.complete":
			result.Text = strings.TrimSpace(assistantText.String())
			if result.Text == "" {
				result.Status = "failed"
				result.Text = "PROXY sub-agent returned run.complete without assistant content"
				result.Error = result.Text
			}
			return result
		}
	}
	if err := scanner.Err(); err != nil {
		result.Status = "failed"
		result.Text = err.Error()
		result.Error = err.Error()
		return result
	}
	result.Text = strings.TrimSpace(assistantText.String())
	if result.Text == "" {
		result.Status = "failed"
		result.Text = "PROXY sub-agent returned an empty SSE stream"
		result.Error = result.Text
	}
	return result
}

func parseProxySSEDataLine(line string) (stream.EventData, bool) {
	event, ok, _ := parseProxySSEDataLineAt(line)
	return event, ok
}

func parseProxySSEDataLineAt(line string) (stream.EventData, bool, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return stream.EventData{}, false, nil
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == stream.DoneSentinel {
		return stream.EventData{}, false, nil
	}
	return decodeProxyEventAt([]byte(payload), "proxy.child.sse.event")
}

func (o *frameOrchestrator) writeChildTaskQueryAndSystem(subReq api.QueryRequest, subSession *contracts.QuerySession, task preparedSubTask) {
	if o.chats == nil {
		return
	}
	var system *chat.QueryLineSystem
	if subSession != nil && o.prepareSystemInit != nil {
		o.systemInitMu.Lock()
		defer o.systemInitMu.Unlock()
		system, _ = o.prepareSystemInit(subReq, subSession, false)
	}
	var liveSeq int64
	if o.nextLiveSeq != nil {
		liveSeq = o.nextLiveSeq()
	}
	_ = o.chats.AppendQueryLine(o.summary.ChatID, chat.QueryLine{
		Type:         "query",
		ChatID:       o.summary.ChatID,
		RunID:        o.session.RunID,
		UpdatedAt:    time.Now().UnixMilli(),
		LiveSeq:      liveSeq,
		TaskID:       task.taskID,
		TaskName:     task.spec.TaskName,
		TaskToolID:   task.mainToolID,
		SubAgentKey:  task.spec.SubAgentKey,
		TeamID:       task.teamID,
		Presentation: task.presentation,
		Query: map[string]any{
			"message":   task.spec.TaskText,
			"agentKey":  task.spec.SubAgentKey,
			"chatId":    o.summary.ChatID,
			"runId":     o.session.RunID,
			"requestId": task.requestID,
			"role":      firstNonEmpty(subReq.Role, api.QueryRoleUser),
		},
		Messages: currentMessagesFromSession(subSession),
		System:   system,
	})
}

func currentMessagesFromSession(session *contracts.QuerySession) []map[string]any {
	if session == nil {
		return nil
	}
	return session.CurrentMessages
}

func (o *frameOrchestrator) injectMainToolError(main contracts.OrchestratableAgentStream, toolID string, message string) {
	_ = main.InjectToolResult(toolID, message, true)
}

func containsInvokeAgentsTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		if strings.EqualFold(strings.TrimSpace(toolName), contracts.InvokeAgentsToolName) {
			return true
		}
	}
	return false
}

func isProxyAgentMode(mode string) bool {
	return catalog.AgentIsProxyMode(mode)
}

func routeChildStreamInput(parentRunID string, taskID string, input stream.StreamInput) stream.StreamInput {
	switch value := input.(type) {
	case stream.ReasoningDelta:
		value.TaskID = taskID
		return value
	case stream.ContentDelta:
		value.TaskID = taskID
		return value
	case stream.ToolArgs:
		value.TaskID = taskID
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		if value.AwaitAsk != nil {
			awaitCopy := *value.AwaitAsk
			awaitCopy.RunID = firstNonEmpty(parentRunID, awaitCopy.RunID)
			awaitCopy.TaskID = taskID
			awaitCopy.AwaitingID = namespaceChildID(taskID, awaitCopy.AwaitingID)
			value.AwaitAsk = &awaitCopy
		}
		return value
	case stream.ToolEnd:
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		return value
	case stream.ToolResult:
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		return value
	case stream.ActionArgs:
		value.TaskID = taskID
		value.ActionID = namespaceChildID(taskID, value.ActionID)
		return value
	case stream.ActionEnd:
		value.ActionID = namespaceChildID(taskID, value.ActionID)
		return value
	case stream.ActionResult:
		value.ActionID = namespaceChildID(taskID, value.ActionID)
		return value
	case stream.SourcePublish:
		value.TaskID = taskID
		value.PublishID = namespaceChildID(taskID, value.PublishID)
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		return value
	case stream.ArtifactPublish:
		value.TaskID = taskID
		value.ToolID = namespaceChildID(taskID, value.ToolID)
		return value
	case stream.AwaitAsk:
		value.RunID = firstNonEmpty(parentRunID, value.RunID)
		value.TaskID = taskID
		value.AwaitingID = namespaceChildID(taskID, value.AwaitingID)
		return value
	case stream.RequestSubmit:
		value.RunID = firstNonEmpty(parentRunID, value.RunID)
		value.TaskID = taskID
		value.AwaitingID = namespaceChildID(taskID, value.AwaitingID)
		return value
	case stream.AwaitingAnswer:
		value.TaskID = taskID
		value.AwaitingID = namespaceChildID(taskID, value.AwaitingID)
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
	default:
		return input
	}
}

func routeTeamChildStreamInput(_ string, teamID string, task preparedSubTask, input stream.StreamInput, options childRunOptions) stream.StreamInput {
	switch value := input.(type) {
	case stream.ToolArgs:
		value.TaskID = task.taskID
		value.ToolID = namespaceChildID(task.taskID, value.ToolID)
		if value.AwaitAsk != nil {
			awaitCopy := *value.AwaitAsk
			awaitCopy.TaskID = task.taskID
			awaitCopy.AwaitingID = namespaceChildID(task.taskID, awaitCopy.AwaitingID)
			value.AwaitAsk = &awaitCopy
		}
		return value
	case stream.ToolEnd:
		value.ToolID = namespaceChildID(task.taskID, value.ToolID)
		return value
	case stream.ToolResult:
		value.ToolID = namespaceChildID(task.taskID, value.ToolID)
		return value
	case stream.ArtifactPublish:
		value.TaskID = task.taskID
		value.ToolID = namespaceChildID(task.taskID, value.ToolID)
		return value
	case stream.ContentDelta:
		value.ActorType = "agent"
		value.TeamID = strings.TrimSpace(teamID)
		value.AgentKey = strings.TrimSpace(task.spec.SubAgentKey)
		value.Presentation = firstNonEmpty(options.Presentation, "task")
		return value
	case stream.InputLLMRequest:
		value.ActorType = "agent"
		value.TeamID = strings.TrimSpace(teamID)
		value.AgentKey = strings.TrimSpace(task.spec.SubAgentKey)
		value.Presentation = firstNonEmpty(options.Presentation, "task")
		return value
	default:
		return input
	}
}

func namespaceChildID(taskID string, rawID string) string {
	rawID = strings.TrimSpace(rawID)
	if rawID == "" {
		return ""
	}
	return taskID + ":" + rawID
}

func deduplicateTeamReferences(references []api.Reference) []api.Reference {
	if len(references) < 2 {
		return append([]api.Reference(nil), references...)
	}
	out := make([]api.Reference, 0, len(references))
	seen := make(map[string]struct{}, len(references)*3)
	for _, reference := range references {
		keys := teamReferenceIdentityKeys(reference)
		duplicate := false
		for _, key := range keys {
			if _, exists := seen[key]; exists {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		for _, key := range keys {
			seen[key] = struct{}{}
		}
		out = append(out, reference)
	}
	return out
}

func teamReferenceIdentityKeys(reference api.Reference) []string {
	keys := make([]string, 0, 5)
	appendKey := func(prefix string, value string) {
		if value = strings.TrimSpace(value); value != "" {
			keys = append(keys, prefix+value)
		}
	}
	appendKey("id:", reference.ID)
	appendKey("sha256:", reference.SHA256)
	appendKey("path:", reference.Path)
	appendKey("url:", reference.URL)
	if referenceType, name := strings.TrimSpace(reference.Type), strings.TrimSpace(reference.Name); len(keys) == 0 && referenceType != "" && name != "" {
		keys = append(keys, "name:"+referenceType+"\x00"+name)
	}
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func errorMessage(payload map[string]any) string {
	if payload == nil {
		return "sub-agent failed"
	}
	if message := firstPayloadString(payload, "message", "error", "reason", "detail", "msg"); message != "" {
		return message
	}
	for _, key := range []string{"error", "rawEvent"} {
		if nested, ok := payload[key].(map[string]any); ok {
			if message := firstPayloadString(nested, "message", "error", "reason", "detail", "msg"); message != "" {
				return message
			}
		}
	}
	if data, err := json.Marshal(payload); err == nil && len(data) > 0 {
		return "sub-agent failed: " + string(data)
	}
	return "sub-agent failed"
}

func firstPayloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := payload[key].(string)
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
