package httpclient

import (
	"fmt"
	"net/url"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winhttpDLL         = windows.NewLazySystemDLL("winhttp.dll")
	winhttpOpen        = winhttpDLL.NewProc("WinHttpOpen")
	winhttpClose       = winhttpDLL.NewProc("WinHttpCloseHandle")
	winhttpTimeouts    = winhttpDLL.NewProc("WinHttpSetTimeouts")
	winhttpProxyForURL = winhttpDLL.NewProc("WinHttpGetProxyForUrl")
	windowsAutoWorkers = autoProxyWorkers{slots: make(chan struct{}, 4), timeout: 10 * time.Second}
)

// Field order and native alignment follow winhttp.h (DWORD/pointer/BOOL).
type winhttpAutoOptions struct {
	Flags, DetectFlags uint32
	ConfigURL          *uint16
	Reserved           unsafe.Pointer
	ReservedFlags      uint32
	AutoLogon          int32
}

type winhttpProxyInfo struct {
	Access        uint32
	Proxy, Bypass *uint16
}

func resolveWindowsAutoProxy(pacURL string, detect bool, target *url.URL) (*url.URL, error) {
	for _, proc := range []*windows.LazyProc{winhttpOpen, winhttpClose, winhttpTimeouts, winhttpProxyForURL} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("WinHTTP automatic proxy API unavailable: %w", err)
		}
	}
	agent, _ := windows.UTF16PtrFromString("agent-platform")
	handle, _, err := winhttpOpen.Call(uintptr(unsafe.Pointer(agent)), 1, 0, 0, 0)
	if handle == 0 {
		return nil, fmt.Errorf("WinHttpOpen: %w", err)
	}
	defer winhttpClose.Call(handle)
	ok, _, err := winhttpTimeouts.Call(handle, 5000, 5000, 5000, 5000)
	if ok == 0 {
		return nil, fmt.Errorf("WinHttpSetTimeouts: %w", err)
	}
	var options winhttpAutoOptions
	if pacURL != "" {
		options.Flags |= 2 // WINHTTP_AUTOPROXY_CONFIG_URL
		options.ConfigURL, err = windows.UTF16PtrFromString(pacURL)
		if err != nil {
			return nil, fmt.Errorf("invalid system PAC URL")
		}
	}
	if detect {
		options.Flags |= 1      // WINHTTP_AUTOPROXY_AUTO_DETECT
		options.DetectFlags = 3 // DHCP and DNS-A
	}
	// Never automatically disclose the process user's credentials to PAC hosts.
	options.AutoLogon = 0
	u, err := windows.UTF16PtrFromString(target.String())
	if err != nil {
		return nil, fmt.Errorf("invalid automatic proxy target URL")
	}
	var info winhttpProxyInfo
	ok, _, err = winhttpProxyForURL.Call(handle, uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&info)))
	runtime.KeepAlive(u)
	runtime.KeepAlive(options)
	for _, p := range []*uint16{info.Proxy, info.Bypass} {
		if p != nil {
			defer globalFree.Call(uintptr(unsafe.Pointer(p)))
		}
	}
	if ok == 0 {
		return nil, fmt.Errorf("WinHttpGetProxyForUrl: %w", err)
	}
	return windowsAutoProxyResult(target, info.Access, windows.UTF16PtrToString(info.Proxy), windows.UTF16PtrToString(info.Bypass))
}
