package commands

import (
	"strings"

	"agent-platform/internal/shellreview/types"
)

// tarOperands models archive creation and extraction. Members named on the
// command line during extraction are archive entries, not local paths; the
// extraction target is -C or the working directory.
func tarOperands(args []string) effectDraft {
	var e effectDraft
	mode := byte(0)
	archive, chdir := "", ""
	var positional []string
	var positionalRefs []types.ValueRef
	var archiveRef, chdirRef, currentRef types.ValueRef
	setMode := func(m byte) {
		if mode != 0 && mode != m {
			e.Incomplete = true
		}
		mode = m
	}
	flagLetters := func(letters string, next func() (string, bool)) {
		for i := 0; i < len(letters); i++ {
			switch c := letters[i]; c {
			case 'c', 'x', 't', 'r', 'u', 'A', 'd':
				setMode(c)
			case 'f', 'C', 'T', 'X', 'b', 'N', 'K', 'V', 'L', 'g', 'H':
				value, ok := next()
				if !ok {
					e.Incomplete = true
					return
				}
				switch c {
				case 'f':
					archive = value
					archiveRef = currentRef
				case 'C':
					chdir = value
					chdirRef = currentRef
				case 'T', 'X':
					e.Files = append(e.Files, types.FileAccess{Path: value, Ref: currentRef})
				case 'g':
					e.Files = append(e.Files, types.FileAccess{Path: value, Write: true, Ref: currentRef, Kind: types.Output})
				}
			case 'I':
				e.ExecutesCode = true
				next()
			case 'P':
				// Absolute member names may extract anywhere on the filesystem.
				e.ExecutesCode = true
			case 'z', 'j', 'J', 'Z', 'v', 'p', 'h', 'k', 'm', 'o', 'O', 'S', 'W', 'w', 'B', 'i', 'a', 'U', 'l', 's', 'M', 'R', 'G', 'n', 'q', 'y':
			default:
				e.Incomplete = true
			}
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				currentRef = types.ArgumentRef(i, args[i])
				return args[i], true
			}
			return "", false
		}
		switch {
		case i == 0 && !strings.HasPrefix(a, "-") && a != "":
			// Traditional bundled form: tar cvf archive.tar dir
			flagLetters(a, next)
		case a == "--":
			positional = append(positional, args[i+1:]...)
			for j := i + 1; j < len(args); j++ {
				positionalRefs = append(positionalRefs, types.ArgumentRef(j, args[j]))
			}
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, value, hasValue := strings.Cut(a[2:], "=")
			take := func() string {
				if hasValue {
					currentRef = types.ValueRef{Index: i, Start: len(name) + 3, End: len(a), Known: true}
					return value
				}
				v, ok := next()
				if !ok {
					e.Incomplete = true
				}
				return v
			}
			switch name {
			case "create":
				setMode('c')
			case "extract", "get":
				setMode('x')
			case "list":
				setMode('t')
			case "append":
				setMode('r')
			case "update":
				setMode('u')
			case "diff", "compare":
				setMode('d')
			case "delete":
				setMode('D')
			case "file":
				archive = take()
				archiveRef = currentRef
			case "directory":
				chdir = take()
				chdirRef = currentRef
			case "files-from", "exclude-from":
				value := take()
				e.Files = append(e.Files, types.FileAccess{Path: value, Ref: currentRef})
			case "to-command", "use-compress-program", "rsh-command", "info-script", "new-volume-script", "checkpoint-action":
				take()
				e.ExecutesCode = true
			case "absolute-names":
				e.ExecutesCode = true
			case "exclude", "transform", "xform", "strip-components", "owner", "group", "mode", "mtime", "format", "checkpoint", "blocking-factor", "label", "newer", "after-date", "newer-mtime", "exclude-vcs-ignores", "occurrence", "record-size", "sort", "warning", "xattrs-include", "xattrs-exclude":
				if name != "exclude-vcs-ignores" {
					take()
				}
			case "gzip", "gunzip", "bzip2", "xz", "lzma", "zstd", "compress", "auto-compress", "verbose", "preserve-permissions", "same-permissions", "dereference", "keep-old-files", "skip-old-files", "overwrite", "no-same-owner", "no-same-permissions", "numeric-owner", "wildcards", "no-wildcards", "anchored", "no-anchored", "exclude-vcs", "ignore-case", "totals", "to-stdout", "touch", "recursion", "no-recursion", "one-file-system", "sparse", "xattrs", "no-xattrs", "acls", "selinux", "show-transformed-names", "unlink-first", "remove-files":
				if name == "remove-files" {
					// Deletes the source files after archiving them.
					e.Destructive = true
				}
			default:
				e.Incomplete = true
			}
		case strings.HasPrefix(a, "-") && a != "-":
			flagLetters(a[1:], next)
		default:
			positional = append(positional, a)
			positionalRefs = append(positionalRefs, types.ArgumentRef(i, a))
		}
	}
	base := chdir
	if base == "" {
		base = "."
	}
	join := func(p string) string {
		if chdir == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
			return p
		}
		return strings.TrimRight(chdir, "/") + "/" + p
	}
	switch mode {
	case 'c', 'r', 'u', 'A':
		if archive != "" && archive != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: archive, Write: true, Ref: archiveRef, Kind: types.Output})
		}
		for i, p := range positional {
			e.Files = append(e.Files, types.FileAccess{Path: join(p), Recursive: true, Ref: positionalRefs[i], Derived: chdir != ""})
		}
		if e.Destructive {
			for i := range e.Files {
				e.Files[i].Write = true
			}
		}
	case 'x':
		if archive != "" && archive != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: archive, Ref: archiveRef})
		}
		e.Files = append(e.Files, types.FileAccess{Path: base, Write: true, Recursive: true, Ref: chdirRef, Kind: types.Create})
	case 't', 'd':
		if archive != "" && archive != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: archive, Ref: archiveRef})
		}
		if mode == 'd' {
			e.Files = append(e.Files, types.FileAccess{Path: base, Recursive: true, Ref: chdirRef})
		}
	case 'D':
		if archive != "" && archive != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: archive, Write: true, Ref: archiveRef, Kind: types.Output})
		}
	default:
		e.Incomplete = true
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

func tarConfig([]string) []string { return []string{"TAR_OPTIONS"} }
