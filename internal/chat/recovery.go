package chat

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/sqlitecontract"
)

// ValidateRecovery checks a stopped store without adopting schemas, opening a
// runtime, repairing records, or writing to the source.
func ValidateRecovery(root string) error {
	for _, item := range []struct {
		relative string
		spec     sqlitecontract.Spec
	}{
		{"chats.db", chatSchemaSpec}, {"archive/archive.db", archiveSchemaSpec},
	} {
		path := filepath.Join(root, item.relative)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			var residual bool
			if item.relative == "chats.db" {
				residual, err = sqlitecontract.HasResidualData(root, "archive")
			} else {
				residual, err = sqlitecontract.HasResidualData(filepath.Dir(path))
			}
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if residual {
				return fmt.Errorf("recovery database is missing but data remains: %s", path)
			}
			continue
		} else if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("recovery requires a regular database: %s", path)
		}
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			return err
		}
		err = sqlitecontract.Verify(db, path, root, item.spec)
		if err == nil {
			var result string
			err = db.QueryRow("PRAGMA integrity_check").Scan(&result)
			if err == nil && result != "ok" {
				err = fmt.Errorf("database integrity check failed: %s", path)
			}
		}
		if err == nil && item.relative == "archive/archive.db" {
			var rows *sql.Rows
			rows, err = db.Query("SELECT CHAT_ID_, JSONL_CONTENT_ FROM ARCHIVED_CHATS")
			if err == nil {
				for rows.Next() {
					var id, content string
					if err = rows.Scan(&id, &content); err != nil {
						break
					}
					if err = ValidateJSONLContent(content, "archive/"+id); err != nil {
						break
					}
				}
				if err == nil {
					err = rows.Err()
				}
				rows.Close()
			}
		}
		db.Close()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("chat recovery refuses symbolic link: %s", path)
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(relative, string(filepath.Separator))
		// Uploaded JSONL and compact backups are opaque resources, not history.
		if len(parts) != 1 && !(len(parts) == 3 && (parts[1] == ".btw" || parts[1] == ".explain")) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return ValidateJSONLContent(string(content), path)
	})
}
