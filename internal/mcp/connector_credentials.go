package mcp

import (
	"fmt"
	"strings"

	"agent-platform/internal/connector"
)

func resolveConnectorCredential(pkg connector.Package, value string, credentials map[string]string) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	var missing bool
	resolved := osExpand(value, func(key string) string {
		value, ok := credentials[key]
		if !ok {
			missing = true
		}
		return value
	})
	if missing {
		return "", fmt.Errorf("connector %s credential is not configured", pkg.ID)
	}
	return resolved, nil
}

// Only ${KEY} is substituted once; shell expressions and recursive expansion
// are not part of the package format.
func osExpand(value string, lookup func(string) string) string {
	var out strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			out.WriteString(value)
			break
		}
		out.WriteString(value[:start])
		value = value[start+2:]
		end := strings.IndexByte(value, '}')
		if end < 0 {
			out.WriteString("${" + value)
			break
		}
		out.WriteString(lookup(value[:end]))
		value = value[end+1:]
	}
	return out.String()
}
