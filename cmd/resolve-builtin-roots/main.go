package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"agent-platform/internal/builtins"
)

func main() {
	repo := flag.String("repo-root", ".", "Platform checkout")
	kind := flag.String("kind", "builtins", "builtins or connectors")
	flag.Parse()
	root, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var relative string
	switch *kind {
	case "builtins":
		relative = "../agent-platform-builtins"
	case "connectors":
		relative = "../agent-platform-connectors"
	default:
		fmt.Fprintln(os.Stderr, "kind must be builtins or connectors")
		os.Exit(1)
	}
	fmt.Println(builtins.DefaultSourceRoot(root, relative))
}
