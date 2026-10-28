package server

import (
	"agent-platform/internal/runtime/session"
)

var addConnectorAccessRoots = session.AddConnectorAccessRoots
var runtimeConnectorMounts = session.RuntimeConnectorMounts
var runtimeConnectorDirs = session.RuntimeConnectorDirs
var runtimeNativeConnectorTools = session.RuntimeNativeConnectorTools
var resolveSkillRuntimeSettings = session.ResolveSkillRuntimeSettings
var buildPromptAppendConfig = session.BuildPromptAppendConfig

type runtimeRequestContextInput = session.ContextInput

var translateReferencePathForHost = session.TranslateReferencePathForHost

type resolvedReferencedSkill = session.ReferencedSkill
type referencedSkillResolution = session.SkillResolution

var buildSkillCatalogPrompt = session.BuildSkillCatalogPrompt
var referencedSkillRunAccess = session.ReferencedSkillRunAccess
var resolveLocalPaths = session.ResolveLocalPaths
var resolveSandboxPaths = session.ResolveSandboxPaths
var anyString = session.AnyString
var containsString = session.ContainsString
var resourceFileParam = session.ResourceFileParam
var runtimeExtraMountsForReferencedSkills = session.RuntimeExtraMountsForSkillReferences
var systemTempRoot = session.SystemTempRoot
var systemTempRoots = session.SystemTempRoots
var excludeHistoryRun = session.ExcludeHistoryRun
var buildSessionToolNames = session.BuildSessionToolNames

var buildSkillScriptScope = session.BuildSkillScriptScope
