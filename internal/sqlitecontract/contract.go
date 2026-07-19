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
	Name             string
	ApplicationID    int
	UserVersion      int
	Objects          []Object
	ForbiddenColumns []Column
	BuildCanonical   SchemaBuilder
}

// UnsupportedSchemaError identifies the database and the exact reason it was
// not accepted. The runtime never edits, migrates, or deletes a rejected
// database.
type UnsupportedSchemaError struct {
	DatabasePath string
	CleanupPath  string
	Reason       string
}

func (e *UnsupportedSchemaError) Error() string {
	if e == nil {
		return ErrUnsupportedSchema.Error()
	}
	return fmt.Sprintf("%s at %s: %s", ErrUnsupportedSchema, e.DatabasePath, e.Reason)
}

func (e *UnsupportedSchemaError) Unwrap() error {
	return ErrUnsupportedSchema
}

// InitializeOrVerify creates a database only when its file does not exist.
// Existing databases are verified before any DDL can run, so opening an old
// runtime can never modify it.
func InitializeOrVerify(db *sql.DB, databasePath, cleanupPath string, spec Spec, hasResidualData func() (bool, error), create func() error) error {
	return initializeOrVerify(db, databasePath, cleanupPath, spec, hasResidualData, create, false)
}

// InitializeOrVerifyAtStartup is the only entry point that may adopt an
// unmarked (0,0) database. It is deliberately separate from the ordinary
// runtime path so refreshes, reloads, reads, and API calls cannot change a
// database marker.
func InitializeOrVerifyAtStartup(db *sql.DB, databasePath, cleanupPath string, spec Spec, hasResidualData func() (bool, error), create func() error) error {
	return initializeOrVerify(db, databasePath, cleanupPath, spec, hasResidualData, create, true)
}

func initializeOrVerify(db *sql.DB, databasePath, cleanupPath string, spec Spec, hasResidualData func() (bool, error), create func() error, startupAdopt bool) error {
	exists, err := databaseFileExists(databasePath)
	if err != nil {
		return err
	}
	if exists {
		if startupAdopt {
			unmarked, err := isUnmarked(db)
			if err != nil {
				return err
			}
			if unmarked {
				if err := AdoptUnmarkedAtStartup(db, databasePath, cleanupPath, spec); err != nil {
					return err
				}
				return nil
			}
		}
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
	if err := VerifyStructure(db, databasePath, cleanupPath, spec); err != nil {
		return err
	}
	return nil
}

// AdoptUnmarkedAtStartup verifies a markerless database against the complete
// canonical schema before assigning this store's identity and version. It
// accepts exactly application_id=0 and user_version=0; any partial or foreign
// marker remains a hard failure.
func AdoptUnmarkedAtStartup(db *sql.DB, databasePath, cleanupPath string, spec Spec) error {
	unmarked, err := isUnmarked(db)
	if err != nil {
		return err
	}
	if !unmarked {
		return Verify(db, databasePath, cleanupPath, spec)
	}
	if err := VerifyStructure(db, databasePath, cleanupPath, spec); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin SQLite marker adoption: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA application_id = %d", spec.ApplicationID)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set adopted sqlite application id: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", spec.UserVersion)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set adopted sqlite user version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite marker adoption: %w", err)
	}
	return Verify(db, databasePath, cleanupPath, spec)
}

func isUnmarked(db *sql.DB) (bool, error) {
	var applicationID, userVersion int
	if err := db.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		return false, fmt.Errorf("read sqlite application id: %w", err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		return false, fmt.Errorf("read sqlite user version: %w", err)
	}
	return applicationID == 0 && userVersion == 0, nil
}

// IsUnmarked reports whether both SQLite identity fields are zero. Callers
// must not treat a partial zero marker as claimable.
func IsUnmarked(db *sql.DB) (bool, error) {
	return isUnmarked(db)
}

func verifyRequiredObjects(db *sql.DB, databasePath, cleanupPath string, spec Spec) error {
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
