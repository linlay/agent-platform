// Package shellanalysis describes command effects without deciding permissions.
package shellanalysis

import (
	"net/url"
	"strconv"
	"strings"
)

type FileAccess struct {
	Path      string
	Write     bool
	Recursive bool
}

type Effects struct {
	Files          []FileAccess
	Unknown        bool
	ExecutesCode   bool
	RemoteMutation bool
}

// Operands is conservative for unsupported options. Plain filenames are file
// operands according to the program grammar, not according to slash presence.
func Operands(base string, args []string) Effects {
	if base == "git" {
		return gitOperands(args)
	}
	if base == "sed" || base == "awk" {
		return textProgramOperands(base, args)
	}
	if base == "find" {
		return findOperands(args)
	}
	var e Effects
	add := func(p string, write, recursive bool) {
		if p != "" && p != "-" {
			e.Files = append(e.Files, FileAccess{p, write, recursive})
		}
	}
	var positional []string
	end := false
	pattern := false
	recursive := base == "rg" || base == "find" || base == "du"
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !end && a == "--" {
			end = true
			continue
		}
		if !end && strings.HasPrefix(a, "-") && a != "-" {
			key, value, hasValue := strings.Cut(a, "=")
			take := func() string {
				if hasValue {
					return value
				}
				if i+1 < len(args) {
					i++
					return args[i]
				}
				e.Unknown = true
				return ""
			}
			switch base {
			case "curl", "wget":
				if strings.HasPrefix(key, "-o") && len(key) > 2 {
					add(key[2:], true, false)
					continue
				}
				if key == "-o" || key == "--output" || key == "-O" && base == "wget" || key == "--output-document" {
					add(take(), true, false)
					continue
				}
				if key == "-T" || key == "--upload-file" || key == "--data-binary" || key == "-d" || key == "--data" || key == "-F" || key == "--form" {
					v := take()
					if key == "-T" || key == "--upload-file" {
						add(v, false, false)
					} else if strings.HasPrefix(v, "@") {
						add(v[1:], false, false)
					} else if _, value, ok := strings.Cut(v, "="); ok && (strings.HasPrefix(value, "@") || strings.HasPrefix(value, "<")) {
						file, _, _ := strings.Cut(value[1:], ";")
						add(file, false, false)
					}
					e.RemoteMutation = true
					continue
				}
				if key == "-X" || key == "--request" {
					v := strings.ToUpper(take())
					e.RemoteMutation = e.RemoteMutation || (v != "GET" && v != "HEAD")
					continue
				}
				if key == "-K" || key == "--config" || key == "--netrc-file" || key == "--cacert" || key == "--cert" || key == "--key" || key == "--cookie" || key == "-b" {
					add(take(), false, false)
					continue
				}
				if key == "-H" || key == "--header" {
					v := take()
					if strings.HasPrefix(v, "@") {
						add(v[1:], false, false)
					}
					continue
				}
				if key == "--max-time" || key == "--connect-timeout" {
					take()
					continue
				}
			case "grep", "rg":
				if key == "-e" || key == "--regexp" {
					take()
					pattern = true
					continue
				}
				if key == "-f" || key == "--file" {
					add(take(), false, false)
					pattern = true
					continue
				}
				if key == "-g" || key == "--glob" || key == "--type" || key == "-t" || key == "--max-count" || key == "-m" || key == "-A" || key == "-B" || key == "-C" {
					take()
					continue
				}
			case "head", "tail":
				if _, err := strconv.Atoi(key); err == nil {
					continue
				}
				if key == "-n" || key == "-c" || key == "--lines" || key == "--bytes" {
					take()
					continue
				}
			case "sort":
				if key == "-o" || key == "--output" {
					add(take(), true, false)
					continue
				}
				if key == "-k" || key == "-t" || key == "--key" || key == "--field-separator" {
					take()
					continue
				}
			case "cp", "mv", "ln":
				if key == "-t" || key == "--target-directory" {
					add(take(), true, true)
					continue
				}
			case "git", "sed", "awk":
				e.ExecutesCode = true
			}
			if key == "-r" || key == "-R" || key == "--recursive" || strings.Contains(strings.TrimPrefix(key, "-"), "r") && (base == "rm" || base == "cp" || base == "grep") {
				recursive = true
			}
			if hasValue || !knownFlag(base, key) {
				e.Unknown = true
				if hasValue {
					add(value, true, false)
				}
			}
			continue
		}
		positional = append(positional, a)
	}
	switch base {
	case "cat", "head", "tail", "wc", "file", "stat", "readlink", "realpath", "ls", "du", "sort", "uniq":
		for _, p := range positional {
			add(p, false, recursive)
		}
		if len(positional) == 0 && (base == "ls" || base == "du") {
			add(".", false, recursive)
		}
	case "grep", "rg":
		if !pattern && len(positional) > 0 {
			positional = positional[1:]
		}
		for _, p := range positional {
			add(p, false, recursive)
		}
		if len(positional) == 0 && recursive {
			add(".", false, true)
		}
	case "mkdir", "touch", "rm", "chmod", "tee":
		if base == "chmod" && len(positional) > 0 {
			positional = positional[1:]
		}
		for _, p := range positional {
			add(p, true, recursive)
		}
	case "cp", "mv", "ln":
		for i, p := range positional {
			add(p, base == "mv" || i == len(positional)-1, recursive || base == "mv")
		}
	case "cd":
		for _, p := range positional {
			add(p, false, false)
		}
	case "curl", "wget":
		for _, p := range positional {
			if u, err := url.Parse(p); err == nil && strings.EqualFold(u.Scheme, "file") {
				add(u.Path, false, false)
			} else if !strings.Contains(p, "://") {
				e.Unknown = true
				add(p, true, false)
			}
		}
	case "git", "sed", "awk", "find":
		// Repository configuration, sed/awk programs and find expressions can
		// execute code. A grammar not yet supported is never inferred read-only.
		e.ExecutesCode = true
		for _, p := range positional {
			if strings.ContainsAny(p, "/\\") && !strings.Contains(p, "://") {
				add(p, true, recursive)
			}
		}
	case "echo", "printf", "pwd", "true", "false", "date", "sleep", "uname", "whoami", "id", "basename", "dirname", "which", "printenv", "test":
	default:
		e.Unknown = true
		for _, p := range positional {
			if strings.ContainsAny(p, "/\\") && !strings.Contains(p, "://") {
				add(p, true, false)
			}
		}
	}
	if e.Unknown {
		// An unrecognized option may change read operands into output targets.
		// Preserve the visible write boundary even in auto_approve.
		for i := range e.Files {
			e.Files[i].Write = true
		}
	}
	return e
}

func knownFlag(base, flag string) bool {
	if flag == "--help" || flag == "--version" {
		return true
	}
	if strings.HasPrefix(flag, "--") {
		return flag == "--recursive"
	}
	letters := ""
	switch base {
	case "cat":
		letters = "AbenstuvET"
	case "ls":
		letters = "aAbBcCdDfFghHiIklLmNnopqQrRsStTuUvwWxXZ1"
	case "head", "tail":
		letters = "qvfF"
	case "wc":
		letters = "clLmw"
	case "grep", "rg":
		letters = "ivnHhlcrRqFsSwxobUaIzZ"
	case "cp", "mv", "ln":
		letters = "afinpPRrsTv"
	case "rm":
		letters = "dfirRvI"
	case "mkdir":
		letters = "pv"
	case "touch":
		letters = "acm"
	case "chmod":
		letters = "Rfv"
	case "tee":
		letters = "ai"
	case "curl":
		letters = "sSfILlvk"
	case "wget":
		letters = "qcv"
	case "sort":
		letters = "bdfginruMhsVz"
	case "uniq":
		letters = "cduiz"
	case "stat":
		letters = "fLltx"
	case "file":
		letters = "bhiLsz"
	case "du":
		letters = "achksx"
	case "readlink", "realpath":
		letters = "efmnqsz"
	}
	return letters != "" && strings.Trim(strings.TrimPrefix(flag, "-"), letters) == ""
}

func findOperands(args []string) Effects {
	var e Effects
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") && args[i] != "!" && args[i] != "(" {
		e.Files = append(e.Files, FileAccess{Path: args[i], Recursive: true})
		i++
	}
	if len(e.Files) == 0 {
		e.Files = append(e.Files, FileAccess{Path: ".", Recursive: true})
	}
	for ; i < len(args); i++ {
		switch args[i] {
		case "-name", "-iname", "-path", "-ipath", "-type", "-maxdepth", "-mindepth", "-size", "-mtime", "-mmin", "-perm":
			i++
			if i >= len(args) {
				e.Unknown = true
			}
		case "-print", "-print0", "-ls", "-empty", "-a", "-o", "-and", "-or", "!", "-not", "(", ")", "-prune":
		case "-delete":
			for j := range e.Files {
				e.Files[j].Write = true
			}
		default:
			e.ExecutesCode = true
			e.Unknown = true
		}
	}
	return e
}
