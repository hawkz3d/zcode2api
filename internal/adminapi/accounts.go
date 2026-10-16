package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"zhipucode2api/internal/models"
)

// providersTuple is the legacy `PROVIDERS` tuple, in order. It is a contract
// value: it appears verbatim in `/admin/api/status` and drives the pool
// grouping order (CR-35).
var providersTuple = []string{models.ProviderZai, models.ProviderBigmodel}

// ── B-01 GET /admin/api/verify ───────────────────────────────────────────────

func (a *API) handleVerify(c *reqCtx) {
	writeJSON(c.w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── B-02 GET /admin/api/accounts ─────────────────────────────────────────────

// listAccountsBody is the `/admin/api/accounts` envelope. Key order is measured:
// accounts, stats, providers, ts.
type listAccountsBody struct {
	Accounts  []accountView `json:"accounts"`
	Stats     statsView     `json:"stats"`
	Providers []string      `json:"providers"`
	TS        float64       `json:"ts"`
}

func (a *API) handleListAccounts(c *reqCtx) {
	rows, err := a.accounts.ListAccounts(c.r.Context())
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	views := make([]accountView, 0, len(rows))
	for _, r := range rows {
		v, err := viewOf(r)
		if err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		views = append(views, v)
	}
	writeJSON(c.w, http.StatusOK, listAccountsBody{
		Accounts:  views,
		Stats:     buildStats(views),
		Providers: providersTuple,
		TS:        a.nowSeconds(),
	})
}

// ── B-03 POST /admin/api/accounts ────────────────────────────────────────────

type addAccountsBody struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

func (a *API) handleAddAccounts(c *reqCtx) {
	ctx := c.r.Context()
	provider := "zai"
	if v, ok := c.object()["provider"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			writeDictType(c.w, c.value)
			return
		}
		if s != "" {
			provider = s
		}
	}
	if !isProvider(provider) {
		writeShapeA(c.w, http.StatusBadRequest, "不支持的 provider")
		return
	}
	tokens, ok := collectTokens(c.object()["tokens"])
	if !ok {
		writeDictType(c.w, c.value)
		return
	}
	if len(tokens) == 0 {
		writeShapeA(c.w, http.StatusBadRequest, detailNoTokens)
		return
	}
	name := ""
	if v, ok := c.object()["name"]; ok && v != nil {
		s, _ := v.(string)
		name = s
	}

	added := make([]string, 0, len(tokens))
	for _, tok := range dedupeStrings(tokens) {
		// The legacy default name depends on the CURRENT pool size, which grows
		// as this loop inserts, so it must be recomputed per iteration.
		rowName := name
		if rowName == "" {
			existing, err := a.accounts.ListAccounts(ctx)
			if err != nil {
				writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			rowName = provider + "-" + itoa(countProvider(existing, provider)+1)
		}
		acc, err := newAccount(provider, rowName, tok)
		if err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if err := a.accounts.UpsertAccount(ctx, acc); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		added = append(added, acc.ID)
	}
	writeJSON(c.w, http.StatusOK, addAccountsBody{Count: len(added), IDs: added})
}

// collectTokens reproduces the legacy token normalisation:
//
//	tokens = payload.get("tokens") or []
//	if isinstance(tokens, str): tokens = [t.strip() for t in tokens.splitlines() if t.strip()]
//	tokens = [t.strip() for t in tokens if t and t.strip()]
//
// Returns ok=false when `tokens` is neither a string nor a list, which is the
// only way this field can fail the enclosing `dict` contract.
func collectTokens(raw any) ([]string, bool) {
	if raw == nil {
		return nil, true
	}
	if s, ok := raw.(string); ok {
		return splitLines(s), true
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, false
		}
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out, true
}

// ── B-04 DELETE /admin/api/accounts ──────────────────────────────────────────

type deleteAccountsBody struct {
	Deleted int `json:"deleted"`
}

func (a *API) handleDeleteAccounts(c *reqCtx) {
	ctx := c.r.Context()
	deleted := 0
	for _, it := range c.arr {
		aid, ok := it.(string)
		if !ok {
			writeListType(c.w, c.value)
			return
		}
		acc, found, err := a.findAny(ctx, aid)
		if err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if !found {
			continue
		}
		ok2, err := a.accounts.DeleteAccount(ctx, acc.ID)
		if err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if ok2 {
			deleted++
		}
	}
	writeJSON(c.w, http.StatusOK, deleteAccountsBody{Deleted: deleted})
}

// ── B-05 GET /admin/api/status ───────────────────────────────────────────────

// statusBody is the measured three-key envelope of B-05. `quota_pool` is an
// insertion-ordered object (`zai` first, then `bigmodel`) because the legacy
// value is a Python dict comprehension over PROVIDERS; a Go map would re-sort
// it alphabetically and the harness compares the raw body positionally.
type statusBody struct {
	Providers     []string    `json:"providers"`
	GatewayKeySet bool        `json:"gateway_key_set"`
	QuotaPool     orderedInts `json:"quota_pool"`
}

// orderedInts is an insertion-ordered {string:int} object.
type orderedInts []intMember

type intMember struct {
	key string
	val int
}

func (o orderedInts) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := marshalJSON(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		v, err := marshalJSON(m.val)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func (a *API) handleStatus(c *reqCtx) {
	ctx := c.r.Context()
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	gatewayKey, _, err := a.settings.GetSetting(ctx, "gateway_key")
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	pool := make(orderedInts, 0, len(providersTuple))
	for _, p := range providersTuple {
		n := 0
		for _, r := range rows {
			if r.Provider != p {
				continue
			}
			if isSelectable(r, a.nowSeconds()) {
				n++
			}
		}
		pool = append(pool, intMember{key: p, val: n})
	}
	writeJSON(c.w, http.StatusOK, statusBody{
		Providers:     providersTuple,
		GatewayKeySet: gatewayKey != "",
		QuotaPool:     pool,
	})
}

// isSelectable reproduces `Account.is_selectable`:
//
//	if not enabled or status in (DISABLED, INVALID): False
//	if status == EXHAUSTED: False
//	if status == COOLING: return cooling_until and now >= cooling_until
//	return True
//
// Note this reads the EFFECTIVE enabled flag, so the `"True"` string form
// counts as enabled (ADR-002 normalisation applies to behaviour, not just to
// serialisation).
func isSelectable(a models.Account, now float64) bool {
	if !a.EffectiveEnabled() {
		return false
	}
	status := derefString(a.Status)
	if status == models.StatusDisabled || status == models.StatusInvalid {
		return false
	}
	if status == models.StatusExhausted {
		return false
	}
	if status == models.StatusCooling {
		d, err := a.ParseData()
		if err != nil {
			return false
		}
		until, ok := d.Float(models.DataKeyCoolingUntil)
		return ok && until > 0 && now >= until
	}
	return true
}

// ── B-06 PUT /admin/api/accounts/{account_id} ────────────────────────────────

type okBody struct {
	OK bool `json:"ok"`
}

func (a *API) handleEditAccount(c *reqCtx) {
	ctx := c.r.Context()
	acc, found, err := a.findAny(ctx, c.param("account_id"))
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !found {
		writeShapeA(c.w, http.StatusNotFound, detailAccountMissing)
		return
	}
	d, err := acc.ParseData()
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if v, ok := c.object()["name"]; ok && v != nil {
		if s, ok := v.(string); ok && s != "" {
			trimmed := strings.TrimSpace(s)
			acc.Name = &trimmed
			if err := d.Set(models.DataKeyName, trimmed); err != nil {
				writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
		}
	}
	// The legacy lookup is `payload.get("token") or payload.get("secret")`, so
	// an empty-string `token` falls through to `secret`.
	secret := firstNonEmptyString(c.object(), "token", "secret")
	if secret != "" {
		secret = strings.TrimSpace(secret)
		mode := models.ModeAPIKey
		if strings.Count(secret, ".") == 2 && acc.Provider == models.ProviderZai {
			mode = models.ModeJWT
		}
		acc.Mode = &mode
		if mode == models.ModeJWT {
			if err := d.Set(models.DataKeyJWTToken, secret); err != nil {
				writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			if err := d.Set(models.DataKeyAPIKey, nil); err != nil {
				writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
		} else {
			if err := d.Set(models.DataKeyAPIKey, secret); err != nil {
				writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
			if err := d.Set(models.DataKeyJWTToken, nil); err != nil {
				writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
		}
		active := models.StatusActive
		acc.Status = &active
		if err := d.Set(models.DataKeyStatus, active); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if err := d.Set(models.DataKeyLastError, nil); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if err := d.Set(models.DataKeyMode, mode); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
	}
	if err := a.persist(ctx, acc, d); err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(c.w, http.StatusOK, okBody{OK: true})
}

// ── B-07 POST /admin/api/accounts/{account_id}/enabled ───────────────────────

func (a *API) handleSetEnabled(c *reqCtx) {
	ctx := c.r.Context()
	acc, found, err := a.findAny(ctx, c.param("account_id"))
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !found {
		writeShapeA(c.w, http.StatusNotFound, detailAccountMissing)
		return
	}
	// `enabled = bool(payload.get("enabled", True))` — absent means true, and
	// Python truthiness is reproduced (only 0/""/[]/{}/null/False are falsy).
	enabled := true
	if v, ok := c.object()["enabled"]; ok {
		enabled = pythonTruthy(v)
	}
	// ADR-003: dual-write the column and `data.enabled`. The legacy
	// `set_enabled` ALSO flips `status` (disabled on off, active when leaving
	// disabled), which is observable through the effective status and the
	// `stats` histogram, so it must be reproduced.
	if _, err := a.accounts.SetEnabled(ctx, acc.ID, enabled); err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	d, err := acc.ParseData()
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	status := derefString(acc.Status)
	switch {
	case !enabled:
		status = models.StatusDisabled
	case status == models.StatusDisabled:
		status = models.StatusActive
	}
	if status != derefString(acc.Status) {
		acc.Status = &status
		if err := d.Set(models.DataKeyStatus, status); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		if err := a.persist(ctx, acc, d); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
	}
	writeJSON(c.w, http.StatusOK, okBody{OK: true})
}

// ── B-08 POST /admin/api/accounts/{account_id}/fingerprint/rotate ────────────

type rotateBody struct {
	OK          bool             `json:"ok"`
	Fingerprint *fingerprintView `json:"fingerprint"`
}

func (a *API) handleRotateFingerprint(c *reqCtx) {
	ctx := c.r.Context()
	acc, found, err := a.findAny(ctx, c.param("account_id"))
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !found {
		writeShapeA(c.w, http.StatusNotFound, detailAccountMissing)
		return
	}
	d, err := acc.ParseData()
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	fp := randomProfile()
	if err := d.Set(models.DataKeyFingerprint, fp); err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if err := a.persist(ctx, acc, d); err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(c.w, http.StatusOK, rotateBody{OK: true, Fingerprint: fp})
}

// ── B-09 POST /admin/api/accounts/refresh ────────────────────────────────────

type refreshAllBody struct {
	Summary        okFailBody `json:"summary"`
	Count          int        `json:"count"`
	SkippedCooling int        `json:"skipped_cooling"`
}

type okFailBody struct {
	OK   int `json:"ok"`
	Fail int `json:"fail"`
}

func (a *API) handleRefreshAll(c *reqCtx) {
	ctx := c.r.Context()
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	all, _ := c.object()["all"].(bool)
	var pool []models.Account
	if all {
		pool = rows
	} else {
		wanted := map[string]bool{}
		if list, ok := c.stringList("ids"); ok {
			for _, id := range list {
				wanted[id] = true
			}
		}
		for _, r := range rows {
			if wanted[r.ID] {
				pool = append(pool, r)
			}
		}
	}
	// Cooling accounts are skipped entirely: refreshing them would generate
	// upstream traffic during a risk-control window (the same invariant the
	// quota monitor holds).
	targets := make([]models.Account, 0, len(pool))
	for _, r := range pool {
		if isCooling(r, a.nowSeconds()) {
			continue
		}
		targets = append(targets, r)
	}
	summary := a.refreshAccounts(ctx, targets)
	writeJSON(c.w, http.StatusOK, refreshAllBody{
		Summary:        summary,
		Count:          len(targets),
		SkippedCooling: len(pool) - len(targets),
	})
}

// ── B-10 POST /admin/api/accounts/{account_id}/refresh ───────────────────────

type refreshOneBody struct {
	OK      bool            `json:"ok"`
	Message string          `json:"message,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Account accountView     `json:"account"`
}

func (a *API) handleRefreshOne(c *reqCtx) {
	ctx := c.r.Context()
	acc, found, err := a.findAny(ctx, c.param("account_id"))
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !found {
		writeShapeA(c.w, http.StatusNotFound, detailAccountMissing)
		return
	}
	if isCooling(acc, a.nowSeconds()) {
		v, err := viewOf(acc)
		if err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		writeJSON(c.w, http.StatusOK, refreshOneBody{
			OK:      false,
			Message: "账号冷却中（风控/限流），已跳过上游刷新",
			Account: v,
		})
		return
	}
	res, err := a.fetchQuota(ctx, acc)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	fresh, _, err := a.accounts.GetAccount(ctx, acc.ID)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	v, err := viewOf(fresh)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(c.w, http.StatusOK, refreshOneBody{
		OK:      res.OK,
		Result:  res.Raw,
		Account: v,
	})
}

// isCooling reproduces `Account.is_cooling`: only a COOLING status with a
// future deadline counts, so a stale cooling_until on an active account does
// NOT skip upstream work.
func isCooling(a models.Account, now float64) bool {
	if derefString(a.Status) != models.StatusCooling {
		return false
	}
	d, err := a.ParseData()
	if err != nil {
		return false
	}
	until, ok := d.Float(models.DataKeyCoolingUntil)
	return ok && until > 0 && now < until
}

// ── shared helpers ───────────────────────────────────────────────────────────

// findAny reproduces `store.find_any`: an exact id match across every provider,
// in PROVIDERS order. A NAME is deliberately not accepted here — measured
// `PUT /admin/api/accounts/probe-renamed` (an existing account's name) is 404.
func (a *API) findAny(ctx context.Context, idOrName string) (models.Account, bool, error) {
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		return models.Account{}, false, err
	}
	for _, p := range providersTuple {
		for _, r := range rows {
			if r.Provider == p && r.ID == idOrName {
				return r, true, nil
			}
		}
	}
	return models.Account{}, false, nil
}

// persist writes an account back with its (possibly mutated) data blob. The
// table columns mirror the fields the legacy `_persist_account` writes.
func (a *API) persist(ctx context.Context, acc models.Account, d *models.AccountData) error {
	raw, err := d.Serialize()
	if err != nil {
		return err
	}
	acc.Data = raw
	return a.accounts.UpsertAccount(ctx, acc)
}

// newAccount builds a fresh pool row with the legacy id derivation and the
// initial `data` key set (CR-11: `name`-prefix + 8 hex chars, NOT a hash of the
// secret).
func newAccount(provider, name, secret string) (models.Account, error) {
	id, err := deriveAccountID(name)
	if err != nil {
		return models.Account{}, err
	}
	trimmed := strings.TrimSpace(secret)
	isJWT := strings.Count(trimmed, ".") == 2 && provider == models.ProviderZai
	mode := models.ModeAPIKey
	if isJWT {
		mode = models.ModeJWT
	}
	display := name
	if display == "" {
		display = provider + "-account"
	}
	now := float64(nowUnix())
	// New accounts get the HOST profile (legacy `_assign_fingerprint` ->
	// `profile_for` -> `assign`), not a random pool draw. Measured: every live
	// account carries this deployment's real shape.
	fp := hostProfile()
	fields := map[string]any{
		models.DataKeyID:            id,
		models.DataKeyName:          display,
		models.DataKeyProvider:      provider,
		models.DataKeyMode:          mode,
		models.DataKeyEnabled:       true,
		models.DataKeyStatus:        models.StatusActive,
		models.DataKeyQuota:         map[string]any{},
		models.DataKeyPlan:          map[string]any{},
		models.DataKeyPlans:         []any{},
		models.DataKeyUsage:         map[string]any{},
		models.DataKeySubscription:  map[string]any{},
		models.DataKeyUseCount:      0,
		models.DataKeyFailCount:     0,
		models.DataKeyRiskStrikes:   0,
		models.DataKeyRecentResults: []any{},
		models.DataKeyLastUsedAt:    nil,
		models.DataKeyLastCheckedAt: nil,
		models.DataKeyCoolingUntil:  nil,
		models.DataKeyLastError:     nil,
		models.DataKeyCreatedAt:     now,
		models.DataKeyFingerprint:   fp,
		models.DataKeyJWTToken:      nil,
		models.DataKeyAPIKey:        nil,
	}
	if isJWT {
		fields[models.DataKeyJWTToken] = trimmed
	} else {
		fields[models.DataKeyAPIKey] = trimmed
	}
	raw, err := marshalJSON(fields)
	if err != nil {
		return models.Account{}, err
	}
	displayCopy, modeCopy, statusCopy := display, mode, models.StatusActive
	return models.Account{
		ID:        id,
		Provider:  provider,
		Name:      &displayCopy,
		Mode:      &modeCopy,
		Status:    &statusCopy,
		Enabled:   1,
		CreatedAt: &now,
		Data:      raw,
	}, nil
}

// deriveAccountID reproduces `models._account_id`:
//
//	safe = "".join(c if c.isalnum() else "-" for c in (name or "account").lower())
//	safe = safe.strip("-")[:32] or "account"
//	return f"{safe}-{secrets.token_hex(4)}"
//
// `str.isalnum()` is Unicode-aware in Python, so a CJK name keeps its
// characters; this uses the Go equivalent (`unicode.IsLetter/IsDigit`).
func deriveAccountID(name string) (string, error) {
	src := name
	if src == "" {
		src = "account"
	}
	var b strings.Builder
	for _, r := range strings.ToLower(src) {
		if isAlnum(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	safe := strings.Trim(b.String(), "-")
	if len(safe) > 32 {
		safe = safe[:32]
	}
	if safe == "" {
		safe = "account"
	}
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return safe + "-" + hex.EncodeToString(buf[:]), nil
}

// countProvider counts the rows of one provider.
func countProvider(rows []models.Account, provider string) int {
	n := 0
	for _, r := range rows {
		if r.Provider == provider {
			n++
		}
	}
	return n
}

func isProvider(p string) bool {
	for _, x := range providersTuple {
		if x == p {
			return true
		}
	}
	return false
}

// dedupeStrings reproduces `dict.fromkeys(tokens)` — dedupe, order preserved.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// firstNonEmptyString reproduces `a or b`: the first value that is a non-empty
// string wins.
func firstNonEmptyString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// pythonTruthy reproduces Python's bool() for the JSON values a body can carry.
func pythonTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

func (a *API) nowSeconds() float64 {
	return float64(a.now().UnixNano()) / 1e9
}
