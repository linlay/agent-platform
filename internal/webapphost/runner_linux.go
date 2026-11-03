//go:build linux

package webapphost

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func lockGeneration(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func ownsLoopbackPort(pid int, address string) bool {
	_, port, ok := strings.Cut(address, ":")
	if !ok {
		return false
	}
	n, _ := strconv.Atoi(port)
	target := fmt.Sprintf("0100007F:%04X", n)
	raw, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return false
	}
	inodes := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) > 9 && f[1] == target && f[3] == "0A" {
			inodes["socket:["+f[9]+"]"] = true
		}
	}
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		link, err := os.Readlink(filepath.Join(dir, entry.Name()))
		if err == nil && inodes[link] {
			return true
		}
	}
	return false
}
func processTreeRSS(pid int) uint64 {
	seen := map[int]bool{}
	var visit func(int) uint64
	visit = func(id int) uint64 {
		if seen[id] || len(seen) > 8192 {
			return 0
		}
		seen[id] = true
		raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/statm", id))
		f := strings.Fields(string(raw))
		var size uint64
		if len(f) > 1 {
			pages, _ := strconv.ParseUint(f[1], 10, 64)
			size = pages * uint64(os.Getpagesize())
		}
		children, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", id, id))
		for _, value := range strings.Fields(string(children)) {
			child, _ := strconv.Atoi(value)
			if child > 1 {
				size += visit(child)
			}
		}
		return size
	}
	return visit(pid)
}
