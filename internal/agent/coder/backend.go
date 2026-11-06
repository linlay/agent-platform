package coder

import "strings"

func IsMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), Mode)
}
