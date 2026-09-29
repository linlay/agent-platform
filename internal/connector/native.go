package connector

const (
	DesktopConnectorID    = "builtin.desktop"
	DesktopWebConnectorID = "builtin.desktop-web"
)

func IsDesktop(id string) bool {
	return id == DesktopConnectorID || id == DesktopWebConnectorID
}

// NativeTools is the platform registry, never a package-selected handler name.
func (p Package) NativeTools() []string {
	if IsDesktop(p.ID) && p.Type == "native" && len(p.Native) == 2 {
		return []string{"desktop_action", "desktop_cdp"}
	}
	return nil
}
