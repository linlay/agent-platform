package shellanalysis

import "testing"

func TestOperandGrammarSeparatesPatternsProgramsAndFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		path  string
		write bool
	}{
		{"cat", []string{"plain.txt"}, "plain.txt", false},
		{"grep", []string{"needle", "plain.txt"}, "plain.txt", false},
		{"sed", []string{"-n", "s/a/b/p", "plain.txt"}, "plain.txt", false},
		{"sed", []string{"-i", "s/a/b/", "plain.txt"}, "plain.txt", true},
		{"curl", []string{"-o", "plain.txt", "https://example.invalid"}, "plain.txt", true},
		{"tail", []string{"-200", "plain.txt"}, "plain.txt", false},
	} {
		e := Operands(tc.name, tc.args)
		if len(e.Files) != 1 || e.Files[0].Path != tc.path || e.Files[0].Write != tc.write {
			t.Fatalf("%s %v: %+v", tc.name, tc.args, e)
		}
	}
}
