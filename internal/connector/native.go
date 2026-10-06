package connector

import "strings"

const (
	KanbanControlConnectorID   = "builtin.kanban-control"
	TaskControlConnectorID     = "builtin.task-control"
	PlatformControlConnectorID = "builtin.platform-control"
	WebControlConnectorID      = "builtin.web-control"
)

// nativeConnectorTools is the single source of truth for which Platform tools
// a native connector mounts. Tool names never come from the package itself.
var nativeConnectorTools = map[string][]string{
	KanbanControlConnectorID:   {"desktop_kanban"},
	TaskControlConnectorID:     {"chat_start", "chat_get_status", "chat_interrupt", "chat_query", "chat_manage", "automation_query", "automation_manage"},
	PlatformControlConnectorID: {"catalog_query", "catalog_manage", "platform_inspect", "desktop_shell", "desktop_settings", "desktop_site", "desktop_webapp", "desktop_service", "desktop_market"},
	WebControlConnectorID: {
		"workpanel_state", "workpanel_open", "workpanel_close",
		"surface_list", "surface_state", "surface_navigate", "surface_activate", "surface_close",
		"surface_screenshot", "surface_evaluate", "surface_click", "surface_element", "surface_cdp",
		"awcp_manual", "awcp_invoke",
	},
}

// IsNative reports whether id is a native connector compiled into Platform.
func IsNative(id string) bool {
	_, ok := nativeConnectorTools[id]
	return ok
}

// NativeConnectorIDs lists the embedded native connectors in stable order.
func NativeConnectorIDs() []string {
	return []string{KanbanControlConnectorID, PlatformControlConnectorID, TaskControlConnectorID, WebControlConnectorID}
}

// NativeToolConnector returns the native connector that owns a Platform tool.
func NativeToolConnector(tool string) (string, bool) {
	for _, id := range NativeConnectorIDs() {
		for _, name := range nativeConnectorTools[id] {
			if name == tool {
				return id, true
			}
		}
	}
	return "", false
}

// NativeTools is the platform registry, never a package-selected handler name.
func (p Package) NativeTools() []string {
	if p.Type != "native" || !IsNative(p.ID) {
		return nil
	}
	return append([]string(nil), nativeConnectorTools[p.ID]...)
}

// NativeToolRequiresDesktop reports tools that only work when Platform runs
// inside Desktop. The WorkPanel tools also reach a standalone WebClient; page
// control and AWCP need the Desktop page host.
func NativeToolRequiresDesktop(tool string) bool {
	id, ok := NativeToolConnector(tool)
	return ok && (id == KanbanControlConnectorID || id == PlatformControlConnectorID && strings.HasPrefix(tool, "desktop_") || id == WebControlConnectorID && !strings.HasPrefix(tool, "workpanel_"))
}
