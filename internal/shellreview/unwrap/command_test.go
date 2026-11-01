package unwrap

import (
	"strings"
	"testing"
)

func TestShellScriptAndEnvSplit(t *testing.T) {
	if script, ok := ShellScript([]string{"bash", "-c", "git push"}); !ok || script != "git push" {
		t.Fatal("bash -c")
	}
	if script, ok := ShellScript([]string{"sh", "-ec", "rm -rf x"}); !ok || script != "rm -rf x" {
		t.Fatal("combined -ec")
	}
	if _, ok := ShellScript([]string{"bash", "-lc", "x"}); ok {
		t.Fatal("login shells load profiles and are not analyzable")
	}
	expanded, ok := Env([]string{"-S", "git push --force"})
	if !ok || strings.Join(expanded.Command, " ") != "git push --force" {
		t.Fatalf("env -S split: %v %v", expanded.Command, ok)
	}
	if _, ok := Env([]string{"-S", `git "$X"`}); ok {
		t.Fatal("env -S with quoting or variables is not static")
	}
}
