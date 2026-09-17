package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getIEProxyConfig = windows.NewLazySystemDLL("winhttp.dll").NewProc("WinHttpGetIEProxyConfigForCurrentUser")
var globalFree = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")

// Native pointer alignment supplies the padding after BOOL on 64-bit Windows.
type ieProxyConfig struct {
	AutoDetect    int32
	AutoConfigURL *uint16
	Proxy         *uint16
	Bypass        *uint16
}

func readSystemSettings(ctx context.Context) (systemSettings, error) {
	if err := ctx.Err(); err != nil {
		return systemSettings{}, err
	}
	if err := getIEProxyConfig.Find(); err != nil {
		return systemSettings{}, errors.New("WinHTTP proxy configuration API unavailable")
	}
	var cfg ieProxyConfig
	ok, _, callErr := getIEProxyConfig.Call(uintptr(unsafe.Pointer(&cfg)))
	for _, p := range []*uint16{cfg.AutoConfigURL, cfg.Proxy, cfg.Bypass} {
		if p != nil {
			defer globalFree.Call(uintptr(unsafe.Pointer(p)))
		}
	}
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) {
			return systemSettings{}, nil
		}
		return systemSettings{}, fmt.Errorf("WinHttpGetIEProxyConfigForCurrentUser failed: %w", callErr)
	}
	pacURL := windows.UTF16PtrToString(cfg.AutoConfigURL)
	detect := cfg.AutoDetect != 0
	settings, err := parseWindowsSettings(windows.UTF16PtrToString(cfg.Proxy), windows.UTF16PtrToString(cfg.Bypass), detect || pacURL != "")
	if err == nil && settings.Auto {
		settings.ResolveAuto = func(ctx context.Context, target *url.URL) (*url.URL, error) {
			// Own a URL copy: the native worker may outlive the request deadline.
			clean := *target
			clean.User, clean.Fragment = nil, ""
			return windowsAutoWorkers.resolve(ctx, func() (*url.URL, error) {
				return resolveWindowsAutoProxy(pacURL, detect, &clean)
			})
		}
	}
	return settings, err
}
