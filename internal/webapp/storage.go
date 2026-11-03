package webapp

import (
	"bytes"
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
func readManaged(path string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("invalid managed record")
	}
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid managed record")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("invalid managed record")
	}
	return raw, nil
}
func readJSON(path string, target any) error {
	raw, err := readManaged(path, 4<<20)
	if err != nil {
		return err
	}
	return strictJSON(raw, target)
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

func verifyInstalledCode(root string, record Record) error {
	digest, err := treeDigest(root, false)
	if err != nil || digest != record.ContentSHA256 {
		return fail("webapp_code_integrity_failed", 409)
	}
	fi, err := os.Lstat(filepath.Join(root, "webapp.json"))
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 256<<10 {
		return fail("webapp_code_integrity_failed", 409)
	}
	raw, err := os.ReadFile(filepath.Join(root, "webapp.json"))
	if err != nil {
		return err
	}
	manifest, err := ParseManifest(raw)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(manifest)
	b, _ := json.Marshal(record.Manifest)
	if !bytes.Equal(a, b) {
		return fail("webapp_installation_manifest_mismatch", 409)
	}
	return nil
}
