package kbases

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const controlReceiptRetention = 30 * 24 * time.Hour

// Completed receipts have a bounded replay window. Interrupted/unknown receipts
// never expire automatically, since deleting them could repeat a committed write.
func (s *Service) cleanupControlReceipts(now time.Time) error {
	s.management.Lock()
	defer s.management.Unlock()
	if s.options.StateDir == "" || !s.receiptCleanupAt.IsZero() && now.Sub(s.receiptCleanupAt) < time.Hour {
		return nil
	}
	s.receiptCleanupAt = now
	root, err := safeDirectory(s.options.StateDir, "kbases-control", false)
	if os.IsNotExist(err) || errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") && len(name) == 64 {
			if _, err := hex.DecodeString(name); err != nil {
				continue
			}
			path := filepath.Join(root, entry.Name())
			b, info, err := readRegular(path)
			if err != nil || !info.ModTime().Before(now.Add(-controlReceiptRetention)) {
				continue
			}
			var receipt controlReceipt
			if json.Unmarshal(b, &receipt) != nil || receipt.State != "completed" || receipt.Plan.RequestDigest == "" || receipt.Result == nil {
				continue
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	}
	return cleanupErr
}
