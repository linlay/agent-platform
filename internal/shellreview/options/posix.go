package options

import (
	"strconv"
	"strings"

	"agent-platform/internal/shellreview/types"
)

func Parse(spec types.OptionSpec, args []string) types.ParsedOptions {
	p := types.ParsedOptions{Flags: map[string]bool{}, Values: map[string][]string{}}
	apply := func(key string, role types.OptionRole, value string) {
		p.Flags[key] = true
		p.Values[key] = append(p.Values[key], value)
		switch role {
		case types.OptionRead:
			p.Files = append(p.Files, types.FileAccess{Path: value})
		case types.OptionWrite:
			p.Files = append(p.Files, types.FileAccess{Path: value, Write: true})
		case types.OptionReadDir:
			p.Files = append(p.Files, types.FileAccess{Path: value, Recursive: true})
		case types.OptionWriteDir:
			p.Files = append(p.Files, types.FileAccess{Path: value, Write: true})
		case types.OptionExec:
			p.Exec = true
		case types.OptionRemote:
			p.Remote = true
		case types.OptionRecursive:
			p.Recursive = true
		}
	}
	end := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if end || a == "-" || !strings.HasPrefix(a, "-") {
			p.Positional = append(p.Positional, a)
			continue
		}
		if a == "--" {
			end = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			name, value, hasValue := strings.Cut(a[2:], "=")
			role, known := spec.Long[name]
			if !known {
				p.Incomplete = true
				if hasValue && strings.ContainsAny(value, "/\\") {
					p.Files = append(p.Files, types.FileAccess{Path: value, Write: true})
				}
				continue
			}
			switch role {
			case types.OptionNone, types.OptionRecursive, types.OptionRemote:
				if hasValue {
					p.Incomplete = true
				}
				apply(name, role, "")
			case types.OptionOptionalValue:
				apply(name, types.OptionValue, value)
			case types.OptionTwoValues, types.OptionNameFile:
				if hasValue || i+2 >= len(args) {
					p.Incomplete = true
					continue
				}
				first, second := args[i+1], args[i+2]
				i += 2
				if role == types.OptionNameFile {
					apply(name, types.OptionRead, second)
				} else {
					apply(name, types.OptionValue, first+"\x00"+second)
				}
			default:
				if !hasValue {
					if i+1 >= len(args) {
						p.Incomplete = true
						continue
					}
					i++
					value = args[i]
				}
				apply(name, role, value)
			}
			continue
		}
		letters := a[1:]
		if spec.NumericShort {
			if _, err := strconv.Atoi(letters); err == nil {
				continue
			}
		}
		for j := 0; j < len(letters); j++ {
			c := letters[j]
			if role, ok := spec.ShortArgs[c]; ok {
				value := letters[j+1:]
				if value == "" {
					if i+1 >= len(args) {
						p.Incomplete = true
						break
					}
					i++
					value = args[i]
				}
				apply(string(c), role, value)
				break
			}
			if strings.IndexByte(spec.ShortFlags, c) >= 0 {
				apply(string(c), types.OptionNone, "")
				continue
			}
			p.Incomplete = true
			break
		}
	}
	return p
}
