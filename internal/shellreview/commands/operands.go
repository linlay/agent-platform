// Package commands describes command effects without deciding permissions.
package commands

import (
	"net/url"
	"strings"

	"agent-platform/internal/shellreview/options"
	"agent-platform/internal/shellreview/types"
)

// Operands is conservative for unsupported options. Plain filenames are file
// operands according to the program grammar, not according to slash presence.
func Operands(base string, args []string) types.Effects {
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
	case "lsof":
		return lsofOperands(args)
	case "tar", "bsdtar", "gtar":
		return tarOperands(args)
	case "echo", "printf", "pwd", "true", "false", "date", "sleep", "uname", "whoami", "id", "basename", "dirname", "which", "printenv", "test", "tr", "cd", "top", "free":
		e := types.Effects{}
		if base == "cd" {
			for _, p := range args {
				if !strings.HasPrefix(p, "-") {
					e.Files = append(e.Files, types.FileAccess{Path: p})
				}
			}
		}
		return e
	}
	spec, known := specs[base]
	if !known {
		return unknownOperands(args)
	}
	p := options.Parse(spec, args)
	e := types.Effects{Incomplete: p.Incomplete, ExecutesCode: p.Exec}
	e.Files = append(e.Files, p.Files...)
	add := func(path string, write, recursive bool) {
		if path != "" && path != "-" {
			e.Files = append(e.Files, types.FileAccess{Path: path, Write: write, Recursive: recursive})
		}
	}
	recursive := p.Recursive || p.Has("r") || p.Has("R")
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
		if !p.Has("e") && !p.Has("regexp") && !p.Has("f") && !p.Has("file") && !(base == "rg" && (p.Has("files") || p.Has("type-list"))) && len(positional) > 0 {
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
		if base == "chmod" && !p.Has("reference") && len(positional) > 0 {
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
		targetGiven := p.Has("t") || p.Has("target-directory")
		for i, path := range p.Positional {
			isTarget := !targetGiven && i == len(p.Positional)-1 && len(p.Positional) > 1
			switch base {
			case "mv":
				add(path, true, true)
			case "cp":
				add(path, isTarget, recursive || p.Has("a") || p.Has("archive"))
			case "ln":
				// ln TARGET LINK: the link location is written; the target is only named.
				if isTarget || len(p.Positional) == 1 {
					add(path, true, false)
				}
			}
		}
	case "curl", "wget":
		for key, values := range p.Values {
			role := spec.Long[key]
			if len(key) == 1 {
				role = spec.ShortArgs[key[0]]
			}
			if role != types.OptionRemoteData {
				continue
			}
			e.RemoteMutation = true
			for _, value := range values {
				if file := remoteDataFile(key, value); file != "" {
					add(file, false, false)
				}
			}
		}
		for _, method := range append(p.Values["X"], p.Values["request"]...) {
			if upper := strings.ToUpper(method); upper != "GET" && upper != "HEAD" && upper != "OPTIONS" {
				e.RemoteMutation = true
			}
		}
		for _, method := range p.Values["method"] {
			if upper := strings.ToUpper(method); upper != "GET" && upper != "HEAD" {
				e.RemoteMutation = true
			}
		}
		if p.Has("T") || p.Has("upload-file") {
			e.RemoteMutation = true
		}
		if (base == "curl" && p.Has("K")) || p.Has("config") {
			// A config file can add outputs, uploads and URLs that are not reviewed here.
			e.ExecutesCode = true
		}
		if p.Has("O") || p.Has("remote-name") || p.Has("remote-name-all") || base == "wget" && !p.Has("O") && !p.Has("output-document") && !p.Has("spider") {
			// The output file name comes from the URL and lands in cwd.
			add(".", true, base == "wget" && (p.Has("r") || p.Has("recursive") || p.Has("m") || p.Has("mirror")))
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

// ConfigVariables names environment variables that make the program read
// options from a file or value outside argv. A non-empty value is behavior the
// analysis has not reviewed. Default HOME/XDG config files are not covered.
func ConfigVariables(base string, args []string) []string {
	noConfig := func() bool {
		for _, a := range args {
			if a == "--" {
				return false
			}
			if a == "--no-config" {
				return true
			}
		}
		return false
	}
	switch base {
	case "curl":
		// curl only honors -q/--disable as the first argument.
		if len(args) > 0 && (args[0] == "-q" || args[0] == "--disable") {
			return nil
		}
		return []string{"CURL_HOME"}
	case "wget":
		if noConfig() {
			return nil
		}
		return []string{"WGETRC"}
	case "rg":
		if noConfig() {
			return nil
		}
		return []string{"RIPGREP_CONFIG_PATH"}
	case "tar", "gtar":
		// GNU tar prepends TAR_OPTIONS to argv; bsdtar ignores it.
		return []string{"TAR_OPTIONS"}
	}
	return nil
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

func escalateWrites(e *types.Effects) {
	// An unrecognized option may turn read operands into output targets.
	for i := range e.Files {
		e.Files[i].Write = true
	}
}

func unknownOperands(args []string) types.Effects {
	e := types.Effects{Incomplete: true, ExecutesCode: true}
	for _, p := range args {
		if value, ok := strings.CutPrefix(p, "--"); ok {
			if _, v, has := strings.Cut(value, "="); has {
				p = v
			} else {
				continue
			}
		}
		if strings.ContainsAny(p, "/\\") && !strings.Contains(p, "://") {
			e.Files = append(e.Files, types.FileAccess{Path: p, Write: true})
		}
	}
	return e
}
