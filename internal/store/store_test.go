package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/store"
)

// productionDBEnv names the environment variable that points at the live
// legacy database. The path is deployment-specific and therefore never
// hard-coded here: set ZCODE_TEST_DB to the deployed accounts.db to enable
// the snapshot-parity tests.
//
// Tests NEVER open it for writing: they VACUUM INTO a temp snapshot and act
// only on the copy.
const productionDBEnv = "ZCODE_TEST_DB"

// requireProductionDB locates the legacy database. The snapshot-parity tests
// are the field-level read-equivalence baseline (cutover acceptance B3) and
// run wherever the production data lives; elsewhere they skip loudly rather
// than silently passing.
func requireProductionDB(t *testing.T) string {
	t.Helper()
	p := os.Getenv(productionDBEnv)
	if p == "" {
		t.Skipf("%s is not set: snapshot-parity tests need the deployed accounts.db "+
			"(e.g. %s=/path/to/accounts.db go test ./internal/store/)", productionDBEnv, productionDBEnv)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("production database %q unavailable: %v", p, err)
	}
	return p
}

func snapshotOfProduction(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "snapshot.db")
	if err := store.Snapshot(context.Background(), requireProductionDB(t), dst); err != nil {
		t.Fatalf("snapshot production db: %v", err)
	}
	return dst
}

// TestProductionSchemaReadable pins the field-level read-equivalence base:
// the pure-Go driver must read every account and meta row the Python service
// wrote, with correct Go types and non-empty data JSON.
func TestProductionSchemaReadable(t *testing.T) {
	snap := snapshotOfProduction(t)
	st, err := store.OpenReadOnly(context.Background(), snap)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	accs, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accs) < 1 {
		t.Fatalf("expected >=1 account in production snapshot, got %d", len(accs))
	}
	for _, a := range accs {
		if a.ID == "" || a.Provider == "" {
			t.Errorf("account missing id/provider: %+v", a)
		}
		if len(a.Data) == 0 {
			t.Errorf("account %q has empty data column", a.ID)
			continue
		}
		d, err := a.ParseData()
		if err != nil {
			t.Errorf("account %q data not a JSON object: %v", a.ID, err)
			continue
		}
		// Known legacy fields must be present in real production rows.
		if _, ok := d.String(models.DataKeyStatus); !ok {
			t.Errorf("account %q data missing status", a.ID)
		}
	}

	// meta table readable. The admin_key row holds a real secret — we assert
	// only its presence/shape, never log the value.
	if v, ok, err := st.GetSetting(ctx, "admin_key"); err != nil || !ok {
		t.Errorf("meta admin_key read: ok=%v err=%v", ok, err)
	} else if v == "" {
		t.Errorf("meta admin_key empty")
	}
	if _, ok, err := st.GetSetting(ctx, "gateway_key"); err != nil || !ok {
		t.Errorf("meta gateway_key read: ok=%v err=%v", ok, err)
	}
}

// TestSnapshotIsConsistentUnderWAL exercises the safety rule: a snapshot
// taken via VACUUM INTO from the WAL-mode production db contains every row;
// a bare cp of the main file could miss WAL frames. We can't easily prove
// the negative here, but we assert full parity against a read-only direct
// listing so the snapshot is demonstrably complete.
func TestSnapshotParityWithLiveReadOnly(t *testing.T) {
	ctx := context.Background()
	live, err := store.OpenReadOnly(ctx, requireProductionDB(t))
	if err != nil {
		t.Fatalf("open live ro: %v", err)
	}
	defer live.Close()
	liveAccs, err := live.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list live: %v", err)
	}

	snap := snapshotOfProduction(t)
	st, err := store.OpenReadOnly(ctx, snap)
	if err != nil {
		t.Fatalf("open snap: %v", err)
	}
	defer st.Close()
	snapAccs, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list snap: %v", err)
	}
	if len(snapAccs) != len(liveAccs) {
		t.Fatalf("snapshot count %d != live ro count %d", len(snapAccs), len(liveAccs))
	}
	for i := range liveAccs {
		if liveAccs[i].ID != snapAccs[i].ID {
			t.Errorf("row %d differs: live %q snap %q", i, liveAccs[i].ID, snapAccs[i].ID)
		}
	}
}

// TestSnapshotRefusesExistingDestination guards against silently overwriting
// a previous snapshot file.
func TestSnapshotRefusesExistingDestination(t *testing.T) {
	src := requireProductionDB(t)
	dst := filepath.Join(t.TempDir(), "dup.db")
	ctx := context.Background()
	if err := store.Snapshot(ctx, src, dst); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := store.Snapshot(ctx, src, dst); err == nil {
		t.Fatalf("second snapshot into same path must fail")
	}
}

// CRUD runs against an empty EnsureSchema db: this is the write path the
// gateway will use after cutover, verified without touching production.
func TestCRUDLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// Re-ensure is a no-op on an existing schema (IF NOT EXISTS).
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("re-ensure schema: %v", err)
	}

	name := "测试号"
	mode := models.ModeAPIKey
	status := models.StatusActive
	created := 1789148266.5
	acc := models.Account{
		ID:        "test-acc-0001",
		Provider:  models.ProviderZai,
		Name:      &name,
		Mode:      &mode,
		Status:    &status,
		Enabled:   1,
		CreatedAt: &created,
		Data:      json.RawMessage(`{"id":"test-acc-0001","api_key":"FAKE","nonsense":{"keep":true}}`),
	}
	if err := st.UpsertAccount(ctx, acc); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := st.GetAccount(ctx, acc.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Provider != models.ProviderZai || got.EnabledBool() != true {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Name == nil || *got.Name != name {
		t.Errorf("name round trip failed")
	}
	d, err := got.ParseData()
	if err != nil {
		t.Fatalf("parse data: %v", err)
	}
	if _, ok := d.Raw("nonsense"); !ok {
		t.Errorf("unknown data key dropped through store round trip")
	}

	// Upsert the same id with changes -> fully replaced, single row.
	updated := acc
	updated.Status = strptr(models.StatusDisabled)
	updated.Enabled = 0
	if err := st.UpsertAccount(ctx, updated); err != nil {
		t.Fatalf("upsert replace: %v", err)
	}
	list, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 row after replace upsert, got %d", len(list))
	}
	if list[0].EnabledBool() {
		t.Errorf("enabled replace not persisted")
	}

	// SetEnabled double-writes (ADR-003): column AND data JSON bool, in sync.
	found, err := st.SetEnabled(ctx, acc.ID, true)
	if err != nil || !found {
		t.Fatalf("set enabled: found=%v err=%v", found, err)
	}
	got, _, _ = st.GetAccount(ctx, acc.ID)
	if !got.EnabledBool() {
		t.Errorf("SetEnabled column not persisted")
	}
	if raw := rawDataEnabled(t, got); raw != "true" {
		t.Errorf("SetEnabled data JSON not double-written: enabled=%q, want bool true", raw)
	}
	if !got.EffectiveEnabled() {
		t.Errorf("effective/API view still false after enable")
	}
	if _, err := st.SetEnabled(ctx, acc.ID, false); err != nil {
		t.Fatalf("set disabled: %v", err)
	}
	got, _, _ = st.GetAccount(ctx, acc.ID)
	if got.EnabledBool() {
		t.Errorf("SetEnabled(false) column still set")
	}
	if raw := rawDataEnabled(t, got); raw != "false" {
		t.Errorf("SetEnabled(false) data JSON: enabled=%q, want bool false", raw)
	}
	if got.EffectiveEnabled() {
		t.Errorf("ADR-003 acceptance: after disable the effective/API view must be false (the column-only-write defect sentinel)")
	}
	dAfter, err := got.ParseData()
	if err != nil {
		t.Fatalf("parse after toggles: %v", err)
	}
	for _, k := range []string{"api_key", "nonsense"} {
		if _, ok := dAfter.Raw(k); !ok {
			t.Errorf("member %q dropped by SetEnabled", k)
		}
	}
	// Unknown id reports not-found without error.
	if found, err := st.SetEnabled(ctx, "no-such-id", true); err != nil || found {
		t.Errorf("SetEnabled unknown id: found=%v err=%v", found, err)
	}
	if found, err := st.DeleteAccount(ctx, "no-such-id"); err != nil || found {
		t.Errorf("Delete unknown id: found=%v err=%v", found, err)
	}

	// meta CRUD.
	if err := st.SetSetting(ctx, "quota_refresh_interval", "60"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if v, ok, err := st.GetSetting(ctx, "quota_refresh_interval"); err != nil || !ok || v != "60" {
		t.Errorf("setting round trip: v=%q ok=%v err=%v", v, ok, err)
	}
	if err := st.SetSetting(ctx, "quota_refresh_interval", "90"); err != nil {
		t.Fatalf("overwrite setting: %v", err)
	}
	if v, _, _ := st.GetSetting(ctx, "quota_refresh_interval"); v != "90" {
		t.Errorf("setting overwrite failed, got %q", v)
	}
	if _, ok, err := st.GetSetting(ctx, "absent-key"); err != nil || ok {
		t.Errorf("absent key: ok=%v err=%v", ok, err)
	}

	// delete the account.
	if found, err := st.DeleteAccount(ctx, acc.ID); err != nil || !found {
		t.Fatalf("delete: found=%v err=%v", found, err)
	}
	if _, ok, _ := st.GetAccount(ctx, acc.ID); ok {
		t.Errorf("account still present after delete")
	}
}

func strptr(s string) *string { return &s }

// TestWALModeEnabled asserts an Open (read-write) handle runs the database
// in WAL journal mode, matching the production layout.
func TestWALModeEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal.db")
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	mode, err := st.JournalMode(ctx)
	if err != nil {
		t.Fatalf("journal mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// TestReadOnlyRejectsWrites proves the read-only handle used against the
// live production db can never mutate it — our cutover safety guarantee.
func TestReadOnlyRejectsWrites(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenReadOnly(ctx, requireProductionDB(t))
	if err != nil {
		t.Fatalf("open live ro: %v", err)
	}
	defer st.Close()
	if err := st.UpsertAccount(ctx, models.Account{ID: "x", Provider: "zai", Data: json.RawMessage(`{}`)}); err != store.ErrReadOnly {
		t.Errorf("upsert on ro: want ErrReadOnly, got %v", err)
	}
	if err := st.SetSetting(ctx, "k", "v"); err != store.ErrReadOnly {
		t.Errorf("set setting on ro: want ErrReadOnly, got %v", err)
	}
	if err := st.EnsureSchema(ctx); err != store.ErrReadOnly {
		t.Errorf("ensure schema on ro: want ErrReadOnly, got %v", err)
	}
}

// TestUpsertValidation ensures we cannot write a row the legacy schema would
// reject or that would corrupt shared reads.
func TestUpsertValidation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "val.db")
	ctx := context.Background()
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := st.UpsertAccount(ctx, models.Account{ID: "", Provider: "zai", Data: json.RawMessage(`{}`)}); err == nil {
		t.Errorf("empty id accepted")
	}
	if err := st.UpsertAccount(ctx, models.Account{ID: "a", Provider: "", Data: json.RawMessage(`{}`)}); err == nil {
		t.Errorf("empty provider accepted")
	}
	if err := st.UpsertAccount(ctx, models.Account{ID: "a", Provider: "zai", Data: nil}); err == nil {
		t.Errorf("empty data accepted")
	}
	if err := st.UpsertAccount(ctx, models.Account{ID: "a", Provider: "zai", Data: json.RawMessage(`not json`)}); err == nil {
		t.Errorf("non-JSON data accepted")
	}
	if err := st.UpsertAccount(ctx, models.Account{ID: "a", Provider: "zai", Data: json.RawMessage(`[]`)}); err == nil {
		t.Errorf("JSON array data accepted (must be object)")
	}
}

// TestProductionSnapshotEnabledNormalized is the ADR-002 "normalisation in
// effect" evidence the captain demanded: reading the *same* production
// snapshot, every account's EffectiveEnabled() is true — including the dirty
// row laohao-bigmodel-2420b3c2 whose data JSON carries the string "True" —
// while the raw storage bytes stay untouched (存储层保持保真). If the account
// pool later gains a genuinely disabled account this assertion goes red on
// purpose, forcing an explicit update rather than silent drift.
func TestProductionSnapshotEnabledNormalized(t *testing.T) {
	ctx := context.Background()
	snap := snapshotOfProduction(t)
	st, err := store.OpenReadOnly(ctx, snap)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer st.Close()

	accs, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accs) < 6 {
		t.Fatalf("expected the 6 production accounts, got %d", len(accs))
	}

	dirtyRows := 0
	for _, a := range accs {
		if !a.EffectiveEnabled() {
			t.Errorf("account %q: EffectiveEnabled() = false, want true (ADR-002: 6 行全为启用)", a.ID)
		}
		d, err := a.ParseData()
		if err != nil {
			t.Errorf("account %q: parse data: %v", a.ID, err)
			continue
		}
		raw, ok := d.Raw(models.DataKeyEnabled)
		if !ok {
			continue
		}
		if string(raw) == `"True"` {
			dirtyRows++
			// Requirement 3: the dirty string must not error and must
			// normalise to true.
			v, known := d.Enabled()
			if !known || !v {
				t.Errorf("account %q: dirty \"True\" did not normalise: v=%v known=%v", a.ID, v, known)
			}
		}
		// Requirement 2: the raw bytes survive a marshal round trip — the
		// dirty value is NOT rewritten by reading.
		out, err := json.Marshal(d)
		if err != nil {
			t.Errorf("account %q: remarshal: %v", a.ID, err)
			continue
		}
		d2, err := models.ParseAccountData(out)
		if err != nil {
			t.Errorf("account %q: reparse remarshal: %v", a.ID, err)
			continue
		}
		if raw2, ok := d2.Raw(models.DataKeyEnabled); !ok || string(raw2) != string(raw) {
			t.Errorf("account %q: enabled raw bytes mutated by round trip: %s -> %s", a.ID, raw, raw2)
		}
	}
	if dirtyRows < 1 {
		t.Errorf("scenario coverage: no production row carries the dirty \"True\" value any more")
	}
}

// TestSnapshotAtomicUnderConcurrentWriters answers the captain's review
// question: what happens when Snapshot races a live writer (the exact
// production situation — the Python service refreshes quotas continuously).
//
// Design: reproduce the legacy write shape on a THROWAWAY db (never
// production): multi-row transactions under WAL, autocheckpoint disabled so
// the WAL always carries un-checkpointed frames — precisely the state where
// a bare `cp` of the main file loses data. VACUUM INTO must every time yield
// an integrity-clean database that shows the writer's cross-row invariant
// (counter(a)+counter(b)==100) fully applied or not at all: a torn or
// frame-losing snapshot shows up immediately as a broken sum.
func TestSnapshotAtomicUnderConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "live-wal.db")

	boot, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := boot.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seed := func(id string, v int64) {
		a := models.Account{ID: id, Provider: models.ProviderZai, Enabled: 1,
			Data: json.RawMessage(fmt.Sprintf(`{"counter":%d}`, v))}
		if err := boot.UpsertAccount(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("acc-a", 60)
	seed("acc-b", 40)
	boot.Close()

	// The "legacy service": independent connection, tight loop of two-row
	// transactions, WAL kept dirty (wal_autocheckpoint=0).
	wdb, err := sql.Open(store.DriverName, "file:"+dbPath+"?mode=rwc&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatalf("writer open: %v", err)
	}
	defer wdb.Close()
	wdb.SetMaxOpenConns(1)

	werr := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		x, dir := int64(60), int64(1)
		for {
			select {
			case <-stop:
				werr <- nil
				return
			default:
			}
			x += dir
			if x >= 100 || x <= 0 {
				dir = -dir
				x += dir
			}
			tx, err := wdb.BeginTx(ctx, nil)
			if err != nil {
				werr <- fmt.Errorf("begin: %w", err)
				return
			}
			if _, err := tx.Exec(`UPDATE accounts SET data = ? WHERE id = 'acc-a'`, fmt.Sprintf(`{"counter":%d}`, x)); err != nil {
				_ = tx.Rollback()
				werr <- fmt.Errorf("update a: %w", err)
				return
			}
			if _, err := tx.Exec(`UPDATE accounts SET data = ? WHERE id = 'acc-b'`, fmt.Sprintf(`{"counter":%d}`, 100-x)); err != nil {
				_ = tx.Rollback()
				werr <- fmt.Errorf("update b: %w", err)
				return
			}
			if err := tx.Commit(); err != nil {
				werr <- fmt.Errorf("commit: %w", err)
				return
			}
		}
	}()

	const rounds = 24
	sawDirtyWAL := 0
	for i := 0; i < rounds; i++ {
		dst := filepath.Join(t.TempDir(), fmt.Sprintf("snap-%d.db", i))
		if err := store.Snapshot(ctx, dbPath, dst); err != nil {
			t.Fatalf("round %d: snapshot failed while db was being written: %v", i, err)
		}
		if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() > 0 {
			sawDirtyWAL++
		}

		// Every snapshot must be a whole-transaction, integrity-clean db.
		st, err := store.OpenReadOnly(ctx, dst)
		if err != nil {
			t.Fatalf("round %d: open snapshot: %v", i, err)
		}
		accs, err := st.ListAccounts(ctx)
		st.Close()
		if err != nil {
			t.Fatalf("round %d: list snapshot: %v", i, err)
		}
		if len(accs) != 2 {
			t.Fatalf("round %d: want 2 accounts, got %d", i, len(accs))
		}
		sum := int64(0)
		for _, a := range accs {
			d, derr := a.ParseData()
			if derr != nil {
				t.Fatalf("round %d: snapshot state is not valid JSON: %v", i, derr)
			}
			c, ok := d.Int("counter")
			if !ok {
				t.Fatalf("round %d: counter missing in account %q", i, a.ID)
			}
			sum += c
		}
		if sum != 100 {
			t.Fatalf("round %d: torn snapshot — counter sum=%d, want 100 (VACUUM INTO must see complete transactions)", i, sum)
		}

		cdb, err := sql.Open(store.DriverName, "file:"+dst+"?mode=ro")
		if err != nil {
			t.Fatalf("round %d: integrity open: %v", i, err)
		}
		var check string
		err = cdb.QueryRow("PRAGMA integrity_check").Scan(&check)
		cdb.Close()
		if err != nil {
			t.Fatalf("round %d: integrity check query: %v", i, err)
		}
		if check != "ok" {
			t.Fatalf("round %d: snapshot corrupted: integrity_check = %s", i, check)
		}
	}

	// If the writer already failed during the rounds, report it now.
	select {
	case err := <-werr:
		if err != nil {
			t.Fatalf("concurrent writer failed while snapshotting: %v", err)
		}
	default:
	}
	// Stop the writer first, then drain its terminal send; the deferred
	// wdb.Close() tears the handle down afterwards. Closing the db while
	// the writer still runs would surface a bogus "database is closed".
	close(stop)
	if err := <-werr; err != nil {
		t.Fatalf("concurrent writer failed while snapshotting: %v", err)
	}
	if sawDirtyWAL == 0 {
		t.Fatalf("scenario not covered: no round observed un-checkpointed WAL frames — the test proved nothing about the bare-cp failure mode")
	}
	t.Logf("coverage: %d/%d snapshots raced un-checkpointed WAL frames, all atomic", sawDirtyWAL, rounds)
}

// TestProductionCopyKeepsLegacyKeyOrder is the sqlite-schema G-4 evidence:
// every production row's data JSON parses into the frozen 23-key order, the
// round trip ParseAccountData(TEXT).Serialize() reproduces the stored TEXT
// byte-for-byte (so our writes can never drift the text style), and after a
// SetEnabled double-write the order survives with only the "enabled" member
// changed. All on a throwaway VACUUM INTO copy.
func TestProductionCopyKeepsLegacyKeyOrder(t *testing.T) {
	// legacyDataKeyOrder is verbatim from sqlite-schema.md §4.1 (frozen).
	legacyDataKeyOrder := []string{
		"id", "name", "provider", "mode", "jwt_token", "api_key", "enabled",
		"status", "quota", "plan", "plans", "usage", "subscription",
		"use_count", "fail_count", "risk_strikes", "recent_results",
		"last_used_at", "last_checked_at", "cooling_until", "last_error",
		"created_at", "fingerprint",
	}
	ctx := context.Background()
	snap := snapshotOfProduction(t)
	st, err := store.Open(ctx, snap)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer st.Close()

	accs, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, a := range accs {
		d, err := a.ParseData()
		if err != nil {
			t.Fatalf("account %q: parse: %v", a.ID, err)
		}
		if got := d.Keys(); !reflect.DeepEqual(got, legacyDataKeyOrder) {
			t.Errorf("account %q: data key order deviates from frozen §4.1: %v", a.ID, got)
		}
		out, err := d.Serialize()
		if err != nil {
			t.Fatalf("account %q: serialize: %v", a.ID, err)
		}
		if string(out) != string(a.Data) {
			t.Errorf("account %q: parse→Serialize is not byte-identical to stored TEXT (style drift would corrupt DB-content diffs):\n got: %s\nwant: %s", a.ID, out, a.Data)
		}
	}

	// Double-write must not disturb the order either.
	const dirtyID = "laohao-bigmodel-2420b3c2"
	if _, err := st.SetEnabled(ctx, dirtyID, false); err != nil {
		t.Fatalf("set enabled: %v", err)
	}
	after, _, err := st.GetAccount(ctx, dirtyID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	d, err := after.ParseData()
	if err != nil {
		t.Fatalf("parse after: %v", err)
	}
	if got := d.Keys(); !reflect.DeepEqual(got, legacyDataKeyOrder) {
		t.Errorf("after SetEnabled key order drifted: %v", got)
	}
	if raw, _ := d.Raw(models.DataKeyEnabled); string(raw) != "false" {
		t.Errorf("after SetEnabled(false) enabled = %s, want bool false", raw)
	}
}

// rawDataEnabled returns the verbatim bytes of the data JSON "enabled"
// member ("" when absent) — used to assert write SHAPE, not just truth.
func rawDataEnabled(t *testing.T, a models.Account) string {
	t.Helper()
	d, err := a.ParseData()
	if err != nil {
		t.Fatalf("account %q: parse data: %v", a.ID, err)
	}
	raw, ok := d.Raw(models.DataKeyEnabled)
	if !ok {
		return ""
	}
	return string(raw)
}

// rawKeyMap snapshots every data JSON member as its verbatim bytes.
func rawKeyMap(t *testing.T, a models.Account) map[string]string {
	t.Helper()
	d, err := a.ParseData()
	if err != nil {
		t.Fatalf("account %q: parse data: %v", a.ID, err)
	}
	out := make(map[string]string)
	for _, k := range d.Keys() {
		raw, _ := d.Raw(k)
		out[k] = string(raw)
	}
	return out
}

func assertOnlyEnabledChanged(t *testing.T, id string, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if k == models.DataKeyEnabled {
			continue
		}
		if w, ok := after[k]; !ok {
			t.Errorf("account %s: member %q dropped (was %s)", id, k, v)
		} else if w != v {
			t.Errorf("account %s: member %q bytes rewritten: %s -> %s", id, k, v, w)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok && k != models.DataKeyEnabled {
			t.Errorf("account %s: member %q appeared out of nowhere", id, k)
		}
	}
}

// TestSetEnabledDoubleWriteOnProductionCopy is the ADR-003 acceptance
// evidence on REAL production data: toggling the dirty row
// laohao-bigmodel-2420b3c2 must move column AND data JSON in sync, replace
// the dirty "True" with a clean bool ONLY under "enabled" (the explicitly
// touched member), and leave every other member byte-for-byte identical.
// All writes go to a throwaway VACUUM INTO copy — production is never
// touched.
func TestSetEnabledDoubleWriteOnProductionCopy(t *testing.T) {
	ctx := context.Background()
	snap := snapshotOfProduction(t)
	st, err := store.Open(ctx, snap) // rw on the copy only
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer st.Close()

	const dirtyID = "laohao-bigmodel-2420b3c2"
	before, ok, err := st.GetAccount(ctx, dirtyID)
	if err != nil || !ok {
		t.Fatalf("production copy missing %q (ok=%v err=%v) — the ADR-002/003 dirty row vanished; update these tests deliberately if the DB was ever cleaned", dirtyID, ok, err)
	}
	beforeRaw := rawDataEnabled(t, before)
	if beforeRaw != `"True"` {
		t.Fatalf("precondition: expected the historical dirty value \"True\", found %s", beforeRaw)
	}
	if !before.EnabledBool() {
		t.Fatalf("precondition: column expected 1, got %d", before.Enabled)
	}
	beforeKeys := rawKeyMap(t, before)

	if _, err := st.SetEnabled(ctx, dirtyID, false); err != nil {
		t.Fatalf("set disabled: %v", err)
	}
	after, _, err := st.GetAccount(ctx, dirtyID)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.Enabled != 0 {
		t.Errorf("double write incomplete: column=%d, want 0", after.Enabled)
	}
	if raw := rawDataEnabled(t, after); raw != "false" {
		t.Errorf("double write incomplete: data enabled=%s, want clean bool false (not %q, not \"False\")", raw, beforeRaw)
	}
	if after.EffectiveEnabled() {
		t.Errorf("ADR-003: after disable the API-visible effective state must be false")
	}
	assertOnlyEnabledChanged(t, dirtyID, beforeKeys, rawKeyMap(t, after))

	if _, err := st.SetEnabled(ctx, dirtyID, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	after2, _, err := st.GetAccount(ctx, dirtyID)
	if err != nil {
		t.Fatalf("get after re-enable: %v", err)
	}
	if !after2.EnabledBool() || rawDataEnabled(t, after2) != "true" || !after2.EffectiveEnabled() {
		t.Errorf("re-enable not double-written in sync: column=%d data=%s", after2.Enabled, rawDataEnabled(t, after2))
	}

	// Durability across handle close: both writes were in one commit.
	st.Close()
	reop, err := store.OpenReadOnly(ctx, snap)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reop.Close()
	relaunched, ok, err := reop.GetAccount(ctx, dirtyID)
	if err != nil || !ok {
		t.Fatalf("relaunched get: ok=%v err=%v", ok, err)
	}
	if !relaunched.EnabledBool() || rawDataEnabled(t, relaunched) != "true" {
		t.Errorf("double write not durable: column=%d data=%s", relaunched.Enabled, rawDataEnabled(t, relaunched))
	}
}

// TestUnrelatedUpsertPreservesDirtyEnabled guards the ADR-003 §2 boundary:
// the dirty "True" may be replaced ONLY by an explicit enabled toggle. The
// rename path (read row → change Name → Upsert with untouched Data, exactly
// what legacy PUT /accounts/{id} does) must leave the dirty value
// byte-identical and the column unchanged. This mirrors the captain's probe
// experiment row "PUT 改名 → 未被误改". Writes hit a throwaway copy.
func TestUnrelatedUpsertPreservesDirtyEnabled(t *testing.T) {
	ctx := context.Background()
	snap := snapshotOfProduction(t)
	st, err := store.Open(ctx, snap)
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	defer st.Close()

	const dirtyID = "laohao-bigmodel-2420b3c2"
	before, ok, err := st.GetAccount(ctx, dirtyID)
	if err != nil || !ok {
		t.Fatalf("get %q: ok=%v err=%v", dirtyID, ok, err)
	}
	if rawDataEnabled(t, before) != `"True"` {
		t.Fatalf("precondition: dirty row no longer carries \"True\" — update this test deliberately")
	}
	beforeKeys := rawKeyMap(t, before)

	renamed := before
	newName := "rename-probe-only"
	renamed.Name = &newName
	if err := st.UpsertAccount(ctx, renamed); err != nil {
		t.Fatalf("rename upsert: %v", err)
	}

	after, _, err := st.GetAccount(ctx, dirtyID)
	if err != nil {
		t.Fatalf("get after rename: %v", err)
	}
	if after.Name == nil || *after.Name != newName {
		t.Errorf("rename did not land")
	}
	if raw := rawDataEnabled(t, after); raw != `"True"` {
		t.Errorf("unrelated rename rewrote the historical dirty value: %s", raw)
	}
	if after.Enabled != before.Enabled {
		t.Errorf("unrelated rename changed the column: %d -> %d", before.Enabled, after.Enabled)
	}
	assertOnlyEnabledChanged(t, dirtyID, beforeKeys, rawKeyMap(t, after)) // no member may change at all here
	if rawDataEnabled(t, after) != beforeKeys[models.DataKeyEnabled] {
		t.Errorf("enabled member bytes changed despite guard")
	}
}
