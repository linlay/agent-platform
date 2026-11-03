package mailbox

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"agent-platform/internal/sqlitecontract"
)

// ValidateRecovery uses the current Mailbox DDL without opening the service or
// performing its startup lifecycle reconciliation.
func ValidateRecovery(root string) error {
	path := filepath.Join(root, "mail.db")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	spec := sqlitecontract.Spec{Name: "mail-v1", ApplicationID: 0x41504D4C, UserVersion: 1,
		BuildCanonical: func(db *sql.DB) error { _, err := db.Exec(schema); return err }}
	if err := sqlitecontract.Verify(db, path, root, spec); err != nil {
		return err
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("mailbox integrity check failed: %s", path)
	}
	return nil
}
