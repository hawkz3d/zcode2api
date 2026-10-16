package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/store"
)

// legacyListOrderCR35 is the EXACT /admin/api/accounts id sequence t5
// measured from the live legacy service over the current production pool
// (compat-risks.md CR-35): zai group first (status providers field order,
// NOT lexical), each group ascending by created_at. rowid order was
// decisively falsified inside the zai group.
var legacyListOrderCR35 = []string{
	"oauth-login-ea92aa60",     // created 1790328959.0
	"oauth-apikey-c5c641a1",    //         1790328961.4
	"oauth-login-8523aba4",     //          1790329509.2
	"oauth-apikey-8808fbac",    //         1790329520.1
	"laohao-bigmodel-2420b3c2", //      1789147671.8
	"xinhao-2000w-02f71f86",    //         1789148266.1
}

// TestListAccountsCR35Order asserts the frozen order over the production
// snapshot copy. Deliberately NOT verifiable via the diff harness (its
// normalize rules sort $.accounts by id, erasing exactly this dimension),
// so this unit assertion is the guard the contract itself mandates.
func TestListAccountsCR35Order(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenReadOnly(ctx, snapshotOfProduction(t))
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer st.Close()
	accs, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// Property assertions - hold for ANY pool shape, always enforced:
	seenGroup := map[string]bool{}
	groupOrder := []string{}
	for _, a := range accs {
		if !seenGroup[a.Provider] {
			seenGroup[a.Provider] = true
			groupOrder = append(groupOrder, a.Provider)
		}
	}
	if got, want := strings.Join(groupOrder, ","), strings.Join(store.ProviderGroupOrder, ","); got != want && seenGroup["bigmodel"] && seenGroup["zai"] {
		t.Errorf("provider group order = %q, want %q (providers field order, NOT lexical)", got, want)
	}
	byGroup := map[string][]models.Account{}
	for _, a := range accs {
		byGroup[a.Provider] = append(byGroup[a.Provider], a)
	}
	for g, list := range byGroup {
		for i := 1; i < len(list); i++ {
			// ties legal, descent not; equal keys fall back to the stable rowid base
			if a, b := list[i-1].CreatedAt, list[i].CreatedAt; a != nil && b != nil && *a > *b {
				t.Errorf("group %q: created_at descends at index %d (%v > %v)", g, i, *a, *b)
			}
		}
	}

	// Exact assertion - pinned to the measured production pool; loud skip
	// (never silent) if the pool has since changed.
	var ids []string
	for _, a := range accs {
		ids = append(ids, a.ID)
	}
	if len(ids) != len(legacyListOrderCR35) {
		t.Skipf("production pool now has %d accounts (baseline measured 6): exact-order pin needs a re-measure, property checks above still ran", len(ids))
	}
	if got := strings.Join(ids, ","); got != strings.Join(legacyListOrderCR35, ",") {
		t.Errorf("exact ListAccounts order drifted from the CR-35 measurement:\n got %v\nwant %v", ids, legacyListOrderCR35)
	}
}

// TestListAccountsCR35SyntheticContradictions pins the rule where real
// production data cannot: a bigmodel account created EARLIER than the zai
// ones must still render in the second group, and a later rowid with an
// earlier created_at must sort before it (the anti-rowid direction t5
// proved).
func TestListAccountsCR35SyntheticContradictions(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cr35.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	mk := func(id, provider string, created float64) {
		c := created
		a := models.Account{ID: id, Provider: provider, Enabled: 1, CreatedAt: &c,
			Data: json.RawMessage(`{"id":"` + id + `"}`)}
		if err := st.UpsertAccount(ctx, a); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	// insertion order deliberately crafted so ROWID order is WRONG:
	mk("z-old-00000001", models.ProviderZai, 100)    // rowid 1
	mk("b-ancient-0002", models.ProviderBigmodel, 1) // rowid 2 - earliest created, still SECOND group
	mk("z-mid-00000003", models.ProviderZai, 300)    // rowid 3
	mk("z-new-00000004", models.ProviderZai, 200)    // rowid 4 - earlier created_at than rowid 3

	accs, err := st.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got []string
	for _, a := range accs {
		got = append(got, fmt.Sprintf("%s", a.ID))
	}
	want := []string{"z-old-00000001", "z-new-00000004", "z-mid-00000003", "b-ancient-0002"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v (provider groups first, created_at asc within group; rowid must not leak)", got, want)
	}
}
