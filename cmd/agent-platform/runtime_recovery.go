package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"agent-platform/internal/runtimerecovery"
)

func runRuntimeRecovery(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("runtime-recovery", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := runtimerecovery.Options{}
	flags.StringVar(&options.Source, "source", "", "stopped source runtime")
	flags.StringVar(&options.SourceConfigDir, "source-config-dir", "", "source service configuration directory, read only")
	flags.StringVar(&options.Target, "ap-runtime-dir", "", "unpublished candidate")
	flags.StringVar(&options.Action, "action", "", "check or restore")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("unexpected runtime-recovery arguments")
	}
	result, err := runtimerecovery.Run(options)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
