package bashast

import (
	"path/filepath"
	"strings"
)

func DetectEmbeddedScripts(cmds []SimpleCommand) []EmbeddedScript {
	var scripts []EmbeddedScript
	for _, cmd := range cmds {
		if len(cmd.Argv) == 0 {
			continue
		}
		scripts = append(scripts, detectEmbeddedScriptsInArgv(cmd.Argv, 0)...)
		if unwrapped, offset, ok := unwrapEnvArgv(cmd.Argv); ok {
			scripts = append(scripts, detectEmbeddedScriptsInArgv(unwrapped, offset)...)
		}
	}
	return dedupeEmbeddedScripts(scripts)
}

func detectEmbeddedScriptsInArgv(argv []string, offset int) []EmbeddedScript {
	var scripts []EmbeddedScript
	for idx := range argv {
		base := strings.ToLower(filepath.Base(argv[idx]))
		switch base {
		case "python", "python3":
			scripts = appendFlagScriptFrom(scripts, argv, idx, offset, "python", []string{"-c"})
		case "node":
			scripts = appendFlagScriptFrom(scripts, argv, idx, offset, "javascript", []string{"-e", "--eval"})
		case "ruby":
			scripts = appendFlagScriptFrom(scripts, argv, idx, offset, "ruby", []string{"-e"})
		case "perl":
			scripts = appendFlagScriptFrom(scripts, argv, idx, offset, "perl", []string{"-e", "-E"})
		case "jq":
			if filter, filterIdx, ok := firstNonFlagArg(argv[idx+1:]); ok && strings.Contains(filter, "system(") {
				scripts = append(scripts, EmbeddedScript{Language: "jq", Code: filter, ArgIndex: offset + idx + 1 + filterIdx})
			}
		case "awk", "gawk":
			if program, programIdx, ok := firstNonFlagArg(argv[idx+1:]); ok && isDangerousAWK(program) {
				scripts = append(scripts, EmbeddedScript{Language: "awk", Code: program, ArgIndex: offset + idx + 1 + programIdx})
			}
		}
	}
	return scripts
}

func appendFlagScriptFrom(out []EmbeddedScript, argv []string, commandIndex int, offset int, language string, flags []string) []EmbeddedScript {
	for idx := commandIndex + 1; idx < len(argv); idx++ {
		arg := argv[idx]
		for _, flag := range flags {
			if arg == flag && idx+1 < len(argv) {
				return append(out, EmbeddedScript{Language: language, Code: argv[idx+1], ArgIndex: offset + idx + 1})
			}
			if strings.HasPrefix(arg, flag+"=") {
				return append(out, EmbeddedScript{Language: language, Code: strings.TrimPrefix(arg, flag+"="), ArgIndex: offset + idx})
			}
		}
	}
	return out
}

func unwrapEnvArgv(argv []string) ([]string, int, bool) {
	if len(argv) == 0 || strings.ToLower(filepath.Base(argv[0])) != "env" {
		return nil, 0, false
	}
	idx := 1
	for idx < len(argv) {
		arg := argv[idx]
		if arg == "--" {
			idx++
			break
		}
		if isEnvAssignmentArg(arg) {
			idx++
			continue
		}
		if arg == "-u" || arg == "--unset" || arg == "-C" || arg == "--chdir" {
			idx += 2
			continue
		}
		if strings.HasPrefix(arg, "-u") && len(arg) > 2 {
			idx++
			continue
		}
		if strings.HasPrefix(arg, "--unset=") || strings.HasPrefix(arg, "--chdir=") {
			idx++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			idx++
			continue
		}
		break
	}
	if idx >= len(argv) {
		return nil, 0, false
	}
	return argv[idx:], idx, true
}

func isEnvAssignmentArg(arg string) bool {
	idx := strings.IndexRune(arg, '=')
	if idx <= 0 {
		return false
	}
	for pos, r := range arg[:idx] {
		if pos == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func dedupeEmbeddedScripts(scripts []EmbeddedScript) []EmbeddedScript {
	if len(scripts) < 2 {
		return scripts
	}
	seen := make(map[EmbeddedScript]struct{}, len(scripts))
	out := make([]EmbeddedScript, 0, len(scripts))
	for _, script := range scripts {
		if _, ok := seen[script]; ok {
			continue
		}
		seen[script] = struct{}{}
		out = append(out, script)
	}
	return out
}

func firstNonFlagArg(args []string) (string, int, bool) {
	for idx := 0; idx < len(args); idx++ {
		arg := strings.TrimSpace(args[idx])
		if arg == "" {
			continue
		}
		if arg == "--" && idx+1 < len(args) {
			return args[idx+1], idx + 1, true
		}
		if strings.HasPrefix(arg, "-") {
			if (arg == "-f" || arg == "--from-file") && idx+1 < len(args) {
				idx++
			}
			continue
		}
		return arg, idx, true
	}
	return "", -1, false
}

func isDangerousAWK(program string) bool {
	lower := strings.ToLower(program)
	return strings.Contains(lower, "system(") || strings.Contains(lower, "| getline")
}

func IsDangerousEmbeddedScript(script EmbeddedScript) bool {
	code := strings.ToLower(script.Code)
	switch script.Language {
	case "python":
		return strings.Contains(code, "os.system(") ||
			strings.Contains(code, "subprocess") ||
			strings.Contains(code, "exec(") ||
			strings.Contains(code, "eval(") ||
			strings.Contains(code, "__import__")
	case "javascript":
		return strings.Contains(code, "child_process") ||
			strings.Contains(code, "exec(") ||
			strings.Contains(code, "eval(") ||
			strings.Contains(code, "require(")
	case "ruby":
		return strings.Contains(code, "system(") ||
			strings.Contains(code, "exec(") ||
			strings.Contains(code, "`")
	case "perl":
		return strings.Contains(code, "system(") ||
			strings.Contains(code, "exec(") ||
			strings.Contains(code, "`")
	case "jq", "awk":
		return true
	default:
		return false
	}
}

func HasDangerousJQFileFlag(cmd SimpleCommand) bool {
	if len(cmd.Argv) == 0 || strings.ToLower(filepath.Base(cmd.Argv[0])) != "jq" {
		return false
	}
	for _, arg := range cmd.Argv[1:] {
		if arg == "-f" || arg == "--from-file" {
			return true
		}
	}
	return false
}
