package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"agent-platform/internal/connectormigrate"
)

func runConnectorMigration(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("connector-migrate", flag.ContinueOnError)
	root := flags.String("runtime-dir", "", "runtime directory to migrate offline")
	apply := flags.Bool("apply", false, "publish migration after validation; stop runtime first")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: agent-platform connector-migrate --runtime-dir <path> [--apply]")
	}
	result, err := connectormigrate.Run(*root, *apply)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
