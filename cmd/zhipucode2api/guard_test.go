package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/store"
)

// These tests pin the ADR-005 A2 guard's JUDGEMENT, not its plumbing. The
// t14 defect was never "the guard is missing" - it was that the guard's
// verdict was bound to a one-shot fact (who created the file), so the states
// it was supposed to catch walked through it on the next restart. Each case
// below therefore asserts the DECISION for a database state, and the
// assertions that matter are the ones where the OLD code returned nil.

// openTestDB opens a database in a fresh temp dir and returns its path.
func openTestDB(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "accounts.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", dbPath, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return dbPath, st
}

// seedAccount inserts one valid account so a database can be made non-empty.
func seedAccount(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if err := st.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	acct := models.Account{
		ID:       id,
		Provider: models.ProviderZai,
		Enabled:  1,
		Data:     json.RawMessage(`{"id":"` + id + `","provider":"zai"}`),
	}
	if err := st.UpsertAccount(context.Background(), acct); err != nil {
		t.Fatalf("UpsertAccount(%s): %v", id, err)
	}
}

// --- case 1: table-less database (the artefact the old guard left behind) -----

// A database with no accounts table is fatal EVEN THOUGH this process did not
// create it. Under the old code freshFile was false here, so the guard
// returned nil and the service listened on a database it could not read - the
// exact second-start accident reproduced live during t14.
func TestGuardRejectsTableLessDatabaseThatAlreadyExists(t *testing.T) {
	dbPath, st := openTestDB(t)
	// store.Open created the FILE but not the schema, so this is precisely the
	// leftover shape. It already exists, which is what defeats the old guard.
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("precondition: store.Open should have created %s: %v", dbPath, err)
	}

	_, err := guardPoolDatabase(context.Background(), dbPath, st)
	if err == nil {
		t.Fatal("guard accepted an existing database with no accounts table; " +
			"this is the t14 regression: the old code returned nil when freshFile was false")
	}
	if !strings.Contains(err.Error(), "no usable accounts table") {
		t.Fatalf("error should name the missing table, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ADR-005 A2") {
		t.Fatalf("error should cite the ADR, got: %v", err)
	}
}

// --- case 2: schema present but zero rows ------------------------------------

// Zero accounts is fatal by default, and the message must point at the opt-in
// so an operator with a genuine green-field deployment knows the way out.
func TestGuardRejectsEmptyPoolByDefaultAndNamesOptIn(t *testing.T) {
	dbPath, st := openTestDB(t)
	if err := st.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	_, err := guardPoolDatabase(context.Background(), dbPath, st)
	if err == nil {
		t.Fatal("guard accepted a schema-present database holding 0 accounts")
	}
	if !strings.Contains(err.Error(), "0 accounts") {
		t.Fatalf("error should report the empty pool, got: %v", err)
	}
	if !strings.Contains(err.Error(), EnvAllowEmptyPool) {
		t.Fatalf("error should name the opt-in variable %s, got: %v", EnvAllowEmptyPool, err)
	}
}

// --- case 3: the opt-in is honoured, and only for the literal "1" ------------

func TestGuardHonoursEmptyPoolOptIn(t *testing.T) {
	dbPath, st := openTestDB(t)
	if err := st.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	t.Setenv(EnvAllowEmptyPool, "1")
	n, err := guardPoolDatabase(context.Background(), dbPath, st)
	if err != nil {
		t.Fatalf("opt-in should allow an empty pool, got: %v", err)
	}
	if n != 0 {
		t.Fatalf("pool size should be 0, got %d", n)
	}
}

// Anything other than "1" leaves the guard refusing: a stray "true" or "yes"
// in a unit file must not silently disable the check that ADR-005 exists for.
func TestGuardOptInIsStrictlyLiteralOne(t *testing.T) {
	for _, v := range []string{"true", "yes", "on", "2", "01", ""} {
		t.Run("value="+v, func(t *testing.T) {
			dbPath, st := openTestDB(t)
			if err := st.EnsureSchema(context.Background()); err != nil {
				t.Fatalf("EnsureSchema: %v", err)
			}
			t.Setenv(EnvAllowEmptyPool, v)
			if _, err := guardPoolDatabase(context.Background(), dbPath, st); err == nil {
				t.Fatalf("%s=%q disabled the guard; only the literal \"1\" may opt in",
					EnvAllowEmptyPool, v)
			}
		})
	}
}

// --- case 4: a real pool passes and reports its size -------------------------

func TestGuardAcceptsNonEmptyPoolAndReportsSize(t *testing.T) {
	dbPath, st := openTestDB(t)
	seedAccount(t, st, "guard-probe-0001")
	seedAccount(t, st, "guard-probe-0002")

	n, err := guardPoolDatabase(context.Background(), dbPath, st)
	if err != nil {
		t.Fatalf("a 2-account pool must start, got: %v", err)
	}
	if n != 2 {
		t.Fatalf("pool size should be 2, got %d", n)
	}
}

// --- idempotence: the interception must not create the thing it refuses ------

// The t14 root cause in one assertion: refuse, then refuse AGAIN in the same
// directory. The old guard could only ever be right once, because its own
// interception left a table-less file that made every later start look fine.
func TestGuardRefusalIsIdempotentAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "accounts.db")

	// First start: the file does not exist, so it is "fresh" and removable.
	_, statErr := os.Stat(dbPath)
	freshFile := os.IsNotExist(statErr)
	if !freshFile {
		t.Fatal("precondition: the database should not exist yet")
	}
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := guardPoolDatabase(context.Background(), dbPath, st); err == nil {
		t.Fatal("first start should be refused")
	}
	// The caller's contract: a refused fresh database is removed.
	if err := failStartup(st, dbPath, freshFile, errRefused); err == nil {
		t.Fatal("failStartup should propagate the refusal cause")
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Fatalf("the fresh database must be removed on refusal, else the next "+
			"start finds a leftover and is let through; stat err = %v", statErr)
	}

	// Second start in the SAME directory: must be refused again. This is the
	// assertion the old code fails - there, the leftover made freshFile false
	// and the guard returned nil.
	st2, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open (second): %v", err)
	}
	defer st2.Close()
	if _, err := guardPoolDatabase(context.Background(), dbPath, st2); err == nil {
		t.Fatal("second start in the same directory was accepted: the refusal is " +
			"not idempotent, which is the exact t14 defect")
	}
}

// errRefused stands in for the guard's own error so the removal-path tests
// exercise failStartup without depending on the guard's message wording.
var errRefused = errors.New("probe: refused")

// --- removal safety: never delete a database we did not create ---------------

// A schema-less database that already existed may be the operator's real pool
// behind a wrong ZCODE_DATA_DIR. Refusing is right; deleting the evidence is
// not. failStartup must therefore leave an existing file alone.
func TestFailStartupKeepsExistingDatabase(t *testing.T) {
	dbPath, st := openTestDB(t)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("precondition: %s should exist: %v", dbPath, err)
	}
	if err := failStartup(st, dbPath, false, errRefused); err == nil {
		t.Fatal("failStartup should propagate the cause")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("an existing database must survive a refused startup: %v", err)
	}
}

// The sidecars store.Open may have produced are cleaned up with the file, so a
// refused fresh start leaves the directory exactly as it found it.
func TestRemoveSelfCreatedDBClearsSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "accounts.db")
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	if err := removeSelfCreatedDB(dbPath); err != nil {
		t.Fatalf("removeSelfCreatedDB: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("refusal should leave no residue, found: %v", names)
	}
}
