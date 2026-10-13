package tools

import "agent-platform/internal/toolinput"

// Normalize only explicitly declared native fields; other Desktop action args
// remain opaque and Desktop retains their validation.
func normalizeDesktopControlBooleans(tool, action string, args map[string]any) {
	if tool != "desktop_site" || action != "site.setPinned" {
		return
	}
	params, _ := args["args"].(map[string]any)
	toolinput.NormalizeBooleanFields(params, map[string]string{"pinned": "b!"})
}
