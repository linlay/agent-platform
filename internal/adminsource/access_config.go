package adminsource

import (
	"reflect"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type AccessConfigChange struct {
	Changed                bool   `json:"changed"`
	BeforePresent          bool   `json:"beforePresent"`
	AfterPresent           bool   `json:"afterPresent"`
	BeforeDefault          string `json:"beforeDefault,omitempty"`
	AfterDefault           string `json:"afterDefault,omitempty"`
	BeforeEffectiveDefault string `json:"beforeEffectiveDefault"`
	AfterEffectiveDefault  string `json:"afterEffectiveDefault"`
}

func accessConfigChange(before, after string) (*AccessConfigChange, error) {
	read := func(source string) (map[string]any, error) {
		if source == "" {
			return map[string]any{}, nil
		}
		tree, err := config.LoadYAMLTreeBytes([]byte(source))
		if err != nil {
			return nil, err
		}
		node, _ := tree.(map[string]any)
		return node, nil
	}
	b, err := read(before)
	if err != nil {
		return nil, err
	}
	a, err := read(after)
	if err != nil {
		return nil, err
	}
	bc, beforeErr := contracts.ParseAgentAccessConfig(b)
	ac, err := contracts.ParseAgentAccessConfig(a)
	if err != nil {
		return nil, err
	}
	bv, bp := b["accessConfig"]
	av, ap := a["accessConfig"]
	beforeEffective := bc.EffectiveDefault()
	if beforeErr != nil {
		beforeEffective = "invalid"
	}
	return &AccessConfigChange{Changed: bp != ap || !reflect.DeepEqual(bv, av), BeforePresent: bp, AfterPresent: ap,
		BeforeDefault: bc.Default, AfterDefault: ac.Default, BeforeEffectiveDefault: beforeEffective, AfterEffectiveDefault: ac.EffectiveDefault()}, nil
}
