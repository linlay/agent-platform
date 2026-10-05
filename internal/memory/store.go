// Package memory owns the personal Markdown files. It has no database or
// semantic index; knowledge indexing belongs to KBX.
package memory

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxFileBytes = 256 << 10

var (
	ErrInvalid  = errors.New("invalid memory document")
	ErrConflict = errors.New("document changed; reload before saving")
)

type Document struct {
	Kind     string `json:"kind"`
	Date     string `json:"date,omitempty"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
	Exists   bool   `json:"exists"`
}

type Match struct {
	Kind string `json:"kind"`
	Date string `json:"date,omitempty"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type Store struct {
	MemoryDir, OwnerDir string
	Location            *time.Location
}

func NewStore(memoryDir, ownerDir string, location *time.Location) *Store {
	if location == nil {
		location = time.Local
	}
	return &Store{MemoryDir: memoryDir, OwnerDir: ownerDir, Location: location}
}

func (s *Store) Today() string { return time.Now().In(s.Location).Format(time.DateOnly) }

func validDate(date string) bool {
	t, err := time.Parse(time.DateOnly, date)
	return err == nil && t.Format(time.DateOnly) == date
}

func (s *Store) target(kind, date string) (string, string, error) {
	switch kind {
	case "owner":
		if date == "" && s.OwnerDir != "" {
			return s.OwnerDir, "OWNER.md", nil
		}
	case "memory", "summary":
		if date == "" && s.MemoryDir != "" {
			return s.MemoryDir, "summary.md", nil
		}
	case "daily":
		if validDate(date) && s.MemoryDir != "" {
			return s.MemoryDir, "daily/" + date + ".md", nil
		}
	}
	return "", "", ErrInvalid
}

func revision(content []byte, exists bool) string {
	if !exists {
		return "missing"
	}
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

// All relative access is anchored by os.Root, including concurrent rename and
// symlink escape protection. Documents and the daily directory cannot be links.
func checkFile(root *os.Root, name string) error {
	if strings.HasPrefix(name, "daily/") {
		info, err := root.Lstat("daily")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return ErrInvalid
		}
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return ErrInvalid
	}
	return nil
}

func read(root *os.Root, name, kind, date string) (Document, error) {
	d := Document{Kind: kind, Date: date, Revision: "missing"}
	if err := checkFile(root, name); err != nil {
		return d, err
	}
	f, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return d, err
	}
	if len(b) > MaxFileBytes || !utf8.Valid(b) {
		return d, ErrInvalid
	}
	d.Content, d.Exists, d.Revision = string(b), true, revision(b, true)
	return d, nil
}

func (s *Store) Read(kind, date string) (Document, error) {
	dir, name, err := s.target(kind, date)
	if err != nil {
		return Document{}, err
	}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Document{Kind: kind, Date: date, Revision: "missing"}, nil
	}
	if err != nil {
		return Document{}, err
	}
	defer root.Close()
	return read(root, name, kind, date)
}

// Mutations share an OS lock across service instances and processes. Editors
// must supply the exact revision returned by Read, including "missing".
func (s *Store) mutate(kind, date, base string, change func(string) (string, bool, error)) (Document, error) {
	dir, name, err := s.target(kind, date)
	if err != nil {
		return Document{}, err
	}
	if err := os.MkdirAll(s.MemoryDir, 0700); err != nil {
		return Document{}, err
	}
	lockRoot, err := os.OpenRoot(s.MemoryDir)
	if err != nil {
		return Document{}, err
	}
	defer lockRoot.Close()
	if err := checkFile(lockRoot, ".memory.lock"); err != nil {
		return Document{}, err
	}
	lock, err := lockRoot.OpenFile(".memory.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		lock, err = lockRoot.OpenFile(".memory.lock", os.O_RDWR, 0600)
	}
	if err != nil {
		return Document{}, err
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return Document{}, err
	}
	// memx owns recovery of its multi-file journal. Do not let UI/tool writes
	// overtake an unfinished automatic transaction after a process crash.
	if _, err := lockRoot.Lstat(".memx-pending.json"); err == nil {
		return Document{}, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return Document{}, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Document{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Document{}, err
	}
	defer root.Close()
	current, err := read(root, name, kind, date)
	if err != nil {
		return Document{}, err
	}
	if base == "" || base != current.Revision {
		return Document{}, ErrConflict
	}
	content, remove, err := change(current.Content)
	if err != nil {
		return Document{}, err
	}
	if remove {
		if current.Exists {
			if err := root.Remove(name); err != nil {
				return Document{}, err
			}
		}
		return Document{Kind: kind, Date: date, Revision: "missing"}, nil
	}
	if len(content) > MaxFileBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return Document{}, ErrInvalid
	}
	if kind == "daily" {
		if err := root.Mkdir("daily", 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return Document{}, err
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Document{}, err
	}
	tmp := fmt.Sprintf(".memory-%x.tmp", nonce)
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Document{}, err
	}
	defer root.Remove(tmp)
	_, writeErr := io.WriteString(f, content)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return Document{}, writeErr
	}
	if closeErr != nil {
		return Document{}, closeErr
	}
	if err := checkFile(root, name); err != nil {
		return Document{}, err
	}
	if err := root.Rename(tmp, name); err != nil {
		return Document{}, err
	}
	return Document{Kind: kind, Date: date, Content: content, Revision: revision([]byte(content), true), Exists: true}, nil
}

func (s *Store) Save(kind, date, content, base string) (Document, error) {
	return s.mutate(kind, date, base, func(string) (string, bool, error) { return content, false, nil })
}

func (s *Store) Delete(kind, date, base string) (Document, error) {
	return s.mutate(kind, date, base, func(string) (string, bool, error) { return "", true, nil })
}

func (s *Store) Append(date, content, base string) (Document, error) {
	if strings.TrimSpace(content) == "" {
		return Document{}, ErrInvalid
	}
	return s.mutate("daily", date, base, func(old string) (string, bool, error) {
		prefix := strings.TrimRight(old, "\n")
		if prefix != "" {
			prefix += "\n\n"
		}
		return prefix + strings.TrimSpace(content) + "\n", false, nil
	})
}

func (s *Store) Dates(before string, limit int) ([]string, error) {
	if before != "" && !validDate(before) {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	out := []string{}
	root, err := os.OpenRoot(s.MemoryDir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkFile(root, "daily/2000-01-01.md"); err != nil {
		return nil, err
	}
	dir, err := root.Open("daily")
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		date := strings.TrimSuffix(entry.Name(), ".md")
		if validDate(date) && (before == "" || date < before) {
			out = append(out, date)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type SearchResult struct {
	Matches            []Match `json:"matches"`
	NextBefore         string  `json:"nextBefore"`
	MaxMatches         int     `json:"maxMatches"`
	SearchedDailyFiles int     `json:"searchedDailyFiles"`
}

// SearchPage returns one snippet per matching file. The cursor advances only
// through dates actually scanned, so a full result page never skips files.
func (s *Store) SearchPage(query, before string) (SearchResult, error) {
	result := SearchResult{Matches: []Match{}, MaxMatches: 40}
	if strings.TrimSpace(query) == "" {
		return result, ErrInvalid
	}
	dates, err := s.Dates(before, 200)
	if err != nil {
		return result, err
	}
	targets := []Document{}
	if before == "" {
		targets = append(targets, Document{Kind: "owner"}, Document{Kind: "memory"})
	}
	for _, date := range dates {
		targets = append(targets, Document{Kind: "daily", Date: date})
	}
	needle := strings.ToLower(query)
	for _, target := range targets {
		d, err := s.Read(target.Kind, target.Date)
		if err != nil {
			return result, err
		}
		if target.Kind == "daily" {
			result.SearchedDailyFiles++
		}
		for index, line := range strings.Split(d.Content, "\n") {
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}
			runes := []rune(line)
			if len(runes) > 500 {
				line = string(runes[:500]) + "…"
			}
			result.Matches = append(result.Matches, Match{Kind: d.Kind, Date: d.Date, Line: index + 1, Text: line})
			break
		}
		if len(result.Matches) >= result.MaxMatches {
			result.NextBefore = target.Date
			return result, nil
		}
	}
	if len(dates) == 200 {
		result.NextBefore = dates[len(dates)-1]
	}
	return result, nil
}

func (s *Store) Search(query, before string) ([]Match, error) {
	result, err := s.SearchPage(query, before)
	return result.Matches, err
}
