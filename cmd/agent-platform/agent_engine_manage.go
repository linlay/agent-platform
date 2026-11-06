package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/httpclient"
)

func runAgentEngineManagement(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent-engine-manage <list|import|enable|disable|uninstall> --runtime-dir <path> [--id <plugin-id>] [--overwrite] [package.zip]")
	}
	action := args[0]
	flags := flag.NewFlagSet("agent-engine-manage "+action, flag.ContinueOnError)
	root := flags.String("runtime-dir", "", "deployment runtime root of the running standalone Platform")
	id := flags.String("id", "", "plugin ID")
	overwrite := flags.Bool("overwrite", false, "replace an installed plugin (import only)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("--runtime-dir is required")
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	var body io.Reader
	method, suffix := http.MethodPost, "/"+action
	switch action {
	case "list":
		if *id != "" || *overwrite || flags.NArg() != 0 {
			return fmt.Errorf("list accepts only --runtime-dir")
		}
		method, suffix = http.MethodGet, ""
	case "import":
		if flags.NArg() != 1 || *id != "" {
			return fmt.Errorf("import requires one ZIP file")
		}
		file, err := os.Open(flags.Arg(0))
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > agentengine.MaxArchiveBytes {
			return fmt.Errorf("invalid plugin archive size")
		}
		body = file
		if *overwrite {
			suffix += "?overwrite=true"
		}
	case "enable", "disable", "uninstall":
		if *id == "" || flags.NArg() != 0 || *overwrite {
			return fmt.Errorf("%s requires --id and no ZIP or --overwrite", action)
		}
		data, err := json.Marshal(map[string]string{"pluginId": *id})
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	default:
		return fmt.Errorf("unknown agent engine action %q", action)
	}
	access, err := agentengine.ReadLocalManagement(absolute)
	if err != nil {
		return fmt.Errorf("read local management authority (start standalone Platform first): %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, access.URL+suffix, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "ZenMindEngine "+access.Secret)
	request.Header.Set("X-ZenMind-Host", access.HostID)
	request.Header.Set("X-ZenMind-Engine-Epoch", access.Epoch)
	request.Header.Set("Content-Type", "application/json")
	if action == "import" {
		request.Header.Set("Content-Type", "application/zip")
	}
	client := httpclient.DirectClient(5 * time.Minute)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("local engine management request failed: %w", err)
	}
	defer response.Body.Close()
	var result struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("invalid engine management response")
	}
	if response.StatusCode != http.StatusOK || result.Code != 200 {
		return fmt.Errorf("engine management: %s (HTTP %d)", result.Msg, response.StatusCode)
	}
	return json.NewEncoder(out).Encode(result.Data)
}
