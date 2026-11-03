package registry

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestRegistryPreservesPreMigrationInventories(t *testing.T) {
	data, err := os.ReadFile("testdata/pre-registry.json")
	if err != nil {
		t.Fatal(err)
	}
	var before map[string][]string
	if err := json.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}
	for key, flag := range map[string]Flag{
		"ordinaryCommands": Arguments, "shellBuiltins": Builtin,
		"wrapperNames": Wrapper, "IsInterpreter": Interpreter,
		"IsShellInterpreter": ShellInterpreter, "Capture": Capture,
		"BuildTool": BuildTool, "unsupportedBashCommands": StrictUnsupported,
	} {
		if got := Names(flag); !reflect.DeepEqual(got, before[key]) {
			t.Fatalf("%s changed: got %v; want %v", key, got, before[key])
		}
	}
	want := append(before["astDangerousCommands"], before["zshDangerousCommands"]...)
	sort.Slice(want, func(i, j int) bool { return strings.ToLower(want[i]) < strings.ToLower(want[j]) })
	if got := Names(HardBlocked); !reflect.DeepEqual(got, want) {
		t.Fatalf("hard block inventory changed: got %v; want %v", got, want)
	}
}

func TestRegistryDoesNotExposeMutableMetadata(t *testing.T) {
	entries := Entries()
	for i := range entries {
		entries[i].Flags = 0
		entries[i].Name = "modified"
	}
	if !Is("eval", HardBlocked) || !Is("env", Wrapper) || Name(CmdCurl) != "curl" {
		t.Fatal("returned metadata changed the production catalog")
	}
}
