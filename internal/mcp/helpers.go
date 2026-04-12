package mcp

import "strings"

func normalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
