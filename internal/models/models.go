// Package models defines the domain model shared by all zhipucode2api layers.
//
// The shapes here are derived from the live production database
// (zcode2api/data/accounts.db, probed read-only on 2026-10-15) and the frozen
// contract baseline. Compatibility rules that matter:
//
//   - The accounts table stores selected columns plus a "data" JSON text
//     column holding the full legacy account dict (credentials + runtime
//     statistics). Reading must keep unknown JSON fields and re-serialising
//     must write them back, so the old service and the new one can share the
//     same database file.
//   - Type drift exists in the real data: the "enabled" field inside the
//     data JSON is a bool in most rows but the literal string "True" in at
//     least one production row. The model therefore keeps raw JSON values and
//     only normalises them on demand (AccountData.Enabled).
//   - Column "created_at" is a SQLite REAL holding Unix seconds with
//     sub-second precision (Python time.time()). Time conversions below are
//     exact round-trips for all representable timestamps.
package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Provider constants (accounts.provider).
const (
	ProviderZai      = "zai"
	ProviderBigmodel = "bigmodel"
)

// legacyIDPattern is the observable account-id shape of the legacy service:
// `<name-or-source>-<8 lowercase hex>` (sqlite-schema.md §2.5 / G-6; all
// production rows match). The suffix GENERATION algorithm is
// contract-unverified, so only the shape is checkable. This guards the
// CREATE path (adminapi/gateway generate ids for new accounts): the store
// itself deliberately accepts any TEXT primary key already present in the
// legacy database.
var legacyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._·-]+-[0-9a-f]{8}$`)

// ValidAccountID reports whether a newly generated account id matches the
// legacy observable shape. Unknown providers are contractually PASS-THROUGH
// (account-model A-1); ids, however, must keep the shape or the legacy admin
// endpoints' expectations break (G-6).
func ValidAccountID(id string) bool { return legacyIDPattern.MatchString(id) }

// Mode constants (accounts.mode / data "mode").
const (
	ModeJWT    = "jwt"
	ModeAPIKey = "apiKey"
)

// Status constants (accounts.status and data "status"). Wire values are
// lowercase, verified against production rows ('active') and the protocol
// docs; the legacy Python enum member names are the uppercase spellings used
// in the rewrite task contract (ACTIVE/EXHAUSTED/COOLING/INVALID/DISABLED).
const (
	StatusActive    = "active"
	StatusExhausted = "exhausted"
	StatusCooling   = "cooling"
	StatusInvalid   = "invalid"
	StatusDisabled  = "disabled"
)

// Well-known keys of the data JSON object (legacy Account.to_dict()).
const (
	DataKeyID            = "id"
	DataKeyProvider      = "provider"
	DataKeyName          = "name"
	DataKeyMode          = "mode"
	DataKeyStatus        = "status"
	DataKeyEnabled       = "enabled"
	DataKeyCreatedAt     = "created_at"
	DataKeyAPIKey        = "api_key"
	DataKeyJWTToken      = "jwt_token"
	DataKeyCoolingUntil  = "cooling_until"
	DataKeyFailCount     = "fail_count"
	DataKeyRiskStrikes   = "risk_strikes"
	DataKeyUseCount      = "use_count"
	DataKeyLastUsedAt    = "last_used_at"
	DataKeyLastCheckedAt = "last_checked_at"
	DataKeyLastError     = "last_error"
	DataKeyFingerprint   = "fingerprint"
	DataKeyPlan          = "plan"
	DataKeyPlans         = "plans"
	DataKeyQuota         = "quota"
	DataKeySubscription  = "subscription"
	DataKeyUsage         = "usage"
	DataKeyRecentResults = "recent_results"
)

// Account is one row of the accounts table. Nullable columns are modelled
// as pointers so a NULL round-trips as NULL instead of as a zero value.
type Account struct {
	ID        string          `json:"id"`
	Provider  string          `json:"provider"`
	Name      *string         `json:"name,omitempty"`
	Mode      *string         `json:"mode,omitempty"`
	Status    *string         `json:"status,omitempty"`
	Enabled   int64           `json:"enabled"`
	CreatedAt *float64        `json:"created_at,omitempty"` // Unix seconds (REAL)
	Data      json.RawMessage `json:"data"`                 // data column, kept verbatim
}

// EnabledBool interprets the INTEGER enabled column with Python truthiness:
// zero is false, anything else is true.
func (a Account) EnabledBool() bool { return a.Enabled != 0 }

// SetEnabledBool writes the enabled column (true→1, false→0).
func (a *Account) SetEnabledBool(v bool) {
	if v {
		a.Enabled = 1
	} else {
		a.Enabled = 0
	}
}

// EffectiveEnabled returns the normalised boolean enabled state of the row.
// Resolution order is FINAL per ADR-003 (2026-10-15, legacy double-write
// verified experimentally against the live service):
//
//  1. the data-JSON "enabled" member when present and normalisable (bool,
//     "True"/"true"/"False"/"false", "1"/"0", number) — the legacy API
//     outputs exactly this value, so preferring it is faithful to the
//     externally observable behaviour;
//  2. otherwise the top-level INTEGER enabled column (NOT NULL fallback for
//     corrupt/missing data members).
//
// The two sources agree on every normal path because SetEnabled double-writes
// both (ADR-003 §2); they can only diverge on manually corrupted data.
func (a Account) EffectiveEnabled() bool {
	if d, err := a.ParseData(); err == nil {
		if v, known := d.Enabled(); known {
			return v
		}
	}
	return a.Enabled != 0
}

// CreatedAtTime converts the REAL created_at column to UTC time.
func (a Account) CreatedAtTime() (time.Time, bool) {
	if a.CreatedAt == nil {
		return time.Time{}, false
	}
	return UnixFloatToTime(*a.CreatedAt), true
}

// AccountData is a lossless view of the data JSON object: every member keeps
// its original raw bytes AND its original position; unknown keys survive an
// unmarshal/marshal cycle. Storage-order preservation is a contract
// requirement (sqlite-schema.md G-4: the legacy 23-key order must survive
// re-serialisation for DB-content diffing).
type AccountData struct {
	fields map[string]json.RawMessage
	order  []string // member names in storage order (Python dict semantics)
}

// ParseAccountData parses the data column text. The payload must be a JSON
// object; arrays, scalars and null are rejected.
func ParseAccountData(raw []byte) (*AccountData, error) {
	d := &AccountData{}
	if err := d.UnmarshalJSON(raw); err != nil {
		return nil, err
	}
	return d, nil
}

// UnmarshalJSON implements json.Unmarshaler. Member order is captured so
// Serialize can emit it unchanged (G-4). Duplicate keys take the LAST value
// at the FIRST position, mirroring Python json.loads dict semantics.
func (d *AccountData) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("models: account data must be a JSON object, got empty/null")
	}
	if !json.Valid(trimmed) {
		return fmt.Errorf("models: account data is not a JSON object: invalid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("models: account data is not a JSON object: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("models: account data is not a JSON object")
	}
	fields := map[string]json.RawMessage{}
	var order []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("models: account data key: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("models: account data: unexpected key token %T", keyTok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fmt.Errorf("models: account data member %q: %w", key, err)
		}
		if _, exists := fields[key]; !exists {
			order = append(order, key)
		}
		fields[key] = raw
	}
	if _, err := dec.Token(); err != nil { // consume the closing brace
		return fmt.Errorf("models: account data: %w", err)
	}
	d.fields = fields
	d.order = order
	return nil
}

// MarshalJSON implements json.Marshaler. Storage order and stored bytes,
// same as Serialize. IMPORTANT: encoding/json compacts the return value of
// any custom MarshalJSON (documented behaviour), so this path loses the
// legacy ", "/": " separators; use Serialize() directly for the byte-exact
// storage round trip (ADR-003 §2 / G-4).
func (d AccountData) MarshalJSON() ([]byte, error) {
	return d.Serialize()
}

// Serialize renders the object in STORAGE order (G-4) using the legacy
// service's json.dumps separators (", " between members, ": " after each
// key), with every member's value bytes verbatim. Consequence: a
// parse → Set(one member) → Serialize cycle on an existing production row
// yields text that is byte-identical to what the legacy Python service
// itself would write for the same logical dict — the untouched members
// including their spacing (ADR-003 §2 其余键与原字节不动), the touched
// member exactly as set. An unset/empty AccountData serialises to "{}".
func (d *AccountData) Serialize() ([]byte, error) {
	if len(d.order) == 0 {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range d.order {
		if i > 0 {
			buf.WriteString(", ")
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, fmt.Errorf("models: marshal data key %q: %w", k, err)
		}
		raw := d.fields[k]
		if !json.Valid(raw) {
			return nil, fmt.Errorf("models: data key %q holds invalid JSON", k)
		}
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(raw)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Keys returns member names in storage order.
func (d *AccountData) Keys() []string {
	return append([]string(nil), d.order...)
}

// Raw returns the original bytes of one member.
func (d *AccountData) Raw(key string) (json.RawMessage, bool) {
	v, ok := d.fields[key]
	return v, ok
}

// IsNull reports whether the member exists and is JSON null.
func (d *AccountData) IsNull(key string) bool {
	v, ok := d.fields[key]
	return ok && bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

// Set stores a member. A json.RawMessage value is stored verbatim; anything
// else is marshalled. Existing members keep their storage position; new
// keys append at the end (Python dict semantics).
func (d *AccountData) Set(key string, val any) error {
	if d.fields == nil {
		d.fields = map[string]json.RawMessage{}
	}
	var b json.RawMessage
	if raw, ok := val.(json.RawMessage); ok {
		b = raw
	} else if raw, ok := val.(*json.RawMessage); ok && raw != nil {
		b = *raw
	} else {
		out, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("models: set data key %q: %w", key, err)
		}
		b = out
	}
	if _, exists := d.fields[key]; !exists {
		d.order = append(d.order, key)
	}
	d.fields[key] = b
	return nil
}

// Drop removes a member, keeping the storage order of the rest.
func (d *AccountData) Drop(key string) {
	if _, ok := d.fields[key]; !ok {
		return
	}
	delete(d.fields, key)
	for i, k := range d.order {
		if k == key {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
}

// String reads a string member. ok=false when missing, null or of a
// different type.
func (d *AccountData) String(key string) (string, bool) {
	v, ok := d.fields[key]
	if !ok || d.IsNull(key) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

// Int reads an integral member. Floats with an integral value are accepted
// (Python may serialise small integers either way).
func (d *AccountData) Int(key string) (int64, bool) {
	v, ok := d.fields[key]
	if !ok || d.IsNull(key) {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(v, &n); err == nil {
		return n, true
	}
	var f float64
	if err := json.Unmarshal(v, &f); err == nil && f == math.Trunc(f) && !math.IsInf(f, 0) {
		return int64(f), true
	}
	return 0, false
}

// Float reads a numeric member. Integers are accepted too.
func (d *AccountData) Float(key string) (float64, bool) {
	v, ok := d.fields[key]
	if !ok || d.IsNull(key) {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return 0, false
	}
	return f, true
}

// Object reads a JSON-object member as its raw children.
func (d *AccountData) Object(key string) (map[string]json.RawMessage, bool) {
	v, ok := d.fields[key]
	if !ok || d.IsNull(key) {
		return nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(v, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// Array reads a JSON-array member as its raw elements.
func (d *AccountData) Array(key string) ([]json.RawMessage, bool) {
	v, ok := d.fields[key]
	if !ok || d.IsNull(key) {
		return nil, false
	}
	var a []json.RawMessage
	if err := json.Unmarshal(v, &a); err != nil {
		return nil, false
	}
	return a, true
}

// Enabled normalises the legacy "enabled" field per ADR-002 (2026-10-15).
// Accepted shapes observed in production data and named by the ruling:
// JSON true/false; the strings "True"/"False"/"true"/"false"; numeric strings
// like "1"/"0"; JSON numbers (Python truthiness: 0 is false).
// Returns (value, known); known=false when the member is missing, null or of
// an unrecognised shape, in which case callers should fall back to the
// authoritative source (the top-level enabled column — see
// Account.EffectiveEnabled).
//
// Normalisation is read-side only: AccountData keeps the member's raw bytes
// untouched, so a parse→store cycle still writes the legacy dirty value
// byte-for-byte (ADR-002 requirement 2: 存储层保持保真).
func (d *AccountData) Enabled() (value bool, known bool) {
	v, ok := d.fields[DataKeyEnabled]
	if !ok || d.IsNull(DataKeyEnabled) {
		return false, false
	}
	if b, err := unmarshalBool(v); err == nil {
		return b, true
	}
	if s, err := unmarshalString(v); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f != 0, true
		}
		return false, false
	}
	if f, err := unmarshalFloat(v); err == nil {
		return f != 0, true
	}
	return false, false
}

// SetEnabled writes a strict JSON bool (normalising drift; raw semantics of
// other members are untouched).
func (d *AccountData) SetEnabled(v bool) error { return d.Set(DataKeyEnabled, v) }

// ParseData parses the row's data column into a lossless AccountData view.
func (a Account) ParseData() (*AccountData, error) { return ParseAccountData(a.Data) }

func unmarshalString(v json.RawMessage) (string, error) {
	var s string
	err := json.Unmarshal(v, &s)
	return s, err
}
func unmarshalFloat(v json.RawMessage) (float64, error) {
	var f float64
	err := json.Unmarshal(v, &f)
	return f, err
}
func unmarshalBool(v json.RawMessage) (bool, error) {
	var b bool
	err := json.Unmarshal(v, &b)
	return b, err
}

// UnixFloatToTime converts a Python-style Unix timestamp (REAL seconds) to
// UTC. The conversion is exact for every timestamp representable in float64
// around real-world epochs.
func UnixFloatToTime(f float64) time.Time {
	sec := math.Floor(f)
	nsec := math.Round((f - sec) * 1e9)
	if nsec >= 1e9 { // fractional rounding may spill into the next second
		sec++
		nsec -= 1e9
	}
	return time.Unix(int64(sec), int64(nsec)).UTC()
}

// TimeToUnixFloat converts t to Unix seconds as float64, the inverse
// (modulo float64 representation) of UnixFloatToTime.
func TimeToUnixFloat(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}
