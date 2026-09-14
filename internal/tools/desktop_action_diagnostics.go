package tools

// Only bounded schema diagnostics cross into model context. Never copy arbitrary
// input values, nested payloads, or undocumented provider fields.
func appendDesktopActionIssues(out, details map[string]any) {
	raw, ok := details["issues"].([]any)
	if !ok {
		return
	}
	issues := make([]any, 0, 16)
	for _, value := range raw {
		if len(issues) == 16 {
			break
		}
		issue, ok := value.(map[string]any)
		if !ok {
			continue
		}
		clean := map[string]any{}
		for _, key := range []string{"path", "code", "expected", "actual"} {
			value, ok := issue[key].(string)
			if !ok || value == "" || len(value) > 256 {
				continue
			}
			if key == "actual" {
				switch value {
				case "missing", "null", "array", "object", "string", "number", "boolean", "present":
				default:
					continue
				}
			}
			clean[key] = value
		}
		if clean["path"] != nil && clean["code"] != nil {
			issues = append(issues, clean)
		}
	}
	if len(issues) > 0 {
		out["issues"] = issues
	}
}
