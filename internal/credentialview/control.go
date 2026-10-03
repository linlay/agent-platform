package credentialview

import (
	"encoding/json"
	"strings"
)

// Buffer incomplete candidates until their resource type can be validated.
// Known candidates are model-authored text; server-resolved secrets never enter here.
func IsCatalogTool(name string) bool { return name == "catalog_query" || name == "catalog_manage" }
func CatalogArguments(raw string) string {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return `{"redacted":true}`
	}
	if params, ok := args["args"].(map[string]any); ok {
		switch params["resourceType"] {
		case "agent", "team", "skill", "connector":
			// Preserve the submitted candidate so subsequent model turns can edit it.
		default:
			if _, ok := params["content"]; ok {
				params["content"] = Hidden
			}
		}
	}
	b, e := json.Marshal(args)
	if e != nil {
		return `{"redacted":true}`
	}
	return string(b)
}
func HasCatalogTools(names []string) bool {
	for _, name := range names {
		if IsCatalogTool(strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}
