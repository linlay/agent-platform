package view

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const SnapshotDirectory = ".views"

var snapshotMu sync.Mutex

func documentBytes(doc Document) ([]byte, error) {
	doc.View.Hash = ""
	data, err := json.Marshal(doc)
	if err != nil || len(data) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: rendering document", ErrInvalid)
	}
	return data, nil
}

func contentHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// SaveSnapshot writes before an event publishes its reference. Chat archive and
// restore move this subtree together with the rest of the Chat resources.
func SaveSnapshot(chatDir string, doc Document) (Reference, error) {
	data, err := documentBytes(doc)
	if err != nil {
		return Reference{}, err
	}
	ref := doc.View
	ref.Hash = contentHash(data)
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	r, err := snapshotRoot(chatDir, true)
	if err != nil {
		return Reference{}, err
	}
	defer r.Close()
	name := ref.Hash + ".json"
	if existing, err := r.ReadFile(name); err == nil {
		if contentHash(existing) != ref.Hash {
			return Reference{}, fmt.Errorf("%w: snapshot hash mismatch", ErrInvalid)
		}
		return ref, nil
	} else if !os.IsNotExist(err) {
		return Reference{}, err
	}
	// Serialize publishers; rename exposes only a complete snapshot.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Reference{}, err
	}
	pending := ".pending-" + hex.EncodeToString(nonce[:])
	f, err := r.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Reference{}, err
	}
	defer r.Remove(pending)
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return Reference{}, writeErr
	}
	if closeErr != nil {
		return Reference{}, closeErr
	}
	if err := r.Rename(pending, name); err != nil {
		return Reference{}, err
	}
	return ref, nil
}

func LoadSnapshot(chatDir string, ref Reference) (Document, error) {
	if err := ref.Validate(); err != nil || ref.Hash == "" {
		return Document{}, ErrInvalid
	}
	r, err := snapshotRoot(chatDir, false)
	if err != nil {
		return Document{}, ErrNotFound
	}
	defer r.Close()
	f, err := r.Open(ref.Hash + ".json")
	if err != nil {
		return Document{}, ErrNotFound
	}
	defer f.Close()
	data, err := readBounded(f)
	if err != nil || contentHash(data) != ref.Hash {
		return Document{}, fmt.Errorf("%w: snapshot hash mismatch", ErrInvalid)
	}
	var doc Document
	if json.Unmarshal(data, &doc) != nil || doc.View.ConnectorID != ref.ConnectorID || doc.View.Key != ref.Key || ref.Version != "" && ref.Version != doc.View.Version {
		return Document{}, ErrInvalid
	}
	doc.View.Hash = ref.Hash
	return doc, nil
}

func snapshotRoot(chatDir string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(chatDir) {
		return nil, ErrInvalid
	}
	if create {
		if err := os.MkdirAll(chatDir, 0700); err != nil {
			return nil, err
		}
	}
	chatInfo, err := os.Lstat(chatDir)
	if err != nil {
		return nil, err
	}
	if !chatInfo.IsDir() || chatInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	r, err := os.OpenRoot(chatDir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if create {
		if err := r.Mkdir(SnapshotDirectory, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	info, err := r.Lstat(SnapshotDirectory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	return r.OpenRoot(SnapshotDirectory)
}
