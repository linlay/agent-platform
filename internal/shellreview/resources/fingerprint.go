package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"

	. "agent-platform/internal/accesspolicy"
	"agent-platform/internal/shellreview/types"
)

// ExecutionFingerprint binds the invocation to evidence from its execution environment.
// Missing container probes leave content unknown; guest names never inspect Host files.
func ExecutionFingerprint(sandbox bool, p ExecutionVersion, x types.BashExecution, variables map[string]string, env *types.BashEnvironment) ExecutionVersion {
	effective := variables
	if x.Env != nil {
		effective = x.Env
	}
	data, _ := json.Marshal(effective)
	content := ""
	target := x.Script
	if target == "" {
		target = x.Program
	}
	if target != "" {
		target = ResolveAgainstCwd(target, x.Cwd)
		if env != nil && env.Inspect != nil {
			_, content, _ = env.Inspect(target)
		} else if !sandbox {
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
	fields := []string{p.Fingerprint, p.RuleKey, p.AccessLevel, x.Cwd, x.Identity, target, content, string(data)}
	if x.DependencySHA256 != "" {
		fields = append(fields, x.DependencySHA256)
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	p.Fingerprint = hex.EncodeToString(sum[:])
	// Run reuse is limited to this exact invocation/environment/content version,
	// never all programs sharing an interpreter and working directory.
	p.RuleKey = "bash-access:execution:" + p.Fingerprint
	p.Scope, p.ScopeKind, p.ContentSHA256 = target, "invocation_version", content
	return p
}

// ExecutionVersion contains only the inputs and outputs of execution-version binding.
type ExecutionVersion struct {
	Fingerprint   string
	RuleKey       string
	AccessLevel   string
	Scope         string
	ScopeKind     string
	ContentSHA256 string
}
