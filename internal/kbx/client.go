// Package kbx adapts the managed KBX CLI. It never starts the retired KBASE engine.
package kbx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/builtins"
)

// Runner is the process boundary; arguments are never interpreted by a shell.
type Runner interface {
	Run(context.Context, string, []byte, ...string) ([]byte, error)
}
type cliRunner struct{}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	const cap = 16 << 20
	n := len(p)
	if n > cap-b.Len() {
		b.exceeded = true
		p = p[:cap-b.Len()]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func (cliRunner) Run(ctx context.Context, database string, config []byte, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("KBX command is required")
	}
	binary, err := builtins.ResolveProcessBuiltin("kbx")
	if err != nil {
		return nil, fmt.Errorf("managed KBX executable unavailable: %w", err)
	}
	// A private explicit configuration prevents inheriting the user's KBX models,
	// credentials, graph jobs or collection synchronization commands.
	dir, err := os.MkdirTemp("", "platform-kbx-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cfg := filepath.Join(dir, "config.yml")
	if err = os.WriteFile(cfg, config, 0600); err != nil {
		return nil, err
	}
	argv := []string{"--kb", database, "--config", cfg}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = dir
	for _, e := range builtins.EnsureBinInEnv(os.Environ()) {
		key, _, _ := strings.Cut(e, "=")
		if !strings.EqualFold(key, "KBX_CONFIG_DIR") && !strings.EqualFold(key, "INDEX_PATH") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "KBX_CONFIG_DIR="+dir)
	var stdout boundedBuffer
	cmd.Stdout = &stdout
	// Do not return raw provider diagnostics: they may contain endpoint credentials.
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("KBX %s failed: %w", args[0], err)
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("KBX output exceeded 16 MiB")
	}
	return stdout.Bytes(), nil
}

type envelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Type          string          `json:"type"`
	Status        string          `json:"status"`
	Data          json.RawMessage `json:"data"`
	Error         json.RawMessage `json:"error"`
}

func decodeEnvelope(data []byte, target any) error {
	var e envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("invalid KBX response: %w", err)
	}
	if e.SchemaVersion != 2 || e.Type != "kbx.agent.response" {
		return fmt.Errorf("unsupported KBX response protocol")
	}
	if e.Status != "ok" && e.Status != "empty" && e.Status != "partial" {
		return fmt.Errorf("KBX operation did not succeed")
	}
	if len(e.Data) == 0 || string(e.Data) == "null" {
		return fmt.Errorf("KBX response has no data")
	}
	return json.Unmarshal(e.Data, target)
}
