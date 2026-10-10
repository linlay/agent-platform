package kbx

import (
	"agent-platform/internal/builtins"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDecodeEnvelopeRejectsUnsupportedAndErrorResponses(t *testing.T) {
	for _, input := range []string{
		`[]`,
		`{"schemaVersion":1,"type":"kbx.agent.response","status":"ok","data":{}}`,
		`{"schemaVersion":2,"type":"other","status":"ok","data":{}}`,
		`{"schemaVersion":2,"type":"kbx.agent.response","status":"error","data":{}}`,
		`{"schemaVersion":2,"type":"kbx.agent.response","status":"ok","data":null}`,
	} {
		var result map[string]any
		if err := decodeEnvelope([]byte(input), &result); err == nil {
			t.Errorf("accepted invalid response %s", input)
		}
	}
	var result struct {
		Value int `json:"value"`
	}
	if err := decodeEnvelope([]byte(`{"schemaVersion":2,"type":"kbx.agent.response","status":"ok","data":{"value":3}}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != 3 {
		t.Fatal("data was not decoded")
	}
}
func TestBoundedOutputReportsTruncation(t *testing.T) {
	var b boundedBuffer
	data := make([]byte, 17<<20)
	n, err := b.Write(data)
	if err != nil || n != len(data) || !b.exceeded || b.Len() != 16<<20 {
		t.Fatal("output limit is not enforced")
	}
}

func TestCLIRunnerNotStartedBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	// Isolate the process-wide builtin path from other tests.
	if os.Getenv("KBX_RUNNER_BOUNDARY_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCLIRunnerNotStartedBoundary$")
		cmd.Env = append(os.Environ(), "KBX_RUNNER_BOUNDARY_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("runner subprocess: %v %s", err, out)
		}
		return
	}
	bin := t.TempDir()
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"missing", "non-executable", "started"} {
		if stage != "missing" {
			mode := os.FileMode(0600)
			if stage == "started" {
				mode = 0700
			}
			path := filepath.Join(bin, "kbx")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 7\n"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
		}
		_, err := (cliRunner{}).Run(context.Background(), filepath.Join(t.TempDir(), "index.sqlite"), defaultLibraryConfig, "update")
		if err == nil || errors.Is(err, errCommandNotStarted) != (stage != "started") {
			t.Fatalf("%s: %v", stage, err)
		}
	}
}
