package agentconfig

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Desktop publishes and revokes this file using rename/delete. Go's os.Open
// does not request FILE_SHARE_DELETE on Windows, so even a short token read can
// prevent that publication. Keep the read handle on its original file snapshot
// while permitting Desktop to replace or remove the directory entry.
func openIdentityFile(path string) (*os.File, error) {
	name, err := filepath.Abs(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// Preserve long data-root paths, including UNC shares. Do not add another
	// prefix to an already extended or device-qualified Windows path.
	if !strings.HasPrefix(name, `\\?\`) && !strings.HasPrefix(name, `\\.\`) {
		if strings.HasPrefix(name, `\\`) {
			name = `\\?\UNC\` + strings.TrimPrefix(name, `\\`)
		} else {
			name = `\\?\` + name
		}
	}
	encoded, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(encoded, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
