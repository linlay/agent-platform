package unwrap

import "strings"

// EnvExpansion describes env's arguments and environment changes. Source refs
// are consumed by Execution; syntax-only callers use Command without granting trust.
type EnvExpansion struct {
	Assignments map[string]string
	Command     []string
	Chdir       string
	Ignore      bool
	Unset       []string
	refs        []argumentRef
}

// Env parses the existing conservative env grammar without executing it.
// Generated split-string words retain their input span but are never literal.
func Env(args []string) (EnvExpansion, bool) {
	result := EnvExpansion{Assignments: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			result.Command, result.refs = args[i+1:], suffixRefs(i+1, len(args))
			return result, true
		case a == "-i" || a == "--ignore-environment" || a == "-":
			result.Ignore = true
		case a == "-u" || a == "--unset":
			if i+1 >= len(args) {
				return EnvExpansion{}, false
			}
			i++
			result.Unset = append(result.Unset, args[i])
		case strings.HasPrefix(a, "--unset=") || strings.HasPrefix(a, "-u") && len(a) > 2:
			name := strings.TrimPrefix(a, "--unset=")
			if strings.HasPrefix(a, "-u") {
				name = a[2:]
			}
			result.Unset = append(result.Unset, name)
		case a == "-C" || a == "--chdir":
			if i+1 >= len(args) {
				return EnvExpansion{}, false
			}
			i++
			result.Chdir = args[i]
		case strings.HasPrefix(a, "--chdir="):
			result.Chdir = strings.TrimPrefix(a, "--chdir=")
		case a == "-S" || a == "--split-string" || strings.HasPrefix(a, "-S") || strings.HasPrefix(a, "--split-string="):
			value := ""
			switch {
			case a == "-S" || a == "--split-string":
				if i+1 >= len(args) {
					return EnvExpansion{}, false
				}
				i++
				value = args[i]
			case strings.HasPrefix(a, "--split-string="):
				value = strings.TrimPrefix(a, "--split-string=")
			default:
				value = strings.TrimPrefix(a, "-S")
			}
			if strings.ContainsAny(value, "\\'\"$#") {
				return EnvExpansion{}, false
			}
			split := strings.Fields(value)
			result.Command = append(split, args[i+1:]...)
			for range split {
				result.refs = append(result.refs, argumentRef{arg: i, generated: true})
			}
			result.refs = append(result.refs, suffixRefs(i+1, len(args))...)
			return result, true
		case strings.HasPrefix(a, "-"):
			return EnvExpansion{}, false
		case strings.Contains(a, "=") && !strings.HasPrefix(a, "="):
			name, value, _ := strings.Cut(a, "=")
			result.Assignments[name] = value
		default:
			result.Command, result.refs = args[i:], suffixRefs(i, len(args))
			return result, true
		}
	}
	return result, true
}
