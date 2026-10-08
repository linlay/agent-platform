package adapter

import (
	"agent-platform/internal/catalog"
)

type Catalog struct{ catalog.Registry }

func (c Catalog) SkillIDs() []string {
	var keys []string
	if c.Registry != nil {
		for _, s := range c.Skills("") {
			keys = append(keys, s.ID)
		}
	}
	return keys
}
