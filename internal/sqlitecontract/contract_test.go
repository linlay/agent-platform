package sqlitecontract

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInitializeOrVerifyAtStartupClaimsExactUnmarkedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := openTestDB(t, path)
	defer db.Close()
	if err := createContractTestSchema(db); err != nil {
		t.Fatal(err)
	}

	if err := InitializeOrVerifyAtStartup(db, path, filepath.Dir(path), contractTestSpec(), nil, func() error {
		t.Fatal("existing unmarked database must not run create callback")
		return nil
	}); err != nil {
		t.Fatalf("claim exact unmarked database: %v", err)
	}
	assertMarker(t, db, contractTestSpec().ApplicationID, contractTestSpec().UserVersion)
	if err := Verify(db, path, filepath.Dir(path), contractTestSpec()); err != nil {
		t.Fatalf("verify claimed database: %v", err)
	}
}

func TestUnmarkedSchemaIsNeverClaimedOutsideStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := openTestDB(t, path)
	defer db.Close()
	if err := createContractTestSchema(db); err != nil {
		t.Fatal(err)
	}

	err := InitializeOrVerify(db, path, filepath.Dir(path), contractTestSpec(), nil, func() error {
		t.Fatal("existing unmarked database must not run create callback")
		return nil
	})
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("strict open error = %v, want unsupported schema", err)
	}
	assertMarker(t, db, 0, 0)
}

func TestStartupClaimIgnoresPhysicalColumnOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db := openTestDB(t, path)
	defer db.Close()
	if err := createContractTestSchemaDifferentColumnOrder(db); err != nil {
		t.Fatal(err)
	}
	if err := InitializeOrVerifyAtStartup(db, path, filepath.Dir(path), contractTestSpec(), nil, func() error {
		t.Fatal("existing unmarked database must not run create callback")
		return nil
	}); err != nil {
		t.Fatalf("claim semantically identical reordered schema: %v", err)
	}
	assertMarker(t, db, contractTestSpec().ApplicationID, contractTestSpec().UserVersion)
}

func TestStartupClaimRejectsEverySchemaDifferenceWithoutWritingMarker(t *testing.T) {
	cases := map[string]string{
		"extra column":  "ALTER TABLE ITEMS ADD COLUMN EXTRA_ TEXT",
		"extra table":   "CREATE TABLE EXTRA_ITEMS (ID_ INTEGER PRIMARY KEY)",
		"extra index":   "CREATE INDEX IDX_EXTRA_ITEMS_NAME ON ITEMS(NAME_)",
		"extra trigger": "CREATE TRIGGER ITEMS_EXTRA AFTER INSERT ON ITEMS BEGIN SELECT 1; END",
		"missing FTS":   "DROP TABLE ITEMS_FTS",
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			db := openTestDB(t, path)
			defer db.Close()
			if err := createContractTestSchema(db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(mutate); err != nil {
				t.Fatalf("apply mismatch: %v", err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			err = InitializeOrVerifyAtStartup(db, path, filepath.Dir(path), contractTestSpec(), nil, func() error {
				t.Fatal("incompatible database must not run create callback")
				return nil
			})
			if !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatalf("claim error = %v, want unsupported schema", err)
			}
			assertMarker(t, db, 0, 0)
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("rejected database bytes changed")
			}
		})
	}
}

func TestStartupClaimRejectsPartialForeignAndVersionMarkers(t *testing.T) {
	cases := map[string][2]int{
		"partial application ID": {contractTestSpec().ApplicationID, 0},
		"partial user version":   {0, contractTestSpec().UserVersion},
		"foreign application ID": {0x44524147, contractTestSpec().UserVersion},
		"old user version":       {contractTestSpec().ApplicationID, 0},
		"future user version":    {contractTestSpec().ApplicationID, contractTestSpec().UserVersion + 1},
	}
	for name, marker := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			db := openTestDB(t, path)
			defer db.Close()
			if err := createContractTestSchema(db); err != nil {
				t.Fatal(err)
			}
			setMarker(t, db, marker[0], marker[1])
			err := InitializeOrVerifyAtStartup(db, path, filepath.Dir(path), contractTestSpec(), nil, func() error {
				t.Fatal("marked database must not run create callback")
				return nil
			})
			if !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatalf("claim error = %v, want unsupported schema", err)
			}
			assertMarker(t, db, marker[0], marker[1])
		})
	}
}

func contractTestSpec() Spec {
	return Spec{
		Name:           "sqlitecontract-test-v1",
		ApplicationID:  0x41505453, // APTS
		UserVersion:    1,
		BuildCanonical: createContractTestSchema,
	}
}

func createContractTestSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE ITEMS (
			ID_ INTEGER PRIMARY KEY,
			NAME_ TEXT NOT NULL DEFAULT '',
			ENABLED_ INTEGER NOT NULL DEFAULT 1,
			CHECK (ENABLED_ IN (0, 1))
		);
		CREATE UNIQUE INDEX IDX_ITEMS_NAME ON ITEMS(NAME_) WHERE ENABLED_ = 1;
		CREATE VIRTUAL TABLE ITEMS_FTS USING fts5(NAME_, content=ITEMS, content_rowid=rowid);
		CREATE TRIGGER ITEMS_AI AFTER INSERT ON ITEMS BEGIN
			INSERT INTO ITEMS_FTS(rowid, NAME_) VALUES (new.rowid, new.NAME_);
		END;
	`)
	return err
}

func createContractTestSchemaDifferentColumnOrder(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE ITEMS (
			NAME_ TEXT NOT NULL DEFAULT '',
			ENABLED_ INTEGER NOT NULL DEFAULT 1,
			ID_ INTEGER PRIMARY KEY,
			CHECK (ENABLED_ IN (0, 1))
		);
		CREATE UNIQUE INDEX IDX_ITEMS_NAME ON ITEMS(NAME_) WHERE ENABLED_ = 1;
		CREATE VIRTUAL TABLE ITEMS_FTS USING fts5(NAME_, content=ITEMS, content_rowid=rowid);
		CREATE TRIGGER ITEMS_AI AFTER INSERT ON ITEMS BEGIN
			INSERT INTO ITEMS_FTS(rowid, NAME_) VALUES (new.rowid, new.NAME_);
		END;
	`)
	return err
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func setMarker(t *testing.T, db *sql.DB, applicationID, userVersion int) {
	t.Helper()
	if _, err := db.Exec("PRAGMA application_id = " + strconv.Itoa(applicationID)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(userVersion)); err != nil {
		t.Fatal(err)
	}
}

func assertMarker(t *testing.T, db *sql.DB, applicationID, userVersion int) {
	t.Helper()
	var actualApplicationID, actualUserVersion int
	if err := db.QueryRow("PRAGMA application_id").Scan(&actualApplicationID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&actualUserVersion); err != nil {
		t.Fatal(err)
	}
	if actualApplicationID != applicationID || actualUserVersion != userVersion {
		t.Fatalf("marker = (%d,%d), want (%d,%d)", actualApplicationID, actualUserVersion, applicationID, userVersion)
	}
}
