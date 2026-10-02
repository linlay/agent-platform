package connector

import "strings"

const (
	DesktopConnectorID    = "builtin.desktop"
	WebControlConnectorID = "builtin.web-control"
)

// nativeConnectorCapabilities is the exact native.json contract of every
// native connector compiled into Platform.
var nativeConnectorCapabilities = map[string][]string{
	DesktopConnectorID:    {"desktop.action"},
	WebControlConnectorID: {"web.workpanel", "web.surface", "web.awcp"},
}

// nativeConnectorTools is the single source of truth for which Platform tools
// a native connector mounts. Tool names never come from the package itself.
var nativeConnectorTools = map[string][]string{
	DesktopConnectorID: {"desktop_action"},
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
	return []string{DesktopConnectorID, WebControlConnectorID}
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
	if p.Type != "native" || len(p.Native) == 0 {
		return nil
	}
	return append([]string(nil), nativeConnectorTools[p.ID]...)
}

func validNativeCapabilities(id string, capabilities []string) bool {
	expected, ok := nativeConnectorCapabilities[id]
	if !ok || len(capabilities) != len(expected) {
		return false
	}
	for index, capability := range expected {
		if capabilities[index] != capability {
			return false
		}
	}
	return true
}

// NativeToolRequiresDesktop reports tools that only work when Platform runs
// inside Desktop. The WorkPanel tools also reach a standalone WebClient; page
// control and AWCP need the Desktop page host.
func NativeToolRequiresDesktop(tool string) bool {
	id, ok := NativeToolConnector(tool)
	return ok && id == WebControlConnectorID && !strings.HasPrefix(tool, "workpanel_")
}
