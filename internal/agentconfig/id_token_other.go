//go:build !windows

package agentconfig

import "os"

func openSsoIdTokenFile(path string) (*os.File, error) { return os.Open(path) }
