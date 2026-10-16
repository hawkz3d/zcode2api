package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"zhipucode2api/internal/models"
)

// monitoringKeep is the legacy `reqlog.KEEP`: the ring buffer's capacity. It is
// reported verbatim by B-21 so the UI can compute "is the buffer full".
const monitoringKeep = 500

// quotaRefreshIntervalEnv is the fallback interval when the meta row is missing
// or unparsable (`settings.QUOTA_REFRESH_INTERVAL`, default 60 s).
const quotaRefreshIntervalEnv = "ZCODE_QUOTA_REFRESH_INTERVAL"

// ── B-17 GET /admin/api/settings ─────────────────────────────────────────────

// settingsBody is the measured three-key envelope, in order. Note the type
// split, which is easy to get wrong: `admin_key` and `gateway_key` are strings,
// while `quota_refresh_interval` is a NUMBER — the store accessor coerces the
// persisted string with `int()` before returning it (measured live: `600`, not
// `"600"`). Emitting a string here would fail the positional type tree.
type settingsBody struct {
	AdminKey            string `json:"admin_key"`
	GatewayKey          string `json:"gateway_key"`
	QuotaRefreshInterval int   `json:"quota_refresh_interval"`
}

func (a *API) handleGetSettings(c *reqCtx) {
	ctx := c.r.Context()
	adminKey, err := a.adminKey(ctx)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	gatewayKey, _, err := a.settings.GetSetting(ctx, "gateway_key")
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	interval, err := a.quotaRefreshInterval(ctx)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	// ADR-004: the admin key is echoed in plaintext. That is the CONTRACT (the
	// legacy UI displays it so an operator can read it back); the mitigation is
	// documentary + tool-side redaction, not a changed response.
	writeJSON(c.w, http.StatusOK, settingsBody{
		AdminKey:             adminKey,
		GatewayKey:           gatewayKey,
		QuotaRefreshInterval: interval,
	})
}

// quotaRefreshInterval reproduces `store.quota_refresh_interval()`:
//
//	try:    return max(0, int(get_setting("quota_refresh_interval", DEFAULT)))
//	except (TypeError, ValueError): return DEFAULT
//
// The stored value is a STRING in meta, so the parse is the load-bearing step.
func (a *API) quotaRefreshInterval(ctx context.Context) (int, error) {
	fallback := 60
	if v := envValue(quotaRefreshIntervalEnv); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			fallback = n
		}
	}
	raw, ok, err := a.settings.GetSetting(ctx, "quota_refresh_interval")
	if err != nil {
		return 0, err
	}
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback, nil
	}
	if n < 0 {
		n = 0
	}
	return n, nil
}

// ── B-18 PUT /admin/api/settings ─────────────────────────────────────────────

func (a *API) handlePutSettings(c *reqCtx) {
	ctx := c.r.Context()
	obj := c.object()

	if v, ok := obj["admin_key"]; ok {
		key := ""
		if s, ok := v.(string); ok {
			key = strings.TrimSpace(s)
		}
		// `payload["admin_key"] or ""` — a non-string truthy value would have
		// raised in Python; the measured contract only requires the empty case.
		if key == "" {
			writeShapeA(c.w, http.StatusBadRequest, "后台密钥不能为空")
			return
		}
		if err := a.settings.SetSetting(ctx, "admin_key", key); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
	}
	if v, ok := obj["gateway_key"]; ok {
		s, _ := v.(string)
		if err := a.settings.SetSetting(ctx, "gateway_key", strings.TrimSpace(s)); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
	}
	if v, ok := obj["quota_refresh_interval"]; ok {
		n, err := pyInt(v)
		if err != nil {
			writeShapeA(c.w, http.StatusBadRequest, "刷新间隔必须是非负整数")
			return
		}
		if n < 0 {
			n = 0
		}
		if err := a.settings.SetSetting(ctx, "quota_refresh_interval", strconv.Itoa(n)); err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
	}
	writeJSON(c.w, http.StatusOK, okBody{OK: true})
}

// pyInt reproduces Python's `int()` for the JSON values a body can carry.
//
// The divergence that matters is `bool`: `int(True) == 1` in Python, whereas a
// naive Go float conversion would also yield 1 — so bools are accepted. Floats
// are TRUNCATED toward zero (`int(2.9) == 2`), and strings are parsed, which is
// what makes `{"quota_refresh_interval": "abc"}` a 400 rather than a 500.
func pyInt(v any) (int, error) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case float64:
		return int(t), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(t))
	}
	return 0, errNotInt
}

var errNotInt = errString("not an integer")

// ── B-19 GET /admin/api/export ───────────────────────────────────────────────

// exportBody is `store.export()`. `providers` is keyed by provider NAME and
// carries the PLAINTEXT credential by design (CR-06): this endpoint exists to
// move an operator's pool between deployments, so masking it would make the
// export useless. The exposure is bounded by the admin-key gate.
//
// The keys are emitted in PROVIDERS order (`zai` before `bigmodel`), which is
// why Providers is an ordered slice rather than a map.
type exportBody struct {
	Version    int            `json:"version"`
	ExportedAt float64        `json:"exported_at"`
	Providers  []exportBucket `json:"providers"`
}

type exportBucket struct {
	Provider string         `json:"-"`
	Items    []exportItem   `json:"-"`
}

type exportItem struct {
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Secret string `json:"secret"`
}

// MarshalJSON emits the providers object with insertion-ordered keys.
func (e exportBody) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteString(`{"version":`)
	v, err := marshalJSON(e.Version)
	if err != nil {
		return nil, err
	}
	b.Write(v)
	b.WriteString(`,"exported_at":`)
	v, err = marshalJSON(e.ExportedAt)
	if err != nil {
		return nil, err
	}
	b.Write(v)
	b.WriteString(`,"providers":{`)
	for i, bucket := range e.Providers {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := marshalJSON(bucket.Provider)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		items := bucket.Items
		if items == nil {
			items = []exportItem{}
		}
		v, err := marshalJSON(items)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteString("}}")
	return []byte(b.String()), nil
}

func (a *API) handleExport(c *reqCtx) {
	ctx := c.r.Context()
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	out := exportBody{Version: 1, ExportedAt: a.nowSeconds()}
	for _, p := range providersTuple {
		bucket := exportBucket{Provider: p, Items: []exportItem{}}
		for _, r := range rows {
			if r.Provider != p {
				continue
			}
			d, err := r.ParseData()
			if err != nil {
				continue
			}
			bucket.Items = append(bucket.Items, exportItem{
				Name:   derefString(r.Name),
				Mode:   derefString(r.Mode),
				Secret: secretOf(r, d),
			})
		}
		out.Providers = append(out.Providers, bucket)
	}
	writeJSON(c.w, http.StatusOK, out)
}

// ── B-20 POST /admin/api/import ──────────────────────────────────────────────

type importBody struct {
	Count int `json:"count"`
}

// handleImport reproduces `store.import_accounts` + the handler's auto-claim
// fan-out.
//
// `count` counts every ACCEPTED entry, not every newly created row: the legacy
// loop increments unconditionally after calling `add_account`, and `add_account`
// silently returns an existing account when the secret is already present. So
// re-importing the same export reports a count while adding nothing — which is
// exactly the idempotence the integration test asserts.
func (a *API) handleImport(c *reqCtx) {
	ctx := c.r.Context()
	obj := c.object()

	before, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	existing := map[string]bool{}
	for _, r := range before {
		if r.Provider == models.ProviderZai {
			existing[r.ID] = true
		}
	}

	providers, _ := obj["providers"].(map[string]any)
	count := 0
	for _, p := range providersTuple {
		raw, ok := providers[p]
		if !ok {
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, it := range items {
			item, ok := it.(map[string]any)
			if !ok {
				continue
			}
			secret := firstNonEmptyString(item, "secret", "token", "jwtToken", "apiKey")
			if secret == "" {
				continue
			}
			name := firstNonEmptyString(item, "name")
			if name == "" {
				name = p
			}
			count++
			if err := a.importOne(ctx, p, name, secret); err != nil {
				writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
				return
			}
		}
	}

	// Auto-claim for the newly added JWT accounts, exactly as the legacy handler
	// does. It runs in the background because the legacy call is a scheduled
	// task: the import response must not wait on the upstream claim chain.
	after, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	for _, r := range after {
		if r.Provider != models.ProviderZai || existing[r.ID] {
			continue
		}
		if derefString(r.Mode) == models.ModeJWT {
			a.scheduleAutoClaim(r)
		}
	}
	writeJSON(c.w, http.StatusOK, importBody{Count: count})
}

// importOne adds one imported entry, reproducing `store.add_account`'s
// SECRET-based dedupe: an identical credential is already in the pool, so the
// call returns the existing row and persists nothing.
func (a *API) importOne(ctx context.Context, provider, name, secret string) error {
	if !isProvider(provider) {
		return nil
	}
	trimmed := strings.TrimSpace(secret)
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Provider != provider {
			continue
		}
		d, err := r.ParseData()
		if err != nil {
			continue
		}
		if s := secretOf(r, d); s != "" && s == trimmed {
			return nil
		}
	}
	acc, err := newAccount(provider, name, trimmed)
	if err != nil {
		return err
	}
	return a.accounts.UpsertAccount(ctx, acc)
}

// ── B-21 / B-22 request monitoring ──────────────────────────────────────────

// monitoringBody is `{"entries": [...], "keep": 500}`. `keep` is the ring
// capacity, not the current length — the UI uses it to show "n/500".
type monitoringBody struct {
	Entries []json.RawMessage `json:"entries"`
	Keep    int               `json:"keep"`
}

// handleMonitoring returns the gateway request log snapshot.
//
// SCOPE NOTE (disclosed in the delivery report): the log is populated by the
// gateway through the injectable `Options.Monitoring` hook, because this
// package must not import internal/gateway. Until the gateway calls into a
// ReqLog, this returns an empty ring.
//
// 🔴 CORRECTION (measured 2026-10-16, captain reproduced independently): an
// earlier revision of this comment claimed the LIVE legacy baseline "also
// returns `{"entries":[],"keep":500}` (measured repeatedly)". That was WRONG —
// it generalised a single observation. The baseline ring is only empty right
// after `POST /admin/api/monitoring/clear` or a restart, and does NOT self-heal
// until new traffic arrives (Hilkiah t5 observed the same). With production
// traffic present the baseline returned 6 entries at the time of measurement.
// The candidate, having no gateway instrumentation, is empty ALWAYS — that is a
// real behavioural gap, not a benign shape match, and the conformance ORDER
// layer correctly fails it (one side has a sample, the other does not).
func (a *API) handleMonitoring(c *reqCtx) {
	entries := []json.RawMessage{}
	if a.opt.Monitoring != nil {
		if got := a.opt.Monitoring(); len(got) > 0 {
			entries = got
			if len(entries) > monitoringKeep {
				entries = entries[len(entries)-monitoringKeep:]
			}
		}
	}
	writeJSON(c.w, http.StatusOK, monitoringBody{Entries: entries, Keep: monitoringKeep})
}

// handleMonitoringClear empties the ring. With no hook installed this is a
// no-op that still answers `{"ok":true}` — the legacy handler has no failure
// path either (it clears an in-memory deque).
func (a *API) handleMonitoringClear(c *reqCtx) {
	if a.opt.MonitoringClear != nil {
		a.opt.MonitoringClear()
	}
	writeJSON(c.w, http.StatusOK, okBody{OK: true})
}

// ── auto-claim scheduling ───────────────────────────────────────────────────

// scheduleAutoClaim fires the claim chain for one freshly added JWT account in
// the background, mirroring `_schedule_auto_claim`.
//
// It is best-effort by design: the legacy task swallows every failure (a claim
// error, a captcha solver outage, a transport fault) and logs it, because the
// account is already usable — only the promo package would be missed.
func (a *API) scheduleAutoClaim(acc models.Account) {
	if derefString(acc.Mode) != models.ModeJWT {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = a.runAutoClaim(ctx, acc)
	}()
}

// runAutoClaim claims every available plan for one account. The legacy helper
// reports activation events first, then claims each previewed plan; failures at
// any stage are informational only.
func (a *API) runAutoClaim(ctx context.Context, acc models.Account) error {
	_ = a.reportActivationEvents(ctx, acc)
	plans, err := a.previewPlans(ctx, acc)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if _, err := a.claimOne(ctx, acc, p.PlanID); err != nil {
			continue
		}
	}
	return nil
}
