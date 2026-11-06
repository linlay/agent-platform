package commands

import "strings"

// sysctlOperands accepts reads of named or all variables on macOS and procps.
// A name=value operand, -w, or a settings file (-f, -p, --load, --system)
// sets kernel state and stays opaque.
func sysctlOperands(args []string) effectDraft {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-B" || a == "-r" || a == "--pattern":
			if i++; i >= len(args) {
				return unknownOperands(args)
			}
		case strings.HasPrefix(a, "--pattern="):
		case strings.HasPrefix(a, "--"):
			switch a {
			case "--all", "--binary", "--ignore", "--names", "--values", "--quiet", "--help", "--version":
			default:
				return unknownOperands(args)
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if strings.Trim(a[1:], "aAbdeFhilNnoqtVWxX") != "" {
				return unknownOperands(args)
			}
		case strings.Contains(a, "="):
			return unknownOperands(args)
		}
	}
	return effectDraft{}
}
