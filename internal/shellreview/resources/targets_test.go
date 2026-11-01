package resources

import (
	"agent-platform/internal/shellreview/types"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGuestTransferTargetsNeverUseHostPathType(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(raw, []byte("host"), 0600); err != nil {
		t.Fatal(err)
	}
	target := types.Target{Path: raw, Placement: types.FileOrDirectory, Child: "source"}
	both := []string{raw, raw + "/source"}
	for _, env := range []*types.BashEnvironment{nil, {}, {PathKind: func(string) (types.PathKind, error) { return types.PathNonDirectory, errors.New("guest failure") }}} {
		if got := TargetPaths(types.Op{}, target, "/guest", true, env); !reflect.DeepEqual(got, both) {
			t.Fatalf("guest uncertainty fell back to Host: %v", got)
		}
	}
	env := &types.BashEnvironment{PathKind: func(string) (types.PathKind, error) { return types.PathDirectory, nil }}
	if got := TargetPaths(types.Op{}, target, "/guest", true, env); !reflect.DeepEqual(got, []string{raw + "/source"}) {
		t.Fatalf("guest directory ignored: %v", got)
	}
}
func TestTransferJoinsPreservePhysicalParentTraversal(t *testing.T) {
	target := types.Target{Path: "jump/../dest", Placement: types.IntoDirectory, Child: "file"}
	if got := TargetPaths(types.Op{}, target, "/workspace", false, nil); !reflect.DeepEqual(got, []string{"/workspace/jump/../dest/file"}) {
		t.Fatalf("physical path order erased: %v", got)
	}
}
