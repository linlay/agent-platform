package server

import session "agent-platform/internal/runtime/session"

var isProxyAgentMode = session.IsProxyAgentMode
var runtimeAgentEnv = session.RuntimeAgentEnv

var mountedViews = session.MountedViews

var effectiveLocalWorkspaceRoot = session.EffectiveLocalWorkspaceRoot

var validateWorkspaceChatsSeparation = session.ValidateWorkspaceChatsSeparation
var resolveHostWorkspaceRoot = session.ResolveHostWorkspaceRoot

var firstStringClaim = session.FirstStringClaim

var effectiveAgentTools = session.EffectiveAgentTools
var hasRuntimeSandbox = session.HasRuntimeSandbox

var normalizeRuntimeMounts = session.NormalizeRuntimeMounts

var stringValue = session.StringValue

type querySessionBuildOptions = session.Options
