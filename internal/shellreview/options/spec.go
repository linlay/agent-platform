package options

import (
	"agent-platform/internal/shellreview/types"
)

func Longs(role types.OptionRole, names ...string) map[string]types.OptionRole {
	m := make(map[string]types.OptionRole, len(names))
	for _, name := range names {
		m[name] = role
	}
	return m
}

func Merge(maps ...map[string]types.OptionRole) map[string]types.OptionRole {
	out := map[string]types.OptionRole{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
