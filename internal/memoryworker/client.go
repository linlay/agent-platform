// Package memoryworker schedules one-shot memx calls. It owns no memory content.
package memoryworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"agent-platform/internal/builtins"
)

type Source struct {
	ID          string `json:"sourceId"`
	ChatID      string `json:"chatId"`
	RunID       string `json:"runId"`
	AgentKey    string `json:"agentKey"`
	ProjectKey  string `json:"projectKey,omitempty"`
	Role        string `json:"role"`
	OccurredAt  string `json:"occurredAt"`
	Content     string `json:"content"`
	ContentHash string `json:"contentHash"`
}
type Batch struct {
	SchemaVersion int      `json:"schemaVersion"`
	BatchID       string   `json:"batchId"`
	Sources       []Source `json:"sources"`
}
type CLI interface {
	Call(context.Context, string, any, any) error
}
type Client struct {
	Root, Timezone, ConfigDir string
	Binary                    string
}
type boundedOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > 2<<20-b.Len() {
		b.exceeded = true
		p = p[:2<<20-b.Len()]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func (c Client) Call(ctx context.Context, method string, params, out any) error {
	binary := c.Binary
	var err error
	if binary == "" {
		binary, err = builtins.ResolveProcessBuiltin("memx")
		if err != nil {
			return fmt.Errorf("managed memx unavailable: %w", err)
		}
	}
	input, err := json.Marshal(map[string]any{"id": "platform-memory", "method": method, "params": params})
	if err != nil {
		return err
	}
	if len(input) > 8<<20 {
		return fmt.Errorf("memx input exceeds 8 MiB")
	}
	return c.execute(ctx, binary, []string{"--root", c.Root, "--timezone", c.Timezone, "--config-dir", c.ConfigDir, "call"}, input, "platform-memory", out)
}
func (c Client) SetConfig(ctx context.Context, input []byte) error {
	binary := c.Binary
	if binary == "" {
		var err error
		binary, err = builtins.ResolveProcessBuiltin("memx")
		if err != nil {
			return fmt.Errorf("managed memx unavailable: %w", err)
		}
	}
	return c.execute(ctx, binary, []string{"--config-dir", c.ConfigDir, "config", "set", "--stdin"}, input, "", nil)
}
func (c Client) execute(ctx context.Context, binary string, args []string, input []byte, id string, out any) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	var output boundedOutput
	cmd.Stdout = &output
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if output.exceeded {
		return fmt.Errorf("memx output too large")
	}
	var response struct {
		SchemaVersion int             `json:"schemaVersion"`
		ID            string          `json:"id"`
		OK            bool            `json:"ok"`
		Data          json.RawMessage `json:"data"`
		Error         *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(output.Bytes(), &response) != nil || response.SchemaVersion != 1 || response.ID != id {
		return fmt.Errorf("invalid memx response: %v", runErr)
	}
	if !response.OK {
		if response.Error != nil {
			return fmt.Errorf("memx: %s", response.Error.Code)
		}
		return fmt.Errorf("memx operation failed")
	}
	if runErr != nil {
		return fmt.Errorf("memx failed: %w", runErr)
	}
	if out != nil {
		return json.Unmarshal(response.Data, out)
	}
	return nil
}
