package connectorauth

import "testing"

func TestCLIFullSemverPrecedence(t *testing.T) {
	for _, v := range []struct {
		actual, minimum string
		want            bool
	}{{"v1.4.0\n", "1.4.0", true}, {"prefix 1.4.0", "1.4.0", false}, {"1.4.0 bad", "1.4.0", false}, {"1.4.0-beta.2", "1.4.0", false}, {"1.4.0-beta.10", "1.4.0-beta.2", true}, {"1.4.0+build", "1.4.0", true}, {"1.4.0-01", "1.4.0", false}, {"2.0.0", "1.99.0", true}} {
		if got := versionAtLeast(v.actual, v.minimum); got != v.want {
			t.Fatalf("%q >= %q = %v", v.actual, v.minimum, got)
		}
	}
}

func TestCLIRegexpSupportsECMAScriptCapture(t *testing.T) {
	re, err := compileCLIRegexp(`(?<=version: )(\d+\.\d+\.\d+)(?= stable)`)
	if err != nil {
		t.Fatal(err)
	}
	match, err := re.FindStringMatch("version: 1.4.0 stable")
	if err != nil || match == nil || match.GroupByNumber(1).String() != "1.4.0" {
		t.Fatal(match, err)
	}
}
