package knowledge

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type matcher struct {
	pattern string
	re      *regexp.Regexp
}

func compileMatchers(patterns []string) []matcher {
	out := []matcher{}
	seen := map[string]struct{}{}
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, matcher{pattern: pattern, re: regexp.MustCompile(globToRegexp(pattern))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pattern < out[j].pattern })
	return out
}

func matchesAny(matchers []matcher, path string) bool {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	for _, matcher := range matchers {
		if matcher.re.MatchString(path) {
			return true
		}
	}
	return false
}

func globToRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if ch == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
				continue
			}
			b.WriteString(`[^/]*`)
			continue
		}
		if ch == '?' {
			b.WriteString(`[^/]`)
			continue
		}
		b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
	}
	b.WriteString("$")
	return b.String()
}
