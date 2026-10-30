// Package syntax owns grammar-independent checks on the bytes shown for review.
package syntax

import (
	"unicode"
	"unicode/utf8"
)

const (
	ControlCharacterReason  = "Command contains non-printable control characters that could bypass security checks"
	UnicodeWhitespaceReason = "Command contains Unicode whitespace characters that could cause parsing inconsistencies"
	InvisibleFormatReason   = "Command contains invisible formatting or bidirectional control characters"
	InvalidEncodingReason   = "Command contains invalid UTF-8 that cannot be displayed faithfully"
)

// CheckText does not interpret quoting, heredocs or a shell grammar. A granted
// connector or an unknown dialect must not hide bytes from the person reviewing
// it. Ordinary spaces, tabs, LF and CRLF remain valid command formatting.
func CheckText(command string) string {
	if !utf8.ValidString(command) {
		return InvalidEncodingReason
	}
	for offset, r := range command {
		switch {
		case r == '\r' && offset+1 < len(command) && command[offset+1] == '\n':
			continue
		case r == '\t' || r == '\n' || r == ' ':
			continue
		case unicode.IsControl(r):
			return ControlCharacterReason
		case unicode.IsSpace(r):
			return UnicodeWhitespaceReason
		case unicode.Is(unicode.Cf, r) || r == '\u034f':
			return InvisibleFormatReason
		}
	}
	return ""
}

func IsTextRejection(reason string) bool {
	switch reason {
	case ControlCharacterReason, UnicodeWhitespaceReason, InvisibleFormatReason, InvalidEncodingReason:
		return true
	}
	return false
}
