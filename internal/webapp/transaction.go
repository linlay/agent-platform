package webapp

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func nowMillis() int64 { return time.Now().UnixMilli() }

type installJournal struct {
	SchemaVersion int      `json:"schemaVersion"`
	AppID         string   `json:"appId"`
	Old           *Record  `json:"old"`
	Next          Record   `json:"next"`
	OldDesired    *Desired `json:"oldDesired,omitempty"`
	Phase         string   `json:"phase"`
}
type deleteJournal struct {
	SchemaVersion int    `json:"schemaVersion"`
	AppID         string `json:"appId"`
	PurgeData     bool   `json:"purgeData"`
}

// recover completes or rolls back each transaction independently. A failure
// keeps that transaction and its application untouched for manual recovery;
// one that cannot be tied to an application stops all mutations instead.
func (s *Service) recover() {
	entries, err := os.ReadDir(filepath.Join(s.root, ".staging"))
	if err != nil {
		s.recoveryRequired = true
		log.Printf("WebApp staging unreadable; mutations stopped: %v", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == "uploads" {
			continue
		}
		id, err := s.recoverTransaction(e)
		if err == nil {
			continue
		}
		if idPattern.MatchString(id) {
			s.markUnavailable(id, "webapp_recovery_required", err)
		} else {
			s.recoveryRequired = true
			log.Printf("WebApp transaction %q not recovered; mutations stopped: %v", name, err)
		}
	}
}

// recoverTransaction returns the application ID as soon as it is known, so
// the caller can confine a failure to that installation.
func (s *Service) recoverTransaction(e os.DirEntry) (string, error) {
	name := e.Name()
	if !e.IsDir() {
		return "", fmt.Errorf("unexpected staging file")
	}
	dir := filepath.Join(s.root, ".staging", name)
	switch {
	case strings.HasPrefix(name, "review-"):
		return "", removeTree(dir)
	case strings.HasPrefix(name, "install-"):
		var j installJournal
		if err := readJSON(filepath.Join(dir, "journal.json"), &j); os.IsNotExist(err) {
			return "", removeTree(dir)
		} else if err != nil {
			return "", err
		}
		if !idPattern.MatchString(j.AppID) {
			return "", fmt.Errorf("invalid installation journal")
		}
		if s.unavailable[j.AppID] != "" {
			return j.AppID, fmt.Errorf("installation is not safe to switch")
		}
		if j.SchemaVersion != 1 || validateRecord(j.Next, j.AppID) != nil || j.Old != nil && validateRecord(*j.Old, j.AppID) != nil {
			return j.AppID, fmt.Errorf("invalid installation journal")
		}
		if j.OldDesired != nil && (j.Old == nil || j.OldDesired.SchemaVersion != 1 || j.OldDesired.State != "running" || !opaquePattern.MatchString(j.OldDesired.Revision)) {
			return j.AppID, fmt.Errorf("invalid original desired state")
		}
		switch j.Phase {
		case "prepared":
			return j.AppID, s.rollback(dir, j)
		case "committed":
			var current Record
			if err := readJSON(filepath.Join(s.root, j.AppID, "install.json"), &current); err != nil || current.Revision != j.Next.Revision {
				return j.AppID, fmt.Errorf("committed installation record mismatch")
			}
			digest, err := treeDigest(filepath.Join(s.root, j.AppID, "app"), false)
			if err != nil || digest != j.Next.ContentSHA256 {
				return j.AppID, fmt.Errorf("committed installation code mismatch")
			}
			return j.AppID, removeTree(dir)
		default:
			return j.AppID, fmt.Errorf("unknown installation journal phase")
		}
	case strings.HasPrefix(name, "delete-"):
		var j deleteJournal
		if err := readJSON(filepath.Join(dir, "delete.json"), &j); os.IsNotExist(err) {
			return "", removeTree(dir)
		} else if err != nil {
			return "", err
		}
		if j.SchemaVersion != 1 || !idPattern.MatchString(j.AppID) {
			return "", fmt.Errorf("invalid deletion journal")
		}
		if s.unavailable[j.AppID] != "" {
			return j.AppID, fmt.Errorf("installation is not safe to delete")
		}
		return j.AppID, s.finishDelete(dir, j)
	default:
		return "", fmt.Errorf("unknown WebApp transaction")
	}
}
func (s *Service) rollback(txn string, j installJournal) error {
	dir := filepath.Join(s.root, j.AppID)
	if j.Old == nil {
		if err := removeTree(dir); err != nil {
			return err
		}
	} else {
		backup := filepath.Join(txn, "old")
		if _, err := os.Lstat(backup); err == nil {
			if err := removeTree(filepath.Join(dir, "app")); err != nil {
				return err
			}
			if err := renamePackage(backup, filepath.Join(dir, "app")); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		digest, err := treeDigest(filepath.Join(dir, "app"), true)
		if err != nil || digest != j.Old.ContentSHA256 {
			return fmt.Errorf("rollback code integrity mismatch")
		}
		if err := writeJSON(filepath.Join(dir, "install.json"), j.Old); err != nil {
			return err
		}
	}
	if j.OldDesired != nil {
		if err := writeJSON(filepath.Join(dir, "desired.json"), j.OldDesired); err != nil {
			return err
		}
	}
	if err := syncDir(s.root); err != nil {
		return err
	}
	return removeTree(txn)
}
func (s *Service) finishDelete(txn string, j deleteJournal) error {
	if err := removeTree(filepath.Join(s.root, j.AppID)); err != nil {
		return err
	}
	if j.PurgeData {
		if err := removeTree(filepath.Join(s.dataRoot, j.AppID)); err != nil {
			return err
		}
		if err := syncDir(s.dataRoot); err != nil {
			return err
		}
	}
	if err := syncDir(s.root); err != nil {
		return err
	}
	return removeTree(txn)
}
