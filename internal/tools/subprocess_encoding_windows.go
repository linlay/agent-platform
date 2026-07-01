//go:build windows

package tools

import "syscall"

var (
	kernel32DLL = syscall.NewLazyDLL("kernel32.dll")
	getACPProc  = kernel32DLL.NewProc("GetACP")
)

func subprocessOutputCodePage() uint32 {
	codePage, _, _ := getACPProc.Call()
	return uint32(codePage)
}
