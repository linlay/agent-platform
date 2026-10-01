package shellanalysis

import "strings"

// tarOperands models archive creation and extraction. Members named on the
// command line during extraction are archive entries, not local paths; the
// extraction target is -C or the working directory.
func tarOperands(args []string) Effects {
	var e Effects
	mode := byte(0)
	archive, chdir := "", ""
	var positional []string
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
				case 'C':
					chdir = value
				case 'T', 'X':
					e.Files = append(e.Files, FileAccess{Path: value})
				case 'g':
					e.Files = append(e.Files, FileAccess{Path: value, Write: true})
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
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, value, hasValue := strings.Cut(a[2:], "=")
			take := func() string {
				if hasValue {
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
			case "directory":
				chdir = take()
			case "files-from", "exclude-from":
				e.Files = append(e.Files, FileAccess{Path: take()})
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
			e.Files = append(e.Files, FileAccess{Path: archive, Write: true})
		}
		for _, p := range positional {
			e.Files = append(e.Files, FileAccess{Path: join(p), Recursive: true})
		}
		if e.Destructive {
			for i := range e.Files {
				e.Files[i].Write = true
			}
		}
	case 'x':
		if archive != "" && archive != "-" {
			e.Files = append(e.Files, FileAccess{Path: archive})
		}
		e.Files = append(e.Files, FileAccess{Path: base, Write: true, Recursive: true})
	case 't', 'd':
		if archive != "" && archive != "-" {
			e.Files = append(e.Files, FileAccess{Path: archive})
		}
		if mode == 'd' {
			e.Files = append(e.Files, FileAccess{Path: base, Recursive: true})
		}
	case 'D':
		if archive != "" && archive != "-" {
			e.Files = append(e.Files, FileAccess{Path: archive, Write: true})
		}
	default:
		e.Incomplete = true
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
