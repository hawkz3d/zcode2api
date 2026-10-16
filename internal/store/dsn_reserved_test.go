package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/store"
)

// TestDSNReservedCharPaths guards the t5-finding / t11-repair: dsn() embeds
// a caller-supplied path into a SQLite URI, so every URI-grammar terminator
// must survive escaping VERBATIM. The defect signature is SILENT
// MISPLACEMENT - Open/EnsureSchema/Upsert all return success while the bytes
// land at a truncated path ("with?question.db" opening as file "with" +
// query "question.db"). Therefore "no error" assertions prove nothing; each
// case asserts:
//
//	a) the claimed path materialized as a real file,
//	b) no stray files appeared in its directory (allowing WAL sidecars),
//	c) a reopen through the claimed path reads back the row we wrote.
//
// Case "trap%2Fslash.db" additionally pins the escape ORDER: a literal %XX
// already in a user path must not be smuggled through as its decoded char,
// and fixing % must not double-escape the %3F/%23 we ourselves introduce
// (path stays intact ⇒ the %3F never appeared ⇒ order correct).
func TestDSNReservedCharPaths(t *testing.T) {
	ctx := context.Background()
	cases := []struct{ name, why string }{
		{"with?question.db", "? opens the URI query string (the captain-reproduced defect)"},
		{"tag#fragment.db", "# opens the URI fragment"},
		{"pct%ent.db", "% introduces URI escapes"},
		{"trap%2Fslash.db", "pre-existing %XX must not decode into a path char"},
		{"combo?a#b%c.db", "all three reserved chars at once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.name)

			st, err := store.Open(ctx, path)
			if err != nil {
				t.Fatalf("%s: open claimed path: %v", tc.why, err)
			}
			if st.Path() != path {
				t.Errorf("Path() = %q, want the claimed %q", st.Path(), path)
			}
			if err := st.EnsureSchema(ctx); err != nil {
				t.Fatalf("EnsureSchema: %v", err)
			}
			probe := models.Account{
				ID: "dsnfix-01234567", Provider: models.ProviderZai, Enabled: 1,
				CreatedAt: ptrFloat(1792100000.5),
				Data:      json.RawMessage(`{"probe":"t11","enabled":true}`),
			}
			if err := st.UpsertAccount(ctx, probe); err != nil {
				t.Fatalf("UpsertAccount: %v", err)
			}
			st.Close()

			// (a) decisive: the claimed path exists on disk.
			if _, err := os.Stat(path); err != nil {
				entries, _ := os.ReadDir(dir)
				var got []string
				for _, e := range entries {
					got = append(got, e.Name())
				}
				t.Fatalf("claimed path %q never materialized (%v); dir actually holds %v — silent misplacement is back", filepath.Base(path), err, got)
			}
			// (b) nothing else materialized (WAL sidecars of OUR file allowed).
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}
			for _, e := range entries {
				n := e.Name()
				if n != tc.name && n != tc.name+"-wal" && n != tc.name+"-shm" && n != tc.name+"-journal" {
					t.Errorf("stray file %q: writes landed outside the claimed path", n)
				}
			}
			// (c) data round-trips through the claimed path, untouched by
			// the escaping layer.
			reop, err := store.OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatalf("reopen read-only: %v", err)
			}
			defer reop.Close()
			accs, err := reop.ListAccounts(ctx)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(accs) != 1 || accs[0].ID != "dsnfix-01234567" {
				t.Fatalf("round trip got %d accounts %+v, want our probe row", len(accs), accs)
			}
		})
	}
}

// TestDSNProductionPathRegression is t11 case 4: the escaping change must
// not disturb the boring, reserved-char-free absolute production path.
// Read-only, zero writes (the t3 iron rule still applies).
func TestDSNProductionPathRegression(t *testing.T) {
	ctx := context.Background()
	path := requireProductionDB(t)
	st, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("open production path read-only: %v", err)
	}
	defer st.Close()
	if st.Path() != path {
		t.Errorf("Path() = %q, want %q", st.Path(), path)
	}
	accs, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accs) < 1 {
		t.Fatalf("production path must still yield accounts, got %d", len(accs))
	}
	t.Logf("production path regression OK: %d accounts readable through escaped dsn", len(accs))
}

func ptrFloat(f float64) *float64 { return &f }
