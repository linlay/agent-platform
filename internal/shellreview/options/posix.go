package options

import (
	"strconv"
	"strings"

	"agent-platform/internal/shellreview/types"
)

func Parse(spec types.OptionSpec, args []string) types.ParsedOptions {
	p := types.ParsedOptions{Flags: map[string]bool{}, Values: map[string][]string{}, ValueRefs: map[string][]types.ValueRef{}}
	apply := func(key string, role types.OptionRole, value string, ref types.ValueRef) {
		p.Flags[key] = true
		p.Values[key] = append(p.Values[key], value)
		p.ValueRefs[key] = append(p.ValueRefs[key], ref)
		switch role {
		case types.OptionRead:
			p.Files = append(p.Files, types.FileAccess{Ref: ref, Path: value, Kind: types.Read})
		case types.OptionWrite:
			p.Files = append(p.Files, types.FileAccess{Ref: ref, Path: value, Write: true, Kind: types.Output})
		case types.OptionReadDir:
			p.Files = append(p.Files, types.FileAccess{Ref: ref, Path: value, Recursive: true, Kind: types.Read})
		case types.OptionWriteDir:
			p.Files = append(p.Files, types.FileAccess{Ref: ref, Path: value, Write: true, Recursive: true, Kind: types.Create})
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
			p.PositionalRefs = append(p.PositionalRefs, types.ArgumentRef(i, a))
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
					p.Files = append(p.Files, types.FileAccess{Ref: types.ValueRef{Index: i, Start: len(name) + 3, End: len(a), Known: true}, Path: value, Write: true})
				}
				continue
			}
			switch role {
			case types.OptionNone, types.OptionRecursive, types.OptionRemote:
				if hasValue {
					p.Incomplete = true
				}
				apply(name, role, "", types.ValueRef{})
			case types.OptionOptionalValue:
				apply(name, types.OptionValue, value, types.ValueRef{Index: i, Start: len(name) + 3, End: len(a), Known: hasValue})
			case types.OptionTwoValues, types.OptionNameFile:
				if hasValue || i+2 >= len(args) {
					p.Incomplete = true
					continue
				}
				first, second := args[i+1], args[i+2]
				i += 2
				if role == types.OptionNameFile {
					apply(name, types.OptionRead, second, types.ArgumentRef(i, second))
				} else {
					apply(name, types.OptionValue, first+"\x00"+second, types.ValueRef{})
				}
			default:
				ref := types.ValueRef{Index: i, Start: len(name) + 3, End: len(a), Known: true}
				if !hasValue {
					if i+1 >= len(args) {
						p.Incomplete = true
						continue
					}
					i++
					value = args[i]
					ref = types.ArgumentRef(i, value)
				}
				apply(name, role, value, ref)
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
				ref := types.ValueRef{Index: i, Start: j + 2, End: len(a), Known: true}
				if value == "" {
					if i+1 >= len(args) {
						p.Incomplete = true
						break
					}
					i++
					value = args[i]
					ref = types.ArgumentRef(i, value)
				}
				apply(string(c), role, value, ref)
				break
			}
			if strings.IndexByte(spec.ShortFlags, c) >= 0 {
				apply(string(c), types.OptionNone, "", types.ValueRef{})
				continue
			}
			p.Incomplete = true
			break
		}
	}
	return p
}
