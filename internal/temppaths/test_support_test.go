package temppaths

import "agent-platform/internal/pathutil"

func (r Resolver) Roots() []pathutil.Canonical {
	return append([]pathutil.Canonical(nil), r.roots...)
}
