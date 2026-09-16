package chat

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// MigrateLegacyDesktopSource is used only by the explicit offline management
// command after verifying the current Desktop principal. It does not alter JSONL,
// attachment contents or any user-owned source other than the legacy query:app.
func MigrateLegacyDesktopSource(ctx context.Context, chatsRoot, backupDir, subject string) (map[string]int64, error) {
	if !regexp.MustCompile(`^desktop-user:[a-f0-9]{64}$`).MatchString(subject) {
		return nil, fmt.Errorf("verified Desktop subject is required")
	}
	result := map[string]int64{}
	for _, entry := range []struct{ path, table, label string }{
		{filepath.Join(chatsRoot, "chats.db"), "CHATS", "active"},
		{filepath.Join(chatsRoot, "archive", "archive.db"), "ARCHIVED_CHATS", "archived"},
	} {
		info, err := os.Lstat(entry.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("chat database must be a regular file")
		}
		db, err := sql.Open("sqlite", entry.path)
		if err != nil {
			return nil, err
		}
		count, err := migrateLegacyDesktopDatabase(ctx, db, entry.table, filepath.Join(backupDir, entry.label+".db"), subject)
		closeErr := db.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		result[entry.label] = count
	}
	return result, nil
}

func migrateLegacyDesktopDatabase(ctx context.Context, db *sql.DB, table, backup, subject string) (int64, error) {
	// Table names are fixed by the two service-owned schemas, never input values.
	if table != "CHATS" && table != "ARCHIVED_CHATS" {
		return 0, fmt.Errorf("unsupported source table")
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE SOURCE_=?", "query:app").Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		return 0, err
	}
	if info, err := os.Lstat(backup); os.IsNotExist(err) {
		temporary, err := os.CreateTemp(filepath.Dir(backup), ".database-backup-*")
		if err != nil {
			return 0, err
		}
		name := temporary.Name()
		temporary.Close()
		defer os.Remove(name)
		// VACUUM INTO produces a transactionally consistent standalone SQLite snapshot,
		// including committed WAL data. Publish the backup only when complete.
		if _, err = db.ExecContext(ctx, "VACUUM INTO ?", name); err != nil {
			return 0, err
		}
		if err = os.Chmod(name, 0600); err != nil {
			return 0, err
		}
		if err = os.Rename(name, backup); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	} else if !info.Mode().IsRegular() || info.Size() == 0 {
		return 0, fmt.Errorf("invalid legacy owner database backup")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	changed, err := tx.ExecContext(ctx, "UPDATE "+table+" SET SOURCE_=? WHERE SOURCE_=?", "query:"+subject, "query:app")
	if err != nil {
		return 0, err
	}
	count, err = changed.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
