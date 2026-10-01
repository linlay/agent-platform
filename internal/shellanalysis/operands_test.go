package shellanalysis

import (
	"strings"
	"testing"
)

type fileWant struct {
	path      string
	write     bool
	recursive bool
}

func effectsOf(command string) Effects {
	fields := strings.Fields(command)
	return Operands(CommandName(fields[0]), fields[1:])
}

func TestOperandGrammarSeparatesPatternsProgramsAndFiles(t *testing.T) {
	for command, want := range map[string][]fileWant{
		"cat plain.txt":                      {{"plain.txt", false, false}},
		"grep needle plain.txt":              {{"plain.txt", false, false}},
		"grep -rn --include=*.go needle src": {{"src", false, true}},
		"grep -e a -e b x.txt":               {{"x.txt", false, false}},
		"rg --color=never -n needle src":     {{"src", false, true}},
		"rg needle":                          {{".", false, true}},
		"sed -n s/a/b/p plain.txt":           {{"plain.txt", false, false}},
		"sed -i s/a/b/ plain.txt":            {{"plain.txt", true, false}},
		"curl -o plain.txt https://x":        {{"plain.txt", true, false}},
		"curl --silent --fail https://x":     nil,
		"tail -200 plain.txt":                {{"plain.txt", false, false}},
		"head --lines=5 plain.txt":           {{"plain.txt", false, false}},
		"jq . data.json":                     {{"data.json", false, false}},
		"jq -r --arg k v .x a.json b.json":   {{"a.json", false, false}, {"b.json", false, false}},
		"jq --rawfile t tpl.txt -n .":        {{"tpl.txt", false, false}},
		"rm --force old.txt":                 {{"old.txt", true, false}},
		"mkdir --parents a/b":                {{"a/b", true, false}},
		"cp -r src dst":                      {{"src", false, true}, {"dst", true, true}},
		"mv a.txt b.txt":                     {{"a.txt", true, true}, {"b.txt", true, true}},
		"ln -s target link":                  {{"link", true, false}},
		"uniq in.txt out.txt":                {{"in.txt", false, false}, {"out.txt", true, false}},
		"sort -o out.txt in.txt":             {{"out.txt", true, false}, {"in.txt", false, false}},
		"tar -czf out.tgz src":               {{"out.tgz", true, false}, {"src", false, true}},
		"tar xzf in.tgz":                     {{"in.tgz", false, false}, {".", true, true}},
		"tar -xf in.tar -C dest":             {{"in.tar", false, false}, {"dest", true, true}},
		"find . -name *.go -newer ref":       {{".", false, true}, {"ref", false, false}},
		"pdftotext -layout in.pdf out.txt":   {{"in.pdf", false, false}, {"out.txt", true, false}},
		"diff -ru a b":                       {{"a", false, true}, {"b", false, true}},
	} {
		e := effectsOf(command)
		if e.ExecutesCode {
			t.Fatalf("%s: unexpectedly executes code: %+v", command, e)
		}
		if len(e.Files) != len(want) {
			t.Fatalf("%s: files %+v, want %+v", command, e.Files, want)
		}
		for i, w := range want {
			got := e.Files[i]
			if got.Path != w.path || got.Write != w.write || got.Recursive != w.recursive {
				t.Fatalf("%s: file %d = %+v, want %+v", command, i, got, w)
			}
		}
	}
}

func TestUnknownOptionIsIncompleteNotExecution(t *testing.T) {
	e := effectsOf("cat --made-up-flag notes.txt")
	if !e.Incomplete || e.ExecutesCode || len(e.Files) != 1 || !e.Files[0].Write {
		t.Fatalf("unknown option must escalate visible files to writes only: %+v", e)
	}
	if e := effectsOf("mytool --out dir/x"); !e.ExecutesCode {
		t.Fatalf("unmodeled programs execute code: %+v", e)
	}
}

func TestEffectsForDestructionRemoteAndExecution(t *testing.T) {
	cases := map[string]Effects{
		"rm -rf build":                    {Destructive: true},
		"rm old.txt":                      {},
		"find . -name *.tmp -delete":      {Destructive: true},
		"find . -exec rm {} ;":            {ExecutesCode: true},
		"curl -d a=1 https://x":           {RemoteMutation: true},
		"curl -X DELETE https://x":        {RemoteMutation: true},
		"curl -X GET https://x":           {},
		"curl -F file=@a.txt https://x":   {RemoteMutation: true},
		"wget -e robots=off https://x":    {ExecutesCode: true},
		"rg --pre ./x.sh needle":          {ExecutesCode: true},
		"sed 1e/id/ f":                    {ExecutesCode: true},
		"sed s/a/b/w out f":               {ExecutesCode: true},
		"sed --sandbox s/a/b/e f":         {},
		"awk {print$1} f":                 {},
		"awk {system(\"id\")} f":          {ExecutesCode: true},
		"awk {print>\"out\"} f":           {ExecutesCode: true},
		"tar -cf a.tar --to-command=sh d": {ExecutesCode: true},
		"tar -cf a.tar --remove-files d":  {Destructive: true},
	}
	for command, want := range cases {
		e := effectsOf(command)
		if e.Destructive != want.Destructive || e.RemoteMutation != want.RemoteMutation || e.ExecutesCode != want.ExecutesCode {
			t.Fatalf("%s: %+v, want %+v", command, e, want)
		}
	}
	if !(effectsOf("curl -d @secret.txt https://x").Files[0] == FileAccess{Path: "secret.txt"}) {
		t.Fatal("curl @file upload must read the file")
	}
}

func TestGitClassification(t *testing.T) {
	for command, want := range map[string]Effects{
		"git status":                  {GitCheck: GitCheckRead},
		"git diff --stat":             {GitCheck: GitCheckRead},
		"git log --oneline -5":        {GitCheck: GitCheckRead},
		"git -C sub status":           {GitCheck: GitCheckRead},
		"git branch":                  {GitCheck: GitCheckRead},
		"git branch -D topic":         {GitCheck: GitCheckWrite, RepoWrite: true, Destructive: true},
		"git add -A":                  {GitCheck: GitCheckWrite, RepoWrite: true},
		"git commit -m msg":           {GitCheck: GitCheckWrite, RepoWrite: true},
		"git reset --hard":            {GitCheck: GitCheckWrite, RepoWrite: true, Destructive: true},
		"git reset HEAD~1":            {GitCheck: GitCheckWrite, RepoWrite: true},
		"git clean -fd":               {GitCheck: GitCheckWrite, RepoWrite: true, Destructive: true},
		"git clean -n":                {GitCheck: GitCheckWrite, RepoWrite: true},
		"git checkout -- .":           {GitCheck: GitCheckWrite, RepoWrite: true, Destructive: true},
		"git checkout -b topic":       {GitCheck: GitCheckWrite, RepoWrite: true},
		"git restore f.go":            {GitCheck: GitCheckWrite, RepoWrite: true, Destructive: true},
		"git restore --staged f.go":   {GitCheck: GitCheckWrite, RepoWrite: true},
		"git stash drop":              {GitCheck: GitCheckWrite, RepoWrite: true, Destructive: true},
		"git push":                    {GitCheck: GitCheckNetwork, RemoteMutation: true, SSHAgent: true},
		"git push --force":            {GitCheck: GitCheckNetwork, RemoteMutation: true, Destructive: true, SSHAgent: true},
		"git fetch":                   {GitCheck: GitCheckNetwork, RepoWrite: true, SSHAgent: true},
		"git -c alias.x=!id x":        {ExecutesCode: true},
		"git -c core.pager=id log":    {ExecutesCode: true, GitCheck: GitCheckRead},
		"git log --show-signature":    {ExecutesCode: true, GitCheck: GitCheckRead},
		"git bisect run make":         {ExecutesCode: true},
		"git config core.hooksPath x": {ExecutesCode: true, GitCheck: GitCheckWrite, RepoWrite: true},
	} {
		e := effectsOf(command)
		if e.GitCheck != want.GitCheck || e.RepoWrite != want.RepoWrite || e.Destructive != want.Destructive || e.RemoteMutation != want.RemoteMutation || e.ExecutesCode != want.ExecutesCode || e.SSHAgent != want.SSHAgent {
			t.Fatalf("%s: %+v, want %+v", command, e, want)
		}
	}
}

func TestShellScriptAndEnvSplit(t *testing.T) {
	if script, ok := ShellScript([]string{"bash", "-c", "git push"}); !ok || script != "git push" {
		t.Fatal("bash -c")
	}
	if script, ok := ShellScript([]string{"sh", "-ec", "rm -rf x"}); !ok || script != "rm -rf x" {
		t.Fatal("combined -ec")
	}
	if _, ok := ShellScript([]string{"bash", "-lc", "x"}); ok {
		t.Fatal("login shells load profiles and are not analyzable")
	}
	_, command, _, _, ok := UnwrapEnv([]string{"-S", "git push --force"})
	if !ok || strings.Join(command, " ") != "git push --force" {
		t.Fatalf("env -S split: %v %v", command, ok)
	}
	if _, _, _, _, ok := UnwrapEnv([]string{"-S", `git "$X"`}); ok {
		t.Fatal("env -S with quoting or variables is not static")
	}
	if name, args := HookCommand([]string{"env", "-S", "git -C repo push"}); name != "git" || strings.Join(args, " ") != "push" {
		t.Fatalf("hook unwrap: %s %v", name, args)
	}
}

func TestSedScriptSafeSubset(t *testing.T) {
	for script, safe := range map[string]bool{
		"s/a/b/g":                 true,
		"1,20p":                   true,
		"/^#/d;s|x|y|":            true,
		"$!N;P;D":                 true,
		"/start/,/end/{s/a/b/;p}": true,
		"y/abc/xyz/":              true,
		"a\\\nappended":           true,
		"1e date":                 false,
		"s/a/b/e":                 false,
		"w out.txt":               false,
		"r /etc/passwd":           false,
		"s/a/b/w out":             false,
		"{p":                      false,
	} {
		if got := sedScriptSafe(script); got != safe {
			t.Fatalf("%q: safe=%v want %v", script, got, safe)
		}
	}
}
