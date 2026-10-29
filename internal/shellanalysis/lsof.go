package shellanalysis

import "strings"

// lsofOperands recognizes query selectors and output flags. Its optional
// arguments may be attached (-iTCP:8080) or separate (-i TCP:8080).
// Device-cache operations (-D), directory walks (+d/+D), and unknown options
// retain opaque review rather than treating an unknown operation as read-only.
func lsofOperands(args []string) Effects {
	var e Effects
	end := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" && !end {
			end = true
			continue
		}
		if end || !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
			e.Files = append(e.Files, FileAccess{Path: a})
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			return unknownOperands(args)
		}
		letters := a[1:]
		for j := 0; j < len(letters); j++ {
			switch letters[j] {
			case 'a', 'b', 'h', 'l', 'n', 'P', 'R', 't', 'v', 'w':
			case 'c', 'p', 'u', 'i', 's', 'F':
				if j+1 == len(letters) {
					if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "+") {
						i++
					} else if strings.ContainsRune("cpu", rune(letters[j])) {
						return unknownOperands(args)
					}
				}
				// The rest of this word is the option's value, not more flags.
				j = len(letters)
			default:
				return unknownOperands(args)
			}
		}
	}
	return e
}
