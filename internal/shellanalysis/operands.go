// Package shellanalysis describes command effects without deciding permissions.
package shellanalysis

import (
	"net/url"
	"strings"
)

type FileAccess struct {
	Path      string
	Write     bool
	Recursive bool
}

// GitCheck names the repository state an analyzed Git invocation depends on.
// The caller must verify it before treating the invocation as non-executing.
type GitCheck string

const (
	GitCheckNone GitCheck = ""
	// GitCheckRead requires that effective configuration defines no program
	// that read commands run implicitly (fsmonitor, diff drivers, filters, gpg).
	GitCheckRead GitCheck = "read"
	// GitCheckWrite additionally requires that no repository hook is active.
	GitCheckWrite GitCheck = "write"
	// GitCheckNetwork additionally requires standard credential helpers and no
	// custom SSH or askpass programs.
	GitCheckNetwork GitCheck = "network"
)

type Effects struct {
	Files []FileAccess
	// Incomplete marks options or grammar the analyzer does not model. Visible
	// file operands are then treated as writes; it does not imply code execution.
	Incomplete bool
	// ExecutesCode means the invocation can run code not described by operands.
	ExecutesCode   bool
	RemoteMutation bool
	// Destructive marks recursive deletion and discarding uncommitted work.
	Destructive bool
	// RepoWrite marks Git commands that mutate repository metadata in cwd.
	RepoWrite bool
	GitCheck  GitCheck
	// SSHAgent marks network operations that may authenticate with an SSH agent.
	SSHAgent bool
}

// Operands is conservative for unsupported options. Plain filenames are file
// operands according to the program grammar, not according to slash presence.
func Operands(base string, args []string) Effects {
	switch base {
	case "git":
		return gitOperands(args)
	case "sed":
		return sedOperands(args)
	case "awk", "gawk", "mawk", "nawk":
		return awkOperands(args)
	case "find":
		return findOperands(args)
	case "jq":
		return jqOperands(args)
	case "pdftotext":
		return pdftotextOperands(args)
	case "tar", "bsdtar", "gtar":
		return tarOperands(args)
	case "echo", "printf", "pwd", "true", "false", "date", "sleep", "uname", "whoami", "id", "basename", "dirname", "which", "printenv", "test", "tr", "cd", "top", "free":
		e := Effects{}
		if base == "cd" {
			for _, p := range args {
				if !strings.HasPrefix(p, "-") {
					e.Files = append(e.Files, FileAccess{Path: p})
				}
			}
		}
		return e
	}
	spec, known := specs[base]
	if !known {
		return unknownOperands(args)
	}
	p := parseOptions(spec, args)
	e := Effects{Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	e.Files = append(e.Files, p.Files...)
	add := func(path string, write, recursive bool) {
		if path != "" && path != "-" {
			e.Files = append(e.Files, FileAccess{Path: path, Write: write, Recursive: recursive})
		}
	}
	recursive := p.Recursive || p.has("r") || p.has("R")
	switch base {
	case "cat", "head", "tail", "wc", "file", "stat", "readlink", "realpath", "cut", "sort", "du", "ls":
		if base == "sort" || base == "cat" || base == "head" || base == "tail" || base == "wc" || base == "cut" {
			recursive = false
		}
		if base == "du" {
			recursive = true
		}
		for _, path := range p.Positional {
			add(path, false, recursive)
		}
		if len(p.Positional) == 0 && (base == "ls" || base == "du") {
			add(".", false, recursive)
		}
	case "uniq":
		if len(p.Positional) > 0 {
			add(p.Positional[0], false, false)
		}
		if len(p.Positional) > 1 {
			add(p.Positional[1], true, false)
		}
	case "grep", "rg":
		positional := p.Positional
		if !p.has("e") && !p.has("regexp") && !p.has("f") && !p.has("file") && !(base == "rg" && (p.has("files") || p.has("type-list"))) && len(positional) > 0 {
			positional = positional[1:]
		}
		if base == "rg" {
			recursive = true
		}
		for _, path := range positional {
			add(path, false, recursive)
		}
		if len(positional) == 0 && recursive {
			add(".", false, true)
		}
	case "diff":
		for _, path := range p.Positional {
			add(path, false, recursive)
		}
	case "mkdir", "touch", "tee", "chmod", "rmdir":
		positional := p.Positional
		if base == "chmod" && !p.has("reference") && len(positional) > 0 {
			positional = positional[1:]
		}
		for _, path := range positional {
			add(path, true, recursive && base == "chmod")
		}
	case "rm":
		for _, path := range p.Positional {
			add(path, true, recursive)
		}
		e.Destructive = recursive
	case "cp", "mv", "ln":
		targetGiven := p.has("t") || p.has("target-directory")
		for i, path := range p.Positional {
			isTarget := !targetGiven && i == len(p.Positional)-1 && len(p.Positional) > 1
			switch base {
			case "mv":
				add(path, true, true)
			case "cp":
				add(path, isTarget, recursive || p.has("a") || p.has("archive"))
			case "ln":
				// ln TARGET LINK: the link location is written; the target is only named.
				if isTarget || len(p.Positional) == 1 {
					add(path, true, false)
				}
			}
		}
	case "curl", "wget":
		for key, values := range p.values {
			role := spec.long[key]
			if len(key) == 1 {
				role = spec.shortArgs[key[0]]
			}
			if role != optRemoteData {
				continue
			}
			e.RemoteMutation = true
			for _, value := range values {
				if file := remoteDataFile(key, value); file != "" {
					add(file, false, false)
				}
			}
		}
		for _, method := range append(p.values["X"], p.values["request"]...) {
			if upper := strings.ToUpper(method); upper != "GET" && upper != "HEAD" && upper != "OPTIONS" {
				e.RemoteMutation = true
			}
		}
		for _, method := range p.values["method"] {
			if upper := strings.ToUpper(method); upper != "GET" && upper != "HEAD" {
				e.RemoteMutation = true
			}
		}
		if p.has("T") || p.has("upload-file") {
			e.RemoteMutation = true
		}
		if p.has("O") || p.has("remote-name") || p.has("remote-name-all") || base == "wget" && !p.has("O") && !p.has("output-document") && !p.has("spider") {
			// The output file name comes from the URL and lands in cwd.
			add(".", true, base == "wget" && (p.has("r") || p.has("recursive") || p.has("m") || p.has("mirror")))
		}
		for _, raw := range p.Positional {
			if u, err := url.Parse(raw); err == nil && strings.EqualFold(u.Scheme, "file") {
				add(u.Path, false, false)
			} else if !strings.Contains(raw, "://") && strings.ContainsAny(raw, "/\\") {
				// A bare host/path is a URL to curl; only obvious local paths are reviewed.
				e.Incomplete = true
			}
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

func remoteDataFile(key, value string) string {
	switch key {
	case "data-raw", "form-string":
		return ""
	case "F", "form":
		_, v, ok := strings.Cut(value, "=")
		if ok && (strings.HasPrefix(v, "@") || strings.HasPrefix(v, "<")) {
			file, _, _ := strings.Cut(v[1:], ";")
			return file
		}
		return ""
	case "data-urlencode":
		if _, v, ok := strings.Cut(value, "@"); ok {
			return v
		}
		return ""
	}
	if strings.HasPrefix(value, "@") {
		return value[1:]
	}
	return ""
}

func escalateWrites(e *Effects) {
	// An unrecognized option may turn read operands into output targets.
	for i := range e.Files {
		e.Files[i].Write = true
	}
}

func unknownOperands(args []string) Effects {
	e := Effects{Incomplete: true, ExecutesCode: true}
	for _, p := range args {
		if value, ok := strings.CutPrefix(p, "--"); ok {
			if _, v, has := strings.Cut(value, "="); has {
				p = v
			} else {
				continue
			}
		}
		if strings.ContainsAny(p, "/\\") && !strings.Contains(p, "://") {
			e.Files = append(e.Files, FileAccess{Path: p, Write: true})
		}
	}
	return e
}

func jqOperands(args []string) Effects {
	p := parseOptions(specs["jq"], args)
	e := Effects{Incomplete: p.Incomplete}
	e.Files = append(e.Files, p.Files...)
	positional := p.Positional
	if !p.has("f") && !p.has("from-file") && len(positional) > 0 {
		positional = positional[1:]
	}
	if !p.has("args") && !p.has("jsonargs") {
		for _, path := range positional {
			if path != "-" {
				e.Files = append(e.Files, FileAccess{Path: path})
			}
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

func pdftotextOperands(args []string) Effects {
	var e Effects
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-f", "-l", "-r", "-x", "-y", "-W", "-H", "-enc", "-eol", "-opw", "-upw", "-fixed", "-cropbox":
			i++
			continue
		case "-layout", "-raw", "-nopgbrk", "-q", "-htmlmeta", "-bbox", "-bbox-layout", "-nodiag", "-listenc", "-v", "-tsv", "-simple", "-simple2":
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			e.Incomplete = true
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) > 0 {
		e.Files = append(e.Files, FileAccess{Path: positional[0]})
	}
	if len(positional) > 1 && positional[1] != "-" {
		e.Files = append(e.Files, FileAccess{Path: positional[1], Write: true})
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}

func findOperands(args []string) Effects {
	var e Effects
	i := 0
	for i < len(args) && (args[i] == "-L" || args[i] == "-H" || args[i] == "-P") {
		i++
	}
	for i < len(args) && !strings.HasPrefix(args[i], "-") && args[i] != "!" && args[i] != "(" {
		e.Files = append(e.Files, FileAccess{Path: args[i], Recursive: true})
		i++
	}
	if len(e.Files) == 0 {
		e.Files = append(e.Files, FileAccess{Path: ".", Recursive: true})
	}
	roots := len(e.Files)
	for ; i < len(args); i++ {
		switch args[i] {
		case "-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename", "-lname", "-ilname", "-regex", "-iregex", "-regextype", "-type", "-xtype", "-maxdepth", "-mindepth", "-size", "-mtime", "-mmin", "-atime", "-amin", "-ctime", "-cmin", "-perm", "-user", "-group", "-uid", "-gid", "-links", "-inum", "-printf", "-used":
			i++
			if i >= len(args) {
				e.Incomplete = true
			}
		case "-newer", "-anewer", "-cnewer", "-samefile":
			i++
			if i < len(args) {
				e.Files = append(e.Files, FileAccess{Path: args[i]})
			} else {
				e.Incomplete = true
			}
		case "-fprint", "-fprint0", "-fls":
			i++
			if i < len(args) {
				e.Files = append(e.Files, FileAccess{Path: args[i], Write: true})
			} else {
				e.Incomplete = true
			}
		case "-fprintf":
			i += 2
			if i-1 < len(args) {
				e.Files = append(e.Files, FileAccess{Path: args[i-1], Write: true})
			} else {
				e.Incomplete = true
			}
		case "-print", "-print0", "-ls", "-empty", "-a", "-o", "-and", "-or", "!", "-not", "(", ")", "-prune", "-true", "-false", "-depth", "-d", "-follow", "-xdev", "-mount", "-readable", "-writable", "-executable", "-nouser", "-nogroup", "-quit", "-noleaf", "-daystart":
		case "-delete":
			for j := 0; j < roots; j++ {
				e.Files[j].Write = true
			}
			e.Destructive = true
		default:
			// -exec/-execdir/-ok run programs over runtime-selected paths.
			e.ExecutesCode = true
			e.Incomplete = true
		}
	}
	if e.Incomplete {
		escalateWrites(&e)
	}
	return e
}
