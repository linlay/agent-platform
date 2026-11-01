package unwrap

import (
	"agent-platform/internal/shellreview/types"
	"reflect"
	"testing"
)

func TestWrapperInspectionNeverSuppliesExecutionTrust(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		inner   string
		execute bool
	}{
		{"command", []string{"-p", "eval", "x"}, "eval", false},
		{"command", []string{"--unknown", "eval", "x"}, "eval", false},
		{"command", []string{"--", "-v", "eval"}, "eval", true},
		{"nice", []string{"-n1", "eval", "x"}, "eval", false},
		{"nice", []string{"--adjustment=1", "eval", "x"}, "eval", false},
		{"nice", []string{"-n", "1", "cat", "x"}, "cat", true},
		{"timeout", []string{"--signal=KILL", "1", "eval", "x"}, "eval", false},
		{"timeout", []string{"--unknown", "1", "eval", "x"}, "eval", false},
		{"timeout", []string{"--", "1", "eval", "x"}, "eval", false},
		{"timeout", []string{"-s", "TERM", "1", "cat", "x"}, "cat", true},
		{"nohup", []string{"--", "eval", "x"}, "eval", true},
		{"stdbuf", []string{"-oL", "cat", "x"}, "cat", true},
	} {
		candidates := InspectionWrappers(append([]string{tc.name}, tc.args...))
		var inspected []string
		ok := false
		for _, candidate := range candidates {
			if len(candidate) > 0 && candidate[0] == tc.inner {
				inspected = candidate
				ok = true
			}
		}
		if !ok {
			t.Fatalf("%s %v lost hard-block candidate: %v %t", tc.name, tc.args, inspected, ok)
		}
		words := make([]types.SourceWord, len(tc.args))
		for i := range words {
			words[i] = types.SourceWord{Arg: i + 1, Origin: types.WordOriginSyntax}
		}
		expanded, ok := Execution(tc.name, tc.args, words, "/cwd", map[string]string{}, func(p, cwd string) string { return cwd + "/" + p })
		if ok != tc.execute {
			t.Fatalf("%s %v: execution trust %t want %t", tc.name, tc.args, ok, tc.execute)
		}
		if ok && len(expanded.Argv) > 0 {
			start := len(tc.args) - len(expanded.Argv)
			if !reflect.DeepEqual(expanded.Words, words[start:]) {
				t.Fatalf("source alignment lost: %+v", expanded)
			}
		}
	}
}
