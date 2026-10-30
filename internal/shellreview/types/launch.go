// Package types defines facts shared by shell launch and review layers.
package types

import "agent-platform/internal/shellreview/dialect"

// LaunchBinding contains no environment values or command text. Fingerprints
// bind approvals to the actual shell bytes, arguments, environment and cwd.
type LaunchBinding struct {
	Executable string
	// Template contains the configured startup arguments with {{command}}, not
	// environment values or credential injections.
	Template        []string
	Dialect         dialect.Dialect
	SyntaxSupported bool
	Reason          string
	// RuleFingerprint excludes cwd/environment; the rule's own target and
	// invocation constraints still apply. Exact grants use Fingerprint instead.
	RuleFingerprint     string
	ContractFingerprint string
	Fingerprint         string
}
