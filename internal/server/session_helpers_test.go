package server

import (
	"agent-platform/internal/runtime/session"
)

var isProxyRoutedAgent = session.IsProxyRoutedAgent
var addConnectorAccessRoots = session.AddConnectorAccessRoots
var runtimeConnectorMounts = session.RuntimeConnectorMounts
var runtimeConnectorDirs = session.RuntimeConnectorDirs
var runtimeNativeConnectorTools = session.RuntimeNativeConnectorTools
var resolveSkillRuntimeSettings = session.ResolveSkillRuntimeSettings
var sortedStringKeys = session.SortedStringKeys
var buildPromptAppendConfig = session.BuildPromptAppendConfig

type runtimeRequestContextInput = session.ContextInput

var translateReferencePathForHost = session.TranslateReferencePathForHost
var referencePathWithinHostRoot = session.ReferencePathWithinHostRoot
var translateReferencePathForContainer = session.TranslateReferencePathForContainer
var localSemanticRoots = session.LocalSemanticRoots
var requireReferenceWorkspacePath = session.RequireReferenceWorkspacePath
var currentChatResourceRelativePath = session.CurrentChatResourceRelativePath
var referenceResourceRelativePath = session.ReferenceResourceRelativePath
var referenceName = session.ReferenceName
var resourceFileName = session.ResourceFileName

type skillCenterCatalog = session.SkillCenterCatalog
type resolvedMustUseSkill = session.MustUseSkill
type mustUseSkillResolution = session.SkillResolution

var buildSkillCatalogPrompt = session.BuildSkillCatalogPrompt
var skillCatalogBlock = session.SkillCatalogBlock
var normalizeMustUseSkills = session.NormalizeMustUseSkills
var resolveMustUseSkillRoot = session.ResolveMustUseSkillRoot
var mustUseSkillRunAccess = session.MustUseSkillRunAccess
var resolveCenterSkillID = session.ResolveCenterSkillID
var buildMustUseSkillConstraint = session.BuildMustUseSkillConstraint
var resolveLocalPaths = session.ResolveLocalPaths
var ensureChatDir = session.EnsureChatDir
var chatDirPath = session.ChatDirPath
var resolveSandboxPaths = session.ResolveSandboxPaths
var resolveContainerSandboxPaths = session.ResolveContainerSandboxPaths
var resolveLocalSandboxPaths = session.ResolveLocalSandboxPaths
var agentHasContextTag = session.AgentHasContextTag
var buildSandboxContext = session.BuildSandboxContext
var fetchSandboxPrompt = session.FetchSandboxPrompt
var summarizeSandboxMounts = session.SummarizeSandboxMounts
var promptContextSandboxMounts = session.PromptContextSandboxMounts
var anyString = session.AnyString
var cleanOrEmpty = session.CleanOrEmpty
var absOrEmpty = session.AbsOrEmpty
var resolveLocalSkillsDir = session.ResolveLocalSkillsDir
var promptContextHasPlatformMount = session.PromptContextHasPlatformMount
var ifNonEmpty = session.IfNonEmpty
var boolPath = session.BoolPath
var containsString = session.ContainsString
var agentConnectorPath = session.AgentConnectorPath
var resourceFileParam = session.ResourceFileParam
var resourceFileParamForChat = session.ResourceFileParamForChat
var isResourceURL = session.IsResourceURL
var normalizedAgentTools = session.NormalizedAgentTools
var normalizeRuntimeMount = session.NormalizeRuntimeMount
var runtimeExtraMounts = session.RuntimeExtraMounts
var runtimeExtraMountsForMustUseSkills = session.RuntimeExtraMountsForMustUseSkills
var nullableStringValue = session.NullableStringValue
var extractRuntimeField = session.ExtractRuntimeField

type runEnvironmentLookup = session.RunEnvironmentLookup

var lookupRunEnvironment = session.LookupRunEnvironment
var containsTool = session.ContainsTool
var systemTempRoot = session.SystemTempRoot
var systemTempRoots = session.SystemTempRoots
var excludeHistoryRun = session.ExcludeHistoryRun
var shouldLoadPlanTaskContext = session.ShouldLoadPlanTaskContext
var resolvedModeCapabilities = session.ResolvedModeCapabilities
var coderProjectPromptFiles = session.CoderProjectPromptFiles
var normalizedAccessLevel = session.NormalizedAccessLevel
var runtimeHostAccess = session.RuntimeHostAccess
var buildSessionToolNames = session.BuildSessionToolNames

type mcpServerToolResolver = session.McpServerToolResolver

var mcpToolNamesForServers = session.McpToolNamesForServers
var buildSkillScriptScope = session.BuildSkillScriptScope
