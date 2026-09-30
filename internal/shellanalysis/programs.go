package shellanalysis

import "strings"

func gitOperands(args []string) Effects {
	// Git consults repository/global executable configuration (fsmonitor,
	// filters, aliases, hooks). Until those dependencies can be frozen, its
	// execution is opaque; explicit paths and network mutations are additional.
	e := Effects{ExecutesCode: true}
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		a := args[i]
		i++
		key, value, hasValue := strings.Cut(a, "=")
		switch key {
		case "-C", "--git-dir", "--work-tree", "--exec-path":
			if !hasValue {
				if i == len(args) {
					e.Unknown = true
					return e
				}
				value = args[i]
				i++
			}
			e.Files = append(e.Files, FileAccess{Path: value, Write: true})
		case "-c", "--config-env":
			if !hasValue {
				if i == len(args) {
					e.Unknown = true
					return e
				}
				i++
			}
		case "--no-pager", "--no-optional-locks", "--literal-pathspecs":
		default:
			if !strings.HasPrefix(a, "-C") && !strings.HasPrefix(a, "-c") {
				e.Unknown = true
			} else if strings.HasPrefix(a, "-C") {
				e.Files = append(e.Files, FileAccess{Path: a[2:], Write: true})
			}
		}
	}
	if i >= len(args) {
		return e
	}
	sub := args[i]
	i++
	e.RemoteMutation = sub == "push" || sub == "send-email"
	switch sub {
	case "config", "update-ref", "symbolic-ref", "init":
		e.Files = append(e.Files, FileAccess{Path: ".git/config", Write: true})
	case "clone":
		if len(args)-i >= 2 {
			e.Files = append(e.Files, FileAccess{Path: args[len(args)-1], Write: true, Recursive: true})
		} else {
			e.Unknown = true
		}
	}
	for ; i < len(args); i++ {
		a := args[i]
		if strings.Contains(a, "://") || strings.HasPrefix(a, "-") {
			continue
		}
		if strings.ContainsAny(a, "/\\") {
			e.Files = append(e.Files, FileAccess{Path: a, Write: true})
		}
	}
	return e
}

func textProgramOperands(base string, args []string) Effects {
	e := Effects{ExecutesCode: true}
	hasProgram, inPlace, end := false, false, false
	var files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !end && a == "--" {
			end = true
			continue
		}
		if !end && strings.HasPrefix(a, "-") && a != "-" {
			switch {
			case a == "-f" || a == "--file":
				if i+1 >= len(args) {
					e.Unknown = true
					break
				}
				i++
				e.Files = append(e.Files, FileAccess{Path: args[i]})
				hasProgram = true
			case a == "-e" || a == "--expression":
				if i+1 >= len(args) {
					e.Unknown = true
					break
				}
				i++
				hasProgram = true
			case strings.HasPrefix(a, "-i") && base == "sed":
				inPlace = true
			case a == "-v" || a == "-F":
				if i+1 < len(args) {
					i++
				} else {
					e.Unknown = true
				}
			case a == "-n" || a == "-E" || a == "-r":
			default:
				e.Unknown = true
			}
			continue
		}
		if !hasProgram {
			hasProgram = true
			continue
		}
		files = append(files, a)
	}
	for _, file := range files {
		if file != "-" {
			e.Files = append(e.Files, FileAccess{Path: file, Write: inPlace || e.Unknown})
		}
	}
	return e
}
