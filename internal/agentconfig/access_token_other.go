//go:build !windows

package agentconfig

import "os"

func openIdentityFile(path string) (*os.File, error) { return os.Open(path) }
