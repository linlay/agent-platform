package config

import (
	"strings"
	"testing"
)

func TestRuntimePromptConfig(t *testing.T) {
	var cfg Config
	err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "shared:\n  runtime:\n    default-locale: en-US\n    environment-prompt-template: 'Environment {{os}} {{ arch }} {{timezone}} {{locale}}'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Prompts.Runtime.Render("linux", "arm64", "UTC", ""); got != "Environment linux arm64 UTC en" {
		t.Fatal(got)
	}
	if got := cfg.Prompts.Runtime.ResolveLocale("zh"); got != "zh-CN" {
		t.Fatal(got)
	}
	if got := (RuntimePromptConfig{}).Render("linux", "arm64", "UTC", ""); !strings.Contains(got, "language: zh-CN") {
		t.Fatal(got)
	}
}

func TestRuntimePromptRejectsInvalidConfig(t *testing.T) {
	for _, body := range []string{
		"default-locale: ''", "default-locale: null", "default-locale: 1", "default-locale: fr",
		"environment-prompt-template: ' '", "environment-prompt-template: null",
		"environment-prompt-template: '{{unknown}}'", "environment-prompt-template: '{{locale}'",
		"environment-prompt-template: '${locale}'", "environment-prompt-template: '{{locale}} }'",
	} {
		t.Run(body, func(t *testing.T) {
			var cfg Config
			if err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "shared:\n  runtime:\n    "+body+"\n")); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	var cfg Config
	if err := cfg.applyAgentPromptFile(configFixture(t, "agent-prompt.yml", "shared: [\n")); err == nil {
		t.Fatal("invalid YAML accepted")
	}
}
