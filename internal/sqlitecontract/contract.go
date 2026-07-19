// Package sqlitecontract enforces the single supported on-disk SQLite schema
// for runtime stores. It deliberately provides no migration facility.
package sqlitecontract

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrUnsupportedSchema = errors.New("unsupported storage schema")

// Object is a sqlite_master object that must be present in the current schema.
type Object struct {
	Type string
	Name string
}

// Column identifies a table column that is not part of the current contract.
type Column struct {
	Table string
	Name  string
}

// Spec identifies the only schema version a store accepts.
type Spec struct {
	ApplicationID    int
	UserVersion      int
	Objects          []Object
	ForbiddenColumns []Column
}

// UnsupportedSchemaError tells operators which runtime directory must be
// cleared before starting this intentionally incompatible release.
type UnsupportedSchemaError struct {
	DatabasePath string
	CleanupPath  string
	Reason       string
}

func (e *UnsupportedSchemaError) Error() string {
	if e == nil {
		return ErrUnsupportedSchema.Error()
	}
	return fmt.Sprintf("%s at %s: %s; remove %s and restart", ErrUnsupportedSchema, e.DatabasePath, e.Reason, e.CleanupPath)
}

func (e *UnsupportedSchemaError) Unwrap() error {
	return ErrUnsupportedSchema
}

// InitializeOrVerify creates a database only when its file does not exist.
// Existing databases are verified before any DDL can run, so opening an old
// runtime can never modify it.
func InitializeOrVerify(db *sql.DB, databasePath, cleanupPath string, spec Spec, hasResidualData func() (bool, error), create func() error) error {
	exists, err := databaseFileExists(databasePath)
	if err != nil {
		return err
	}
	if exists {
		return Verify(db, databasePath, cleanupPath, spec)
	}
	if hasResidualData != nil {
		residual, err := hasResidualData()
		if err != nil {
			return err
		}
		if residual {
			return unsupported(databasePath, cleanupPath, "database is missing but the runtime directory contains residual data")
		}
	}
	if err := create(); err != nil {
		return err
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA application_id = %d", spec.ApplicationID)); err != nil {
		return fmt.Errorf("set sqlite application id: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", spec.UserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	return Verify(db, databasePath, cleanupPath, spec)
}

// Verify checks the marker and the objects required by the current schema.
func Verify(db *sql.DB, databasePath, cleanupPath string, spec Spec) error {
	var applicationID, userVersion int
	if err := db.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		return fmt.Errorf("read sqlite application id: %w", err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		return fmt.Errorf("read sqlite user version: %w", err)
	}
	if applicationID != spec.ApplicationID || userVersion != spec.UserVersion {
		return unsupported(databasePath, cleanupPath, fmt.Sprintf("expected application_id=%d and user_version=%d, got application_id=%d and user_version=%d", spec.ApplicationID, spec.UserVersion, applicationID, userVersion))
	}
	for _, object := range spec.Objects {
		var found int
		err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE lower(type) = lower(?) AND lower(name) = lower(?) LIMIT 1`, object.Type, object.Name).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return unsupported(databasePath, cleanupPath, fmt.Sprintf("missing required %s %s", object.Type, object.Name))
		}
		if err != nil {
			return fmt.Errorf("verify sqlite object %s %s: %w", object.Type, object.Name, err)
		}
	}
	for _, column := range spec.ForbiddenColumns {
		rows, err := db.Query(`PRAGMA table_info(` + quoteIdentifier(column.Table) + `)`)
		if err != nil {
			return fmt.Errorf("inspect sqlite table %s: %w", column.Table, err)
		}
		found := false
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, dataType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan sqlite table %s: %w", column.Table, err)
			}
			if strings.EqualFold(name, column.Name) {
				found = true
				break
			}
		}
		rowsErr := rows.Err()
		_ = rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("inspect sqlite table %s: %w", column.Table, rowsErr)
		}
		if found {
			return unsupported(databasePath, cleanupPath, fmt.Sprintf("contains removed column %s.%s", column.Table, column.Name))
		}
	}
	return nil
}

// HasResidualData reports whether root has entries other than the explicitly
// ignored names. A missing root is treated as empty.
func HasResidualData(root string, ignoredNames ...string) (bool, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ignored := make(map[string]struct{}, len(ignoredNames))
	for _, name := range ignoredNames {
		ignored[strings.TrimSpace(name)] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := ignored[entry.Name()]; !ok {
			return true, nil
		}
	}
	return false, nil
}

func databaseFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("sqlite database path %s is a directory", path)
	}
	return true, nil
}

func unsupported(databasePath, cleanupPath, reason string) error {
	return Unsupported(databasePath, cleanupPath, reason)
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// Unsupported builds the standard destructive-upgrade error for callers that
// need to validate store-specific metadata in addition to SQLite markers.
func Unsupported(databasePath, cleanupPath, reason string) error {
	return &UnsupportedSchemaError{
		DatabasePath: filepath.Clean(databasePath),
		CleanupPath:  filepath.Clean(cleanupPath),
		Reason:       reason,
	}
}
