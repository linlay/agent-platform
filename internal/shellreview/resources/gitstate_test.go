package resources

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/shellreview/types"
)

func TestGitProbeNeverStartsWithInjectedLoaderEnvironment(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "started")
	program := filepath.Join(root, "git")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"LD_PRELOAD", "BASH_ENV", "DYLD_INSERT_LIBRARIES"} {
		inspector := NewDependencyInspector(false, nil, writableDependency)
		result := inspector.ReviewGitExecution(types.BashExecution{Program: program, Cwd: root, Env: map[string]string{key: "unsafe"}}, root, types.GitCheckRead, false)
		if result.State != types.Unresolved {
			t.Fatal(result)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("unsafe Git probe started before approval")
		}
	}
}
func TestBoundedProbeOutputCannotBypassWriteThroughReaderFrom(t *testing.T) {
	output := boundedProbeOutput{limit: 8}
	if _, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", 100))); err == nil || output.Len() > 8 {
		t.Fatalf("output cap bypassed: %d %v", output.Len(), err)
	}
}
