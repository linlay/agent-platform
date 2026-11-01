package resources

import (
	"agent-platform/internal/shellreview/types"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func nullWord() types.SourceWord {
	return types.SourceWord{WordSpan: types.WordSpan{Literal: true}, Origin: types.WordOriginSyntax}
}
func TestNullDeviceProtocolIsNumericAndChecksTypeAndDevice(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		null bool
	}{
		{"21b6|1|3\n", true}, {"2000|01|03", true}, {"81b6|1|3", false}, {"a1ff|1|3", false}, {"61b6|1|3", false}, {"21b6|1|5", false}, {"21b6|3|2", false},
	} {
		got, err := ParseLinuxNullDevice(tc.raw)
		if err != nil || got.State != types.Known || got.NullDevice != tc.null {
			t.Fatalf("%q: %+v %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{"", "character special file|1|3", "21b6|+1|3", "21b6|0x1|3", "21b6|1|3|x", "121b6|1|3", "21b6|1|100000000", "21b6|1|3\n21b6|1|3"} {
		if _, err := ParseLinuxNullDevice(raw); err == nil {
			t.Fatalf("accepted malformed stat output: %q", raw)
		}
	}
}
func TestNullOffsetUsesOriginCompletenessAndExactLiteral(t *testing.T) {
	for _, origin := range []types.Origin{types.Argument, types.ShellRedirect, types.Implicit} {
		for _, complete := range []bool{false, true} {
			calls := 0
			i := NewDeviceInspector(true, &types.BashEnvironment{InspectNullDevice: func() (string, error) { calls++; return "21b6|1|3", nil }})
			target := types.Target{Path: "/dev/null", Word: nullWord()}
			got := i.NullOffset(types.Op{Kind: types.Output, Origin: origin}, target, complete)
			want := origin == types.ShellRedirect || origin == types.Argument && complete
			if got.NullDevice != want || calls != map[bool]int{true: 1, false: 0}[want] {
				t.Fatalf("%s complete=%t: %+v calls=%d", origin, complete, got, calls)
			}
		}
	}
	for _, kind := range []types.OpKind{types.Create, types.Delete, types.Rename, types.Chmod, types.Link, types.UnknownWrite} {
		i := NewDeviceInspector(true, &types.BashEnvironment{InspectNullDevice: func() (string, error) { t.Fatal("structural mutation probed for exemption"); return "", nil }})
		if r := i.NullOffset(types.Op{Kind: kind, Origin: types.Argument}, types.Target{Path: "/dev/null", Word: nullWord()}, true); r.NullDevice {
			t.Fatal(r)
		}
	}
	for _, path := range []string{" /dev/null", "/dev/null ", "//dev/null", "/dev/./null", "~/null", "dev/null", "/dev/zero", "/dev/stdout", "/dev/fd/1"} {
		i := NewDeviceInspector(true, nil)
		if got := i.NullOffset(types.Op{Kind: types.Read, Origin: types.ShellRedirect}, types.Target{Path: path, Word: nullWord()}, true); got.State != types.Known || got.NullDevice {
			t.Fatalf("nonliteral spelling probed: %q: %+v", path, got)
		}
	}
	word := nullWord()
	word.Literal = false
	if got := NewDeviceInspector(true, nil).NullOffset(types.Op{Kind: types.Output, Origin: types.ShellRedirect}, types.Target{Path: "/dev/null", Word: word}, true); got.State != types.Known {
		t.Fatal("expanded value borrowed literal trust")
	}
}
func TestGuestNullProbeFailureNeverFallsBackAndCachesOnlyOneReview(t *testing.T) {
	calls := 0
	env := &types.BashEnvironment{InspectNullDevice: func() (string, error) { calls++; return "21b6|1|3", errors.New("failed") }}
	target := types.Target{Path: "/dev/null", Word: nullWord()}
	op := types.Op{Kind: types.Output, Origin: types.ShellRedirect}
	i := NewDeviceInspector(true, env)
	for range 3 {
		if r := i.NullOffset(op, target, true); r.State != types.Unresolved || r.NullDevice {
			t.Fatalf("guest failure borrowed Host identity: %+v", r)
		}
	}
	if calls != 1 {
		t.Fatalf("review cache calls=%d", calls)
	}
	NewDeviceInspector(true, env).NullOffset(op, target, true)
	if calls != 2 {
		t.Fatal("second review reused stale result")
	}
	if r := NewDeviceInspector(true, nil).NullOffset(op, target, true); r.State != types.Unresolved {
		t.Fatal("missing guest probe read Host")
	}
}
func TestHostNullDeviceIdentity(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no supported Host device contract")
	}
	got := inspectHostNullDevice()
	if got.State != types.Known || !got.NullDevice {
		t.Fatalf("actual Host /dev/null identity: %+v", got)
	}
}

func TestHostDeviceIdentityNeverFollowsFinalSymlink(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regular, []byte("regular"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := inspectHostNullAt(regular); got.State != types.Known || got.NullDevice {
		t.Fatalf("ordinary file became a device: %+v", got)
	}
	link := filepath.Join(t.TempDir(), "null-link")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Skip(err)
	}
	if got := inspectHostNullAt(link); got.State != types.Known || got.NullDevice {
		t.Fatalf("final symlink followed: %+v", got)
	}
}
