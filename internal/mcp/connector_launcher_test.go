package mcp

import (
	"path/filepath"
	"testing"
)

func TestConnectorLauncherAdaptsOnlyWindowsBatchInsideConnectorBin(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	for _, tc := range []struct {
		entry, binDir, goos string
		want                bool
	}{
		{filepath.Join(bin, "office.cmd"), bin, "windows", true},
		{filepath.Join(bin, "office.BAT"), bin, "windows", true},
		{filepath.Join(bin, "office.exe"), bin, "windows", false},
		{filepath.Join(bin, "..", "npx.cmd"), bin, "windows", false},
		{filepath.Join(t.TempDir(), "npx.cmd"), bin, "windows", false},
		{filepath.Join(bin, "office.cmd"), "", "windows", false},
		{filepath.Join(bin, "office.cmd"), bin, "darwin", false},
	} {
		if got := connectorLauncher(tc.entry, tc.binDir, tc.goos); got != tc.want {
			t.Fatalf("%s in %q on %s: got %v", tc.entry, tc.binDir, tc.goos, got)
		}
	}
}
