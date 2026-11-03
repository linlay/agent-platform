package adminsource

import "testing"

func TestAccessConfigSemanticChange(t *testing.T) {
	for _, tc := range []struct {
		before, after string
		changed       bool
	}{
		{"key: a", "key: a\naccessConfig: {}", true},
		{"key: a\naccessConfig: {}", "key: a\naccessConfig:\n  default: default", true},
		{"accessConfig:\n  default: full_access", "key: a", true},
		{"accessConfig:\n  default: full_access", "accessConfig:\n  default: default", true},
		{"accessConfig: {default: full_access}", "# unchanged\naccessConfig:\n  default: full_access", false},
		{"key: a", "key: a\nname: renamed", false},
	} {
		change, err := accessConfigChange(tc.before, tc.after)
		if err != nil || change.Changed != tc.changed {
			t.Fatalf("%q -> %q: %+v %v", tc.before, tc.after, change, err)
		}
	}
}
