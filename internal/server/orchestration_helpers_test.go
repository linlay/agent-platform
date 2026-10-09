package server

import orchestration "agent-platform/internal/runtime/orchestration"

var currentMessagesFromSession = orchestration.CurrentMessagesFromSession
var containsInvokeAgentsTool = orchestration.ContainsInvokeAgentsTool
var sameAgentKey = orchestration.SameAgentKey
var routeChildStreamInput = orchestration.RouteChildStreamInput
var routeTeamChildStreamInput = orchestration.RouteTeamChildStreamInput
var namespaceChildID = orchestration.NamespaceChildID
var namespaceChildIDs = orchestration.NamespaceChildIDs
var deduplicateTeamReferences = orchestration.DeduplicateTeamReferences
var errorMessage = orchestration.ErrorMessage
var firstPayloadString = orchestration.FirstPayloadString
