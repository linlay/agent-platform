package accesspolicy

import "strings"

// RuleReusable is shared by card generation and grant registration. Unknown
// shell structure is approved only for one invocation, never a run-wide class.
func RuleReusable(rule string) bool {
	return rule != "" && !strings.HasPrefix(rule, "bash-access:complex") && !strings.HasPrefix(rule, "bashast:too_complex") && !strings.HasPrefix(rule, "bash-access:remote")
}
