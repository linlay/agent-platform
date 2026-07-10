package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/builtins"
)

func main() {
	repoRoot := flag.String("repo-root", ".", "agent-platform repository root")
	lockPath := flag.String("lock", "scripts/release-assets/builtins.lock.json", "builtins lock path")
	builtinsRoot := flag.String("builtins-root", "", "absolute builtins collection root")
	outputDir := flag.String("output", "", "service bundle output directory")
	targetOS := flag.String("os", "", "target operating system")
	targetArch := flag.String("arch", "", "target architecture")
	resolveComponent := flag.String("resolve-component", "", "print locked version and repository path")
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		log.Fatal(err)
	}
	resolvedLockPath := *lockPath
	if !filepath.IsAbs(resolvedLockPath) {
		resolvedLockPath = filepath.Join(root, resolvedLockPath)
	}
	if strings.TrimSpace(*resolveComponent) != "" {
		lock, err := builtins.LoadLock(resolvedLockPath)
		if err != nil {
			log.Fatal(err)
		}
		collectionRoot, err := builtins.ResolveRoot(root, *builtinsRoot, lock)
		if err != nil {
			log.Fatal(err)
		}
		component, err := builtins.FindComponent(lock, *resolveComponent)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s\t%s\n", component.Version, filepath.Join(collectionRoot, component.Repository))
		return
	}
	if strings.TrimSpace(*outputDir) == "" || strings.TrimSpace(*targetOS) == "" || strings.TrimSpace(*targetArch) == "" {
		flag.Usage()
		os.Exit(2)
	}
	result, err := builtins.Stage(builtins.StageOptions{
		RepoRoot:     root,
		LockPath:     resolvedLockPath,
		BuiltinsRoot: *builtinsRoot,
		OutputDir:    *outputDir,
		GOOS:         *targetOS,
		GOARCH:       *targetArch,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("[builtins] staged %d components for %s/%s from %s\n", len(result.Manifest.Components), *targetOS, *targetArch, result.BuiltinsRoot)
}
