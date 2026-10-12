package contracts

import (
	"fmt"
	"sync"

	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/skillsexec"
)

const MaxReferencedSkills = 32
const MaxReferencedKbases = 8
const MaxReferencedChats = 32

// ReferenceGrant contains only trusted, prepared state. Preparation does not
// mutate the live Run. Script byte snapshots are prepared outside control locks.
type ReferenceGrant struct {
	References []queryinput.Reference
	Roots      RunAccessRoots
	Scripts    map[string]*skillsexec.Scope
}

// RunReferences is shared by all copies of one execution Session. A child Run
// receives its own scope; this state is never inferred from prompt text.
type RunReferences struct {
	mu      sync.RWMutex
	refs    []queryinput.Reference
	roots   RunAccessRoots
	scripts *skillsexec.Scope
}

func NewRunReferences(scripts *skillsexec.Scope) *RunReferences {
	return &RunReferences{scripts: scripts}
}

func (s *RunReferences) Commit(grant ReferenceGrant) error {
	if s == nil {
		return fmt.Errorf("Run reference state unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := append(cloneReferences(s.refs), grant.References...)
	refs, err := queryinput.NormalizeResourceReferences(refs)
	if err != nil {
		return err
	}
	for kind, limit := range map[string]int{"skill": MaxReferencedSkills, "kbase": MaxReferencedKbases, "chat": MaxReferencedChats} {
		if len(queryinput.ResourceIDs(refs, kind)) > limit {
			return fmt.Errorf("Run accepts at most %d %s references", limit, kind)
		}
	}
	newSkills := map[string]bool{}
	for _, id := range queryinput.ResourceIDs(s.refs, "skill") {
		newSkills[id] = true
	}
	for _, id := range queryinput.ResourceIDs(grant.References, "skill") {
		if !newSkills[id] && s.scripts != nil {
			s.scripts.Merge(grant.Scripts[id])
		}
	}
	s.refs = cloneReferences(refs)
	s.roots.ReadRoots = uniqueStrings(append(s.roots.ReadRoots, grant.Roots.ReadRoots...))
	s.roots.ReadonlyRoots = uniqueStrings(append(s.roots.ReadonlyRoots, grant.Roots.ReadonlyRoots...))
	return nil
}

func (s *RunReferences) References() []queryinput.Reference {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneReferences(s.refs)
}
func (s *RunReferences) AccessRoots() RunAccessRoots {
	if s == nil {
		return RunAccessRoots{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return RunAccessRoots{ReadRoots: append([]string(nil), s.roots.ReadRoots...), ReadonlyRoots: append([]string(nil), s.roots.ReadonlyRoots...)}
}
func (s *RunReferences) CanReadChat(id string) bool {
	for _, chat := range queryinput.ResourceIDs(s.References(), "chat") {
		if chat == id {
			return true
		}
	}
	return false
}
func (s QuerySession) ReferenceAccessRoots() RunAccessRoots {
	roots := s.RunAccessRoots
	added := s.ReferencesScope.AccessRoots()
	return RunAccessRoots{ReadRoots: uniqueStrings(append(append([]string(nil), roots.ReadRoots...), added.ReadRoots...)), ReadonlyRoots: uniqueStrings(append(append([]string(nil), roots.ReadonlyRoots...), added.ReadonlyRoots...))}
}

func cloneReferences(refs []queryinput.Reference) []queryinput.Reference {
	out := append([]queryinput.Reference(nil), refs...)
	for i := range out {
		out[i].Meta = CloneMap(out[i].Meta)
		if out[i].SizeBytes != nil {
			n := *out[i].SizeBytes
			out[i].SizeBytes = &n
		}
	}
	return out
}
func uniqueStrings(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
