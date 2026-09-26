package memory

import (
	"reflect"
	"testing"

	"agent-platform/internal/timecontract"
)

func TestSQLiteAccessMetadataAcrossReads(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir(), "memory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	// Bypass write normalization to exercise nullable persisted metadata.
	_, err = store.db.Exec(`INSERT INTO MEMORIES (ID_, TS_, UPDATED_AT_, SUMMARY_, ACCESS_COUNT_) VALUES ('record', ?, ?, 'needle', NULL)`, testEpochMillis, testEpochMillis)
	if err != nil {
		t.Fatal(err)
	}
	for _, accessed := range []bool{false, true} {
		if accessed {
			if _, err := store.db.Exec(`UPDATE MEMORIES SET ACCESS_COUNT_ = 7, LAST_ACCESSED_AT_ = ?`, testEpochMillis); err != nil {
				t.Fatal(err)
			}
		}
		projection, err := store.readStoredMemoryByIDLocked("record", "memory.sqlite.projection")
		if err != nil {
			t.Fatal(err)
		}
		wantCount := 0
		var wantTime *int64
		if accessed {
			wantCount = 7
			stamp := testEpochMillis
			wantTime = &stamp
		}
		if projection.AccessCount != wantCount || !reflect.DeepEqual(projection.LastAccessedAt, wantTime) {
			t.Fatalf("projection access metadata: %#v", projection)
		}
		listed, err := store.List("", "", 10, "recent")
		if err != nil || len(listed) != 1 || listed[0].AccessCount != wantCount || !reflect.DeepEqual(listed[0].LastAccessedAt, wantTime) {
			t.Fatalf("tool access metadata: %v %#v", err, listed)
		}
		matches, err := store.ftsSearch("needle", 10)
		if err != nil || len(matches) != 1 || !reflect.DeepEqual(matches[0].item, *projection) {
			t.Fatalf("scored projection: %v %#v", err, matches)
		}
		read, err := store.Read("record")
		if err != nil || !reflect.DeepEqual(read, projection) {
			t.Fatalf("Read must return pre-access snapshot: %v %#v", err, read)
		}
	}
}

func TestSQLiteInvalidLastAccessPreventsReadBookkeeping(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir(), "memory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	_, err = store.db.Exec(`INSERT INTO MEMORIES (ID_, TS_, UPDATED_AT_, SUMMARY_, LAST_ACCESSED_AT_) VALUES ('invalid', ?, ?, 'needle', 0)`, testEpochMillis, testEpochMillis)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("invalid"); !timecontract.IsViolation(err) {
		t.Fatalf("Read: expected time violation, got %v", err)
	}
	if _, err := store.ftsSearch("needle", 10); !timecontract.IsViolation(err) {
		t.Fatalf("search: expected time violation, got %v", err)
	}
	var count int
	var last int64
	if err := store.db.QueryRow(`SELECT ACCESS_COUNT_, LAST_ACCESSED_AT_ FROM MEMORIES WHERE ID_='invalid'`).Scan(&count, &last); err != nil || count != 0 || last != 0 {
		t.Fatalf("invalid row was modified: %v %d %d", err, count, last)
	}
}
