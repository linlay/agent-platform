package server

import orchestration "agent-platform/internal/runtime/orchestration"

type frameOrchestrator = orchestration.Coordinator
type childTaskResult = orchestration.ChildTaskResult
type preparedSubTask = orchestration.PreparedSubTask
type childRunOptions = orchestration.ChildRunOptions
type childRouteEvent = orchestration.ChildRouteEvent
type teamChildAwaiting = orchestration.TeamChildAwaiting
type teamMergedHITLBatch = orchestration.TeamMergedHITLBatch

var firstNonEmpty = orchestration.FirstNonEmpty

type teamDelegateToolResult = orchestration.TeamDelegateToolResult
