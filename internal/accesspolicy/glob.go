package accesspolicy

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func expandOperandPaths(raw string) ([]string, error) {
	if !strings.ContainsAny(raw, "*?[") {
		return []string{raw}, nil
	}
	volume := filepath.VolumeName(raw)
	parts := strings.Split(strings.TrimPrefix(raw, volume), string(filepath.Separator))
	if len(parts) > 128 {
		return nil, errors.New("glob path exceeds review depth")
	}
	for _, part := range parts {
		if part == ".." {
			return nil, errors.New("glob with parent traversal cannot be safely expanded")
		}
		if _, err := filepath.Match(part, ""); err != nil {
			return nil, err
		}
	}
	var matches []string
	visited := 0
	var walk func(string, int) error
	walk = func(parent string, i int) error {
		if i == len(parts) {
			if _, err := os.Lstat(parent); err == nil {
				matches = append(matches, parent)
			}
			if len(matches) > 512 {
				return errors.New("glob exceeds 512 reviewed targets")
			}
			return nil
		}
		part := parts[i]
		if part == "" || part == "." {
			return walk(parent, i+1)
		}
		if !strings.ContainsAny(part, "*?[") {
			return walk(filepath.Join(parent, part), i+1)
		}
		dir, err := os.Open(parent)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			entries, err := dir.ReadDir(128)
			if err != nil && err != io.EOF {
				return err
			}
			for _, entry := range entries {
				visited++
				if visited > 4096 {
					return errors.New("glob exceeds 4096 inspected directory entries")
				}
				match, _ := filepath.Match(part, entry.Name())
				if match {
					if err := walk(filepath.Join(parent, entry.Name()), i+1); err != nil {
						return err
					}
				}
			}
			if err == io.EOF {
				break
			}
		}
		return nil
	}
	root := volume + string(filepath.Separator)
	if !filepath.IsAbs(raw) {
		root = "."
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return []string{raw}, nil
	}
	sort.Strings(matches)
	return matches, nil
}
