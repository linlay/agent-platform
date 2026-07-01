package runtimeenv

import "testing"

func TestDetectWithNonWindowsACPZero(t *testing.T) {
	info := detectWith("darwin", "arm64", func() uint32 { return 936 })

	if info.GOOS != "darwin" || info.GOARCH != "arm64" || info.ACP != 0 {
		t.Fatalf("unexpected runtime env: %#v", info)
	}
}

func TestDetectWithWindowsACP(t *testing.T) {
	info := detectWith("windows", "amd64", func() uint32 { return 936 })

	if info.GOOS != "windows" || info.GOARCH != "amd64" || info.ACP != 936 {
		t.Fatalf("unexpected runtime env: %#v", info)
	}
}

func TestDetectReturnsCurrentPlatform(t *testing.T) {
	info := Detect()

	if info.GOOS == "" || info.GOARCH == "" {
		t.Fatalf("expected current platform, got %#v", info)
	}
	if info.GOOS != "windows" && info.ACP != 0 {
		t.Fatalf("non-Windows ACP must be zero, got %#v", info)
	}
}
