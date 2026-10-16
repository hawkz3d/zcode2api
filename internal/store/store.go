// Package store is the SQLite persistence layer for zhipucode2api.
//
// It talks to the very same database file the legacy Python service created
// (accounts + meta tables, WAL journal, pure-Go driver modernc.org/sqlite —
// no cgo) so the two services can be compared field-for-field and the new
// one can take over without any schema migration.
//
// Hard safety rule (encoded in Snapshot): a running WAL-mode database must
// NEVER be copied with a bare file copy of its main file — that drops WAL
// frames and can yield a corrupt/truncated snapshot. The only sanctioned way
// to duplicate a live database here is `VACUUM INTO` over a read-only
// connection (or, outside tests, the sqlite backup API).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"zhipucode2api/internal/models"
)

// DriverName is the database/sql driver used throughout.
const DriverName = "sqlite"

// ErrReadOnly is returned by every mutating method on a read-only Store.
// The underlying SQLite connection rejects writes as well; this guard keeps
// the failure loud and typed at the API boundary.
var ErrReadOnly = errors.New("store: database handle is read-only")

// Store is a handle on one SQLite database file.
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
}

// Open opens dbPath for reading and writing, enabling WAL and a busy
// timeout on every pooled connection.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	return openWith(ctx, dbPath, false)
}

// OpenReadOnly opens dbPath strictly read-only. Any write attempt — from us
// or from a bug that bypassed our guards — is refused by SQLite itself
// (SQLITE_READONLY).
func OpenReadOnly(ctx context.Context, dbPath string) (*Store, error) {
	return openWith(ctx, dbPath, true)
}

func openWith(ctx context.Context, dbPath string, readOnly bool) (*Store, error) {
	if dbPath == "" {
		return nil, errors.New("store: empty database path")
	}
	db, err := sql.Open(DriverName, dsn(dbPath, readOnly))
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", dbPath, err)
	}
	// sql.Open is lazy; run one cheap query to surface open failures now.
	var probe int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&probe); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %q: %w", dbPath, err)
	}
	// Keep the pool tight: SQLite is a single-writer engine; one connection
	// avoids SQLITE_BUSY ping-pong between our own writes.
	db.SetMaxOpenConns(1)
	return &Store{db: db, path: dbPath, readOnly: readOnly}, nil
}

// dsn builds a modernc.org/sqlite URI. mode and _pragma are honoured by the
// driver (verified against the live production WAL database on 2026-10-15).
//
// Escaping (t11 / t5 finding): the path is embedded into URI GRAMMAR, so
// every character that terminates or redirects a URI must be percent-encoded,
// or the file silently lands somewhere else - "a?b.db" opens as "a" with
// "b.db" as the query: zero errors, wrong location (see
// docs/contract/verification-methodology.md §九). Order is load-bearing:
// '%' must be escaped FIRST, otherwise the '%' introducers we add for '?'
// and '#' get escaped a second time (%3F -> %253F) and decode to a different
// name than claimed.
func dsn(path string, readOnly bool) string {
	p := filepath.ToSlash(path)
	p = strings.ReplaceAll(p, "%", "%25") // URI escape introducer - FIRST
	p = strings.ReplaceAll(p, "?", "%3F") // else opens the query section
	p = strings.ReplaceAll(p, "#", "%23") // else opens the fragment section
	q := "_pragma=busy_timeout(5000)"
	if readOnly {
		return "file:" + p + "?mode=ro&" + q
	}
	// mode=rwc: read-write, create the file if it does not exist (fresh
	// databases). mode=rw would require the file to already be present.
	return "file:" + p + "?mode=rwc&_pragma=journal_mode(WAL)&" + q
}

// Path returns the database file behind this handle.
func (s *Store) Path() string { return s.path }

// ReadOnly reports whether the handle refuses writes.
func (s *Store) ReadOnly() bool { return s.readOnly }

// Close releases the underlying pool.
func (s *Store) Close() error { return s.db.Close() }

// JournalMode returns the current PRAGMA journal_mode value.
func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var m string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&m); err != nil {
		return "", fmt.Errorf("store: journal_mode: %w", err)
	}
	return m, nil
}

// Snapshot copies srcPath to dstPath using VACUUM INTO over a read-only
// connection: the source database is never modified, and — unlike a bare
// file copy — pending WAL frames are included in the result. dstPath must
// not exist (SQLite refuses to overwrite). This is the sanctioned way to
// duplicate the live production database for tests or migration checks.
//
// Concurrency — the exact production situation, since the legacy service
// keeps writing while we snapshot: VACUUM INTO reads the source through a
// single SQLite read transaction. Under WAL mode a reader observes the
// last-committed snapshot (MVCC), so concurrent writers neither block it
// nor tear it; every snapshot is a whole-transaction-consistent database.
// busy_timeout(5000) on the snapshot connection bounds the only real
// contention window (wal-index header updates at commit instants).
// TestSnapshotAtomicUnderConcurrentWriters verifies this empirically: a
// tight multi-row-transaction writer with auto-checkpoint disabled (WAL
// permanently dirty — the bare-cp failure mode) racing 24 snapshots, each
// integrity-clean with the cross-row invariant intact. Known edge: a WAL
// database with a missing and uncreatable -shm cannot be opened mode=ro;
// irrelevant here because snapshots are taken precisely while the legacy
// service runs (it keeps -shm alive) and the data dir is writable.
func Snapshot(ctx context.Context, srcPath, dstPath string) error {
	db, err := sql.Open(DriverName, dsn(srcPath, true))
	if err != nil {
		return fmt.Errorf("store: snapshot open %q: %w", srcPath, err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dstPath); err != nil {
		return fmt.Errorf("store: snapshot %q into %q: %w", srcPath, dstPath, err)
	}
	return nil
}

// Schema mirrors the legacy production DDL (probed from the live
// accounts.db on 2026-10-15, matching docs/contract/baseline-snapshot). It
// uses IF NOT EXISTS so an existing production database is left untouched;
// on a fresh file it reproduces exactly the old schema, indexes included.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s.readOnly {
		return ErrReadOnly
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
                    id          TEXT PRIMARY KEY,
                    provider    TEXT NOT NULL,
                    name        TEXT,
                    mode        TEXT,
                    status      TEXT,
                    enabled     INTEGER NOT NULL DEFAULT 1,
                    created_at  REAL,
                    data        TEXT NOT NULL
                )`,
		`CREATE TABLE IF NOT EXISTS meta (
                    key   TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                )`,
		`CREATE INDEX IF NOT EXISTS idx_acc_provider ON accounts (provider)`,
		`CREATE INDEX IF NOT EXISTS idx_acc_status ON accounts (status)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("store: ensure schema: %w", err)
		}
	}
	return nil
}

const accountColumns = "id, provider, name, mode, status, enabled, created_at, data"

type rowScanner interface{ Scan(dest ...any) error }

func scanAccount(row rowScanner) (models.Account, error) {
	var (
		a         models.Account
		name      sql.NullString
		mode      sql.NullString
		status    sql.NullString
		createdAt sql.NullFloat64
		data      []byte
	)
	if err := row.Scan(&a.ID, &a.Provider, &name, &mode, &status, &a.Enabled, &createdAt, &data); err != nil {
		return models.Account{}, err
	}
	if name.Valid {
		v := name.String
		a.Name = &v
	}
	if mode.Valid {
		v := mode.String
		a.Mode = &v
	}
	if status.Valid {
		v := status.String
		a.Status = &v
	}
	if createdAt.Valid {
		v := createdAt.Float64
		a.CreatedAt = &v
	}
	a.Data = json.RawMessage(append([]byte(nil), data...))
	return a, nil
}

// ProviderGroupOrder is the FROZEN display order of account groups
// (compat-risks CR-35): the legacy /admin/api/accounts array groups by
// provider in the order of the status endpoint's "providers" field,
// ["zai","bigmodel"], each group ascending by created_at. Beware the trap:
// a plain SQL "ORDER BY provider" sorts LEXICALLY and yields bigmodel
// first - exactly backwards. The order is user-visible (UI list) even
// though the diff harness currently normalizes it away (sort_by=id rule),
// so it is enforced here and asserted by unit tests, never by the diff.
var ProviderGroupOrder = []string{models.ProviderZai, models.ProviderBigmodel}

// providerRank maps a provider to its group position; unknown providers
// sort AFTER the known ones, among themselves by name (pass-through
// providers exist per account-model A-1; their group order is unverified,
// this choice is deterministic and documented).
func providerRank(p string) (int, string) {
	for i, known := range ProviderGroupOrder {
		if p == known {
			return i, ""
		}
	}
	return len(ProviderGroupOrder), p
}

// createdOf is the in-group sort key. A NULL created_at (column nullable;
// never observed in production rows - unverified branch) sorts first inside
// its group.
func createdOf(a models.Account) float64 {
	if a.CreatedAt == nil {
		return math.Inf(-1)
	}
	return *a.CreatedAt
}

// ListAccounts returns every account in the frozen CR-35 order:
// (provider group order, created_at ascending). The SQL keeps ORDER BY
// rowid purely as a deterministic base so the STABLE sort below is total
// even for fully equal keys; rowid itself is NOT the contract (t5 measured
// rowid out of order within the zai group while created_at was strictly
// ascending).
func (s *Store) ListAccounts(ctx context.Context) ([]models.Account, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+accountColumns+" FROM accounts ORDER BY rowid")
	if err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	defer rows.Close()
	var out []models.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan account: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list accounts: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, tieI := providerRank(out[i].Provider)
		rj, tieJ := providerRank(out[j].Provider)
		if ri != rj {
			return ri < rj
		}
		if tieI != tieJ { // unknown-provider group: deterministic tie-break
			return tieI < tieJ
		}
		return createdOf(out[i]) < createdOf(out[j])
	})
	return out, nil
}

// GetAccount fetches one account by primary key.
func (s *Store) GetAccount(ctx context.Context, id string) (models.Account, bool, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id = ?", id)
	a, err := scanAccount(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return models.Account{}, false, nil
	case err != nil:
		return models.Account{}, false, fmt.Errorf("store: get account %q: %w", id, err)
	}
	return a, true, nil
}

// UpsertAccount inserts or fully replaces the row for a.ID, preserving the
// rowid on update (INSERT OR REPLACE — the legacy semantics — would move a
// replaced account to the end of rowid order; ON CONFLICT does not).
func (s *Store) UpsertAccount(ctx context.Context, a models.Account) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if a.ID == "" {
		return errors.New("store: upsert: empty account id")
	}
	if a.Provider == "" {
		return errors.New("store: upsert: empty provider")
	}
	if len(a.Data) == 0 {
		return fmt.Errorf("store: upsert %q: empty data column (NOT NULL in legacy schema)", a.ID)
	}
	if _, err := models.ParseAccountData(a.Data); err != nil {
		return fmt.Errorf("store: upsert %q: invalid data column: %w", a.ID, err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO accounts (`+accountColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider    = excluded.provider,
			name        = excluded.name,
			mode        = excluded.mode,
			status      = excluded.status,
			enabled     = excluded.enabled,
			created_at  = excluded.created_at,
			data        = excluded.data`,
		a.ID, a.Provider, nullableString(a.Name), nullableString(a.Mode),
		nullableString(a.Status), a.Enabled, nullableFloat(a.CreatedAt), []byte(a.Data))
	if err != nil {
		return fmt.Errorf("store: upsert account %q: %w", a.ID, err)
	}
	return nil
}

// DeleteAccount removes one account; ok=false when the id is unknown.
func (s *Store) DeleteAccount(ctx context.Context, id string) (bool, error) {
	if s.readOnly {
		return false, ErrReadOnly
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("store: delete account %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: delete account %q: %w", id, err)
	}
	return n > 0, nil
}

// SetEnabled flips the enabled state of one account. Per ADR-003 (2026-10-15,
// established experimentally against the live legacy service) enabled is a
// DOUBLE WRITE kept inside one transaction:
//
//   - the INTEGER enabled column (1/0), and
//   - the data JSON "enabled" member as a strict JSON bool.
//
// Writing only the column would be a functional defect: the API outputs the
// data JSON value (ADR-002 root cause), so a column-only disable would still
// report the account as enabled — contradicting itself.
//
// Fidelity (ADR-003 §2): exactly one data JSON member changes; every other
// member survives byte-for-byte, including historical dirty values under
// other keys. The touched "enabled" member becomes a clean bool even when it
// previously held the legacy dirty "True" string — that is deliberate ("正常
// 写 bool"); unrelated operations never touch it because they go through
// UpsertAccount, which writes the caller's data verbatim.
//
// ok=false when the id is unknown. The single-connection pool plus the
// read-modify-write transaction guarantees observers never see column and
// JSON diverge.
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) (bool, error) {
	if s.readOnly {
		return false, ErrReadOnly
	}
	col := int64(0)
	if enabled {
		col = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: set enabled %q: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // no-op after commit

	var data []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM accounts WHERE id = ?`, id).Scan(&data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("store: set enabled %q: read data: %w", id, err)
	}
	d, err := models.ParseAccountData(data)
	if err != nil {
		return false, fmt.Errorf("store: set enabled %q: existing data column invalid: %w", id, err)
	}
	if err := d.SetEnabled(enabled); err != nil {
		return false, fmt.Errorf("store: set enabled %q: %w", id, err)
	}
	merged, err := d.Serialize()
	if err != nil {
		return false, fmt.Errorf("store: set enabled %q: remarshal data: %w", id, err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE accounts SET enabled = ?, data = ? WHERE id = ?`, col, merged, id)
	if err != nil {
		return false, fmt.Errorf("store: set enabled %q: update: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: set enabled %q: rows affected: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: set enabled %q: commit: %w", id, err)
	}
	return n > 0, nil
}

// GetSetting reads one meta KV entry.
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("store: get setting %q: %w", key, err)
	}
	return v, true, nil
}

// SetSetting writes one meta KV entry (insert or replace).
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if key == "" {
		return errors.New("store: empty setting key")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: set setting %q: %w", key, err)
	}
	return nil
}

func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
