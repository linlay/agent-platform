package runtimeenv

import "runtime"

type Info struct {
	GOOS   string
	GOARCH string
	ACP    uint32
}

func Detect() Info {
	return detectWith(runtime.GOOS, runtime.GOARCH, detectACP)
}

func (i Info) IsZero() bool {
	return i.GOOS == "" && i.GOARCH == "" && i.ACP == 0
}

func detectWith(goos string, goarch string, acp func() uint32) Info {
	info := Info{
		GOOS:   goos,
		GOARCH: goarch,
	}
	if goos == "windows" && acp != nil {
		info.ACP = acp()
	}
	return info
}
