package kbx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CenterEngine implements explicit manual indexing for independent libraries.
// It is separate from the Agent capability's reconnectable-worker contract.
type CenterEngine struct{ runner Runner }

func NewCenterEngine() *CenterEngine { return &CenterEngine{runner: cliRunner{}} }

var centerConfig = []byte(`{"models":{"embedding":null,"query_expansion":null,"reranker":null,"graph_extraction":null}}`)

func (e *CenterEngine) Update(ctx context.Context, db, source string) error {
	actual, err := filepath.EvalSymlinks(source)
	if err != nil || actual != source {
		return fmt.Errorf("source directory is unavailable or changed identity")
	}
	st, err := os.Stat(source)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("source directory is unavailable")
	}
	// Collection registration is checked on every retry: an interrupted initial
	// scan can leave a valid database with an already registered collection.
	args := []string{"collection", "add", source, "--name", "workspace"}
	if st, err := os.Lstat(db); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("invalid KBX index path")
		}
		raw, err := e.runner.Run(ctx, db, centerConfig, "ls", "--agent")
		if err != nil {
			return err
		}
		var inventory struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		}
		if err = decodeEnvelope(raw, &inventory); err != nil {
			return fmt.Errorf("invalid KBX collection response")
		}
		for _, c := range inventory.Collections {
			if c.Name == "workspace" {
				args = []string{"update", "-c", "workspace", "--no-commands"}
				break
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	out, err := e.runner.Run(ctx, db, centerConfig, args...)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "status=complete") || strings.Contains(string(out), "status=partial") {
		return fmt.Errorf("KBX indexing was incomplete; inspect the source documents and retry")
	}
	return nil
}
func (e *CenterEngine) Read(ctx context.Context, db, operation, arg string, limit int) (json.RawMessage, error) {
	var args []string
	switch operation {
	case "status":
		args = []string{"status", "--agent"}
	case "files":
		args = []string{"ls", "kbx://workspace", "--agent"}
	case "search":
		args = []string{"search", "--agent", "--full", "-c", "workspace", "-n", strconv.Itoa(limit), "--", arg}
	case "read":
		args = []string{"get", "--agent", "--no-line-numbers", "--lines", "200", "--", arg}
	default:
		return nil, fmt.Errorf("unknown KBX operation")
	}
	raw, err := e.runner.Run(ctx, db, centerConfig, args...)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err = decodeEnvelope(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}
