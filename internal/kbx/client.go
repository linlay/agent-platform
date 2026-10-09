// Package kbx adapts the managed KBX CLI. It never starts the retired KBASE engine.
package kbx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/knowledge"
)

// Runner is the process boundary; arguments are never interpreted by a shell.
type Runner interface {
	Run(context.Context, string, []byte, ...string) ([]byte, error)
}
type cliRunner struct{ configFile string }

// errCommandNotStarted is private to the process boundary, not an index-level guarantee.
var errCommandNotStarted = errors.New("KBX process did not start")

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

func (runner cliRunner) Run(ctx context.Context, database string, config []byte, args ...string) (output []byte, resultErr error) {
	started := false
	defer func() {
		if resultErr != nil && !started {
			resultErr = fmt.Errorf("%w: %w", errCommandNotStarted, resultErr)
		}
	}()

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
	cfg := runner.configFile
	if cfg == "" {
		cfg = filepath.Join(dir, "config.yml")
		if err = os.WriteFile(cfg, config, 0600); err != nil {
			return nil, err
		}
	} else {
		if !filepath.IsAbs(cfg) {
			return nil, fmt.Errorf("KBX config file must be absolute")
		}
		st, statErr := os.Lstat(cfg)
		if statErr != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("KBX config file unavailable")
		}
	}
	argv := []string{"--kb", database, "--config", cfg}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = dir
	for _, e := range builtins.EnsureBinInEnv(os.Environ()) {
		key, _, _ := strings.Cut(e, "=")
		if !strings.EqualFold(key, "KBX_CONFIG_FILE") && !strings.EqualFold(key, "KBX_CONFIG_DIR") && !strings.EqualFold(key, "INDEX_PATH") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "KBX_CONFIG_DIR="+filepath.Dir(cfg))
	var stdout boundedBuffer
	cmd.Stdout = &stdout
	// Do not return raw provider diagnostics: they may contain endpoint credentials.
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	started = true
	err = cmd.Wait()
	if stdout.exceeded {
		return nil, fmt.Errorf("KBX output exceeded 16 MiB")
	}
	if err != nil {
		if ctx.Err() != nil {
			return stdout.Bytes(), ctx.Err()
		}
		return stdout.Bytes(), fmt.Errorf("KBX %s failed: %w", args[0], err)
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

// Inspect even nonzero-exit responses. Return only fixed guidance and the
// bounded machine code: provider error messages may contain credentials.
func readerFailure(data []byte, operation string) error {
	var response envelope
	if json.Unmarshal(data, &response) != nil || response.SchemaVersion != 2 || response.Type != "kbx.agent.response" || response.Status != "error" {
		return nil
	}
	var detail struct{ Code string }
	if json.Unmarshal(response.Error, &detail) != nil || len(detail.Code) == 0 || len(detail.Code) > 64 {
		detail.Code = "EXECUTION_FAILED"
	}
	for _, char := range detail.Code {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			detail.Code = "EXECUTION_FAILED"
			break
		}
	}
	message := fmt.Sprintf("KBX %s failed (%s)", operation, detail.Code)
	switch operation {
	case "gsearch":
		message += "; graph retrieval requires a complete graph index; Platform refresh currently maintains text and vectors only"
	case "vsearch":
		message += "; vector retrieval requires a complete vector index and matching embedding configuration; choose query explicitly if fallback is acceptable"
	}
	if detail.Code == "INVALID_INPUT" {
		return &knowledge.PolicyError{Kind: knowledge.ErrorInvalid, Message: message + "; check method-specific parameters, configuration, and filter syntax; do not drop scope constraints"}
	}
	return unavailable(message)
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
