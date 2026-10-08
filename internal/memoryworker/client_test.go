package memoryworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("MEMX_CLIENT_TEST_PROCESS") == "1" {
		if os.Getenv("MEMX_CONFIG_FILE") != filepath.Join(os.Getenv("MEMX_EXPECT_CONFIG_DIR"), "models.yml") {
			os.Exit(84)
		}
		if os.Getenv("MEMX_CONFIG_DIR") != os.Getenv("MEMX_EXPECT_CONFIG_DIR") {
			os.Exit(81)
		}
		for _, arg := range os.Args[1:] {
			if strings.HasPrefix(arg, "--config-dir") {
				os.Exit(82)
			}
		}
		id := ""
		version := "0.4.1"
		if v := os.Getenv("MEMX_TEST_VERSION"); v != "" {
			version = v
		}
		data := map[string]any{"version": version, "maintenanceVersion": 2, "configDirEnv": os.Getenv("MEMX_TEST_UNSUPPORTED") != "1"}
		if len(os.Args) > 1 && os.Args[1] == "config" {
			os.WriteFile(filepath.Join(os.Getenv("MEMX_CONFIG_DIR"), "set-called"), []byte("set"), 0600)
		} else {
			var req struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(os.Stdin).Decode(&req) != nil {
				os.Exit(83)
			}
			id = req.ID
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"schemaVersion": 1, "id": id, "ok": true, "data": data})
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestEverySubprocessReceivesInstanceConfiguration(t *testing.T) {
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	t.Setenv("MEMX_CLIENT_TEST_PROCESS", "1")
	t.Setenv("MEMX_EXPECT_CONFIG_DIR", dir)
	t.Setenv("MEMX_CONFIG_DIR", filepath.Join(t.TempDir(), "inherited-other-instance"))
	t.Setenv("MEMX_CONFIG_FILE", "/wrong-inherited-models.yml")
	c := Client{Binary: executable, ConfigDir: dir, Root: t.TempDir(), Timezone: "UTC"}
	if e := c.SetConfig(context.Background(), []byte(`{"schemaVersion":1,"models":{}}`)); e != nil {
		t.Fatal(e)
	}
	for _, method := range []string{"ping", "receipt", "update", "summarize"} {
		if e := c.Call(context.Background(), method, struct{}{}, nil); e != nil {
			t.Fatal(method, e)
		}
	}
	if _, e := os.Stat(filepath.Join(dir, "set-called")); e != nil {
		t.Fatal(e)
	}
}
func TestUnsupportedCLIReceivesNoConfiguration(t *testing.T) {
	executable, _ := os.Executable()
	dir := t.TempDir()
	t.Setenv("MEMX_CLIENT_TEST_PROCESS", "1")
	t.Setenv("MEMX_EXPECT_CONFIG_DIR", dir)
	t.Setenv("MEMX_TEST_UNSUPPORTED", "1")
	c := Client{Binary: executable, ConfigDir: dir, Root: t.TempDir(), Timezone: "UTC"}
	if e := c.SetConfig(context.Background(), []byte(`{"secret":"must-not-send"}`)); e == nil || !strings.Contains(e.Error(), "MEMX_CONFIG_DIR") {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(dir, "set-called")); !os.IsNotExist(e) {
		t.Fatal("configuration sent to unsupported CLI", e)
	}
	for _, bad := range []string{"", "relative"} {
		c.ConfigDir = bad
		if e := c.Call(context.Background(), "ping", struct{}{}, nil); e == nil {
			t.Fatal("invalid instance directory accepted")
		}
	}
}

func TestOldCLIReceivesNoConfiguration(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.3.1", "0.3.2", "0.4.0"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("MEMX_CLIENT_TEST_PROCESS", "1")
			t.Setenv("MEMX_EXPECT_CONFIG_DIR", dir)
			t.Setenv("MEMX_TEST_VERSION", version)
			c := Client{Binary: executable, ConfigDir: dir, Root: t.TempDir(), Timezone: "UTC"}
			if err := c.SetConfig(context.Background(), []byte(`{"secret":"must-not-send"}`)); err == nil || !strings.Contains(err.Error(), "0.4.1") {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "set-called")); !os.IsNotExist(err) {
				t.Fatal("configuration sent to old CLI", err)
			}
		})
	}
}
