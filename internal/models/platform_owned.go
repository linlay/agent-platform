package models

import (
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
)

// platformOwnedRequestFields are derived per call from the Chat identity
// (session affinity); a static compat value would pin every Chat to one route.
var platformOwnedRequestFields = map[string][]string{
	"OPENAI_RESPONSES": {"prompt_cache_key"},
	"ANTHROPIC":        {"metadata"},
}

// ValidatePlatformOwnedCompat rejects compat.request overrides of fields the
// Platform owns for the given protocol.
func ValidatePlatformOwnedCompat(protocol string, rawCompat any) error {
	fields := platformOwnedRequestFields[strings.ToUpper(strings.TrimSpace(protocol))]
	if len(fields) == 0 {
		return nil
	}
	request := contracts.AnyMapNode(contracts.AnyMapNode(rawCompat)["request"])
	for _, branch := range []string{"always", "whenReasoningEnabled"} {
		overrides := contracts.AnyMapNode(request[branch])
		for _, field := range fields {
			if _, exists := overrides[field]; exists {
				return fmt.Errorf("compat.request.%s.%s is set by the platform for %s; remove it", branch, field, strings.ToUpper(strings.TrimSpace(protocol)))
			}
		}
	}
	return nil
}

// ParseRequestIDHeader validates the optional provider requestIdHeader: one
// HTTP header name whose value identifies the request at a relay.
func ParseRequestIDHeader(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("requestIdHeader must be a string")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	for _, r := range value {
		if !isHeaderTokenRune(r) {
			return "", fmt.Errorf("requestIdHeader %q is not a valid HTTP header name", value)
		}
	}
	return value, nil
}

func isHeaderTokenRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}
