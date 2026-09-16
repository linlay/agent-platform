package tools

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateDesktopAwcpCall is shared by pre-approval preparation and execution.
// It only checks the static envelope, never a page Action's business schema.
func ValidateDesktopAwcpCall(args map[string]any) error {
	method, _ := args["method"].(string)
	method = strings.TrimSpace(method)
	if method != desktopAwcpGetSnapshotMethod && method != desktopAwcpInvokeMethod {
		return nil
	}
	expected := `{"method":"AWCP.getSnapshot"}`
	if method == desktopAwcpInvokeMethod {
		expected = `{"method":"AWCP.invoke","params":{"action":{"<discovered action>":{}}}}`
	}
	reject := func(message string) error {
		return fmt.Errorf("%s: %s. No page Action was invoked. AWCP uses the current authorized page, not CDP target fields. Expected shape: %s", method, message, expected)
	}
	var extra []string
	for key := range args {
		if key != "method" && key != "params" {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return reject(fmt.Sprintf("remove unsupported top-level fields %q", extra))
	}
	if method == desktopAwcpGetSnapshotMethod {
		if raw, present := args["params"]; present {
			params, ok := raw.(map[string]any)
			if !ok || params == nil || len(params) != 0 {
				return reject("omit params or pass an empty JSON object {}")
			}
		}
		return nil
	}
	if _, _, err := DesktopAwcpInvocation(args); err != nil {
		return reject(err.Error())
	}
	return nil
}

// DesktopAwcpInvocation decodes the single model-facing Action envelope.
// revision is deliberately absent; only the run can provide its trusted binding.
func DesktopAwcpInvocation(args map[string]any) (string, map[string]any, error) {
	params, ok := args["params"].(map[string]any)
	if !ok || len(params) != 1 {
		return "", nil, fmt.Errorf("params must contain exactly action")
	}
	actions, ok := params["action"].(map[string]any)
	if !ok || len(actions) != 1 {
		return "", nil, fmt.Errorf("action must be an object with exactly one discovered Action key")
	}
	for action, raw := range actions {
		if utf16Length(action) > 128 || !desktopAwcpActionPattern.MatchString(action) {
			return "", nil, fmt.Errorf("invalid Action key")
		}
		input, ok := raw.(map[string]any)
		if !ok || input == nil || !isDesktopJSONValue(input) {
			return "", nil, fmt.Errorf("the Action value must be a JSON object matching its inputSchema")
		}
		return action, input, nil
	}
	return "", nil, fmt.Errorf("missing Action key")
}
