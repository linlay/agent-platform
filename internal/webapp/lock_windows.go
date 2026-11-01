//go:build windows

package webapp

import "os"

// WebApp hosting is Linux Compose only; fail closed outside that deployment.
func acquireInstance(f *os.File) error { return fail("webapp_host_platform_unsupported", 503) }
func availableBytes(path string) (uint64, error) {
	return 0, fail("webapp_host_platform_unsupported", 503)
}
