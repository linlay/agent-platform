package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSharedToolsTemplatePreservesPlatformDefaults(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			cfg := defaultConfig(LoadOptions{})
			want := defaultBashAllowedCommands(goos)
			cfg.Bash.AllowedCommands = want
			if err := cfg.applyToolsFile(filepath.Join("..", "..", "configs", "tools.example.yml"), false); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Bash.AllowedCommands, want) {
				t.Fatalf("template overrides %s command defaults", goos)
			}
			if goos == "windows" && !reflect.DeepEqual(want, []string{"*"}) {
				t.Fatalf("Windows defaults: %v", want)
			}
			if goos != "windows" {
				for _, command := range []string{"git", "dbx", "httpx", "pdftotext"} {
					found := false
					for _, allowed := range want {
						if allowed == command {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing command %s", command)
					}
				}
			}
			if cfg.Bash.ShellExecutable != "" || len(cfg.Bash.ShellArgs) != 0 {
				t.Fatal("template must retain automatic shell selection")
			}
			level := cfg.AccessPolicy.Levels["full_access"]
			if !reflect.DeepEqual(level.ReadRoots, []string{"@root"}) || !reflect.DeepEqual(level.WriteRoots, []string{"@root"}) {
				t.Fatalf("full_access roots: %#v", level)
			}
			cfg.applyBashValues(map[string]any{"allowed-commands": "git,rg"})
			if !reflect.DeepEqual(cfg.Bash.AllowedCommands, []string{"git", "rg"}) {
				t.Fatal("explicit command override ignored")
			}
		})
	}
}
