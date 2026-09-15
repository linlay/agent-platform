package tools

import (
	"regexp"
	"strings"
)

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

var desktopDiagnosticSecret = regexp.MustCompile(`(?i)(token|password|secret|authorization|cookie|api[_-]?key)\s*[=:]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
var desktopDiagnosticBearer = regexp.MustCompile(`(?i)Bearer\s+\S+`)
var desktopDiagnosticPath = regexp.MustCompile(`([A-Za-z]:[\\/]|/(Users|home|private|tmp)/)[^\s'"\n)]+`)

// Preserve the public Action diagnostic skeleton, independently of CDP.
// Unknown fields and raw inputs never become model-visible metadata.
func appendDesktopActionDiagnostics(out, details map[string]any) {
	budget := 12000
	for _, key := range []string{"category", "stage", "executionState", "diagnosticId", "path", "field", "suggestion", "recovery", "cause", "context", "webappId", "operation", "archivePath", "expectedId", "diagnostic", "state", "item", "info", "missingFields"} {
		if value, ok := details[key]; ok {
			out[key] = cleanDesktopActionDiagnostic(value, &budget, 0)
		}
	}
}

func cleanDesktopActionDiagnostic(value any, budget *int, depth int) any {
	if *budget <= 0 || depth > 6 {
		return "[TRUNCATED]"
	}
	*budget -= 8
	switch v := value.(type) {
	case string:
		v = desktopDiagnosticSecret.ReplaceAllString(v, "$1=[REDACTED]")
		v = desktopDiagnosticBearer.ReplaceAllString(v, "Bearer [REDACTED]")
		v = desktopDiagnosticPath.ReplaceAllString(v, "[HOST_PATH]")
		chars := []rune(v)
		limit := min(2048, *budget)
		if len(chars) > limit {
			chars = chars[:limit]
		}
		*budget -= len(chars)
		return string(chars)
	case map[string]any:
		result := map[string]any{}
		for key, child := range v {
			if len(result) >= 32 || *budget <= 0 {
				break
			}
			*budget -= len(key)
			if len(key) > 128 {
				continue
			}
			normalized := strings.ToLower(key)
			sensitive := false
			for _, word := range []string{"token", "password", "secret", "authorization", "cookie", "credential", "privatekey", "stack", "workspaceRoot"} {
				if strings.Contains(normalized, strings.ToLower(word)) {
					sensitive = true
					break
				}
			}
			if sensitive {
				result[key] = "[REDACTED]"
			} else {
				result[key] = cleanDesktopActionDiagnostic(child, budget, depth+1)
			}
		}
		return result
	case []any:
		result := make([]any, 0, min(len(v), 20))
		for _, child := range v {
			if len(result) >= 20 {
				break
			}
			result = append(result, cleanDesktopActionDiagnostic(child, budget, depth+1))
		}
		return result
	case bool, float64, nil:
		return v
	default:
		return nil
	}
}
