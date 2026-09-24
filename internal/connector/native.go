package connector

// NativeTools is the platform registry, never a package-selected handler name.
func (p Package) NativeTools() []string {
	if p.ID == "builtin.desktop" && p.Type == "native" && len(p.Native) == 2 {
		return []string{"desktop_action", "desktop_cdp"}
	}
	return nil
}
