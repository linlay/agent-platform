package webapp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func writeJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func readJSON(path string, target any) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 512<<10 {
		return fmt.Errorf("invalid managed record")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return strictJSON(b, target)
}
func removeTree(path string) error {
	if err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(p, 0700)
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(path)
}
func treeDigest(root string, freeze bool) (string, error) {
	paths := []string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular package member")
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	sum := sha256.New()
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(sum, "%s\x00", filepath.ToSlash(rel))
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		f.Close()
		if err != nil {
			return "", err
		}
		sum.Write(hash.Sum(nil))
		if freeze {
			if err := os.Chmod(p, 0444); err != nil {
				return "", err
			}
		}
	}
	if freeze {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if err := syncDir(p); err != nil {
					return err
				}
				return os.Chmod(p, 0555)
			}
			return nil
		})
	}
	return hex.EncodeToString(sum.Sum(nil)), err
}
func ensureDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed directory is not a directory")
	}
	return nil
}

func renamePackage(source, target string) error {
	// Moving a directory between parents changes its '..' entry. On Linux and
	// macOS the stopped install transaction temporarily grants owner write,
	// restoring the frozen mode before code can be published again.
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		fi, err := os.Stat(source)
		if err != nil {
			return err
		}
		mode := fi.Mode().Perm()
		if err := os.Chmod(source, mode|0200); err != nil {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			_ = os.Chmod(source, mode)
			return err
		}
		return os.Chmod(target, mode)
	}
	return os.Rename(source, target)
}
func isSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}
