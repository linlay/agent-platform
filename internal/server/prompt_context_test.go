package server

import (
	"path/filepath"
	"testing"

	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/config"
)

func TestResolveSandboxPathsIncludesDefaultOwnerAndMemoryDirs(t *testing.T) {
	cfg := config.Config{
		Paths: config.PathsConfig{
			RootDir:         filepath.Join("runtime", "root"),
			OwnerDir:        filepath.Join("runtime", "owner"),
			MemoryDir:       filepath.Join("runtime", "memory"),
			PanDir:          filepath.Join("runtime", "pan"),
			SkillsMarketDir: filepath.Join("runtime", "skills-market"),
		},
		ContainerHub: config.ContainerHubConfig{
			DefaultSandboxLevel: "run",
		},
	}
	def := catalog.AgentDefinition{
		Key:      "demo-agent",
		AgentDir: filepath.Join("runtime", "agents", "demo-agent"),
		Sandbox: map[string]any{
			"level": "run",
		},
	}

	paths := resolveSandboxPaths(cfg, def, "chat-1")
	if paths.AgentDir != "/agent" {
		t.Fatalf("expected sandbox agent dir, got %#v", paths)
	}
	if paths.OwnerDir != "/owner" {
		t.Fatalf("expected default owner dir, got %#v", paths)
	}
	if paths.MemoryDir != "/memory" {
		t.Fatalf("expected default memory dir, got %#v", paths)
	}
}
