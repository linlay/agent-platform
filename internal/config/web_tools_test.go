package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadWebToolsFixture(t *testing.T, body string) (Config, error) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "configs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "tools.yml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var cfg Config
	var err error
	withIsolatedEnv(t, nil, func() {
		cfg, err = Load(LoadOptions{ConfigDir: root})
	})
	return cfg, err
}

func TestLoadWebSearchDefaultsDisabledWithoutProvider(t *testing.T) {
	cfg, err := loadWebToolsFixture(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch.Enabled || cfg.WebSearch.ProviderKey != "" || cfg.WebSearch.Timeout != 60 || cfg.WebSearch.MaxResponseBytes != 10*1024*1024 {
		t.Fatalf("unexpected web search defaults: %#v", cfg.WebSearch)
	}
}

func TestLoadWebSearchExplicitProviderAndLimits(t *testing.T) {
	cfg, err := loadWebToolsFixture(t, `web-search:
  enabled: true
  provider-key: ' deepseek '
  timeout: 17
  max-response-bytes: 32768
`)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WebSearch.Enabled || cfg.WebSearch.ProviderKey != "deepseek" || cfg.WebSearch.Timeout != 17 || cfg.WebSearch.MaxResponseBytes != 32768 {
		t.Fatalf("web search configuration was not preserved: %#v", cfg.WebSearch)
	}
}

func TestLoadWebSearchDoesNotInferProviderWhenEnabled(t *testing.T) {
	cfg, err := loadWebToolsFixture(t, "web-search:\n  enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WebSearch.Enabled || cfg.WebSearch.ProviderKey != "" || cfg.WebSearch.Timeout != 60 || cfg.WebSearch.MaxResponseBytes != 10*1024*1024 {
		t.Fatalf("search must keep an unspecified provider empty: %#v", cfg.WebSearch)
	}
}

func TestLoadWebFetchBackendDefaultsPreserveExistingConfiguration(t *testing.T) {
	cfg, err := loadWebToolsFixture(t, `web-fetch:
  enabled: true
  default-profile: general
  preapproved-hosts: [Example.com]
  profiles:
    general:
      model-key: summary-model
      system-prompt: ' extract relevant content '
    deepseek:
      backend: ' DeepSeek '
      provider-key: ' deepseek '
    deepseek-custom:
      backend: deepseek
      provider-key: external-search
      model-key: summary-model
      timeout: 13
      fetch-timeout: 19
      max-url-length: 4096
      max-response-bytes: 131072
      max-markdown-chars: 20000
      max-output-tokens: 1000
`)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WebFetch.Enabled || cfg.WebFetch.DefaultProfile != "general" || len(cfg.WebFetch.PreapprovedHosts) != 1 || cfg.WebFetch.PreapprovedHosts[0] != "example.com" {
		t.Fatalf("existing fetch settings changed: %#v", cfg.WebFetch)
	}
	general := cfg.WebFetch.Profiles["general"]
	if general.Backend != "direct" || general.ProviderKey != "" || general.ModelKey != "summary-model" || general.Timeout != 60 || general.FetchTimeout != 60 || general.MaxURLLength != 2000 || general.MaxResponseBytes != 10*1024*1024 || general.MaxMarkdownChars != 100000 || general.MaxOutputTokens != 4000 || general.SystemPrompt != "extract relevant content" {
		t.Fatalf("legacy fetch profile must retain direct defaults: %#v", general)
	}
	deepseek := cfg.WebFetch.Profiles["deepseek"]
	if deepseek.Backend != "deepseek" || deepseek.ProviderKey != "deepseek" || deepseek.ModelKey != "" || deepseek.MaxURLLength != 8192 || deepseek.Timeout != 60 || deepseek.FetchTimeout != 60 || deepseek.MaxResponseBytes != 10*1024*1024 {
		t.Fatalf("unexpected DeepSeek fetch defaults: %#v", deepseek)
	}
	custom := cfg.WebFetch.Profiles["deepseek-custom"]
	if custom.Backend != "deepseek" || custom.ProviderKey != "external-search" || custom.ModelKey != "summary-model" || custom.Timeout != 13 || custom.FetchTimeout != 19 || custom.MaxURLLength != 4096 || custom.MaxResponseBytes != 131072 || custom.MaxMarkdownChars != 20000 || custom.MaxOutputTokens != 1000 {
		t.Fatalf("explicit DeepSeek limits were not preserved: %#v", custom)
	}
}

func TestLoadWebToolsRejectInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"search section type", "web-search: []\n", "tools.web-search must be an object"},
		{"unknown search field", "web-search:\n  api-key: example\n", "tools.web-search.api-key"},
		{"search enable type", "web-search:\n  enabled: yes\n", "tools.web-search.enabled must be boolean"},
		{"search provider type", "web-search:\n  provider-key: [deepseek]\n", "tools.web-search.provider-key must be a string"},
		{"search timeout type", "web-search:\n  timeout: '60'\n", "tools.web-search.timeout must be a positive integer"},
		{"search timeout zero", "web-search:\n  timeout: 0\n", "tools.web-search.timeout must be a positive integer"},
		{"search timeout negative", "web-search:\n  timeout: -1\n", "tools.web-search.timeout must be a positive integer"},
		{"search response bytes type", "web-search:\n  max-response-bytes: true\n", "tools.web-search.max-response-bytes must be a positive integer"},
		{"search response bytes zero", "web-search:\n  max-response-bytes: 0\n", "tools.web-search.max-response-bytes must be a positive integer"},
		{"search response bytes negative", "web-search:\n  max-response-bytes: -10\n", "tools.web-search.max-response-bytes must be a positive integer"},
		{"unknown fetch field", "web-fetch:\n  profiles:\n    general:\n      provider: deepseek\n", "tools.web-fetch.profiles.general.provider"},
		{"fetch provider type", "web-fetch:\n  profiles:\n    general:\n      provider-key: true\n", "tools.web-fetch.profiles.general.provider-key must be a string"},
		{"fetch backend type", "web-fetch:\n  profiles:\n    general:\n      backend: true\n", "tools.web-fetch.profiles.general.backend must be a string"},
		{"unsupported fetch backend", "web-fetch:\n  profiles:\n    general:\n      backend: unknown\n", "tools.web-fetch.profiles.general.backend must be direct or deepseek"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadWebToolsFixture(t, tt.body)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestWebToolsDistributionExamples(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "configs", "tools.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadWebToolsFixture(t, string(body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebSearch.Enabled || cfg.WebSearch.ProviderKey != "deepseek" {
		t.Fatalf("search example must remain opt-in with an explicit provider: %#v", cfg.WebSearch)
	}
	if cfg.WebFetch.DefaultProfile != "general" || cfg.WebFetch.Profiles["general"].Backend != "direct" || cfg.WebFetch.Profiles["general"].MaxURLLength != 2000 {
		t.Fatalf("the shipped default must retain direct fetching: %#v", cfg.WebFetch)
	}
	if profile := cfg.WebFetch.Profiles["deepseek"]; profile.Backend != "deepseek" || profile.ProviderKey != "deepseek" || profile.MaxURLLength != 8192 {
		t.Fatalf("missing explicit DeepSeek profile: %#v", profile)
	}
	if err := cfg.applyAgentSettingsFile(filepath.Join("..", "..", "configs", "agent-settings.example.yml")); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"web_search", "web_fetch"} {
		found := false
		for _, preset := range cfg.PresetTools {
			found = found || preset == tool
		}
		if !found {
			t.Fatalf("agent settings example is missing %s", tool)
		}
	}
}
