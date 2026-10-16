package models

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

// productionLike mirrors the real shape of a data JSON column: legacy
// credential fields, runtime statistics, the "True"-string enabled drift seen
// in production, and two foreign keys a future writer must not drop.
const productionLike = `{
	"id": "laohao-bigmodel-2420b3c2",
	"provider": "bigmodel",
	"name": "老号·BigModelKey",
	"mode": "apiKey",
	"status": "active",
	"enabled": "True",
	"created_at": 1789147671.7793698,
	"api_key": "FAKE-PLACEHOLDER-NOT-A-KEY",
	"jwt_token": null,
	"cooling_until": null,
	"fail_count": 0,
	"risk_strikes": 0,
	"use_count": 42,
	"last_used_at": 1789147700.5,
	"last_checked_at": null,
	"last_error": null,
	"quota": {},
	"plan": {},
	"plans": [],
	"subscription": {"no_plan": true, "level": null},
	"usage": {},
	"recent_results": [],
	"fingerprint": {"arch": "x64", "device_mid": "FAKE-MID", "platform": "linux"},
	"future_python_field": {"nested": [1, 2, 3]},
	"mystery_scalar": 1789147671.7793698
}`

func TestAccountDataRoundTripKeepsUnknownFields(t *testing.T) {
	d, err := ParseAccountData([]byte(productionLike))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out1, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Re-parse and re-serialise; the two outputs must be semantically equal.
	d2, err := ParseAccountData(out1)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	out2, err := json.Marshal(d2)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}

	var m1, m2 any
	if err := json.Unmarshal(out1, &m1); err != nil {
		t.Fatalf("decode out1: %v", err)
	}
	if err := json.Unmarshal(out2, &m2); err != nil {
		t.Fatalf("decode out2: %v", err)
	}
	if !reflect.DeepEqual(m1, m2) {
		t.Fatalf("round trip not semantically equal")
	}

	// Every original key survives, unknown ones included.
	var orig map[string]json.RawMessage
	if err := json.Unmarshal([]byte(productionLike), &orig); err != nil {
		t.Fatalf("decode orig: %v", err)
	}
	for k := range orig {
		if _, ok := d.Raw(k); !ok {
			t.Errorf("key %q lost", k)
		}
	}

	// The high-precision float survived byte-for-byte (no float64 re-parse).
	if raw, ok := d.Raw(DataKeyCreatedAt); !ok || string(raw) != "1789147671.7793698" {
		t.Errorf("created_at raw drifted: %s", raw)
	}
	if raw, ok := d2.Raw("mystery_scalar"); !ok || string(raw) != "1789147671.7793698" {
		t.Errorf("unknown float key drifted after round trip: %s", raw)
	}
}

func TestAccountDataAccessors(t *testing.T) {
	d, err := ParseAccountData([]byte(productionLike))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s, ok := d.String(DataKeyStatus); !ok || s != StatusActive {
		t.Errorf("status = %q ok=%v", s, ok)
	}
	if n, ok := d.Int(DataKeyUseCount); !ok || n != 42 {
		t.Errorf("use_count = %d ok=%v", n, ok)
	}
	if f, ok := d.Float(DataKeyLastUsedAt); !ok || f != 1789147700.5 {
		t.Errorf("last_used_at = %v ok=%v", f, ok)
	}
	if !d.IsNull(DataKeyJWTToken) {
		t.Errorf("jwt_token should be JSON null")
	}
	if _, ok := d.String(DataKeyJWTToken); ok {
		t.Errorf("null string read must report !ok")
	}
	if _, ok := d.Array(DataKeyPlans); !ok {
		t.Errorf("plans should read as array")
	}
	if _, ok := d.Object(DataKeyFingerprint); !ok {
		t.Errorf("fingerprint should read as object")
	}
	if _, ok := d.Float("api_key"); ok {
		t.Errorf("type-mismatched read must report !ok")
	}
}

// TestMarshalKeepsUntouchedMembersByteIdentical pins the ADR-003 §2
// fidelity rule at serialisation level: Python json.dumps writes with ", "
// and ": " separators; a mutate-one-key cycle must preserve every untouched
// member's bytes EXACTLY (whitespace included), changing only the touched
// member — which encoding/json's default RawMessage compaction would
// silently break.
func TestMarshalKeepsUntouchedMembersByteIdentical(t *testing.T) {
	raw := `{"api_key": "k1", "enabled": "True", "quota": {"total": 100, "used": 0}, "weird": "带空 格"}`
	d, err := ParseAccountData([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := d.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	// Serialize is the storage path: encoding/json would compact even a
	// custom MarshalJSON's output, so byte fidelity requires this route.
	out, err := d.Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	// Byte-exact: storage order and Python json.dumps separators (", " /
	// ": ") survive; ONLY the touched member's value changed. This is the
	// strongest form of ADR-003 §2 "其余键与原字节不动" — our post-write text
	// equals what the legacy json.dumps would write for the same dict.
	want := `{"api_key": "k1", "enabled": true, "quota": {"total": 100, "used": 0}, "weird": "带空 格"}`
	if string(out) != want {
		t.Fatalf("serialise not byte-exact:\n got: %s\nwant: %s", out, want)
	}
	d2, err := ParseAccountData(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if got, _ := d2.Raw("enabled"); string(got) != "true" {
		t.Errorf("touched member = %s, want clean bool true", got)
	}
	if got, _ := d2.Raw("quota"); string(got) != `{"total": 100, "used": 0}` {
		t.Errorf("quota bytes changed: %s", got)
	}
	if got, _ := d2.Raw("weird"); string(got) != `"带空 格"` {
		t.Errorf("unicode member changed: %s", got)
	}
}

// TestAccountDataOrderAndDuplicateKeys pins sqlite-schema G-4's foundation:
// member order survives parse→serialise, duplicate keys take LAST value at
// FIRST position (Python json.loads dict semantics), Set appends new keys,
// Drop removes positions.
func TestAccountDataOrderAndDuplicateKeys(t *testing.T) {
	d, err := ParseAccountData([]byte(`{"b": 2, "a": 1, "b": 3, "c": 4}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := d.Keys(); len(got) != 3 || got[0] != "b" || got[1] != "a" || got[2] != "c" {
		t.Fatalf("Keys order = %v, want [b a c]", got)
	}
	if raw, _ := d.Raw("b"); string(raw) != "3" {
		t.Errorf("duplicate key value = %s, want last-wins 3", raw)
	}
	out, err := d.Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if string(out) != `{"b": 3, "a": 1, "c": 4}` {
		t.Errorf("Serialize = %s, want order+separators preserved", out)
	}
	if err := d.Set("z", 9); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := d.Keys(); got[len(got)-1] != "z" {
		t.Errorf("new key must append at end, got %v", got)
	}
	d.Drop("a")
	if got := d.Keys(); len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "z" {
		t.Errorf("after Drop keys = %v, want [b c z]", got)
	}
}

// TestValidAccountIDShape pins contract G-6 / sqlite-schema §2.5 on the
// real production ids (all six must validate) and the observable failure
// modes.
func TestValidAccountIDShape(t *testing.T) {
	for _, id := range []string{
		"oauth-login-ea92aa60", "oauth-login-8523aba4", "oauth-apikey-c5c641a1",
		"oauth-apikey-8808fbac", "laohao-bigmodel-2420b3c2", "xinhao-2000w-02f71f86",
		"contract-probe-bf31b757", "a-00000000", "x.y_z-abcdef01",
	} {
		if !ValidAccountID(id) {
			t.Errorf("ValidAccountID(%q) = false, want true (production/contract shape)", id)
		}
	}
	for _, id := range []string{
		"", "test-acc-0001", "UPPER-ABCDEF01", "prefix-ZZZZZZZZ", "prefix-abcdefg1",
		"no-dash-here-not-hex", "-abc12345", "x-123456789",
	} {
		if ValidAccountID(id) {
			t.Errorf("ValidAccountID(%q) = true, want false", id)
		}
	}
}

func TestEnabledDriftNormalization(t *testing.T) {
	cases := []struct {
		json string
		want bool
	}{
		{`{"enabled": true}`, true},
		{`{"enabled": false}`, false},
		{`{"enabled": "True"}`, true}, // production laohao-bigmodel row
		{`{"enabled": "False"}`, false},
		{`{"enabled": "true"}`, true}, // case-tolerant
		{`{"enabled": "1"}`, true},    // ADR-002 numeric-string form
		{`{"enabled": "0"}`, false},
		{`{"enabled": " true "}`, true}, // space-tolerant
		{`{"enabled": 1}`, true},        // python truthiness
		{`{"enabled": 0}`, false},
	}
	for _, tc := range cases {
		d, err := ParseAccountData([]byte(tc.json))
		if err != nil {
			t.Fatalf("%s: %v", tc.json, err)
		}
		got, known := d.Enabled()
		if !known {
			t.Errorf("%s: not recognised", tc.json)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: enabled = %v, want %v", tc.json, got, tc.want)
		}
	}
	// Missing or unrecognised shapes report !known.
	for _, bad := range []string{`{}`, `{"enabled": null}`, `{"enabled": [1]}`} {
		d, err := ParseAccountData([]byte(bad))
		if err != nil {
			t.Fatalf("%s: %v", bad, err)
		}
		if _, known := d.Enabled(); known {
			t.Errorf("%s: must report !known", bad)
		}
	}
}

// TestEffectiveEnabledResolution pins the ADR-002 resolution order:
// data-JSON "enabled" (normalised) when present/known, else the top-level
// INTEGER column (NOT NULL, authoritative fallback).
func TestEffectiveEnabledResolution(t *testing.T) {
	mk := func(data string, col int64) Account {
		return Account{ID: "x", Provider: ProviderZai, Enabled: col, Data: json.RawMessage(data)}
	}
	cases := []struct {
		name string
		acc  Account
		want bool
	}{
		{"dirty string True in data wins", mk(`{"enabled":"True"}`, 1), true},
		{"clean bool in data", mk(`{"enabled":false}`, 1), false},
		{"data missing key falls back to column 1", mk(`{"id":"x"}`, 1), true},
		{"data missing key falls back to column 0", mk(`{"id":"x"}`, 0), false},
		{"data null falls back to column 1", mk(`{"enabled":null}`, 1), true},
		{"unrecognised data shape falls back to column 0", mk(`{"enabled":[1]}`, 0), false},
		{"unparseable data falls back to column 1", mk(`not-json`, 1), true},
	}
	for _, tc := range cases {
		if got := tc.acc.EffectiveEnabled(); got != tc.want {
			t.Errorf("%s: EffectiveEnabled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSetMutationsPreserveEverythingElse(t *testing.T) {
	d, err := ParseAccountData([]byte(productionLike))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := d.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if err := d.Set(DataKeyStatus, StatusCooling); err != nil {
		t.Fatalf("Set: %v", err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	d2, err := ParseAccountData(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if v, _ := d2.Enabled(); v != true {
		t.Errorf("enabled not normalised to bool true")
	}
	if s, _ := d2.String(DataKeyStatus); s != StatusCooling {
		t.Errorf("status not updated")
	}
	if _, ok := d2.Raw("future_python_field"); !ok {
		t.Errorf("unknown field lost during mutation cycle")
	}
	// Unrelated raw bytes untouched: the odd float survived as written.
	if raw, _ := d2.Raw("mystery_scalar"); string(raw) != "1789147671.7793698" {
		t.Errorf("unrelated raw value rewritten: %s", raw)
	}
}

func TestParseAccountDataRejectsNonObjects(t *testing.T) {
	for _, bad := range []string{``, `null`, `[]`, `[1,2]`, `"str"`, `123`, `{oops`} {
		if _, err := ParseAccountData([]byte(bad)); err == nil {
			t.Errorf("input %q: want error", bad)
		}
	}
}

func TestAccountJSONRoundTrip(t *testing.T) {
	name := "老号·抢包(JWT)"
	mode := ModeJWT
	status := StatusActive
	createdAt := 1789148266.096497
	a := Account{
		ID:        "oauth-login-ea92aa60",
		Provider:  ProviderZai,
		Name:      &name,
		Mode:      &mode,
		Status:    &status,
		Enabled:   1,
		CreatedAt: &createdAt,
		Data:      json.RawMessage(productionLike),
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var a2 Account
	if err := json.Unmarshal(b, &a2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Data must round-trip as a nested object (not string-re-encoded).
	if !json.Valid(a2.Data) {
		t.Fatalf("data column lost JSON validity")
	}
	d1, err := a.ParseData()
	if err != nil {
		t.Fatalf("parse d1: %v", err)
	}
	d2, err := a2.ParseData()
	if err != nil {
		t.Fatalf("parse d2: %v", err)
	}
	m1, _ := json.Marshal(d1)
	m2, _ := json.Marshal(d2)
	var v1, v2 any
	_ = json.Unmarshal(m1, &v1)
	_ = json.Unmarshal(m2, &v2)
	if !reflect.DeepEqual(v1, v2) {
		t.Errorf("Account JSON round trip lost data fidelity")
	}
	if a2.Name == nil || *a2.Name != name || a2.CreatedAt == nil || *a2.CreatedAt != createdAt {
		t.Errorf("scalar fields lost: %+v", a2)
	}
}

func TestAccountNullPointerRoundTrip(t *testing.T) {
	// Nullable legacy columns must stay NULL across marshal/unmarshal, not
	// silently become "" / 0.
	a := Account{ID: "x", Provider: ProviderZai, Enabled: 1, Data: json.RawMessage(`{}`)}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var a2 Account
	if err := json.Unmarshal(b, &a2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a2.Name != nil || a2.Mode != nil || a2.Status != nil || a2.CreatedAt != nil {
		t.Errorf("nil columns materialised: %+v", a2)
	}
}

func TestUnixFloatTimeConversion(t *testing.T) {
	for _, f := range []float64{
		1789148266.096497,  // real production value
		1790329520.1371915, // real production value
		1789147671.7793698, // real production value
		0,
		1,
		999.999999999,
		1735689599.9999995, // ~end of 2024, fractional rounding spills to next second
	} {
		tm := UnixFloatToTime(f)
		back := TimeToUnixFloat(tm)
		if math.Abs(back-f) > 1e-6 {
			t.Errorf("round trip %v -> %v -> %v off by %v", f, tm, back, back-f)
		}
		if tm.Location() != time.UTC {
			t.Errorf("conversion must yield UTC")
		}
	}
	// Second-level exactness for a real production timestamp. Sub-second
	// nanoseconds are intentionally NOT asserted exact: float64 at Unix-
	// epoch magnitude (≈1.8e9) quantises to ~2.4e-7 s ≈ 240 ns, so the ns
	// digits of 1789148266.096497 are float representation, not truth. The
	// store path writes the REAL column from the raw *float64 and never
	// through this conversion, so field-level byte fidelity is unaffected.
	tm := UnixFloatToTime(1789148266.096497)
	if tm.Unix() != 1789148266 {
		t.Errorf("unix seconds = %d, want 1789148266", tm.Unix())
	}
	if got := TimeToUnixFloat(tm); math.Abs(got-1789148266.096497) > 1e-6 {
		t.Errorf("round trip = %v, off >1e-6", got)
	}
}
