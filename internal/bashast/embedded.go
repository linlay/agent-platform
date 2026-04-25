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
		base := strings.ToLower(filepath.Base(cmd.Argv[0]))
		switch base {
		case "python", "python3":
			scripts = appendFlagScript(scripts, cmd.Argv, "python", []string{"-c"})
		case "node":
			scripts = appendFlagScript(scripts, cmd.Argv, "javascript", []string{"-e", "--eval"})
		case "ruby":
			scripts = appendFlagScript(scripts, cmd.Argv, "ruby", []string{"-e"})
		case "perl":
			scripts = appendFlagScript(scripts, cmd.Argv, "perl", []string{"-e", "-E"})
		case "jq":
			if filter, idx, ok := firstNonFlagArg(cmd.Argv[1:]); ok && strings.Contains(filter, "system(") {
				scripts = append(scripts, EmbeddedScript{Language: "jq", Code: filter, ArgIndex: idx + 1})
			}
		case "awk", "gawk":
			if program, idx, ok := firstNonFlagArg(cmd.Argv[1:]); ok && isDangerousAWK(program) {
				scripts = append(scripts, EmbeddedScript{Language: "awk", Code: program, ArgIndex: idx + 1})
			}
		}
	}
	return scripts
}

func appendFlagScript(out []EmbeddedScript, argv []string, language string, flags []string) []EmbeddedScript {
	for idx := 1; idx < len(argv); idx++ {
		arg := argv[idx]
		for _, flag := range flags {
			if arg == flag && idx+1 < len(argv) {
				return append(out, EmbeddedScript{Language: language, Code: argv[idx+1], ArgIndex: idx + 1})
			}
			if strings.HasPrefix(arg, flag+"=") {
				return append(out, EmbeddedScript{Language: language, Code: strings.TrimPrefix(arg, flag+"="), ArgIndex: idx})
			}
		}
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
