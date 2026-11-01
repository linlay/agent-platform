package accesspolicy

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

var updateShellBaseline = flag.Bool("update-shell-baseline", false, "update the reviewed shell policy baseline")

// One explicit example per recognized name keeps this inventory independent
// of the implementation's decision paths. Changes to the name lists require a
// conscious inventory update even when a name currently falls back to opaque.
var shellBaselineCommands = map[string]string{
	"ls": "ls notes.txt", "cat": "cat notes.txt", "head": "head -n 2 notes.txt",
	"tail": "tail -n 2 notes.txt", "top": "top", "free": "free", "df": "df",
	"git": "git status", "rg": "rg pattern notes.txt", "dbx": "dbx query demo",
	"httpx": "httpx https://example.invalid", "pdftotext": "pdftotext in.pdf out.txt",
	"find": "find . -name '*.txt'", "echo": "echo ok", "printf": "printf '%s' ok",
	"sed": "sed -n '1p' notes.txt", "awk": "awk '{print $1}' notes.txt",
	"gawk": "gawk '{print $1}' notes.txt", "grep": "grep pattern notes.txt",
	"wc": "wc notes.txt", "sort": "sort notes.txt", "uniq": "uniq notes.txt",
	"tr": "tr a b", "cut": "cut -d : -f 1 notes.txt", "cd": "cd .",
	"stat": "stat notes.txt", "file": "file notes.txt", "du": "du .", "test": "test -f notes.txt",
	"which": "which cat", "mkdir": "mkdir build", "rmdir": "rmdir build",
	"touch": "touch notes.txt", "cp": "cp notes.txt copy.txt", "mv": "mv notes.txt moved.txt",
	"rm": "rm notes.txt", "ln": "ln notes.txt linked.txt", "chmod": "chmod 600 notes.txt",
	"date": "date", "curl": "curl -q https://example.invalid/x", "wget": "wget --no-config -O out https://example.invalid/x",
	"lsof": "lsof -nP -iTCP:8080", "pwd": "pwd", "true": "true", "false": "false",
	"sleep": "sleep 1", "uname": "uname", "whoami": "whoami", "id": "id",
	"basename": "basename notes.txt", "dirname": "dirname notes.txt", "readlink": "readlink notes.txt",
	"realpath": "realpath notes.txt", "tee": "tee out.txt", "printenv": "printenv PATH",
	"jq": "jq '.name' data.json", "tar": "tar -tf archive.tar", "diff": "diff a.txt b.txt",
	"Get-ChildItem": "Get-ChildItem", "Get-Content": "Get-Content notes.txt", "Remove-Item": "Remove-Item notes.txt",
	"type": "type cat", "read": "read value", "export": "export VALUE=ok", "unset": "unset VALUE",
	"declare": "declare VALUE=ok", "local": "local VALUE=ok", "typeset": "typeset VALUE=ok",
	"set": "set -e", "shift": "shift", "break": "break", "continue": "continue", "return": "return", ":": ":",
	"env": "env cat notes.txt", "command": "command cat notes.txt", "builtin": "builtin echo ok",
	"nohup": "nohup cat notes.txt", "nice": "nice -n 1 cat notes.txt",
	"timeout": "timeout 1 cat notes.txt", "stdbuf": "stdbuf -oL cat notes.txt",
	"bash": "bash task.sh", "sh": "sh task.sh", "zsh": "zsh task.sh", "dash": "dash task.sh", "ksh": "ksh task.sh",
	"python": "python task.py", "python3": "python3 task.py", "node": "node task.js",
	"ruby": "ruby task.rb", "perl": "perl task.pl", "powershell": "powershell -File task.ps1",
	"pwsh": "pwsh -File task.ps1", "cmd": "cmd /c task.cmd",
	"go": "go build", "npm": "npm test", "npx": "npx tool", "yarn": "yarn test",
	"pnpm": "pnpm test", "pip": "pip install example", "make": "make test",
}

func TestBashReviewNormalizedBaseline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix policy baseline; Windows grammar is tested separately")
	}
	for _, name := range ordinaryCommands {
		if _, ok := shellBaselineCommands[name]; !ok {
			t.Fatalf("recognized command %q has no baseline example", name)
		}
	}
	for _, names := range []map[string]bool{shellBuiltins, wrapperNames} {
		for name := range names {
			if _, ok := shellBaselineCommands[name]; !ok {
				t.Fatalf("builtin/wrapper %q has no baseline example", name)
			}
		}
	}
	names := make([]string, 0, len(shellBaselineCommands))
	for name := range shellBaselineCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	vars := fakeSystemPrograms(t, names...)

	cases := map[string]string{}
	for name, command := range shellBaselineCommands {
		cases["command/"+name] = command
	}
	for name, command := range map[string]string{
		"read-outside": "cat /outside-shell-baseline/read", "write-outside": "tee /outside-shell-baseline/write",
		"delete": "rm -r build", "remote": "curl -q -X DELETE https://example.invalid/x",
		"multi-requirement": "rm -r --unrecognized /outside-shell-baseline/build",
		"unknown-option":    "ls --unrecognized", "explicit-config": "curl -K cfg https://example.invalid/x",
		"implicit-config": "CURL_HOME=. curl https://example.invalid/x",
		"literal-shell":   "bash -c 'cat notes.txt'", "unknown": "unknown-command ./input",
		"null-output": "printf ok > /dev/null", "null-input": "cat < /dev/null",
		"null-curl": "curl -q -o /dev/null https://example.invalid/x", "null-cp": "cp notes.txt /dev/null",
		"null-tee": "tee /dev/null", "null-cat": "cat /dev/null", "null-noncanonical": "printf ok > //dev/null",
		"xargs": "xargs cat", "find-exec": "find . -exec cat '{}' ';'",
		"shell-state": "eval 'echo ok'", "text-block": "echo '\u202e'",
	} {
		cases["effect/"+name] = command
	}
	keys := make([]string, 0, len(cases))
	for key := range cases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var lines []string
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		ctx := &ExecutionContext{Session: f.session}
		for _, key := range keys {
			p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, cases[key], f.workspace, vars, ctx)
			lines = append(lines, shellBaselineLine(key, level, p, cases[key], vars))
		}
		for name, command := range map[string]string{"write": "touch notes.txt", "exec": "python3 task.py"} {
			readonly := f.session
			readonly.WorkspaceReadOnly = true
			p := ReviewBashCommand(config.AccessPolicyConfig{}, readonly, command, f.workspace, vars, ctx)
			lines = append(lines, shellBaselineLine("readonly/"+name, level, p, command, vars))
		}
		// Grants remove only their matching entry, leaving surrounding effects.
		skill, _ := skillFixture(t)
		skill.Session.AccessLevel = level
		for _, command := range []string{"sh selected/scripts/task.sh", "sh selected/scripts/task.sh > /outside-shell-baseline/out"} {
			p := ReviewBashCommand(config.AccessPolicyConfig{}, skill.Session, command, "", vars, skill)
			lines = append(lines, shellBaselineLine("skill/"+command, level, p, command, vars))
		}
		ctx.EnsureAuthoredScripts()
		file := filepath.Join(f.chat, "task.sh")
		data := []byte("#!/bin/sh\necho ok\n")
		if err := os.WriteFile(file, data, 0700); err != nil {
			t.Fatal(err)
		}
		ctx.AuthoredScripts.Record(ctx.ScriptOwner(), file, data, "", true)
		p := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, "sh task.sh", "@chat", vars, ctx)
		lines = append(lines, shellBaselineLine("authored/chat-script", level, p, "sh task.sh", vars))

		mounted, connectorVars, _ := connectorFixture(t)
		mounted.AccessLevel = level
		for _, command := range []string{"wecom-cli send", "wecom-cli send > /outside-shell-baseline/out"} {
			p := ReviewBashCommand(config.AccessPolicyConfig{}, mounted, command, "", connectorVars)
			lines = append(lines, shellBaselineLine("connector/"+command, level, p, command, connectorVars))
		}
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "bash-review-baseline.txt")
	if *updateShellBaseline {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		wantLines := strings.Split(strings.TrimSuffix(string(want), "\n"), "\n")
		for i, line := range lines {
			if i >= len(wantLines) || wantLines[i] != line {
				t.Fatalf("baseline differs at row %d:\n got: %s\nreview testdata/bash-review-baseline.txt before updating", i+1, line)
			}
		}
		t.Fatalf("baseline row count changed: got %d, want %d", len(lines), len(wantLines))
	}
}

func shellBaselineLine(name, level string, p BashPlan, command string, vars map[string]string) string {
	var leaves []string
	var visit func(BashPlan)
	visit = func(p BashPlan) {
		if len(p.Requirements) > 0 {
			for _, leaf := range p.Requirements {
				visit(leaf)
			}
			return
		}
		// Strip machine-dependent hashes, retaining category, target access mode,
		// grant scope and multiplicity. Never snapshot raw paths or environment.
		rule := shellBaselineHash.ReplaceAllString(p.RuleKey, "<hash>")
		leaves = append(leaves, fmt.Sprintf("%s/%s/%s/reusable=%t", p.Decision, rule, p.ScopeKind, RuleReusable(p.RuleKey)))
	}
	visit(p)
	sort.Strings(leaves)
	security := p.SecurityReview(command, vars)
	return fmt.Sprintf("%s\t%s\t%s\tsecurity=%s/%s\tconnector=%t/%t\tleaves=%d:%s", name, level, p.Decision, security.Decision, security.RuleKey, p.HasConnector, p.ConnectorOnly, len(leaves), strings.Join(leaves, ","))
}

var shellBaselineHash = regexp.MustCompile(`[0-9a-f]{16,64}`)
