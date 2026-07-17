package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	template := flag.String("template", "", "manifest template")
	output := flag.String("output", "", "rendered manifest")
	version := flag.String("version", "", "release version")
	targetOS := flag.String("os", "", "target OS")
	targetArch := flag.String("arch", "", "target architecture")
	backend := flag.String("backend", "", "backend entry")
	asset := flag.String("asset", "", "archive file name")
	flag.Parse()
	if err := render(*template, *output, *version, *targetOS, *targetArch, *backend, *asset); err != nil {
		fmt.Fprintln(os.Stderr, "render program manifest:", err)
		os.Exit(1)
	}
}

func render(template, output, version, targetOS, targetArch, backend, asset string) error {
	for name, value := range map[string]string{
		"template": template, "output": output, "version": version, "os": targetOS,
		"arch": targetArch, "backend": backend, "asset": asset,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if targetOS != "darwin" && targetOS != "linux" && targetOS != "windows" {
		return fmt.Errorf("unsupported target OS %q", targetOS)
	}
	if targetArch != "amd64" && targetArch != "arm64" {
		return fmt.Errorf("unsupported target architecture %q", targetArch)
	}
	payload, err := os.ReadFile(template)
	if err != nil {
		return err
	}
	start, stop, deploy, common := "start.sh", "stop.sh", "deploy.sh", "scripts/program-common.sh"
	sidecar := "bin/kbase-lance-engine"
	errorLog := ""
	if targetOS == "windows" {
		start, stop, deploy, common = "start.ps1", "stop.ps1", "deploy.ps1", "scripts/program-common.ps1"
		sidecar = "bin/kbase-lance-engine.exe"
		errorLog = "    \"errorLogRelativePath\": \"run/agent-platform.stderr.log\","
	}
	poppler := ""
	if targetOS == "darwin" && targetArch == "arm64" || targetOS == "windows" && targetArch == "amd64" {
		binary := "bin/pdftotext"
		if targetOS == "windows" {
			binary += ".exe"
		}
		poppler = ",\n      \"" + binary + "\",\n      \"libexec/poppler-pdftotext/" + targetOS + "-" + targetArch + "\""
	}
	rendered := string(payload)
	rendered = strings.ReplaceAll(rendered, "    \"__ERROR_LOG_LINE__\": \"__ERROR_LOG_LINE__\",", errorLog)
	for placeholder, value := range map[string]string{
		"__VERSION__": version, "__TARGET_OS__": targetOS, "__TARGET_ARCH__": targetArch,
		"__BACKEND_ENTRY__": backend, "__SIDECAR_ENTRY__": sidecar,
		"__START_SCRIPT__": start, "__STOP_SCRIPT__": stop, "__DEPLOY_SCRIPT__": deploy,
		"__PROGRAM_COMMON__": common, "__POPLER_REQUIRED_PATHS__": poppler,
		"__ASSET_FILENAME__": asset,
	} {
		rendered = strings.ReplaceAll(rendered, placeholder, value)
	}
	if strings.Contains(rendered, "__") {
		return errors.New("manifest contains an unresolved placeholder")
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(rendered), &manifest); err != nil {
		return err
	}
	platform, ok := manifest["platform"].(map[string]any)
	if !ok || platform["os"] != targetOS || platform["arch"] != targetArch {
		return errors.New("rendered manifest platform mismatch")
	}
	runtime, ok := manifest["runtime"].(map[string]any)
	if !ok {
		return errors.New("rendered manifest runtime is missing")
	}
	required, ok := runtime["requiredPaths"].([]any)
	if !ok || len(required) == 0 {
		return errors.New("rendered manifest requiredPaths are missing")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(output, []byte(strings.TrimRight(rendered, "\r\n")+"\n"), 0o644)
}
