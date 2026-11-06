package commands

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"agent-platform/internal/shellreview/types"
)

func checkSpecExample(t *testing.T, name string, c specCommand, ex specExample) {
	t.Helper()
	e := c.analyze(ex.args)
	got := map[string][]string{}
	for _, f := range e.Files {
		got[string(f.Kind)] = append(got[string(f.Kind)], f.Path)
		if f.Recursive {
			got["recursive"] = append(got["recursive"], f.Path)
		}
	}
	want := map[string][]string{}
	for key, paths := range ex.expect {
		if len(paths) > 0 {
			want[key] = slices.Sorted(slices.Values(paths))
		}
	}
	for key := range got {
		slices.Sort(got[key])
	}
	if e.Incomplete || e.ExecutesCode != ex.exec || !reflect.DeepEqual(got, want) {
		t.Errorf("%s %q: got %v exec=%t incomplete=%t, want %v exec=%t", name, ex.args, got, e.ExecutesCode, e.Incomplete, want, ex.exec)
	}
}

func TestSpecCommandExamples(t *testing.T) {
	for name, c := range specCommands {
		for _, ex := range c.examples {
			checkSpecExample(t, name, c, ex)
		}
	}
}

func TestSpecCommandsTreatUnlistedOptionsAsUnknown(t *testing.T) {
	for name, c := range specCommands {
		unlisted := [][]string{{"--shellreview-unlisted", "notes.txt"}}
		for _, letter := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" {
			if _, used := c.spec.ShortArgs[byte(letter)]; !used && !strings.ContainsRune(c.spec.ShortFlags, letter) {
				unlisted = append(unlisted, []string{"-" + string(letter), "notes.txt"})
				break
			}
		}
		for _, args := range unlisted {
			e := nativeFacts(name, args...)
			written := false
			for _, op := range e.Ops {
				for _, target := range op.Targets {
					written = written || target.Path == "notes.txt" && op.WritesTarget(target)
				}
			}
			if !e.Unknown(types.UnknownOption) || !written {
				t.Errorf("%s %q: an unlisted option must stay unknown and turn operands into writes: %+v", name, args, e)
			}
		}
	}
}

func TestOptionsThatReadUnseenFilesStayUnknown(t *testing.T) {
	// shasum -c reads the files listed in sums.txt; procps ps -n reads a namelist.
	for _, args := range [][]string{{"shasum", "-c", "sums.txt"}, {"shasum", "--check", "sums.txt"}, {"ps", "-n", "list"}} {
		if e := nativeFacts(args[0], args[1:]...); !e.Unknown(types.UnknownOption) {
			t.Errorf("%q must stay unknown: %+v", args, e)
		}
	}
}

// Fake commands exercise every operand use and role, so the format is covered
// before any real command depends on a part of it.
const specFormatYAML = `
fake-none:
  operands: none
  short:
    flag: ["0", a]
    value: [s]
    read: [f]
  long:
    value: [suffix]
  examples:
    - args: [-a0, -s, x, notes.txt]
      expect: {}
    - args: [-f, in.txt, --suffix=.c, notes.txt]
      expect: {read: [in.txt]}
fake-reject:
  operands: reject
  short:
    value: [s]
  word:
    flag: [quiet]
    value: [level]
  examples:
    - args: [-s, "5", -quiet, -level, warn]
      expect: {}
fake-read:
  operands: read
  numeric: true
  short:
    value: [n]
    optional: [U]
    read-dir: [D]
    recursive: [r]
  long:
    read: [from]
    exec: [program]
    recursive: [recursive]
  examples:
    - args: ["-20", -n, "5", a.txt]
      expect: {read: [a.txt]}
    - args: [-U, b.txt, -U3]
      expect: {read: [b.txt]}
    - args: [-r, src]
      expect: {read: [src], recursive: [src]}
    - args: [--recursive, src]
      expect: {read: [src], recursive: [src]}
    - args: [-D, lib, --from=list.txt]
      expect: {read: [lib, list.txt], recursive: [lib]}
    - args: [--program, gzip, a.txt]
      expect: {read: [a.txt], exec: true}
fake-create:
  operands: create
  short:
    write-dir: [T]
  word:
    write: [plist]
  examples:
    - args: [-T, tmp, new]
      expect: {create: [tmp, new], recursive: [tmp]}
    - args: [-plist, out.plist, new]
      expect: {output: [out.plist], create: [new]}
fake-output:
  operands: output
  long:
    write: [log]
  examples:
    - args: [--log=run.log, out.txt, "-"]
      expect: {output: [run.log, out.txt]}
fake-delete:
  operands: delete
  short:
    flag: [f]
    recursive: [r, R]
  examples:
    - args: [-f, old]
      expect: {delete: [old]}
    - args: [-fR, build]
      expect: {delete: [build], recursive: [build]}
`

func TestSpecFormatCoversEveryOperandUseAndRole(t *testing.T) {
	parsed, err := parseSpecCommands([]byte(specFormatYAML))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range parsed {
		if len(c.examples) == 0 {
			t.Fatalf("%s has no examples", name)
		}
		for _, ex := range c.examples {
			checkSpecExample(t, name, c, ex)
		}
	}
	del := parsed["fake-delete"]
	if !del.analyze([]string{"-r", "build"}).Destructive || del.analyze([]string{"build"}).Destructive {
		t.Fatal("only recursive deletion is destructive")
	}
	if e := parsed["fake-none"].analyze([]string{"--made-up", "notes.txt", "https://x/y"}); !e.Incomplete || len(e.Files) != 1 || !e.Files[0].Write {
		t.Fatalf("unknown option must turn data operands into writes, except URLs: %+v", e)
	}
	if e := parsed["fake-reject"].analyze([]string{"notes.txt"}); !e.Incomplete || len(e.Files) != 1 || !e.Files[0].Write {
		t.Fatalf("an operand of a reject command is unmodeled: %+v", e)
	}
}

func TestSpecLoaderIsStrict(t *testing.T) {
	for name, source := range map[string]string{
		"missing operands":        "x:\n  short:\n    flag: [a]\n",
		"unknown operands":        "x:\n  operands: write\n",
		"unknown field":           "x:\n  operands: read\n  aliases: [y]\n",
		"unknown role":            "x:\n  operands: read\n  long:\n    path: [from]\n",
		"role not a list":         "x:\n  operands: read\n  long:\n    flag: all\n",
		"unquoted digit":          "x:\n  operands: read\n  short:\n    flag: [1]\n",
		"two letters":             "x:\n  operands: read\n  short:\n    flag: [ab]\n",
		"dashed long":             "x:\n  operands: read\n  long:\n    flag: [--all]\n",
		"dashed word":             "x:\n  operands: read\n  word:\n    flag: [-raw]\n",
		"one-letter word":         "x:\n  operands: read\n  word:\n    flag: [r]\n",
		"duplicate word":          "x:\n  operands: read\n  word:\n    flag: [raw]\n    value: [raw]\n",
		"recursive reject":        "x:\n  operands: reject\n  short:\n    recursive: [r]\n",
		"long with value":         "x:\n  operands: read\n  long:\n    flag: [color=auto]\n",
		"duplicate short":         "x:\n  operands: read\n  short:\n    flag: [a]\n    value: [a]\n",
		"duplicate long":          "x:\n  operands: read\n  long:\n    flag: [all]\n    value: [all]\n",
		"duplicate command":       "x:\n  operands: read\nx:\n  operands: none\n",
		"recursive without files": "x:\n  operands: none\n  long:\n    recursive: [recursive]\n",
		"numeric not bool":        "x:\n  operands: read\n  numeric: yes\n",
		"example without expect":  "x:\n  operands: read\n  examples:\n    - args: [a]\n",
		"unknown effect":          "x:\n  operands: read\n  examples:\n    - args: [a]\n      expect: {write: [a]}\n",
		"unquoted example number": "x:\n  operands: read\n  examples:\n    - args: [-20]\n      expect: {}\n",
	} {
		if _, err := parseSpecCommands([]byte(source)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, name := range []string{"WC", "bad/name", "-x", "git", "python", "env", "xargs"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: accepted as a table command", name)
				}
			}()
			mustSpecCommands([]byte(name + ":\n  operands: none\n"))
		}()
	}
	if got := mustSpecCommands([]byte("no-such-command:\n  operands: none\n")); len(got) != 1 {
		t.Fatal("an unregistered lowercase program name is a new table program")
	}
}
