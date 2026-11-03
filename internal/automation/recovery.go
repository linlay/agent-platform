package automation

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
)

// ValidateRecovery accepts only current execution history. Unlike the normal
// history worker, recovery must not rebuild a legacy store as an empty database.
func ValidateRecovery(root string) error {
	path := filepath.Join(root, defaultExecutionDBFileName)
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
	if err := validateExecutionSchema(db, path); err != nil {
		return err
	}
	return validateExecutionIntegrity(db)
}
