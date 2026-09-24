package main

import (
	"agent-platform/internal/connectormigrate"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	root := flag.String("runtime-dir", "", "deployment runtime root")
	apply := flag.Bool("apply", false, "apply the previewed migration while Platform is stopped")
	offline := flag.Bool("offline", false, "confirm Platform and editors are stopped")
	expand := flag.Bool("allow-expansion", false, "accept explicitly reported additional Desktop tool entry points")
	configure := flag.Bool("configure", false, "mark migrated Desktop configuration complete")
	rollback := flag.String("rollback", "", "restore a migration backup while Platform is stopped")
	flag.Parse()
	if (*apply || *rollback != "") && !*offline {
		fail(fmt.Errorf("stop Platform and use --offline before mutation"))
	}
	if *rollback != "" {
		if err := connectormigrate.RollbackDesktop(*rollback); err != nil {
			fail(err)
		}
		return
	}
	if *root == "" {
		fail(fmt.Errorf("--runtime-dir is required"))
	}
	plan, err := connectormigrate.PreviewDesktop(*root)
	if err != nil {
		fail(err)
	}
	if *apply {
		plan, err = connectormigrate.ApplyDesktop(plan, *expand, *configure)
		if err != nil {
			fail(err)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		fail(err)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
