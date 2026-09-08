package hitl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectorViewRuleRequiresExplicitFormMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "forms.yml")
	definition := "key: forms\ncommands:\n  - command: demo\n    subcommands:\n      - match: update\n        level: 1\n        mode: form\n        view:\n          connectorId: crm\n          key: edit\n"
	if err := os.WriteFile(path, []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	rules, err := loadRulesFromDir(root)
	if err != nil || len(rules) != 1 || rules[0].View == nil || rules[0].EffectiveMode() != "form" || rules[0].ViewportType != "" {
		t.Fatalf("rules: %#v %v", rules, err)
	}
	for _, invalid := range []string{
		strings.Replace(definition, "mode: form", "mode: approval", 1),
		strings.Replace(definition, "        mode: form\n", "", 1),
		strings.Replace(definition, "key: edit", "key: ../edit", 1),
		strings.Replace(definition, "        view:", "        viewportType: html\n        view:", 1),
	} {
		if err := os.WriteFile(path, []byte(invalid), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRulesFromDir(root); err == nil {
			t.Fatalf("invalid rule accepted: %s", invalid)
		}
	}
}
