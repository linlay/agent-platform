package connector

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WithPath adds only this Agent's mounted command directories. It never
// modifies the Platform process environment or adds bin/libs to PATH.
func WithPath(env []string, bins []string) []string {
	if len(bins) == 0 {
		return append([]string(nil), env...)
	}
	result := make([]string, 0, len(env)+1)
	var current string
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok && (key == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(key, "PATH")) {
			current = value
			continue
		}
		result = append(result, item)
	}
	result = append(result, "PATH="+PathValue(current, bins, string(os.PathListSeparator)))
	return result
}

func PathValue(current string, bins []string, separator string) string {
	seen := map[string]bool{}
	var result []string
	for _, value := range append(append([]string(nil), bins...), strings.Split(current, separator)...) {
		if value == "" {
			continue
		}
		key := filepath.Clean(value)
		if separator == ";" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return strings.Join(result, separator)
}

// ContainerEnvironment uses guest mount paths in both policy review and
// execution. Host PATH is never substituted for the container's environment.
func ContainerEnvironment(env map[string]string, hostBins []string) map[string]string {
	if len(hostBins) == 0 {
		return env
	}
	result := make(map[string]string, len(env)+1)
	for key, value := range env {
		result[key] = value
	}
	var bins []string
	for _, bin := range hostBins {
		bins = append(bins, "/connectors/"+filepath.Base(filepath.Dir(bin))+"/bin")
	}
	current := result["PATH"]
	if current == "" {
		current = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	result["PATH"] = PathValue(current, bins, ":")
	return result
}
