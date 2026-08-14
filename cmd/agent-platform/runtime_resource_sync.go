package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"agent-platform/internal/runtimeresource"
)

func runRuntimeResourceSync(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("agent-platform runtime-resource-sync", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := runtimeresource.Options{}
	flags.StringVar(&options.RuntimeDir, "ap-runtime-dir", "", "Agent Platform runtime root")
	flags.StringVar(&options.Source, "runtime-resource-source", "", "validated Desktop env.zip")
	flags.StringVar(&options.PreviousSource, "runtime-resource-previous-source", "", "optional previous Desktop env.zip")
	flags.StringVar(&options.DesktopFrom, "desktop-version-from", "", "previous Desktop version or legacy")
	flags.StringVar(&options.DesktopTo, "desktop-version-to", "", "target Desktop version")
	flags.StringVar(&options.Mode, "mode", "", "version-change or manual-import")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse runtime-resource-sync arguments: %w", err)
	}
	if remaining := flags.Args(); len(remaining) > 0 {
		return fmt.Errorf("unexpected runtime-resource-sync argument(s): %s", strings.Join(remaining, " "))
	}
	result, err := runtimeresource.Sync(options)
	if err != nil {
		return fmt.Errorf("runtime resource sync failed: %w", err)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write runtime resource sync result: %w", err)
	}
	return nil
}
