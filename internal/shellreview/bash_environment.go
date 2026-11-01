package shellreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"

	. "agent-platform/internal/accesspolicy"
	shellplan "agent-platform/internal/shellreview/plan"
	"agent-platform/internal/shellreview/types"
)

func executionFingerprint(p shellplan.BashPlan, x types.BashExecution, variables map[string]string, env *types.BashEnvironment) shellplan.BashPlan {
	data, _ := json.Marshal(variables)
	content := ""
	target := x.Script
	if target == "" {
		target = x.Program
	}
	if target != "" {
		target = ResolveAgainstCwd(target, x.Cwd)
		if env != nil && env.Inspect != nil {
			_, content, _ = env.Inspect(target)
		} else {
			if canonical, err := NormalizePath(target); err == nil {
				target = canonical
			}
			if f, err := os.Open(target); err == nil {
				if info, err := f.Stat(); err == nil && info.Mode().IsRegular() {
					hash := sha256.New()
					_, _ = io.Copy(hash, f)
					content = hex.EncodeToString(hash.Sum(nil))
				}
				f.Close()
			}
		}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{p.Fingerprint, p.RuleKey, p.AccessLevel, x.Cwd, x.Identity, target, content, string(data)}, "\x00")))
	p.Fingerprint = hex.EncodeToString(sum[:])
	// Run reuse is limited to this exact invocation/environment/content version,
	// never all programs sharing an interpreter and working directory.
	p.RuleKey = "bash-access:execution:" + p.Fingerprint
	p.Scope, p.ScopeKind, p.ContentSHA256 = target, "invocation_version", content
	return p
}
