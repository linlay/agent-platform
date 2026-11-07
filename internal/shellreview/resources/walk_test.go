package resources

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/shellreview/types"
)

func TestWalkSkipsOnlyWhatFindNeverVisits(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(root, "env", ".state")
	if err := os.MkdirAll(protected, 0700); err != nil {
		t.Fatal(err)
	}
	prune := func(tests ...types.WalkTest) types.Walk {
		return types.Walk{MaxDepth: -1, Prune: [][]types.WalkTest{tests}}
	}
	name := func(v string) types.WalkTest { return types.WalkTest{Primary: "-name", Value: v} }
	path := func(v string) types.WalkTest { return types.WalkTest{Primary: "-path", Value: v} }
	dir := types.WalkTest{Primary: "-type", Value: "d"}
	for _, tc := range []struct {
		name  string
		walk  types.Walk
		typed string
		want  bool
	}{
		{"beyond maxdepth", types.Walk{MaxDepth: 1}, root, true},
		{"within maxdepth", types.Walk{MaxDepth: 2}, root, false},
		{"name", prune(dir, name(".state")), root, true},
		{"ancestor name", prune(name("env")), root, true},
		{"other name", prune(name("node_modules")), root, false},
		{"absolute path", prune(path(protected)), root, true},
		{"relative root", prune(path("./env/.state")), ".", true},
		{"pattern", prune(path("*/.state")), root, true},
		{"trailing slash root", prune(path("*/.state")), root + "/", true},
		{"literal path after trailing slash", prune(path(root + "/env/.state")), root + "/", false},
		{"unknown root", prune(path("*/.state")), "", false},
		{"bracket", prune(name("[.]state")), root, false},
		{"case differs", prune(name(".State")), root, false},
		{"escaped", prune(name(`.st\ate`)), root, true},
		{"question mark", prune(name("?state")), root, true},
	} {
		if got := WalkSkips(tc.walk, tc.typed, root, protected); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if WalkSkips(types.Walk{MaxDepth: 0}, root, protected, protected) {
		t.Error("a walk of the protected root itself is never skipped")
	}
}
