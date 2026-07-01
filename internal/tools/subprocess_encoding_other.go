//go:build !windows

package tools

func subprocessOutputCodePage() uint32 {
	return 0
}
