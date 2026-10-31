package webapphost

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecodeProbeResult(t *testing.T) {
	result, err := decodeProbeResult([]byte(`{"protocolVersion":1,"abi":6,"ready":true}`))
	if err != nil || !result.Ready || result.ABI != 6 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, raw := range []string{
		`{"protocolVersion":1,"abi":6,"ready":true} {}`,
		`{"protocolVersion":1,"abi":6,"ready":true,"stage":"enforcement"}`,
		`{"protocolVersion":1,"abi":-1,"ready":false}`,
		`{"protocolVersion":0,"abi":6,"ready":true}`,
		`{"protocolVersion":1,"abi":6,"ready":true,"unknown":true}`,
		`not json`,
	} {
		if _, err := decodeProbeResult([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed result %s", raw)
		}
	}
}

func TestProbeDoesNotInheritSecretsOrNodeInjection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX probe environment")
	}
	t.Setenv("AP_WEBAPP_TEST_SECRET", "must-not-be-inherited")
	t.Setenv("NODE_OPTIONS", "--require=/nonexistent")
	t.Setenv("LD_PRELOAD", "/nonexistent")
	raw, err := runProbe(context.Background(), "/bin/sh", "-c", `test -z "$AP_WEBAPP_TEST_SECRET$NODE_OPTIONS$LD_PRELOAD" && test "$LANG" = C && printf clean`)
	if err != nil || string(raw) != "clean" {
		t.Fatalf("probe environment output=%q err=%v", raw, err)
	}
}

func TestProbeOutputLimit(t *testing.T) {
	b := &boundedOutput{}
	if _, err := b.Write(make([]byte, 65537)); err == nil || b.Len() != 0 {
		t.Fatal("unbounded output accepted")
	}
}

func TestNonLinuxProbeDoesNotExecuteOrClaimHosting(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non Linux guard")
	}
	status := Inspect(context.Background())
	if status.HostingAvailable || status.BackendPrerequisitesReady || status.NodeReady || status.IsolationReady || len(status.Issues) != 1 {
		t.Fatalf("unsupported host: %+v", status)
	}
}

func TestNodeVersionMatchesPinnedDockerfile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "AP_WEBAPP_NODE_VERSION="+strings.TrimPrefix(NodeVersion, "v")) {
		t.Fatal("Docker image and prerequisite checker use different Node versions")
	}
}
