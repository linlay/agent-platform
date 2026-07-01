//go:build !windows

package runtimeenv

func detectACP() uint32 {
	return 0
}
