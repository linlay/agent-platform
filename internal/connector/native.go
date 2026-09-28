package connector

import "errors"

const (
	DesktopConnectorID    = "builtin.desktop"
	DesktopWebConnectorID = "builtin.desktop-web"
)

var ErrDesktopVariantConflict = errors.New("choose either Desktop or Desktop (Web) for one Agent")

func IsDesktop(id string) bool {
	return id == DesktopConnectorID || id == DesktopWebConnectorID
}

func ValidateDesktopSelection(ids []string) error {
	var selected string
	for _, id := range ids {
		if IsDesktop(id) {
			if selected != "" && selected != id {
				return ErrDesktopVariantConflict
			}
			selected = id
		}
	}
	return nil
}

func (p Package) MutuallyExclusiveWith() []string {
	switch p.ID {
	case DesktopConnectorID:
		return []string{DesktopWebConnectorID}
	case DesktopWebConnectorID:
		return []string{DesktopConnectorID}
	default:
		return nil
	}
}

// NativeTools is the platform registry, never a package-selected handler name.
func (p Package) NativeTools() []string {
	if IsDesktop(p.ID) && p.Type == "native" && len(p.Native) == 2 {
		return []string{"desktop_action", "desktop_cdp"}
	}
	return nil
}
