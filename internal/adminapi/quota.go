package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// quotaRefreshConcurrency is the legacy `Semaphore(8)`.
const quotaRefreshConcurrency = 8

// quotaResult is `fetch_quota`'s return: the upstream-derived result object plus
// whether the refresh succeeded (`"error" not in res`).
//
// `Raw` is pre-serialised rather than a map because Python dicts preserve
// insertion order and the legacy code inserts `billing`, `balance`, `usage`,
// `subscription`, `error` in that order. A Go map would re-sort the keys
// alphabetically, and the harness compares the raw body positionally.
type quotaResult struct {
	OK  bool
	Raw json.RawMessage
}

// quotaOrigins is `QUOTA_ORIGINS`: the hosts serving the subscription-quota
// endpoint, in preference order per provider.
var quotaOrigins = map[string][]string{
	models.ProviderZai:      {zaiAPIOrigin, bigmodelOrigin},
	models.ProviderBigmodel: {bigmodelOrigin, zaiAPIOrigin},
}

// quotaLimitBadKeyCodes are the business codes that mean "this key is rejected",
// as opposed to "this key is fine but has no plan".
var quotaLimitBadKeyCodes = map[int]bool{1000: true, 401: true}

// ── B-09 / B-10 / B-12 / B-14 support: fetch_quota ──────────────────────────

// billingProbe is one billing-family response, kept whole because the auth
// classification needs the STATUS and the BODY TEXT, not just the parsed JSON.
type billingProbe struct {
	body   map[string]any
	status int
	text   string
	ok     bool // HTTP 200 + parseable JSON object
}

// fetchQuota reproduces `quota.fetch_quota`: pull plans / balance / usage for a
// JWT account (or the subscription snapshot for an apiKey account), write the
// derived state back, and return the result object.
//
// The ledger split is load-bearing: `billing/*` lives on zcode.z.ai and accepts
// ONLY a JWT. Sending an apiKey account there guarantees a 401, and the legacy
// code's 401 branch would then mark a perfectly usable key INVALID — which is
// why apiKey accounts are routed to the subscription endpoint instead.
func (a *API) fetchQuota(ctx context.Context, acc models.Account) (quotaResult, error) {
	d, err := acc.ParseData()
	if err != nil {
		return quotaResult{}, err
	}
	jwt, _ := d.String(models.DataKeyJWTToken)
	isJWT := derefString(acc.Mode) == models.ModeJWT && jwt != ""

	// Result members, in legacy insertion order.
	var result []jsonMember
	var billing, balance, usage *map[string]any

	if isJWT {
		headers := a.billingHeaders(acc, a.opt.Version)
		probes := make([]billingProbe, 3)
		paths := []string{"/billing/current", "/billing/balance", "/usage"}
		var wg sync.WaitGroup
		for i, p := range paths {
			wg.Add(1)
			go func(i int, p string) {
				defer wg.Done()
				probes[i] = a.billingProbe(ctx, headers, p)
			}(i, p)
		}
		wg.Wait()
		if probes[0].ok {
			billing = &probes[0].body
		}
		if probes[1].ok {
			balance = &probes[1].body
		}
		if probes[2].ok {
			usage = &probes[2].body
		}

		// A 401/403 on the billing ledger means the JWT is rejected — but only
		// when the body is a real auth rejection: a captcha/verify challenge
		// means the account is being gated, not invalidated.
		if billing == nil && !probes[0].ok {
			if probes[0].status == http.StatusUnauthorized || probes[0].status == http.StatusForbidden {
				lower := strings.ToLower(probes[0].text)
				if !strings.Contains(lower, "captcha") && !strings.Contains(lower, "verify") {
					msg := "鉴权失败 HTTP " + strconv.Itoa(probes[0].status)
					if err := a.markInvalid(ctx, acc, d, msg); err != nil {
						return quotaResult{}, err
					}
					raw, err := marshalJSON(orderedObject{jsonMember{"error", msg}})
					if err != nil {
						return quotaResult{}, err
					}
					return quotaResult{OK: false, Raw: raw}, nil
				}
			}
		}
	}

	now := a.nowSeconds()
	if err := d.Set(models.DataKeyLastCheckedAt, now); err != nil {
		return quotaResult{}, err
	}

	quotaMap := map[string]any{}
	if billing != nil {
		result = append(result, jsonMember{"billing", *billing})
		if plans, ok := nestedPlans(*billing); ok {
			// The whole list is retained: a multi-plan account must not lose
			// entitlements, and `plans` is what the claim preview reads back.
			if err := d.Set(models.DataKeyPlans, plans); err != nil {
				return quotaResult{}, err
			}
			first := any(map[string]any{})
			if len(plans) > 0 {
				first = plans[0]
			}
			if err := d.Set(models.DataKeyPlan, first); err != nil {
				return quotaResult{}, err
			}
		}
	}
	if balance != nil {
		result = append(result, jsonMember{"balance", *balance})
		mergeBalances(quotaMap, *balance)
		mergePlanEntitlements(quotaMap, *balance)
	}
	if usage != nil {
		result = append(result, jsonMember{"usage", *usage})
		if err := d.Set(models.DataKeyUsage, *usage); err != nil {
			return quotaResult{}, err
		}
	}

	// The apiKey ledger has its own endpoint and its own snapshot; it may also
	// report an error, which is folded into the result exactly like a billing
	// failure.
	sub, subErr := a.fetchSubscriptionQuota(ctx, acc, d)
	if sub != nil {
		result = append(result, jsonMember{"subscription", sub})
	}

	if len(quotaMap) > 0 {
		if err := d.Set(models.DataKeyQuota, quotaMap); err != nil {
			return quotaResult{}, err
		}
		if err := applyQuotaState(d, acc, quotaMap, now); err != nil {
			return quotaResult{}, err
		}
	}

	if err := a.persist(ctx, acc, d); err != nil {
		return quotaResult{}, err
	}

	// The `error` member is last (and is what makes the refresh count as a
	// failure), so it is appended after everything else.
	if subErr != "" {
		result = append(result, jsonMember{"error", subErr})
	} else if len(result) == 0 {
		result = append(result, jsonMember{"error", "无法获取额度数据"})
	}
	raw, err := marshalJSON(orderedObject(result))
	if err != nil {
		return quotaResult{}, err
	}
	return quotaResult{OK: subErr == "", Raw: raw}, nil
}

// jsonMember is one key/value pair of an insertion-ordered object.
type jsonMember struct {
	key string
	val any
}

// orderedObject marshals its members in the order given. It exists only so the
// result object keeps Python dict order.
type orderedObject []jsonMember

func (o orderedObject) MarshalJSON() ([]byte, error) {
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

// markInvalid sets an account to INVALID with a message, persisting it.
func (a *API) markInvalid(ctx context.Context, acc models.Account, d *models.AccountData, msg string) error {
	invalid := models.StatusInvalid
	acc.Status = &invalid
	if err := d.Set(models.DataKeyStatus, invalid); err != nil {
		return err
	}
	if err := d.Set(models.DataKeyLastError, msg); err != nil {
		return err
	}
	return a.persist(ctx, acc, d)
}

// applyQuotaState reproduces the exhaustion / recovery branch of `fetch_quota`.
//
// The bonus pool is deliberately excluded from the daily balance, so "every
// daily window is at zero" is NOT exhaustion while a one-time grant is still
// live — treating it as such would route traffic away from an account holding
// hundreds of millions of usable tokens.
func applyQuotaState(d *models.AccountData, acc models.Account, quotaMap map[string]any, now float64) error {
	remainings := []float64{}
	for _, q := range quotaMap {
		obj, ok := q.(map[string]any)
		if !ok {
			continue
		}
		if r, ok := obj["remaining"].(float64); ok {
			remainings = append(remainings, r)
		}
	}
	dailyExhausted := len(remainings) > 0
	hasDaily := false
	for _, r := range remainings {
		if r > 0 {
			dailyExhausted = false
			hasDaily = true
		}
	}
	hasBonus := false
	if plan, ok := asObject(d, models.DataKeyPlan); ok {
		hasBonus = bonusActive(plan, now)
	}
	if !hasBonus {
		if plans, ok := asArray(d, models.DataKeyPlans); ok {
			for _, it := range plans {
				p, ok := it.(map[string]any)
				if ok && bonusActive(p, now) {
					hasBonus = true
					break
				}
			}
		}
	}
	status := derefString(acc.Status)
	switch {
	case dailyExhausted && !hasBonus:
		exhausted := models.StatusExhausted
		acc.Status = &exhausted
		if err := d.Set(models.DataKeyStatus, exhausted); err != nil {
			return err
		}
		return d.Set(models.DataKeyLastError, "额度已用完")
	case (status == models.StatusExhausted || status == models.StatusCooling ||
		status == models.StatusInvalid) && (hasDaily || hasBonus):
		if isCooling(acc, now) {
			return nil
		}
		active := models.StatusActive
		acc.Status = &active
		if err := d.Set(models.DataKeyStatus, active); err != nil {
			return err
		}
		if err := d.Set(models.DataKeyLastError, nil); err != nil {
			return err
		}
		return d.Set(models.DataKeyCoolingUntil, nil)
	}
	return nil
}

// bonusActive reproduces `_bonus_active`: a live one-time grant inside the plan.
func bonusActive(plan map[string]any, now float64) bool {
	ents, ok := plan["entitlements"].([]any)
	if !ok {
		return false
	}
	for _, e := range ents {
		ent, ok := e.(map[string]any)
		if !ok || str(ent["period"]) != "one_time" {
			continue
		}
		eff := numOr(ent["effective_at"])
		ends := numOr(ent["ends_at"])
		if ends == 0 {
			ends = numOr(ent["expires_at"])
		}
		if eff > 0 && eff <= now && (ends == 0 || now <= ends) {
			return true
		}
	}
	return false
}

// ── billing-family GET ─────────────────────────────────────────────────────

// billingProbe performs one billing GET and keeps the status + body text so the
// caller can classify auth failures. HTTP statuses are results, not errors.
func (a *API) billingProbe(ctx context.Context, headers http.Header, path string) billingProbe {
	res, err := a.client.Send(ctx, &upstream.Request{
		Method:  http.MethodGet,
		URL:     a.opt.ZaiBillingBase + path,
		Header:  headers,
		Timeout: 20 * time.Second,
	})
	if err != nil {
		return billingProbe{}
	}
	text := readBodyText(res.Body)
	res.Body.Close()
	p := billingProbe{status: res.StatusCode, text: text}
	if res.StatusCode != http.StatusOK {
		return p
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return p
	}
	p.body = out
	p.ok = true
	return p
}

// nestedPlans digs `data.plans` out of a billing payload.
func nestedPlans(body map[string]any) ([]any, bool) {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, false
	}
	plans, ok := data["plans"].([]any)
	return plans, ok
}

// mergeBalances folds `data.balances[]` into the quota map. Same-name windows
// accumulate rather than overwrite: a daily window and a one-time window for the
// same model are separate rows upstream and must sum locally.
func mergeBalances(quotaMap map[string]any, body map[string]any) {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return
	}
	balances, ok := data["balances"].([]any)
	if !ok {
		return
	}
	for _, it := range balances {
		bal, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := firstString(bal, "show_name", "model")
		if name == "" {
			name = "model"
		}
		if prev, ok := quotaMap[name].(map[string]any); ok {
			for _, k := range []string{"total", "used", "remaining"} {
				prev[k] = numOr(prev[k]) + numOr(bal[unitKey(k)])
			}
			prev["expires_at"] = maxNum(numOr(prev["expires_at"]), numOr(bal["expires_at"]))
			continue
		}
		quotaMap[name] = map[string]any{
			"total":      bal["total_units"],
			"used":       bal["used_units"],
			"remaining":  bal["remaining_units"],
			"expires_at": bal["expires_at"],
		}
	}
}

func unitKey(field string) string {
	switch field {
	case "total":
		return "total_units"
	case "used":
		return "used_units"
	case "remaining":
		return "remaining_units"
	}
	return field
}

// mergePlanEntitlements parses `plans[].entitlements[]` for active plans.
//
// The upstream reports weekend/promo grants there while `balances[]` stays
// empty, so skipping this pass would make a freshly claimed package invisible to
// the local quota view (the exact failure the 2026-09-18 fix addressed).
func mergePlanEntitlements(quotaMap map[string]any, body map[string]any) {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return
	}
	plans, ok := data["plans"].([]any)
	if !ok {
		return
	}
	for _, it := range plans {
		plan, ok := it.(map[string]any)
		if !ok || str(plan["status"]) != "active" {
			continue
		}
		planEnds := numOr(plan["ends_at"])
		planStarts := plan["starts_at"]
		ents, ok := plan["entitlements"].([]any)
		if !ok {
			continue
		}
		for _, e := range ents {
			ent, ok := e.(map[string]any)
			if !ok || str(ent["meter"]) != "model_usage" || str(ent["unit_type"]) != "token" {
				continue
			}
			name := firstString(ent, "show_name", "model")
			if name == "" {
				continue
			}
			units := numOr(ent["grant_units"])
			effectiveAt := ent["effective_at"]
			if effectiveAt == nil {
				effectiveAt = planStarts
			}
			if prev, ok := quotaMap[name].(map[string]any); ok && str(prev["source"]) == "plan" {
				prev["total"] = numOr(prev["total"]) + units
				prev["remaining"] = numOr(prev["remaining"]) + units
				prev["expires_at"] = maxNum(numOr(prev["expires_at"]), planEnds)
				continue
			}
			quotaMap[name] = map[string]any{
				"total":        units,
				"used":         0,
				"remaining":    units,
				"expires_at":   plan["ends_at"],
				"starts_at":    planStarts,
				"effective_at": effectiveAt,
				"plan_name":    firstString(plan, "name", "plan_id"),
				"source":       "plan",
			}
		}
	}
}

// ── subscription ledger (apiKey channel) ────────────────────────────────────

// fetchSubscriptionQuota reproduces `fetch_subscription_quota`. It returns the
// normalised snapshot plus an error string when the upstream reported one (which
// `fetch_quota` folds into its result object).
func (a *API) fetchSubscriptionQuota(ctx context.Context, acc models.Account, d *models.AccountData) (map[string]any, string) {
	apiKey, _ := d.String(models.DataKeyAPIKey)
	if derefString(acc.Mode) == models.ModeJWT || apiKey == "" {
		// `{"not_applicable": True}` contributes no member to the result.
		return nil, ""
	}
	origins := quotaOrigins[acc.Provider]
	if len(origins) == 0 {
		origins = []string{zaiAPIOrigin}
	}
	lastErr := "未查询"
	for _, origin := range origins {
		res, err := a.client.Send(ctx, &upstream.Request{
			Method: http.MethodGet,
			URL:    origin + quotaLimitPath,
			Header: http.Header{
				"Content-Type":  {"application/json"},
				"Authorization": {"Bearer " + apiKey},
			},
			Timeout: 20 * time.Second,
		})
		if err != nil {
			lastErr = "网络错误: " + err.Error()
			continue
		}
		text := readBodyText(res.Body)
		res.Body.Close()
		// This endpoint answers HTTP 200 unconditionally: success is decided by
		// the body, never by the status line.
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			lastErr = "响应非 JSON (HTTP " + strconv.Itoa(res.StatusCode) + ")"
			continue
		}
		code := businessCode(payload)
		switch {
		case truthy(payload["success"]):
			sub := parseSubscription(payload, a.nowSeconds())
			if sub == nil {
				lastErr = "订阅响应无法解析"
				continue
			}
			if err := d.Set(models.DataKeySubscription, sub); err != nil {
				return nil, err.Error()
			}
			if derefString(acc.Status) == models.StatusExhausted {
				active := models.StatusActive
				acc.Status = &active
				if err := d.Set(models.DataKeyStatus, active); err != nil {
					return nil, err.Error()
				}
			}
			if err := a.persist(ctx, acc, d); err != nil {
				return nil, err.Error()
			}
			return sub, ""
		case code == quotaLimitNoPlanCode:
			// A valid key with no Coding Plan is a NORMAL state, not an error
			// and never an auth failure. The account's status is left alone —
			// exhaustion is decided by the model-call / billing ledger instead.
			sub := map[string]any{
				"level":         nil,
				"windows":       []any{},
				"token_windows": []any{},
				"checked_at":    a.nowSeconds(),
				"no_plan":       true,
			}
			if err := d.Set(models.DataKeySubscription, sub); err != nil {
				return nil, err.Error()
			}
			if err := a.persist(ctx, acc, d); err != nil {
				return nil, err.Error()
			}
			return sub, ""
		case quotaLimitBadKeyCodes[code] || res.StatusCode == http.StatusUnauthorized ||
			res.StatusCode == http.StatusForbidden:
			msg := "API Key 鉴权失败（订阅额度端点）"
			if err := a.markInvalid(ctx, acc, d, msg); err != nil {
				return nil, err.Error()
			}
			return nil, msg
		default:
			lastErr = strings.TrimSpace("上游 code=" + strconv.Itoa(code) + " " + str(payload["msg"]))
		}
	}
	return nil, lastErr
}

// parseSubscription reproduces `_parse_subscription`: normalise the upstream
// quota-limit payload into a percentage-based window snapshot. Field names are
// unioned across known upstream variants and a missing value is tolerated —
// showing less is preferable to failing the whole refresh chain.
func parseSubscription(payload map[string]any, checkedAt float64) map[string]any {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil
	}
	limits, ok := data["limits"].([]any)
	if !ok {
		return nil
	}
	windows := []any{}
	for _, it := range limits {
		item, ok := it.(map[string]any)
		if !ok {
			continue
		}
		wtype := strings.ToUpper(str(item["type"]))
		pct, hasPct := firstNum(item, "percentage", "used_percent", "usedPercent",
			"usage", "used_percentage", "percent")
		if !hasPct {
			// Some upstream variants report the REMAINING percentage; convert it
			// so the field has one meaning.
			if left, ok := firstNum(item, "remaining_percent", "remainPercent", "remaining"); ok {
				pct = 100 - left
				if pct < 0 {
					pct = 0
				}
				hasPct = true
			}
		}
		var pctVal any
		if hasPct {
			pctVal = pct
		}
		kind := "mcp"
		if wtype == "TOKENS_LIMIT" {
			kind = "token"
		}
		typ := wtype
		if typ == "" {
			typ = "UNKNOWN"
		}
		windows = append(windows, map[string]any{
			"kind":            kind,
			"type":            typ,
			"used_percent":    pctVal,
			"limit":           firstNumAny(item, "limit", "total", "quota"),
			"remaining":       firstNumAny(item, "remaining", "remain"),
			"next_reset_time": firstNonNil(item["nextResetTime"], item["next_reset_time"]),
		})
	}
	tokenWindows := []any{}
	for _, w := range windows {
		if obj, ok := w.(map[string]any); ok && str(obj["kind"]) == "token" {
			tokenWindows = append(tokenWindows, w)
		}
	}
	// Upstream order is "soonest reset first" — the rolling window then the
	// weekly one.
	sort.SliceStable(tokenWindows, func(i, j int) bool {
		x, _ := tokenWindows[i].(map[string]any)
		y, _ := tokenWindows[j].(map[string]any)
		return numOr(x["next_reset_time"]) < numOr(y["next_reset_time"])
	})
	level := firstNonNil(data["level"], data["plan"])
	if level == nil {
		level = "unknown"
	}
	return map[string]any{
		"level":         level,
		"windows":       windows,
		"token_windows": tokenWindows,
		"checked_at":    checkedAt,
	}
}

// ── batch refresh ───────────────────────────────────────────────────────────

// refreshAccounts reproduces `quota.refresh_accounts`: concurrent refresh behind
// a semaphore of 8. `ok` counts the accounts whose result carries no error;
// `fail` is everything else (including transport exceptions).
func (a *API) refreshAccounts(ctx context.Context, accounts []models.Account) okFailBody {
	if len(accounts) == 0 {
		return okFailBody{}
	}
	sem := make(chan struct{}, quotaRefreshConcurrency)
	results := make([]bool, len(accounts))
	var wg sync.WaitGroup
	for i, acc := range accounts {
		wg.Add(1)
		go func(i int, acc models.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := a.fetchQuota(ctx, acc)
			results[i] = err == nil && res.OK
		}(i, acc)
	}
	wg.Wait()
	ok := 0
	for _, r := range results {
		if r {
			ok++
		}
	}
	return okFailBody{OK: ok, Fail: len(accounts) - ok}
}

// ── credential accessors ────────────────────────────────────────────────────

// a0JWT reads an account's stored JWT without a receiver. Free function so the
// pure helpers (jwtUserID) can use it.
func a0JWT(acc models.Account) (string, bool) {
	d, err := acc.ParseData()
	if err != nil {
		return "", false
	}
	s, ok := d.String(models.DataKeyJWTToken)
	return s, ok && s != ""
}

// jwtToken returns the account's JWT when it is usable as a Bearer credential.
func (a *API) jwtToken(acc models.Account) (string, bool) { return a0JWT(acc) }

// apiKey returns the account's API key when it is usable as a subscription
// credential. A JWT account has no api_key, and vice versa — the two modes are
// mutually exclusive (models.Account.create sets exactly one of them).
func (a *API) apiKey(acc models.Account) (string, bool) {
	d, err := acc.ParseData()
	if err != nil {
		return "", false
	}
	s, ok := d.String(models.DataKeyAPIKey)
	return s, ok && s != ""
}

// ── small JSON helpers ──────────────────────────────────────────────────────

// numOr reads a JSON number, treating absent/null as 0 (Python's `or 0`).
func numOr(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func maxNum(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// truthy reproduces Python truthiness for the JSON values these payloads carry.
func truthy(v any) bool {
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

func firstNum(obj map[string]any, names ...string) (float64, bool) {
	for _, n := range names {
		if f, ok := obj[n].(float64); ok {
			return f, true
		}
	}
	return 0, false
}

func firstNumAny(obj map[string]any, names ...string) any {
	for _, n := range names {
		if f, ok := obj[n].(float64); ok {
			return f
		}
	}
	return nil
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}
