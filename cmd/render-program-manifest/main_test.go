package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderWindowsManifest(t *testing.T) {
	output := filepath.Join(t.TempDir(), "manifest.json")
	if err := render("../../scripts/release-assets/manifest.template.json", output, "v1.2.3", "windows", "amd64", "backend/agent-platform.exe", "agent-platform-v1.2.3-windows-amd64.zip"); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "__") {
		t.Fatal("rendered manifest contains an unresolved placeholder")
	}
	var manifest map[string]any
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	platform := manifest["platform"].(map[string]any)
	if platform["os"] != "windows" || platform["arch"] != "amd64" {
		t.Fatalf("platform = %#v", platform)
	}
}

func TestRenderRejectsInvalidTarget(t *testing.T) {
	err := render("unused", filepath.Join(t.TempDir(), "manifest.json"), "v1.2.3", "solaris", "mips", "backend/app", "bundle.zip")
	if err == nil {
		t.Fatal("render accepted an invalid target")
	}
}
