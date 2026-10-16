package adminapi

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"zhipucode2api/internal/models"
)

// accountView is the 21-key `Account.public_view()` projection, in the exact
// measured key order. The order is contract: it is compared positionally by the
// harness (L2 type tree) and is what the legacy UI iterates.
//
// `usage` is deliberately absent — it exists in the stored 23-key `data` blob
// (account-model.md §5) but `public_view()` never emits it (CR-14: the two key
// orders differ, so one struct cannot serialise both).
type accountView struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Provider       string          `json:"provider"`
	Mode           string          `json:"mode"`
	TokenMasked    string          `json:"token_masked"`
	Enabled        bool            `json:"enabled"`
	Status         string          `json:"status"`
	Quota          json.RawMessage `json:"quota"`
	Subscription   json.RawMessage `json:"subscription"`
	Plan           json.RawMessage `json:"plan"`
	Plans          json.RawMessage `json:"plans"`
	UseCount       int64           `json:"use_count"`
	FailCount      int64           `json:"fail_count"`
	RiskStrikes    int64           `json:"risk_strikes"`
	RecentResults  json.RawMessage `json:"recent_results"`
	LastUsedAt     json.RawMessage `json:"last_used_at"`
	LastCheckedAt  json.RawMessage `json:"last_checked_at"`
	CoolingUntil   json.RawMessage `json:"cooling_until"`
	LastError      json.RawMessage `json:"last_error"`
	CreatedAt      json.RawMessage `json:"created_at"`
	Fingerprint    json.RawMessage `json:"fingerprint"`
}

// fingerprintView is the 7-key device profile shape (account-model.md §5.2).
type fingerprintView struct {
	Platform   string `json:"platform"`
	Arch       string `json:"arch"`
	OSVersion  string `json:"os_version"`
	Language   string `json:"language"`
	Timezone   string `json:"timezone"`
	Screen     string `json:"screen"`
	DeviceMid  string `json:"device_mid"`
}

// defaultJSON is the neutral value for a key the account's `data` blob does not
// carry. The legacy dataclass declares `quota = {}`, `plan = {}`, `plans = []`,
// `recent_results = []`, `subscription = {}`, `last_error = None`,
// `fingerprint = None`, and the two timestamps `None`; those are reproduced
// here rather than omitted, because their PRESENCE is asserted by L2.
var (
	emptyObject = json.RawMessage(`{}`)
	emptyArray  = json.RawMessage(`[]`)
	jsonNull    = json.RawMessage(`null`)
)

// viewOf projects one stored account onto the legacy public view.
//
// The stored row is faithful to the database (account-model.md §5): `enabled`
// may be a JSON string (`"True"` on `laohao-bigmodel-2420b3c2`, measured) and
// the column is a separate 0/1. Per ADR-002 the OUTPUT layer normalises it to a
// real JSON bool while the STORAGE layer keeps the raw value untouched — so the
// normalisation lives here and nowhere else.
func viewOf(a models.Account) (accountView, error) {
	d, err := a.ParseData()
	if err != nil {
		return accountView{}, err
	}
	v := accountView{
		ID:            a.ID,
		Name:          derefString(a.Name),
		Provider:      a.Provider,
		Mode:          derefString(a.Mode),
		TokenMasked:   maskSecret(secretOf(a, d)),
		Enabled:       a.EffectiveEnabled(),
		Status:        effectiveStatus(a, d),
		Quota:         orRaw(d, models.DataKeyQuota, emptyObject),
		Subscription:  orRaw(d, models.DataKeySubscription, emptyObject),
		Plan:          orRaw(d, models.DataKeyPlan, emptyObject),
		Plans:         orRaw(d, models.DataKeyPlans, emptyArray),
		UseCount:      intOf(d, models.DataKeyUseCount),
		FailCount:     intOf(d, models.DataKeyFailCount),
		RiskStrikes:   intOf(d, models.DataKeyRiskStrikes),
		RecentResults: orRaw(d, models.DataKeyRecentResults, emptyArray),
		LastUsedAt:    orRaw(d, models.DataKeyLastUsedAt, jsonNull),
		LastCheckedAt: orRaw(d, models.DataKeyLastCheckedAt, jsonNull),
		CoolingUntil:  orRaw(d, models.DataKeyCoolingUntil, jsonNull),
		LastError:     orRaw(d, models.DataKeyLastError, jsonNull),
		CreatedAt:     createdRaw(a),
		Fingerprint:   fingerprintRaw(d),
	}
	return v, nil
}

// derefString flattens the nullable columns the legacy dataclass always renders
// as a string.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// secretOf returns the credential the view masks. The legacy `Account.secret`
// resolves to the JWT when in jwt mode and the API key otherwise; both live in
// `data`, so read whichever key the mode selects and fall back to the other.
func secretOf(a models.Account, d *models.AccountData) string {
	if derefString(a.Mode) == models.ModeJWT {
		if s, ok := d.String(models.DataKeyJWTToken); ok {
			return s
		}
	}
	if s, ok := d.String(models.DataKeyAPIKey); ok {
		return s
	}
	if s, ok := d.String(models.DataKeyJWTToken); ok {
		return s
	}
	return ""
}

// maskSecret reproduces `public_view`'s masking exactly:
//
//	masked = secret if len(secret) <= 16 else f"{secret[:8]}…{secret[-6:]}"
//
// The separator is U+2026 HORIZONTAL ELLIPSIS, not three ASCII dots — a
// measured 15-character result for a long credential (8 + 1 + 6).
func maskSecret(secret string) string {
	if len(secret) <= 16 {
		return secret
	}
	// Operate on bytes: both slices are ASCII in every credential we store, and
	// the legacy code slices str, which is equivalent for ASCII.
	return secret[:8] + "\u2026" + secret[len(secret)-6:]
}

// effectiveStatus reproduces `Account.effective_status()`: a cooling account
// whose deadline has passed reports `active`, which is what the UI and the
// `stats` histogram both consume.
func effectiveStatus(a models.Account, d *models.AccountData) string {
	status := derefString(a.Status)
	if status != models.StatusCooling {
		return status
	}
	until, ok := d.Float(models.DataKeyCoolingUntil)
	if ok && until > 0 && float64(time.Now().UnixNano())/1e9 >= until {
		return models.StatusActive
	}
	return status
}

// orRaw returns the stored raw JSON for key, or a default when absent/null.
func orRaw(d *models.AccountData, key string, def json.RawMessage) json.RawMessage {
	if raw, ok := d.Raw(key); ok && !d.IsNull(key) {
		return raw
	}
	return def
}

// intOf reads an integer-valued key, tolerating the JSON-string form the legacy
// store can produce. Absent keys are 0 (the dataclass defaults).
func intOf(d *models.AccountData, key string) int64 {
	if n, ok := d.Int(key); ok {
		return n
	}
	if f, ok := d.Float(key); ok {
		return int64(f)
	}
	if s, ok := d.String(key); ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// createdRaw emits `created_at` as the stored float, falling back to null.
func createdRaw(a models.Account) json.RawMessage {
	if a.CreatedAt == nil {
		return jsonNull
	}
	return json.RawMessage(strconv.FormatFloat(*a.CreatedAt, 'f', -1, 64))
}

// fingerprintRaw emits the 7-key fingerprint object, or null when the account
// has no assigned profile (the legacy `fingerprint_view` contract).
func fingerprintRaw(d *models.AccountData) json.RawMessage {
	if _, ok := d.Raw(models.DataKeyFingerprint); !ok || d.IsNull(models.DataKeyFingerprint) {
		return jsonNull
	}
	obj, ok := d.Object(models.DataKeyFingerprint)
	if !ok {
		return jsonNull
	}
	fp := fingerprintView{
		Platform:  rawString(obj, "platform"),
		Arch:      rawString(obj, "arch"),
		OSVersion: rawString(obj, "os_version"),
		Language:  rawString(obj, "language"),
		Timezone:  rawString(obj, "timezone"),
		Screen:    rawString(obj, "screen"),
		DeviceMid: rawString(obj, "device_mid"),
	}
	if fp.DeviceMid == "" {
		return jsonNull
	}
	b, err := marshalJSON(fp)
	if err != nil {
		return jsonNull
	}
	return b
}

func rawString(obj map[string]json.RawMessage, key string) string {
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// statsView is the `stats` histogram, in measured key order.
type statsView struct {
	Total     int   `json:"total"`
	Active    int   `json:"active"`
	Exhausted int   `json:"exhausted"`
	Cooling   int   `json:"cooling"`
	Invalid   int   `json:"invalid"`
	Disabled  int   `json:"disabled"`
	Calls     int64 `json:"calls"`
	Fail      int64 `json:"fail"`
}

// buildStats reproduces the legacy accumulation loop verbatim: each account's
// effective status increments the matching counter when the status name is one
// of the six known ones, and the counters always accumulate.
func buildStats(views []accountView) statsView {
	s := statsView{Total: len(views)}
	for _, v := range views {
		switch v.Status {
		case models.StatusActive:
			s.Active++
		case models.StatusExhausted:
			s.Exhausted++
		case models.StatusCooling:
			s.Cooling++
		case models.StatusInvalid:
			s.Invalid++
		case models.StatusDisabled:
			s.Disabled++
		}
		s.Calls += v.UseCount
		s.Fail += v.FailCount
	}
	return s
}

// splitLines mirrors the legacy token normalisation for `POST /admin/api/accounts`:
// a single string is split on newlines, then every entry is stripped and empties
// dropped.
func splitLines(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
