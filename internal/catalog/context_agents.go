package catalog

import "strings"

// Legacy candidate configuration is ignored, even if its value has an invalid
// type. Keep one bounded, source-independent warning on the loaded admin record.
func ignoredContextAgentsDiagnostics(definition map[string]any) []AdminAgentDiagnostic {
	context := mapNode(definition["contextConfig"])
	_, legacy := context["agents"]
	for _, tag := range listStrings(context["tags"]) {
		if strings.EqualFold(strings.TrimSpace(tag), "agents") {
			legacy = true
		}
	}
	if !legacy {
		return nil
	}
	return []AdminAgentDiagnostic{{Severity: "warning", Code: "context_agents_ignored", Message: "contextConfig.agents and the agents context tag are deprecated and ignored; use catalog_query.list with resourceType=agent for discovery (requires builtin.platform-control)."}}
}
