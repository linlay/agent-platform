package config

import "testing"

func TestGitBashConfigurationIsExplicitOptIn(t *testing.T) {
	var c Config
	c.applyBashValues(map[string]any{"shell-executable": "custom.exe"})
	if c.Bash.GitBash.Enabled {
		t.Fatal("absent flag changed old configuration")
	}
	for _, enabled := range []bool{true, false} {
		values := map[string]any{"git-bash": map[string]any{"enabled": enabled}}
		if err := validateGitBashValues(values); err != nil {
			t.Fatal(err)
		}
		c.applyBashValues(values)
		if c.Bash.GitBash.Enabled != enabled || c.Bash.ShellExecutable != "custom.exe" {
			t.Fatal("switch destroyed legacy shell configuration")
		}
	}
}

func TestGitBashConfigurationRejectsInvalidValues(t *testing.T) {
	for _, raw := range []any{true, nil, "true", map[string]any{"enabled": "true"}, map[string]any{"runtime-root": "untrusted"}, map[string]any{"enable": true}} {
		if err := validateGitBashValues(map[string]any{"git-bash": raw}); err == nil {
			t.Fatalf("accepted %v", raw)
		}
	}
}
