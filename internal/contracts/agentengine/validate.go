// Package agentengine defines ACP data boundaries. Runtime adoption is separate.
package agentengine

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

func isObject(v any) bool         { _, ok := v.(map[string]any); return ok }
func field(v any, key string) any { m, _ := v.(map[string]any); return m[key] }
func has(v any, key string) bool  { m, _ := v.(map[string]any); _, ok := m[key]; return ok }
func fields(v any, required, optional string, closed bool) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	allowed := strings.Fields(required + " " + optional)
	for _, key := range strings.Fields(required) {
		if _, ok := m[key]; !ok {
			return false
		}
	}
	if closed {
		for key := range m {
			found := false
			for _, allowedKey := range allowed {
				if key == allowedKey {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
func text(v any, min, max int, pattern ...string) bool {
	s, ok := v.(string)
	if !ok || utf8.RuneCountInString(s) < min || utf8.RuneCountInString(s) > max {
		return false
	}
	if len(pattern) == 0 {
		return true
	}
	return regexp.MustCompile(pattern[0]).MatchString(s)
}
func integer(v any, min, max float64) bool {
	n, ok := v.(float64)
	return ok &&
		!math.IsNaN(n) &&
		math.Trunc(n) == n &&
		n >= min &&
		n <= max
}
func isBool(v any) bool { _, ok := v.(bool); return ok }
func equal(v, expected any) bool {
	if n, ok := expected.(int); ok {
		expected = float64(n)
	}
	return v == expected
}
func oneOf(v any, choices ...any) bool {
	for _, choice := range choices {
		if equal(v, choice) {
			return true
		}
	}
	return false
}
func list(v any, min, max int, check func(any) bool) bool {
	items, ok := v.([]any)
	if !ok || len(items) < min || len(items) > max {
		return false
	}
	for _, item := range items {
		if !check(item) {
			return false
		}
	}
	return true
}

func validContract(name string, value any) bool {
	switch name {
	case "PlatformTarget":
		return (fields(value, "os arch", "", true) &&
			oneOf(field(value, "os"), "darwin", "windows", "linux") &&
			oneOf(field(value, "arch"), "arm64", "amd64"))
	case "EngineDependency":
		return (fields(value, "id version delivery", "path entry", true) &&
			text(field(value, "id"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "version"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
			(!has(value, "path") || text(field(value, "path"), 1, 9007199254740991, "^[A-Za-z0-9_@-][A-Za-z0-9._@-]*(/[A-Za-z0-9_@-][A-Za-z0-9._@-]*)*$")) &&
			(!has(value, "entry") || text(field(value, "entry"), 1, 9007199254740991, "^[A-Za-z0-9_@-][A-Za-z0-9._@-]*(/[A-Za-z0-9_@-][A-Za-z0-9._@-]*)*$")) &&
			oneOf(field(value, "delivery"), "host-managed", "bundled"))
	case "EngineLaunch":
		return (fields(value, "runtime entry args", "", true) &&
			oneOf(field(value, "runtime"), "node", "executable") &&
			text(field(value, "entry"), 1, 9007199254740991, "^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$") &&
			list(field(value, "args"), 0, 32, func(item any) bool { return text(item, 0, 4096) }))
	case "AdapterVersion":
		return (fields(value, "package version acpSdkVersion", "", true) &&
			text(field(value, "package"), 1, 9007199254740991) &&
			text(field(value, "version"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
			text(field(value, "acpSdkVersion"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$"))
	case "EngineResource":
		return (fields(value, "id name protocol transport launch dependencies", "adapter authentication", true) &&
			text(field(value, "id"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "name"), 1, 9007199254740991) &&
			equal(field(value, "protocol"), "acp") &&
			equal(field(value, "transport"), "stdio") &&
			validContract("EngineLaunch", field(value, "launch")) &&
			(!has(value, "adapter") || validContract("AdapterVersion", field(value, "adapter"))) &&
			list(field(value, "dependencies"), 0, 16, func(item any) bool { return validContract("EngineDependency", item) }) &&
			(!has(value, "authentication") || (fields(field(value, "authentication"), "terminalMethods", "agentMethods", true) &&
				list(field(field(value, "authentication"), "terminalMethods"), 0, 8, func(item any) bool { return validContract("TerminalAuthLaunch", item) }) &&
				(!has(field(value, "authentication"), "agentMethods") || list(field(field(value, "authentication"), "agentMethods"), 0, 8, func(item any) bool { return text(item, 1, 128) })))))
	case "EnginePluginManifest":
		return (fields(value, "pluginApiVersion id name version description platform desktop requires resources", "", true) &&
			equal(field(value, "pluginApiVersion"), 2) &&
			text(field(value, "id"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "name"), 1, 9007199254740991) &&
			text(field(value, "version"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
			text(field(value, "description"), 1, 9007199254740991) &&
			validContract("PlatformTarget", field(value, "platform")) &&
			(fields(field(value, "desktop"), "bundleTopLevelDir", "", true) &&
				text(field(field(value, "desktop"), "bundleTopLevelDir"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$")) &&
			(fields(field(value, "requires"), "platformAgentEngineApi webclientAgentEngineApi", "", true) &&
				equal(field(field(value, "requires"), "platformAgentEngineApi"), 2) &&
				equal(field(field(value, "requires"), "webclientAgentEngineApi"), 1)) &&
			(fields(field(value, "resources"), "agentEngines", "", true) &&
				list(field(field(value, "resources"), "agentEngines"), 1, 16, func(item any) bool { return validContract("EngineResource", item) })))
	case "EngineInstallation":
		return (fields(value, "engineId pluginId pluginVersion installationRef artifactDigest enabled", "", true) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "pluginId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "pluginVersion"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
			text(field(value, "installationRef"), 1, 9007199254740991, "^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$") &&
			text(field(value, "artifactDigest"), 1, 9007199254740991, "^sha256:[a-f0-9]{64}$") &&
			isBool(field(value, "enabled")))
	case "EngineUpsert":
		return (fields(value, "apiVersion revision engineId pluginId pluginVersion installationRef artifactDigest enabled", "", true) &&
			equal(field(value, "apiVersion"), 1) &&
			integer(field(value, "revision"), 1, 9007199254740991) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "pluginId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "pluginVersion"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
			text(field(value, "installationRef"), 1, 9007199254740991, "^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$") &&
			text(field(value, "artifactDigest"), 1, 9007199254740991, "^sha256:[a-f0-9]{64}$") &&
			isBool(field(value, "enabled")))
	case "EngineSnapshot":
		return (fields(value, "apiVersion revision entries", "", true) &&
			equal(field(value, "apiVersion"), 1) &&
			integer(field(value, "revision"), 1, 9007199254740991) &&
			list(field(value, "entries"), 0, 256, func(item any) bool { return validContract("EngineInstallation", item) }))
	case "EngineRevoke":
		return (fields(value, "apiVersion revision engineId", "", true) &&
			equal(field(value, "apiVersion"), 1) &&
			integer(field(value, "revision"), 1, 9007199254740991) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$"))
	case "ACPProject":
		return (fields(value, "engine runtimeConfig", "", true) &&
			equal(field(value, "engine"), "acp") &&
			(fields(field(value, "runtimeConfig"), "engineId workspaceRoot", "", true) &&
				text(field(field(value, "runtimeConfig"), "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
				text(field(field(value, "runtimeConfig"), "workspaceRoot"), 1, 9007199254740991, "^(/|[A-Za-z]:[/\\\\]|\\\\\\\\).+")))
	case "ConfigSelection":
		return (fields(value, "configId value", "", true) &&
			text(field(value, "configId"), 1, 9007199254740991) &&
			(text(field(value, "value"), 0, 9007199254740991) || isBool(field(value, "value"))))
	case "SessionBinding":
		return (fields(value, "schemaVersion chatId engineId profileId workspaceRoot acpSessionId contextState configSelection", "lastUsed", true) &&
			equal(field(value, "schemaVersion"), 1) &&
			text(field(value, "chatId"), 1, 9007199254740991) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "profileId"), 1, 9007199254740991, "^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$") &&
			text(field(value, "workspaceRoot"), 1, 9007199254740991, "^(/|[A-Za-z]:[/\\\\]|\\\\\\\\).+") &&
			text(field(value, "acpSessionId"), 1, 9007199254740991) &&
			oneOf(field(value, "contextState"), "prepared", "submitted", "persisted") &&
			list(field(value, "configSelection"), 0, 64, func(item any) bool { return validContract("ConfigSelection", item) }) &&
			(!has(value, "lastUsed") || (fields(field(value, "lastUsed"), "pluginVersion artifactDigest", "", true) &&
				text(field(field(value, "lastUsed"), "pluginVersion"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
				text(field(field(value, "lastUsed"), "artifactDigest"), 1, 9007199254740991, "^sha256:[a-f0-9]{64}$"))))
	case "ConfigChoice":
		return (fields(value, "value name", "group", true) &&
			text(field(value, "value"), 0, 9007199254740991) &&
			text(field(value, "name"), 1, 9007199254740991) &&
			(!has(value, "group") || text(field(value, "group"), 1, 9007199254740991)))
	case "ExternalConfigOption":
		return ((fields(value, "id name type currentValue choices", "category", true) &&
			text(field(value, "id"), 1, 9007199254740991) &&
			text(field(value, "name"), 1, 9007199254740991) &&
			(!has(value, "category") || text(field(value, "category"), 1, 9007199254740991)) &&
			equal(field(value, "type"), "select") &&
			text(field(value, "currentValue"), 0, 9007199254740991) &&
			list(field(value, "choices"), 1, 1024, func(item any) bool { return validContract("ConfigChoice", item) })) || (fields(value, "id name type currentValue", "category", true) &&
			text(field(value, "id"), 1, 9007199254740991) &&
			text(field(value, "name"), 1, 9007199254740991) &&
			(!has(value, "category") || text(field(value, "category"), 1, 9007199254740991)) &&
			equal(field(value, "type"), "boolean") &&
			isBool(field(value, "currentValue"))))
	case "PreparedSession":
		return (fields(value, "chatId engineId preparationId configRevision configOptions", "", true) &&
			text(field(value, "chatId"), 1, 9007199254740991) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "preparationId"), 1, 9007199254740991) &&
			integer(field(value, "configRevision"), 1, 9007199254740991) &&
			list(field(value, "configOptions"), 0, 64, func(item any) bool { return validContract("ExternalConfigOption", item) }))
	case "ConfigUpdate":
		return (fields(value, "chatId preparationId configRevision selections", "", true) &&
			text(field(value, "chatId"), 1, 9007199254740991) &&
			text(field(value, "preparationId"), 1, 9007199254740991) &&
			integer(field(value, "configRevision"), 1, 9007199254740991) &&
			list(field(value, "selections"), 1, 64, func(item any) bool { return validContract("ConfigSelection", item) }))
	case "TerminalAuthLaunch":
		return (fields(value, "methodId args", "", true) &&
			text(field(value, "methodId"), 1, 9007199254740991) &&
			list(field(value, "args"), 1, 16, func(item any) bool { return text(item, 0, 1024) }))
	case "AuthMethod":
		return (fields(value, "id name type available", "", true) &&
			text(field(value, "id"), 1, 9007199254740991) &&
			text(field(value, "name"), 1, 9007199254740991) &&
			oneOf(field(value, "type"), "agent", "terminal") &&
			isBool(field(value, "available")))
	case "AuthStatus":
		return (fields(value, "engineId state methods", "authOperationId", true) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
			oneOf(field(value, "state"), "unknown", "required", "authenticating", "authenticated", "error") &&
			list(field(value, "methods"), 0, 16, func(item any) bool { return validContract("AuthMethod", item) }) &&
			(!has(value, "authOperationId") || text(field(value, "authOperationId"), 1, 9007199254740991)))
	case "AuthStart":
		return (fields(value, "engineId methodId", "", true) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "methodId"), 1, 9007199254740991))
	case "ElicitationRequest":
		return (fields(value, "sessionId mode message requestedSchema", "toolCallId _meta", false) &&
			text(field(value, "sessionId"), 1, 9007199254740991) &&
			(!has(value, "toolCallId") || text(field(value, "toolCallId"), 1, 9007199254740991)) &&
			equal(field(value, "mode"), "form") &&
			text(field(value, "message"), 0, 9007199254740991) &&
			isObject(field(value, "requestedSchema")) &&
			(!has(value, "_meta") || (isObject(field(value, "_meta")) || field(value, "_meta") == nil)))
	case "QuestionOption":
		return (fields(value, "id label", "", true) &&
			text(field(value, "id"), 0, 9007199254740991) &&
			text(field(value, "label"), 1, 9007199254740991))
	case "ExternalQuestion":
		return ((fields(value, "id question required type", "", true) &&
			text(field(value, "id"), 1, 9007199254740991) &&
			text(field(value, "question"), 1, 9007199254740991) &&
			isBool(field(value, "required")) &&
			equal(field(value, "type"), "text")) || (fields(value, "id question required type options", "", true) &&
			text(field(value, "id"), 1, 9007199254740991) &&
			text(field(value, "question"), 1, 9007199254740991) &&
			isBool(field(value, "required")) &&
			equal(field(value, "type"), "select") &&
			list(field(value, "options"), 1, 128, func(item any) bool { return validContract("QuestionOption", item) })))
	case "EngineError":
		return (fields(value, "code msg data", "", true) &&
			oneOf(field(value, "code"), 400, 401, 403, 409, 429, 502) &&
			oneOf(field(value, "msg"), "invalid_arguments", "manifest_unsupported", "invalid_config_selection", "invalid_permission_option", "capability_unsupported", "execute_unsupported_agent", "elicitation_unsupported", "management_unauthorized", "management_forbidden", "awaiting_forbidden", "artifact_untrusted", "management_epoch_mismatch", "revision_stale", "revision_conflict", "registry_not_ready", "unsupported_deployment", "dependency_missing", "engine_unavailable", "auth_required", "auth_method_unavailable", "desktop_required", "interaction_required", "session_restore_failed", "session_configuration_stale", "awaiting_expired", "submit_conflict", "artifact_digest_mismatch", "engine_busy", "rpc_connection_lost") &&
			(fields(field(value, "data"), "errorCode executionState retryable", "reason", true) &&
				oneOf(field(field(value, "data"), "errorCode"), "invalid_arguments", "manifest_unsupported", "invalid_config_selection", "invalid_permission_option", "capability_unsupported", "execute_unsupported_agent", "elicitation_unsupported", "management_unauthorized", "management_forbidden", "awaiting_forbidden", "artifact_untrusted", "management_epoch_mismatch", "revision_stale", "revision_conflict", "registry_not_ready", "unsupported_deployment", "dependency_missing", "engine_unavailable", "auth_required", "auth_method_unavailable", "desktop_required", "interaction_required", "session_restore_failed", "session_configuration_stale", "awaiting_expired", "submit_conflict", "artifact_digest_mismatch", "engine_busy", "rpc_connection_lost") &&
				(!has(field(value, "data"), "reason") || text(field(field(value, "data"), "reason"), 1, 9007199254740991, "^[a-z][a-z0-9_]*$")) &&
				oneOf(field(field(value, "data"), "executionState"), "not_started", "started", "unknown") &&
				equal(field(field(value, "data"), "retryable"), false)))
	case "ManagementBootstrap":
		return (fields(value, "schemaVersion hostId launchNonce managementSecret deliveryRoot", "", true) &&
			equal(field(value, "schemaVersion"), 1) &&
			text(field(value, "hostId"), 1, 9007199254740991) &&
			text(field(value, "launchNonce"), 1, 9007199254740991) &&
			text(field(value, "managementSecret"), 1, 9007199254740991, "^[a-f0-9]{64}$") &&
			text(field(value, "deliveryRoot"), 1, 9007199254740991, "^(/|[A-Za-z]:[/\\\\]|\\\\\\\\).+"))
	case "ManagementStatus":
		return (fields(value, "apiVersion hostId epoch ready appliedRevision", "", true) &&
			equal(field(value, "apiVersion"), 1) &&
			text(field(value, "hostId"), 1, 9007199254740991) &&
			text(field(value, "epoch"), 1, 9007199254740991) &&
			isBool(field(value, "ready")) &&
			integer(field(value, "appliedRevision"), 0, 9007199254740991))
	case "SyncAck":
		return (fields(value, "apiVersion appliedRevision action ready drainingEngineIds", "", true) &&
			equal(field(value, "apiVersion"), 1) &&
			integer(field(value, "appliedRevision"), 0, 9007199254740991) &&
			oneOf(field(value, "action"), "applied", "idempotent") &&
			isBool(field(value, "ready")) &&
			list(field(value, "drainingEngineIds"), 0, 256, func(item any) bool {
				return text(item, 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$")
			}))
	case "ExecutionBackend":
		return oneOf(value, "native", "acp", "proxy", "channel")
	case "EngineCapabilities":
		return (fields(value, "question permission configuration resume image file planning access_level service_tier steer boolean_config", "", true) &&
			isBool(field(value, "question")) &&
			isBool(field(value, "permission")) &&
			isBool(field(value, "configuration")) &&
			isBool(field(value, "resume")) &&
			isBool(field(value, "image")) &&
			isBool(field(value, "file")) &&
			isBool(field(value, "planning")) &&
			isBool(field(value, "access_level")) &&
			isBool(field(value, "service_tier")) &&
			isBool(field(value, "steer")) &&
			isBool(field(value, "boolean_config")))
	case "EngineDirectoryEntry":
		return (fields(value, "engineId pluginId pluginVersion enabled syncState dependencyState authState runtimeState capabilities", "", true) &&
			text(field(value, "engineId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "pluginId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "pluginVersion"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
			isBool(field(value, "enabled")) &&
			oneOf(field(value, "syncState"), "pending", "applied") &&
			oneOf(field(value, "dependencyState"), "unknown", "missing", "ready") &&
			oneOf(field(value, "authState"), "unknown", "required", "authenticating", "authenticated", "error") &&
			oneOf(field(value, "runtimeState"), "stopped", "starting", "ready", "busy", "draining", "failed") &&
			validContract("EngineCapabilities", field(value, "capabilities")))
	case "EngineDirectory":
		return (fields(value, "apiVersion catalogGeneration revision ready engines", "", true) &&
			equal(field(value, "apiVersion"), 1) &&
			text(field(value, "catalogGeneration"), 1, 9007199254740991) &&
			integer(field(value, "revision"), 0, 9007199254740991) &&
			isBool(field(value, "ready")) &&
			list(field(value, "engines"), 0, 256, func(item any) bool { return validContract("EngineDirectoryEntry", item) }))
	case "DirectoryChanged":
		return (fields(value, "catalogGeneration revision", "", true) &&
			text(field(value, "catalogGeneration"), 1, 9007199254740991) &&
			integer(field(value, "revision"), 0, 9007199254740991))
	case "EpochMillis":
		return integer(value, 1000000000000, 9007199254740991)
	case "QuestionAnswer":
		return (fields(value, "id answer", "", true) &&
			text(field(value, "id"), 1, 9007199254740991) &&
			text(field(value, "answer"), 0, 9007199254740991))
	case "DeliverySource":
		return (fields(value, "kind authorizationRef archiveDigest", "", true) &&
			oneOf(field(value, "kind"), "market", "local-import") &&
			text(field(value, "authorizationRef"), 1, 9007199254740991) &&
			text(field(value, "archiveDigest"), 1, 9007199254740991, "^sha256:[a-f0-9]{64}$"))
	case "TrustedDeliveryRecord":
		return (fields(value, "schemaVersion installationRef ownerHostId pluginId pluginVersion platform artifactDigest source", "", true) &&
			equal(field(value, "schemaVersion"), 1) &&
			text(field(value, "installationRef"), 1, 9007199254740991, "^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$") &&
			text(field(value, "ownerHostId"), 1, 9007199254740991) &&
			text(field(value, "pluginId"), 1, 9007199254740991, "^[a-z0-9][a-z0-9.-]*$") &&
			text(field(value, "pluginVersion"), 1, 9007199254740991, "^[0-9]+\\.[0-9]+\\.[0-9]+$") &&
			validContract("PlatformTarget", field(value, "platform")) &&
			text(field(value, "artifactDigest"), 1, 9007199254740991, "^sha256:[a-f0-9]{64}$") &&
			validContract("DeliverySource", field(value, "source")))
	case "SessionPrepare":
		return (fields(value, "chatId", "agentKey", true) &&
			(!has(value, "agentKey") || text(field(value, "agentKey"), 1, 9007199254740991)) &&
			text(field(value, "chatId"), 1, 9007199254740991))
	case "AuthCancel":
		return (fields(value, "authOperationId", "", true) &&
			text(field(value, "authOperationId"), 1, 9007199254740991))
	default:
		return false
	}
}

// Validate checks data only; it neither authenticates nor authorizes execution.
func Validate(name string, raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid_arguments: %w", err)
	}
	if !validContract(name, value) {
		return fmt.Errorf("invalid_arguments: invalid %s", name)
	}
	return nil
}
