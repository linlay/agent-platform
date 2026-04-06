package config

import (
	"bufio"
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

func LoadTopLevelYAML(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := cleanConfigValue(parts[1])
		if key != "" {
			result[key] = interpolateEnvValue(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
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

func cleanConfigValue(raw string) string {
	value := stripInlineComment(raw)
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return value
}

func stripInlineComment(raw string) string {
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
			if !inSingle {
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
