package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	projectRootOnce sync.Once
	projectRootDir  string
)

func ProjectFile(relative string) string {
	return filepath.Join(projectRoot(), filepath.Clean(relative))
}

func ConfigFile(relative string) string {
	return configFile("", relative)
}

func configFile(root string, relative string) string {
	return filepath.Join(resolveConfigRoot(root), filepath.Clean(relative))
}

func resolveConfigRoot(root string) string {
	configured := strings.TrimSpace(root)
	if configured == "" {
		return projectRoot()
	}
	if abs, err := filepath.Abs(configured); err == nil {
		return abs
	}
	return filepath.Clean(configured)
}

func projectRoot() string {
	projectRootOnce.Do(func() {
		cwd, err := os.Getwd()
		if err != nil {
			projectRootDir = "."
			return
		}
		dir := cwd
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				projectRootDir = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				projectRootDir = cwd
				return
			}
			dir = parent
		}
	})
	return projectRootDir
}

func stripInlineComment(raw string) string {
	return stripInlineCommentWithDoubleQuoteEscapes(raw, false)
}

func stripInlineCommentWithDoubleQuoteEscapes(raw string, decodeDoubleQuotedEscapes bool) string {
	var out strings.Builder
	inSingle := false
	inDouble := false
	for i, ch := range raw {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && (!decodeDoubleQuotedEscapes || !isBackslashEscaped(raw, i)) {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				if i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t' {
					return out.String()
				}
			}
		}
		out.WriteRune(ch)
	}
	return out.String()
}

func isBackslashEscaped(value string, index int) bool {
	backslashes := 0
	for index--; index >= 0 && value[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func interpolateEnvValue(raw string) string {
	if !strings.HasPrefix(raw, "${") || !strings.HasSuffix(raw, "}") {
		return raw
	}
	body := strings.TrimSuffix(strings.TrimPrefix(raw, "${"), "}")
	envKey, fallback, hasFallback := strings.Cut(body, ":")
	if envValue, ok := os.LookupEnv(strings.TrimSpace(envKey)); ok {
		envValue = strings.TrimSpace(envValue)
		if envValue != "" {
			return envValue
		}
	}
	if hasFallback {
		return strings.TrimSpace(fallback)
	}
	return ""
}
