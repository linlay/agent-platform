package accesspolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// BashEnvironment inspects the actual execution filesystem. A missing/failed
// container resolver is opaque; it must never fall back to the host PATH.
type BashEnvironment struct {
	Resolve   func(name, cwd string, env map[string]string) (string, error)
	Canonical func(path, cwd string) (string, error)
	Directory func(path string) (string, error)
	Inspect   func(path string) (header, sha256 string, err error)
}

var ErrBashTemporaryEscape = errors.New("container temporary path escapes /tmp through a symlink")

func executionFingerprint(p BashPlan, x BashExecution, variables map[string]string, env *BashEnvironment) BashPlan {
	data, _ := json.Marshal(variables)
	content := ""
	target := x.Script
	if target == "" {
		target = x.Program
	}
	if target != "" {
		target = resolveAgainstCwd(target, x.Cwd)
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
	return p
}
