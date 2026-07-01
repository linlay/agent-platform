//go:build windows

package runtimeenv

import "syscall"

var (
	kernel32DLL = syscall.NewLazyDLL("kernel32.dll")
	getACPProc  = kernel32DLL.NewProc("GetACP")
)

func detectACP() uint32 {
	codePage, _, _ := getACPProc.Call()
	return uint32(codePage)
}
