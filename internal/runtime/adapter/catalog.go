package adapter

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

type Catalog struct{ catalog.Registry }

func (c Catalog) SkillKeys() []string {
	var keys []string
	if c.Registry != nil {
		for _, s := range c.Skills("") {
			keys = append(keys, s.Key)
		}
	}
	return keys
}
func (c Catalog) AgentDigests() []contracts.AgentDigest {
	var out []contracts.AgentDigest
	if c.Registry != nil {
		for _, a := range c.Agents("") {
			out = append(out, contracts.AgentDigest{Key: a.Key, Name: a.Name, Role: a.Role, Description: a.Description, Mode: a.Mode})
		}
	}
	return out
}
