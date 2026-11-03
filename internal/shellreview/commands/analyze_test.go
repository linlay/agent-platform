package commands

import (
	"testing"

	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/registry"
	"agent-platform/internal/shellreview/types"
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

func TestNetworkOptionsUseTheirOwnGrammar(t *testing.T) {
	for _, args := range [][]string{{"-T", "3", "https://x"}, {"-X", "private", "https://x"}, {"--post-data=@literal", "https://x"}} {
		e := nativeFacts("wget", args...)
		if args[0] != "--post-data=@literal" && e.Has(types.RemoteMutation) {
			t.Fatalf("wget borrowed curl's mutation flags: %+v", e)
		}
		if args[0] == "--post-data=@literal" {
			for _, op := range e.Ops {
				for _, target := range op.Targets {
					if target.Path == "literal" {
						t.Fatal("wget literal data became a filename")
					}
				}
			}
		}
	}
	for _, key := range []string{"post-file", "body-file"} {
		e := nativeFacts("wget", "--"+key+"=secret.txt", "https://x")
		found := false
		for _, op := range e.Ops {
			for _, target := range op.Targets {
				found = found || op.Kind == types.Read && target.Path == "secret.txt"
			}
		}
		if !found || !e.Has(types.RemoteMutation) {
			t.Fatalf("wget --%s lost body read or mutation: %+v", key, e)
		}
	}
}
func TestSimpleCommandUnknownOptionsAndStateMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"date", []string{"--unknown", "file"}}, {"whoami", []string{"--unknown"}}, {"git", []string{"status", "--unknown"}}, {"git", []string{"push", "--unknown", "--force"}},
	} {
		e := nativeFacts(tc.name, tc.args...)
		if !e.Unknown(types.UnknownOption) {
			t.Fatalf("%s silently accepted unknown options: %+v", tc.name, e)
		}
	}
	for _, args := range [][]string{{"-v", "PATH", "x"}, {"%n", "PATH"}, {"%010n", "PATH"}} {
		if e := nativeFacts("printf", args...); !e.Has(types.Exec) {
			t.Fatalf("printf shell mutation was accepted: %+v", e)
		}
	}
	if e := nativeFacts("printf", "%s", "%n"); e.Has(types.Exec) {
		t.Fatal("printf data was parsed as a format")
	}
	if e := nativeFacts("printf", "%%n"); e.Has(types.Exec) {
		t.Fatal("escaped percent became state mutation")
	}
	if e := nativeFacts("test", "-f", "secret"); len(e.Ops) != 1 || e.Ops[0].Targets[0].Path != "secret" || e.Ops[0].Writes() {
		t.Fatalf("test file predicate lost read fact: %+v", e)
	}
	if e := nativeFacts("git", "push", "--unknown", "--force"); !e.Has(types.RemoteMutation) || !e.Has(types.Discard) {
		t.Fatal("Git unknown option erased mutation facts")
	}
}

func TestGitHistoryOptionsFollowGitGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"log", "--since=2026-01-01", "--pretty=tformat:", "--numstat", "--no-merges"},
		{"log", "--shortstat", "--no-merges", "HEAD"},
		{"log", "--author=x", "--date", "short", "--reverse", "-n", "5", "HEAD"},
		{"log", "-Sneedle", "-G", "re", "-m", "-c", "-p", "HEAD"},
		{"diff", "--numstat", "-U0", "-C50%", "-M", "HEAD~1"},
		{"show", "--stat=120", "--format=%H", "HEAD"},
		{"shortlog", "-sne", "HEAD"},
		{"rev-list", "--count", "--first-parent", "HEAD"},
	} {
		e := nativeFacts("git", args...)
		if len(e.Unknowns) > 0 {
			t.Fatalf("%v: read-only history options left unknown: %+v", args, e)
		}
		for _, op := range e.Ops {
			if op.Writes() {
				t.Fatalf("%v: history query gained a write: %+v", args, e)
			}
		}
	}
	for _, args := range [][]string{{"log", "-m", "--output=out"}, {"log", "-C", "--output=out"}, {"diff", "-U", "--output=out"}} {
		found := false
		for _, op := range nativeFacts("git", args...).Ops {
			for _, target := range op.Targets {
				found = found || op.Kind == types.Output && target.Path == "out"
			}
		}
		if !found {
			t.Fatalf("%v: option value hid --output", args)
		}
	}
	for _, args := range [][]string{{"log", "--textconv", "HEAD"}, {"log", "-L", "1,2:file", "HEAD"}, {"diff", "--submodule=diff"}} {
		if e := nativeFacts("git", args...); !e.Unknown(types.UnknownOption) {
			t.Fatalf("%v: unmodeled option was accepted: %+v", args, e)
		}
	}
}

func TestMetadataInventoryCannotSilentlyGainAnAnalyzer(t *testing.T) {
	// Historical identity entries without an analyzer retain opaque behavior.
	// Deployment allowlists are a separate policy and never inferred here.
	unmodeled := map[registry.ID]bool{
		registry.CmdColon: true, registry.CmdBreak: true, registry.CmdContinue: true, registry.CmdDbx: true, registry.CmdDeclare: true, registry.CmdDf: true, registry.CmdExport: true,
		registry.CmdGetChildItem: true, registry.CmdGetContent: true, registry.CmdHttpx: true, registry.CmdLocal: true, registry.CmdRead: true, registry.CmdRemoveItem: true,
		registry.CmdReturn: true, registry.CmdSet: true, registry.CmdShift: true, registry.CmdType: true, registry.CmdTypeset: true, registry.CmdUnset: true,
	}
	for _, entry := range registry.Entries() {
		if entry.Flags&(registry.Arguments|registry.Builtin) == 0 {
			continue
		}
		_, analyzed := analyzers[entry.ID]
		if analyzed == unmodeled[entry.ID] {
			t.Fatalf("metadata/analyzer inventory drift: %s analyzed=%t", entry.Name, analyzed)
		}
	}
	for id := range analyzers {
		if name := registry.Name(id); name == "" {
			t.Fatalf("analyzer has no shared metadata: %d", id)
		}
	}
}
