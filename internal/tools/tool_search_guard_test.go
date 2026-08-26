package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func hostFilesystemRootForTest(t *testing.T) string {
	t.Helper()
	volume := filepath.VolumeName(t.TempDir())
	return volume + string(filepath.Separator)
}

func TestValidateRelativeSearchGlob(t *testing.T) {
	for _, value := range []string{
		"/Users/u/models/*.yml",
		`C:\Users\u\models\*.yml`,
		`\\server\share\*.yml`,
		"~/.config/*.yml",
		"@workspace/**/*.yml",
		"@skills-center/**/SKILL.md",
		"file:///tmp/*.yml",
	} {
		t.Run(value, func(t *testing.T) {
			if err := validateRelativeSearchGlob(value, "pattern"); err == nil || !strings.Contains(err.Error(), "must be relative to path") {
				t.Fatalf("validateRelativeSearchGlob(%q) error = %v", value, err)
			}
		})
	}

	for _, value := range []string{
		"*.yml",
		"**/agent.yml",
		"internal/**/*.go",
		"configs/*.{yml,yaml}",
	} {
		t.Run("relative-"+value, func(t *testing.T) {
			if err := validateRelativeSearchGlob(value, "pattern"); err != nil {
				t.Fatalf("validateRelativeSearchGlob(%q): %v", value, err)
			}
		})
	}
}

func TestRipgrepSearchFailurePreservesBoundedDiagnostics(t *testing.T) {
	lines := make([]string, 150)
	for index := range lines {
		lines[index] = fmt.Sprintf("file-%03d.yml", index)
	}
	stderr := strings.Repeat("permission denied; ", 1024)
	result := ripgrepSearchFailure(
		"file_glob",
		"glob_failed",
		"/workspace",
		2,
		strings.Join(lines, "\n")+"\n",
		stderr,
		"exit status 2",
		map[string]any{"head_limit": 0},
		defaultGlobHeadLimit,
		false,
		map[string]any{"pattern": "*.yml"},
	)

	if result.Error != "glob_failed" || result.ExitCode != -1 {
		t.Fatalf("unexpected failure result: %#v", result)
	}
	if result.Structured["rgExitCode"] != 2 || result.Structured["partialMatchCount"] != 150 {
		t.Fatalf("missing rg diagnostics: %#v", result.Structured)
	}
	partial := stringSliceResult(t, result.Structured["partialResults"])
	if len(partial) != defaultGlobHeadLimit || result.Structured["partialTruncated"] != true {
		t.Fatalf("partial results were not bounded: %#v", result.Structured)
	}
	if result.Structured["stderrTruncated"] != true || len(result.Structured["stderr"].(string)) > maxGrepRawBytes {
		t.Fatalf("stderr was not bounded: %#v", result.Structured)
	}
}
