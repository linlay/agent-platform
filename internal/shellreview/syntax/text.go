// Package syntax owns grammar-independent checks on the bytes shown for review.
package syntax

import (
	"unicode"
	"unicode/utf8"
)

const (
	ControlCharacterReason  = "Command contains non-printable control characters that could bypass security checks"
	BidiControlReason       = "Command contains bidirectional override or isolate characters that can reorder displayed text"
	InvalidEncodingReason   = "Command contains invalid UTF-8 that cannot be displayed faithfully"
	UnicodeWhitespaceReason = "Command contains Unicode whitespace characters that could cause parsing inconsistencies"
	InvisibleFormatReason   = "Command contains invisible formatting characters that could cause parsing inconsistencies"
)

// CheckText rejects text that cannot be displayed as it executes, whatever the
// grammar or quoting: control characters, bidirectional overrides/isolates and
// invalid UTF-8. Ordinary spaces, tabs, LF and CRLF remain valid formatting.
func CheckText(command string) string {
	if !utf8.ValidString(command) {
		return InvalidEncodingReason
	}
	for offset, r := range command {
		switch {
		case r == '\r' && offset+1 < len(command) && command[offset+1] == '\n':
			continue
		case r == '\t' || r == '\n':
			continue
		case unicode.IsControl(r):
			return ControlCharacterReason
		case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
			return BidiControlReason
		}
	}
	return ""
}

// CheckHidden reports non-ASCII whitespace and invisible formatting characters.
// They are legitimate inside quoted data (emoji joiners, CJK spaces, RTL marks),
// so a grammar-aware caller passes only unquoted text; a caller that cannot tell
// quoting apart passes everything.
func CheckHidden(text string) string {
	for _, r := range text {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
		case unicode.IsSpace(r):
			return UnicodeWhitespaceReason
		case unicode.Is(unicode.Cf, r) || r == '\u034f':
			return InvisibleFormatReason
		}
	}
	return ""
}

// CheckAll applies both checks to text without quoting: commands of an
// unsupported dialect and configured shell paths/arguments.
func CheckAll(text string) string {
	if reason := CheckText(text); reason != "" {
		return reason
	}
	return CheckHidden(text)
}

func IsTextRejection(reason string) bool {
	switch reason {
	case ControlCharacterReason, BidiControlReason, InvalidEncodingReason, UnicodeWhitespaceReason, InvisibleFormatReason:
		return true
	}
	return false
}
