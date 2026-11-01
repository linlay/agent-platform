package unwrap

import (
	"agent-platform/internal/shellreview/registry"
	"strconv"
	"strings"
)

// InspectionWrapper finds candidates solely for hard blocks. It cannot prove
// executable identity, and deliberately inspects through options that execution
// analysis rejects. Its result must never grant command or filesystem access.
func InspectionWrapper(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	id := registry.IDOf(CommandName(argv[0]))
	if id == registry.CmdEnv {
		expanded, ok := Env(argv[1:])
		return expanded.Command, ok && len(expanded.Command) > 0
	}
	index, terminal, ok := wrapperIndex(id, argv[1:], true, false)
	if !ok || terminal {
		return nil, false
	}
	return argv[1+index:], true
}

// wrapperIndex is the shared non-env wrapper grammar. Inspection intentionally
// overapproximates candidates; execution accepts only understood options. The
// distinction preserves hard blocks without treating inspection as an identity
// proof, or guessing the PATH selected by command -p.
func wrapperIndex(id registry.ID, args []string, inspect bool, durationAfterDash bool) (index int, terminal, ok bool) {
	switch id {
	case registry.CmdCommand, registry.CmdBuiltin, registry.CmdNohup, registry.CmdNice, registry.CmdTimeout, registry.CmdStdbuf:
	default:
		return 0, false, false
	}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			if inspect && (id == registry.CmdCommand || id == registry.CmdBuiltin) {
				continue
			}
			break
		}
		switch id {
		case registry.CmdCommand, registry.CmdBuiltin:
			if !inspect && (a == "-v" || a == "-V") {
				return 0, true, true
			}
			if !inspect && a == "-p" {
				return 0, false, false
			}
			if inspect && strings.HasPrefix(a, "-") {
				i++
				continue
			}
		case registry.CmdNice:
			if a == "-n" || a == "--adjustment" {
				if i+1 >= len(args) {
					return 0, false, false
				}
				i += 2
				continue
			}
			if inspect && (strings.HasPrefix(a, "-n") || strings.HasPrefix(a, "--adjustment=")) {
				i++
				continue
			}
			if strings.HasPrefix(a, "-") {
				_, err := strconv.Atoi(a)
				numeric := err == nil
				if inspect {
					v := strings.TrimLeft(a, "-+")
					numeric = v != ""
					for _, c := range v {
						numeric = numeric && c >= '0' && c <= '9'
					}
				}
				if numeric {
					i++
					continue
				}
			}
		case registry.CmdTimeout:
			if a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after" {
				if i+1 >= len(args) {
					return 0, false, false
				}
				i += 2
				continue
			}
			if a == "--foreground" || a == "--preserve-status" {
				i++
				continue
			}
			if inspect && strings.HasPrefix(a, "-") {
				i++
				continue
			}
			if !strings.HasPrefix(a, "-") {
				i++
				return i, false, i < len(args)
			}
		case registry.CmdStdbuf:
			if a == "-i" || a == "-o" || a == "-e" {
				if i+1 >= len(args) {
					return 0, false, false
				}
				i += 2
				continue
			}
			if len(a) > 2 && (strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "-e")) {
				i++
				continue
			}
		}
		if strings.HasPrefix(a, "-") {
			return 0, false, false
		}
		break
	}
	// timeout's first non-option is always DURATION, including after --.
	if id == registry.CmdTimeout {
		if !inspect {
			return 0, false, false
		}
		if durationAfterDash {
			i++
		}
	}
	return i, false, i < len(args)
}

// InspectionWrappers preserves the historical candidate after timeout -- and
// also inspects the actual command after DURATION. Neither candidate grants trust.
func InspectionWrappers(argv []string) [][]string {
	first, ok := InspectionWrapper(argv)
	if !ok {
		return nil
	}
	out := [][]string{first}
	if registry.IDOf(CommandName(argv[0])) == registry.CmdTimeout {
		primary, _, _ := wrapperIndex(registry.CmdTimeout, argv[1:], true, false)
		alternate, terminal, ok := wrapperIndex(registry.CmdTimeout, argv[1:], true, true)
		if ok && !terminal && alternate != primary {
			out = append(out, argv[1+alternate:])
		}
	}
	return out
}
