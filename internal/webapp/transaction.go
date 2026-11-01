package webapp

import (
	"fmt"
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

func (s *Service) recover() error {
	entries, err := os.ReadDir(filepath.Join(s.root, ".staging"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "uploads" {
			continue
		}
		if !e.IsDir() {
			return fmt.Errorf("unexpected staging file")
		}
		dir := filepath.Join(s.root, ".staging", name)
		switch {
		case strings.HasPrefix(name, "review-"):
			if err := removeTree(dir); err != nil {
				return err
			}
		case strings.HasPrefix(name, "install-"):
			var j installJournal
			if err := readJSON(filepath.Join(dir, "journal.json"), &j); os.IsNotExist(err) {
				if err := removeTree(dir); err != nil {
					return err
				}
				continue
			} else if err != nil {
				return err
			}
			if j.SchemaVersion != 1 || validateRecord(j.Next, j.AppID) != nil || j.Old != nil && validateRecord(*j.Old, j.AppID) != nil {
				return fmt.Errorf("invalid installation journal")
			}
			if j.OldDesired != nil && (j.Old == nil || j.OldDesired.SchemaVersion != 1 || j.OldDesired.State != "running" || !opaquePattern.MatchString(j.OldDesired.Revision)) {
				return fmt.Errorf("invalid original desired state")
			}
			if j.Phase == "prepared" {
				if err := s.rollback(dir, j); err != nil {
					return err
				}
			} else if j.Phase == "committed" {
				var current Record
				if err := readJSON(filepath.Join(s.root, j.AppID, "install.json"), &current); err != nil || current.Revision != j.Next.Revision {
					return fmt.Errorf("committed installation record mismatch")
				}
				digest, err := treeDigest(filepath.Join(s.root, j.AppID, "app"), false)
				if err != nil || digest != j.Next.ContentSHA256 {
					return fmt.Errorf("committed installation code mismatch")
				}
				if err := removeTree(dir); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("unknown installation journal phase")
			}
		case strings.HasPrefix(name, "delete-"):
			var j deleteJournal
			if err := readJSON(filepath.Join(dir, "delete.json"), &j); os.IsNotExist(err) {
				if err := removeTree(dir); err != nil {
					return err
				}
				continue
			} else if err != nil {
				return err
			}
			if j.SchemaVersion != 1 || !idPattern.MatchString(j.AppID) {
				return fmt.Errorf("invalid deletion journal")
			}
			if err := s.finishDelete(dir, j); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown WebApp transaction")
		}
	}
	return nil
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
