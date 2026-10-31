// Package webapphost owns Linux Compose WebApp process prerequisites.
// It does not infer deployment mode or start processes when the host is disabled.
package webapphost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	NodeVersion         = "v24.22.2"
	NodePath            = "/opt/node/bin/node"
	SupervisorPath      = "/opt/libexec/webapp-supervisor"
	RequiredLandlockABI = 4
	ProtocolVersion     = 2
)

type Issue struct {
	Code string `json:"code"`
}

// Status describes prerequisites, not a promise that application hosting is
// implemented. Keep this distinction until installation and lifecycle land.
type Status struct {
	Host                      string  `json:"host"`
	OS                        string  `json:"os"`
	Arch                      string  `json:"arch"`
	NodeVersion               string  `json:"nodeVersion,omitempty"`
	NodeReady                 bool    `json:"nodeReady"`
	SupervisorReady           bool    `json:"supervisorReady"`
	LandlockABI               int     `json:"landlockAbi"`
	IsolationReady            bool    `json:"isolationReady"`
	IsolationProfile          string  `json:"isolationProfile,omitempty"`
	IPCScoped                 bool    `json:"ipcScoped"`
	DeviceIoctlRestricted     bool    `json:"deviceIoctlRestricted"`
	BackendPrerequisitesReady bool    `json:"backendPrerequisitesReady"`
	HostingAvailable          bool    `json:"hostingAvailable"`
	Stage                     string  `json:"stage"`
	Issues                    []Issue `json:"issues"`
	Warnings                  []Issue `json:"warnings"`
}

type probeResult struct {
	ProtocolVersion       int    `json:"protocolVersion"`
	ABI                   int    `json:"abi"`
	Ready                 bool   `json:"ready"`
	Stage                 string `json:"stage,omitempty"`
	Errno                 int    `json:"errno,omitempty"`
	Check                 int    `json:"check,omitempty"`
	IPCScoped             bool   `json:"ipcScoped"`
	DeviceIoctlRestricted bool   `json:"deviceIoctlRestricted"`
}

func Inspect(ctx context.Context) Status {
	status := Status{Host: "compose", OS: runtime.GOOS, Arch: runtime.GOARCH, Stage: "foundation", Issues: []Issue{}, Warnings: []Issue{}}
	issue := func(code string) { status.Issues = append(status.Issues, Issue{Code: code}) }
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		issue("webapp_backend_platform_unsupported")
		return status
	}
	// Immutable ownership plus build-generated file digests detect replacement,
	// truncation and accidental mismatches before executing either binary.
	if err := verifyExecutable(NodePath, "/opt/node/node.sha256"); err != nil {
		issue("webapp_node_integrity_failed")
	} else {
		output, err := runProbe(ctx, NodePath, "--version")
		if err != nil || strings.TrimSpace(string(output)) != NodeVersion {
			issue("webapp_node_version_mismatch")
		} else {
			status.NodeReady = true
			status.NodeVersion = NodeVersion
		}
	}
	if err := verifyExecutable(SupervisorPath, "/opt/libexec/webapp-supervisor.sha256"); err != nil {
		issue("webapp_supervisor_integrity_failed")
		return status
	}
	status.SupervisorReady = true
	output, runErr := runProbe(ctx, SupervisorPath, "--probe")
	status.applyIsolationReport(output, runErr)
	status.BackendPrerequisitesReady = status.NodeReady && status.SupervisorReady && status.IsolationReady
	return status
}

func (status *Status) applyIsolationReport(output []byte, runErr error) {
	issue := func(code string) { status.Issues = append(status.Issues, Issue{Code: code}) }
	result, err := decodeProbeResult(output)
	if err != nil {
		issue("webapp_isolation_probe_failed")
		return
	}
	status.LandlockABI = result.ABI
	if result.ProtocolVersion != ProtocolVersion {
		issue("webapp_supervisor_protocol_mismatch")
		return
	}
	if result.ABI < RequiredLandlockABI {
		issue("webapp_landlock_unsupported")
		return
	}
	if runErr != nil || !result.Ready {
		issue("webapp_isolation_enforcement_failed")
		return
	}
	status.IsolationReady = true
	status.IPCScoped = result.IPCScoped
	status.DeviceIoctlRestricted = result.DeviceIoctlRestricted
	status.IsolationProfile = "landlock-files-tcp"
	if result.IPCScoped {
		status.IsolationProfile = "landlock-files-tcp-ipc"
	} else {
		status.Warnings = append(status.Warnings, Issue{Code: "webapp_ipc_scoping_unavailable"})
	}
	if !result.DeviceIoctlRestricted {
		status.Warnings = append(status.Warnings, Issue{Code: "webapp_device_ioctl_restriction_unavailable"})
	}
}

func decodeProbeResult(raw []byte) (probeResult, error) {
	var result probeResult
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&result); err != nil {
		return result, err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return result, fmt.Errorf("unexpected probe output")
	}
	if result.ABI < 0 || result.ProtocolVersion < 1 || result.Ready && (result.Stage != "" || result.Errno != 0 || result.Check != 0) {
		return result, fmt.Errorf("inconsistent probe result")
	}
	if result.Ready && (result.ABI < RequiredLandlockABI || result.IPCScoped != (result.ABI >= 6) || result.DeviceIoctlRestricted != (result.ABI >= 5)) {
		return result, fmt.Errorf("probe capability flags do not match ABI")
	}
	return result, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, fmt.Errorf("probe output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func runProbe(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	// No env_file, HOME, NODE_OPTIONS, LD_PRELOAD or credentials are inherited.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return output.Bytes(), err
}

func verifyExecutable(path, digestPath string) error {
	for _, name := range []string{path, digestPath} {
		current := name
		for {
			fi, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm()&0022 != 0 || !rootOwned(fi) {
				return fmt.Errorf("mutable runtime path")
			}
			if current == name && !fi.Mode().IsRegular() {
				return fmt.Errorf("not a regular runtime file")
			}
			if current == "/" {
				break
			}
			current = filepath.Dir(current)
		}
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("runtime is not executable")
	}
	f, err := os.Open(digestPath)
	if err != nil {
		return err
	}
	expected, err := io.ReadAll(io.LimitReader(f, 66))
	_ = f.Close()
	if err != nil || len(strings.TrimSpace(string(expected))) != 64 {
		return fmt.Errorf("invalid runtime digest")
	}
	want, err := hex.DecodeString(strings.TrimSpace(string(expected)))
	if err != nil {
		return err
	}
	f, err = os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	if !bytes.Equal(sum.Sum(nil), want) {
		return fmt.Errorf("runtime digest mismatch")
	}
	return nil
}
