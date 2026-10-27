package mailbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/chat"
)

// chatPath rejects traversal and symlink escapes, including an escaped Chat
// directory. Every read/copy revalidates; a file name never grants host access.
func (s *Service) chatPath(chatID, ref string) (string, error) {
	if !chat.ValidChatID(chatID) {
		return "", failure("invalid_request", "invalid Chat identity")
	}
	rel := filepath.FromSlash(strings.TrimPrefix(ref, "@chat/"))
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", failure("invalid_request", "files must be relative paths in the sending Chat")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", failure("invalid_request", "file escapes Chat")
	}
	root, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, chatID, clean))
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(filepath.Join(root, chatID), path)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", failure("file_forbidden", "file escapes Chat")
	}
	return path, nil
}
func digestFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", failure("invalid_request", "mail attachments must be regular files")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}
func (s *Service) prepareFiles(chatID, id string, paths []string) ([]File, error) {
	out := make([]File, 0, len(paths))
	names := map[string]bool{}
	for _, ref := range paths {
		p, err := s.chatPath(chatID, ref)
		if err != nil {
			return nil, err
		}
		size, hash, err := digestFile(p)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(p)
		if names[strings.ToLower(name)] {
			return nil, failure("invalid_request", "duplicate attachment file name")
		}
		names[strings.ToLower(name)] = true
		out = append(out, File{Name: name, Size: size, SHA256: hash, Source: strings.TrimPrefix(ref, "@chat/"), Path: filepath.ToSlash(filepath.Join("mail", id, name))})
	}
	return out, nil
}
func (s *Service) copyFiles(m Mail) error {
	for _, file := range m.Files {
		// A completed copy is already the immutable attachment. Never require
		// the sender's original file to survive a restart once the copy exists.
		if dest, e := s.chatPath(m.ToChatID, file.Path); e == nil {
			size, hash, e := digestFile(dest)
			if e == nil && size == file.Size && hash == file.SHA256 {
				continue
			}
			return failure("mail_attachment_changed", "destination attachment no longer matches its accepted digest")
		}
		source, err := s.chatPath(m.FromChatID, file.Source)
		if err != nil {
			return err
		}
		size, hash, err := digestFile(source)
		if err != nil {
			return err
		}
		if size != file.Size || hash != file.SHA256 {
			return failure("mail_attachment_changed", "source attachment changed after acceptance")
		}
		root, err := filepath.EvalSymlinks(s.root)
		if err != nil {
			return err
		}
		dir, err := attachmentDirectory(root, m.ToChatID, m.ID)
		if err != nil {
			return err
		}
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		out, err := os.CreateTemp(dir, ".copy-")
		if err != nil {
			in.Close()
			return err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, h), in)
		in.Close()
		if copyErr == nil {
			copyErr = out.Sync()
		}
		closeErr := out.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil && (n != file.Size || hex.EncodeToString(h.Sum(nil)) != file.SHA256) {
			copyErr = failure("mail_attachment_changed", "attachment changed while copying")
		}
		if copyErr == nil {
			copyErr = os.Rename(out.Name(), filepath.Join(dir, file.Name))
		}
		if copyErr != nil {
			os.Remove(out.Name())
			return copyErr
		}
	}
	return nil
}

// Check each existing ancestor before creating the next component. MkdirAll
// would follow a pre-existing mail symlink and write outside the Chat first.
func attachmentDirectory(root, chatID, mailID string) (string, error) {
	if !chat.ValidChatID(chatID) || filepath.Base(mailID) != mailID || mailID == "." || mailID == ".." {
		return "", failure("file_forbidden", "invalid attachment destination")
	}
	dir := root
	for _, part := range []string{chatID, "mail", mailID} {
		dir = filepath.Join(dir, part)
		if err := os.Mkdir(dir, 0755); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", failure("file_forbidden", "attachment destination contains a symlink or non-directory")
		}
	}
	return dir, nil
}
func (s *Service) verifyFiles(m Mail) error {
	for _, f := range m.Files {
		p, err := s.chatPath(m.ToChatID, f.Path)
		if err != nil {
			return err
		}
		n, h, err := digestFile(p)
		if err != nil {
			return err
		}
		if n != f.Size || h != f.SHA256 {
			return failure("mail_attachment_changed", fmt.Sprintf("attachment %q changed", f.Name))
		}
	}
	return nil
}

func (s *Service) replyFiles(m *Mail) error {
	reader, ok := s.artifacts()
	if !ok {
		return nil
	}
	items, err := reader.PublishedArtifacts(m.FromChatID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	files := []File{}
	for _, a := range items {
		if a.RunID != m.ReplyOfRun {
			continue
		}
		name := filepath.Base(filepath.FromSlash(strings.TrimPrefix(a.URL, "@chat/")))
		if seen[strings.ToLower(name)] {
			return failure("mail_attachment_changed", "published attachments have duplicate names")
		}
		seen[strings.ToLower(name)] = true
		file := File{Name: name, Size: a.SizeBytes, SHA256: a.SHA256, Source: strings.TrimPrefix(a.URL, "@chat/"), Path: filepath.ToSlash(filepath.Join("mail", m.ID, name))}
		if file.SHA256 == "" {
			legacy, e := s.prepareFiles(m.FromChatID, m.ID, []string{a.URL})
			if e != nil {
				return e
			}
			file = legacy[0]
		}
		files = append(files, file)
	}
	m.Files = files
	return nil
}
