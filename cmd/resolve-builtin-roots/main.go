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
	flag.Parse()
	root, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(builtins.DefaultSourceRoot(root, "../agent-platform-builtins"))
}
