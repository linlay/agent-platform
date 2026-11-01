package commands

import (
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
	"testing"
)

func nativeFacts(name string, args ...string) types.Effects {
	return Analyze(types.CommandInput{Name: name, Args: args, Identity: types.CommandIdentity{Domain: types.NativeCLI, Source: types.SystemProgram}, Dialect: dialect.Bash})
}
func TestCommandDomainSourceAndDialectMustMatch(t *testing.T) {
	for _, input := range []types.CommandInput{
		{Name: "curl", Args: []string{"https://x"}, Dialect: dialect.Bash},
		{Name: "curl", Args: []string{"https://x"}, Identity: types.CommandIdentity{Domain: types.PowerShellCmdlet, Source: types.BuiltinProgram}, Dialect: dialect.Bash},
		{Name: "curl", Args: []string{"https://x"}, Identity: types.CommandIdentity{Domain: types.ShellBuiltin, Source: types.BuiltinProgram}, Dialect: dialect.Bash},
		{Name: "curl", Args: []string{"https://x"}, Identity: types.CommandIdentity{Domain: types.NativeCLI, Source: types.SystemProgram}, Dialect: dialect.PowerShell},
	} {
		e := Analyze(input)
		if !e.Has(types.Exec) || !e.Unknown(types.UnknownDialect) {
			t.Fatalf("identity mismatch acquired semantics: %+v", e)
		}
	}
	if e := nativeFacts("curl", "https://x"); e.Has(types.Exec) || len(e.Unknowns) > 0 {
		t.Fatal(e)
	}
}
func TestUnknownOptionsRetainDangerousFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		kind types.OpKind
	}{
		{"rm", []string{"-rf", "--unknown", "tree"}, types.Delete},
		{"chmod", []string{"--unknown", "777", "tree"}, types.Chmod},
		{"curl", []string{"--unknown", "-X", "DELETE", "https://x"}, types.RemoteMutation},
		{"mv", []string{"--unknown", "a", "b"}, types.Rename},
	} {
		e := nativeFacts(tc.name, tc.args...)
		if !e.Unknown(types.UnknownOption) || !e.Has(tc.kind) {
			t.Fatalf("%s: known dangerous fact lost: %+v", tc.name, e)
		}
	}
}
func TestStructuredDependencies(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		kind types.DepKind
		path string
	}{
		{"curl", []string{"-K", "cfg", "https://x"}, types.ConfigFile, "cfg"},
		{"wget", []string{"--config=cfg", "https://x"}, types.ConfigFile, "cfg"},
		{"sed", []string{"-f", "prog.sed", "data"}, types.Script, "prog.sed"},
		{"git", []string{"-C", "sub", "status"}, types.GitConfig, "sub"},
	} {
		e := nativeFacts(tc.name, tc.args...)
		found := false
		for _, d := range e.Deps {
			found = found || d.Kind == tc.kind && d.Path == tc.path
		}
		if !found {
			t.Fatalf("%s: missing dependency %+v", tc.name, e)
		}
	}
}

func TestTransferFactsKeepAllRoles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   types.OpKind
		source types.Role
	}{
		{"cp", types.Create, types.Source}, {"mv", types.Rename, types.Source}, {"ln", types.Link, types.LinkTarget},
	} {
		e := nativeFacts(tc.name, "a", "b")
		found := false
		for _, op := range e.Ops {
			if op.Kind != tc.kind {
				continue
			}
			roles := map[types.Role]bool{}
			for _, target := range op.Targets {
				roles[target.Role] = true
			}
			found = roles[tc.source] && roles[types.Destination] && roles[types.Replaced]
		}
		if !found {
			t.Fatalf("%s lost multi-target facts: %+v", tc.name, e)
		}
	}
}
