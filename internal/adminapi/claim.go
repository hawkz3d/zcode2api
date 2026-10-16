package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// ── upstream coordinates (constants.py) ─────────────────────────────────────

const (
	zcodeOrigin         = "https://zcode.z.ai"
	bigmodelOrigin      = "https://open.bigmodel.cn"
	zaiAPIOrigin        = "https://api.z.ai"
	zcodeBillingBase    = zcodeOrigin + "/api/v1/zcode-plan"
	quotaLimitPath      = "/api/monitor/usage/quota/limit"
	clientConfigsURL    = zcodeOrigin + "/api/v1/client/configs"
	eventReportURL      = zcodeOrigin + "/api/v1/event/report"
	billingAppVersion   = "3.11.2"
	billingTitle        = "Z Code@electron"
	billingReleaseCh    = "stable"
	clientPlatform      = "darwin-arm64"
	captchaHeader       = "X-Aliyun-Captcha-Verify-Param"
	captchaDefaultsSID  = "11xygtvd"
	captchaDefaultsRgn  = "cn"
	captchaDefaultsPfx  = "no8xfe"
	quotaLimitNoPlanCode = 500
)

// captchaConfigCacheTTL is the legacy `settings.CAPTCHA_CONFIG_CACHE_TTL`
// (600_000 ms).
const captchaConfigCacheTTL = 10 * time.Minute

// claimFailMessages is `_CLAIM_FAIL`, the upstream business-code -> user text
// table. The exact strings are contract (they surface in `message`).
var claimFailMessages = map[int]string{
	1001: "套餐不存在",
	1002: "活动已结束或套餐暂不可领取",
	1003: "该套餐已经领取过",
	1004: "不符合领取条件",
	1005: "今日领取名额已用完",
	3001: "领取参数错误，请刷新后重试",
	3007: "验证码校验失败，请重试",
	401:  "请先登录后再领取",
}

// claimError is the Go analogue of `ClaimError`: a business failure whose
// message is user-facing.
type claimError struct{ msg string }

func (e *claimError) Error() string { return e.msg }

// failMessage reproduces `_fail_message`: the base text plus the upstream
// server message in full-width parentheses when one is present.
func failMessage(code int, body map[string]any) string {
	base, ok := claimFailMessages[code]
	if !ok {
		base = "领取失败"
	}
	server := ""
	if s, ok := body["msg"].(string); ok && s != "" {
		server = s
	} else if s, ok := body["message"].(string); ok && s != "" {
		server = s
	}
	if server != "" {
		return base + "（" + server + "）"
	}
	return base
}

// businessCode is `_business_code`: the upstream `code`, or -1 when absent or
// unparsable.
func businessCode(body map[string]any) int {
	v, ok := body["code"]
	if !ok || v == nil {
		return -1
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		if n, err := parseIntStrict(t); err == nil {
			return n
		}
	}
	return -1
}

// ── B-13 GET /admin/api/claim/preview ───────────────────────────────────────

// claimPreviewItem is one entry of the preview list. Key order is measured:
// account_id, account_name, plans, error, activated, activation_error.
type claimPreviewItem struct {
	AccountID       string           `json:"account_id"`
	AccountName     string           `json:"account_name"`
	Plans           []claimPlanView  `json:"plans"`
	Error           *string          `json:"error"`
	Activated       bool             `json:"activated"`
	ActivationError *string          `json:"activation_error"`
}

type claimPreviewBody struct {
	Preview []claimPreviewItem `json:"preview"`
}

// claimPlanView is `parse_plan`'s output.
type claimPlanView struct {
	PlanID      string          `json:"plan_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Priority    json.RawMessage `json:"priority"`
	Grants      []claimGrant    `json:"grants"`
}

type claimGrant struct {
	Name   string  `json:"name"`
	Units  float64 `json:"units"`
	Period string  `json:"period"`
}

func (a *API) handleClaimPreview(c *reqCtx) {
	ctx := c.r.Context()
	accountID := c.r.URL.Query().Get("account_id")
	accounts, err := a.jwtAccounts(ctx, accountID)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	out := make([]claimPreviewItem, 0, len(accounts))
	for _, acc := range accounts {
		item := claimPreviewItem{
			AccountID:   acc.ID,
			AccountName: derefString(acc.Name),
			Plans:       []claimPlanView{},
		}
		if isCooling(acc, a.nowSeconds()) {
			msg := "账号冷却中（风控/限流），已跳过上游查询"
			item.Error = &msg
			out = append(out, item)
			continue
		}
		// Activation events are reported before the preview: the upstream treats
		// a same-day desktop "active" signal as a投放资格 cue. A failure is
		// surfaced in `activation_error` but does NOT abort the preview.
		actErr := a.reportActivationEvents(ctx, acc)
		item.Activated = actErr == ""
		if actErr != "" {
			e := actErr
			item.ActivationError = &e
		}
		plans, err := a.previewPlans(ctx, acc)
		if err != nil {
			var ce *claimError
			if errors.As(err, &ce) {
				e := ce.msg
				item.Error = &e
				out = append(out, item)
				continue
			}
			e := err.Error()
			item.Error = &e
			out = append(out, item)
			continue
		}
		item.Plans = plans
		out = append(out, item)
	}
	writeJSON(c.w, http.StatusOK, claimPreviewBody{Preview: out})
}

// jwtAccounts reproduces `_jwt_accounts`: zai accounts in jwt mode with a
// token, optionally narrowed to one id.
func (a *API) jwtAccounts(ctx context.Context, onlyID string) ([]models.Account, error) {
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := []models.Account{}
	for _, r := range rows {
		if r.Provider != models.ProviderZai {
			continue
		}
		if onlyID != "" && r.ID != onlyID {
			continue
		}
		if derefString(r.Mode) != models.ModeJWT {
			continue
		}
		d, err := r.ParseData()
		if err != nil {
			continue
		}
		if s, ok := d.String(models.DataKeyJWTToken); !ok || s == "" {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// previewPlans performs `GET {billing}/billing/preview?app_version=&platform=`
// and parses the plans, sorted by (-priority, plan_id).
func (a *API) previewPlans(ctx context.Context, acc models.Account) ([]claimPlanView, error) {
	q := url.Values{}
	q.Set("app_version", a.opt.Version)
	q.Set("platform", clientPlatform)
	body, err := a.billingRequest(ctx, acc, http.MethodGet,
		"/billing/preview?"+q.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	if code := businessCode(body); code != 0 {
		return nil, &claimError{msg: failMessage(code, body)}
	}
	data, _ := body["data"].(map[string]any)
	raw, _ := data["plans"].([]any)
	plans := make([]claimPlanView, 0, len(raw))
	for _, it := range raw {
		obj, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := parsePlan(obj); ok {
			plans = append(plans, p)
		}
	}
	// Stable sort by (-priority, plan_id). Insertion sort keeps it explicit and
	// dependency-free; plan lists are tiny.
	for i := 1; i < len(plans); i++ {
		for j := i; j > 0 && planLess(plans[j], plans[j-1]); j-- {
			plans[j], plans[j-1] = plans[j-1], plans[j]
		}
	}
	return plans, nil
}

func planLess(a, b claimPlanView) bool {
	pa, pb := planPriority(a), planPriority(b)
	if pa != pb {
		return pa > pb // descending priority
	}
	return a.PlanID < b.PlanID
}

func planPriority(p claimPlanView) int {
	var n int
	_ = json.Unmarshal(p.Priority, &n)
	return n
}

// parsePlan reproduces `parse_plan`: require a plan_id, keep only
// `model_usage`+`token` entitlements with a show_name, and default the period
// to `one_time`.
func parsePlan(raw map[string]any) (claimPlanView, bool) {
	planID := strings.TrimSpace(firstString(raw, "plan_id", "planId"))
	if planID == "" {
		return claimPlanView{}, false
	}
	grants := []claimGrant{}
	if ents, ok := raw["entitlements"].([]any); ok {
		for _, e := range ents {
			ent, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if str(ent["meter"]) != "model_usage" || str(ent["unit_type"]) != "token" {
				continue
			}
			name := strings.TrimSpace(firstString(ent, "show_name", "showName"))
			if name == "" {
				continue
			}
			units := 0.0
			if f, ok := ent["grant_units"].(float64); ok {
				units = f
			} else if f, ok := ent["grantUnits"].(float64); ok {
				units = f
			}
			period := str(ent["period"])
			if period == "" {
				period = "one_time"
			}
			grants = append(grants, claimGrant{Name: name, Units: units, Period: period})
		}
	}
	prio := json.RawMessage("0")
	if v, ok := raw["priority"]; ok && v != nil {
		if b, err := marshalJSON(v); err == nil {
			prio = b
		}
	}
	return claimPlanView{
		PlanID:      planID,
		Name:        strings.TrimSpace(str(raw["name"])),
		Description: strings.TrimSpace(str(raw["description"])),
		Priority:    prio,
		Grants:      grants,
	}, true
}

// ── B-14 POST /admin/api/claim ──────────────────────────────────────────────

type claimOutcome struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	OK          bool   `json:"ok"`
	PlanName    string `json:"plan_name,omitempty"`
	PlanID      string `json:"plan_id,omitempty"`
	Message     string `json:"message,omitempty"`
}

type claimBody struct {
	Outcomes []claimOutcome `json:"outcomes"`
	Summary  okFailBody     `json:"summary"`
}

func (a *API) handleClaim(c *reqCtx) {
	ctx := c.r.Context()
	accountIDs, _ := c.stringList("account_ids")
	planID := strings.TrimSpace(str(c.object()["plan_id"]))

	// `account_ids` absent or empty means "all JWT accounts".
	only := ""
	if len(accountIDs) > 0 {
		// The legacy code passes the LIST through to `_jwt_accounts`, which
		// filters on membership; an empty list is normalised to None.
		accounts, err := a.jwtAccountsMulti(ctx, accountIDs)
		if err != nil {
			writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		a.runClaim(ctx, c, accounts, planID)
		return
	}
	accounts, err := a.jwtAccounts(ctx, only)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	a.runClaim(ctx, c, accounts, planID)
}

// runClaim executes the claim loop over the candidate accounts.
func (a *API) runClaim(ctx context.Context, c *reqCtx, accounts []models.Account, planID string) {
	if len(accounts) == 0 {
		// Measured legacy early return: an empty candidate list short-circuits
		// BEFORE any cooling note is emitted.
		writeJSON(c.w, http.StatusOK, claimBody{
			Outcomes: []claimOutcome{},
			Summary:  okFailBody{},
		})
		return
	}
	outcomes := make([]claimOutcome, 0, len(accounts))
	// Cooling accounts get a skip note first, in candidate order, and are then
	// excluded from the actual claim loop.
	for _, acc := range accounts {
		if isCooling(acc, a.nowSeconds()) {
			outcomes = append(outcomes, claimOutcome{
				AccountID:   acc.ID,
				AccountName: derefString(acc.Name),
				OK:          false,
				Message:     "账号冷却中（风控/限流），已跳过领取",
			})
		}
	}
	for _, acc := range accounts {
		if isCooling(acc, a.nowSeconds()) {
			continue
		}
		result, err := a.claimOne(ctx, acc, planID)
		if err != nil {
			// Every failure mode is a business receipt, never a 500: the legacy
			// handler catches ClaimError / CaptchaSolveError / RuntimeError and
			// appends `ok:false` with the message.
			outcomes = append(outcomes, claimOutcome{
				AccountID:   acc.ID,
				AccountName: derefString(acc.Name),
				OK:          false,
				Message:     err.Error(),
			})
			continue
		}
		if _, err := a.fetchQuota(ctx, acc); err != nil {
			_ = err
		}
		outcomes = append(outcomes, claimOutcome{
			AccountID:   acc.ID,
			AccountName: derefString(acc.Name),
			OK:          true,
			PlanID:      result.planID,
			PlanName:    result.planName,
		})
	}
	ok := 0
	for _, o := range outcomes {
		if o.OK {
			ok++
		}
	}
	writeJSON(c.w, http.StatusOK, claimBody{
		Outcomes: outcomes,
		Summary:  okFailBody{OK: ok, Fail: len(outcomes) - ok},
	})
}

// claimResult is `claim()`'s return: {"plan_id","plan_name","grants"}.
type claimResult struct {
	planID   string
	planName string
}

// claimOne reproduces `claim()`: auto-pick a plan when none is given, then
// attempt twice when the upstream rejects the captcha with code 3007.
func (a *API) claimOne(ctx context.Context, acc models.Account, planID string) (claimResult, error) {
	planName := ""
	if planID == "" {
		plans, err := a.previewPlans(ctx, acc)
		if err != nil {
			return claimResult{}, err
		}
		if len(plans) == 0 {
			return claimResult{}, &claimError{msg: "没有待领取的套餐"}
		}
		planID = plans[0].PlanID
		planName = plans[0].Name
		if planName == "" {
			planName = plans[0].PlanID
		}
	}
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		verifyParam, verifyRegion, err := a.solveCaptcha(ctx)
		if err != nil {
			return claimResult{}, err
		}
		headers := a.claimHeaders(acc, verifyParam, verifyRegion, a.opt.Version)
		body, err := a.billingRequest(ctx, acc, http.MethodPost, "/billing/claim",
			headers, map[string]any{"plan_id": planID})
		if err != nil {
			return claimResult{}, err
		}
		code := businessCode(body)
		if code == 0 {
			return claimResult{planID: planID, planName: planName}, nil
		}
		if code == 3007 && attempt == 1 {
			// A rejected captcha is retried ONCE with a freshly solved token;
			// the legacy code invalidates the pooled token before retrying.
			a.invalidateCaptcha()
			lastErr = &claimError{msg: failMessage(code, body)}
			continue
		}
		return claimResult{}, &claimError{msg: failMessage(code, body)}
	}
	if lastErr != nil {
		return claimResult{}, lastErr
	}
	return claimResult{}, &claimError{msg: "领取失败"}
}

// ── B-15 GET /admin/api/claim/captcha-config ────────────────────────────────

// captchaConfigBody is the manual-claim SDK bootstrap. Key order is measured:
// enabled, scene_id, region, prefix.
type captchaConfigBody struct {
	Enabled bool   `json:"enabled"`
	SceneID string `json:"scene_id"`
	Region  string `json:"region"`
	Prefix  string `json:"prefix"`
}

func (a *API) handleCaptchaConfig(c *reqCtx) {
	cfg := a.captchaConfig(c.r.Context())
	writeJSON(c.w, http.StatusOK, captchaConfigBody{
		Enabled: true,
		SceneID: cfg.sceneID,
		Region:  cfg.region,
		Prefix:  cfg.prefix,
	})
}

// captchaConfig reads the live client config and falls back to the built-in
// defaults. Measured live response: scene_id 11xygtvd, region **cn**, prefix
// no8xfe.
//
// ⚠️ The upstream-protocol doc says the default region is `sgp`, but the live
// service answers `cn`; live is authoritative for contract equivalence.
func (a *API) captchaConfig(ctx context.Context) struct{ sceneID, region, prefix string } {
	defaults := captchaConfigBody{
		Enabled: true,
		SceneID: captchaDefaultsSID,
		Region:  captchaDefaultsRgn,
		Prefix:  captchaDefaultsPfx,
	}

	// Cached for captchaConfigCacheTTL, mirroring the legacy
	// `settings.CAPTCHA_CONFIG_CACHE_TTL` (600_000 ms): the admin UI polls this
	// endpoint and the upstream config changes at most a few times a day, so an
	// uncached call per request would be a self-inflicted traffic amplifier.
	a.captchaMu.Lock()
	if a.captchaCached.IsZero() || a.now().Sub(a.captchaCached) >= captchaConfigCacheTTL {
		a.captchaCache = a.fetchCaptchaConfig(ctx, defaults)
		a.captchaCached = a.now()
	}
	cached := a.captchaCache
	a.captchaMu.Unlock()
	if cached.SceneID == "" {
		cached = defaults
	}
	return struct{ sceneID, region, prefix string }{
		sceneID: cached.SceneID, region: cached.Region, prefix: cached.Prefix,
	}
}

// fetchCaptchaConfig performs the single upstream read behind the cache.
func (a *API) fetchCaptchaConfig(ctx context.Context, defaults captchaConfigBody) captchaConfigBody {
	res, err := a.client.Send(ctx, &upstream.Request{
		Method: http.MethodGet,
		URL: clientConfigsURL + "?app_version=" + url.QueryEscape(a.opt.Version) +
			"&platform=" + url.QueryEscape(clientPlatform),
		Timeout: 15 * time.Second,
	})
	if err != nil {
		return defaults
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return defaults
	}
	var env struct {
		Data struct {
			Configs struct {
				Captcha struct {
					Enabled bool   `json:"enabled"`
					SceneID string `json:"sceneId"`
					Region  string `json:"region"`
					Prefix  string `json:"prefix"`
				} `json:"captcha"`
			} `json:"configs"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return defaults
	}
	out := defaults
	c := env.Data.Configs.Captcha
	if c.SceneID != "" {
		out.SceneID = c.SceneID
	}
	if c.Region != "" {
		out.Region = c.Region
	}
	if c.Prefix != "" {
		out.Prefix = c.Prefix
	}
	return out
}

// ── B-16 POST /admin/api/claim/manual ───────────────────────────────────────

func (a *API) handleClaimManual(c *reqCtx) {
	ctx := c.r.Context()
	accountID := strings.TrimSpace(str(c.object()["account_id"]))
	verifyParam := strings.TrimSpace(str(c.object()["captcha_verify_param"]))
	region := strings.TrimSpace(str(c.object()["captcha_region"]))
	planID := strings.TrimSpace(str(c.object()["plan_id"]))

	if accountID == "" {
		writeShapeA(c.w, http.StatusBadRequest, detailMissingAccountID)
		return
	}
	acc, found, err := a.jwtAccountByID(ctx, accountID)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !found {
		// Distinct detail text from the generic account 404 (measured).
		writeShapeA(c.w, http.StatusNotFound, detailJWTAccountMissing)
		return
	}
	if isCooling(acc, a.nowSeconds()) {
		writeJSON(c.w, http.StatusOK, claimBody{
			Outcomes: []claimOutcome{{
				AccountID:   acc.ID,
				AccountName: derefString(acc.Name),
				OK:          false,
				Message:     "账号冷却中（风控/限流），已跳过领取",
			}},
			Summary: okFailBody{Fail: 1},
		})
		return
	}
	if verifyParam == "" {
		// The manual path forwards a browser-solved token and never invokes the
		// solver; a missing token is a business failure, not a 400.
		writeJSON(c.w, http.StatusOK, claimBody{
			Outcomes: []claimOutcome{{
				AccountID:   acc.ID,
				AccountName: derefString(acc.Name),
				OK:          false,
				Message:     "缺少验证码参数，请先完成人机验证",
			}},
			Summary: okFailBody{Fail: 1},
		})
		return
	}
	result, err := a.claimManual(ctx, acc, verifyParam, region, planID)
	if err != nil {
		writeJSON(c.w, http.StatusOK, claimBody{
			Outcomes: []claimOutcome{{
				AccountID:   acc.ID,
				AccountName: derefString(acc.Name),
				OK:          false,
				Message:     err.Error(),
			}},
			Summary: okFailBody{Fail: 1},
		})
		return
	}
	if _, err := a.fetchQuota(ctx, acc); err != nil {
		_ = err
	}
	writeJSON(c.w, http.StatusOK, claimBody{
		Outcomes: []claimOutcome{{
			AccountID:   acc.ID,
			AccountName: derefString(acc.Name),
			OK:          true,
			PlanID:      result.planID,
			PlanName:    result.planName,
		}},
		Summary: okFailBody{OK: 1},
	})
}

// claimManual reproduces `claim_with_captcha`: auto-pick when no plan_id, then
// forward the caller's verify param verbatim (solver invoked 0 times).
func (a *API) claimManual(ctx context.Context, acc models.Account, verifyParam, region, planID string) (claimResult, error) {
	if derefString(acc.Mode) != models.ModeJWT {
		return claimResult{}, &claimError{msg: "仅 Coding Plan (JWT) 账号支持领取"}
	}
	planName := ""
	if planID == "" {
		plans, err := a.previewPlans(ctx, acc)
		if err != nil {
			return claimResult{}, err
		}
		if len(plans) == 0 {
			return claimResult{}, &claimError{msg: "没有待领取的套餐"}
		}
		planID = plans[0].PlanID
		planName = plans[0].Name
		if planName == "" {
			planName = plans[0].PlanID
		}
	}
	if region == "" {
		region = a.captchaConfig(ctx).region
	}
	headers := a.claimHeaders(acc, verifyParam, region, a.opt.Version)
	body, err := a.billingRequest(ctx, acc, http.MethodPost, "/billing/claim",
		headers, map[string]any{"plan_id": planID})
	if err != nil {
		return claimResult{}, err
	}
	if code := businessCode(body); code != 0 {
		return claimResult{}, &claimError{msg: failMessage(code, body)}
	}
	return claimResult{planID: planID, planName: planName}, nil
}

// jwtAccountByID reproduces `store.find("zai", id)` plus the jwt-mode and
// non-empty-token guards of the manual handler.
func (a *API) jwtAccountByID(ctx context.Context, id string) (models.Account, bool, error) {
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		return models.Account{}, false, err
	}
	for _, r := range rows {
		if r.Provider != models.ProviderZai {
			continue
		}
		// `store.find` matches id OR name; the manual endpoint uses find, not
		// find_any, so a name is accepted here.
		if r.ID != id && derefString(r.Name) != id {
			continue
		}
		if derefString(r.Mode) != models.ModeJWT {
			continue
		}
		d, err := r.ParseData()
		if err != nil {
			continue
		}
		if s, ok := d.String(models.DataKeyJWTToken); !ok || s == "" {
			continue
		}
		return r, true, nil
	}
	return models.Account{}, false, nil
}

// jwtAccountsMulti is `_jwt_accounts(ids)` with an explicit id list.
func (a *API) jwtAccountsMulti(ctx context.Context, ids []string) ([]models.Account, error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	rows, err := a.accounts.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := []models.Account{}
	for _, r := range rows {
		if r.Provider != models.ProviderZai || !want[r.ID] {
			continue
		}
		if derefString(r.Mode) != models.ModeJWT {
			continue
		}
		d, err := r.ParseData()
		if err != nil {
			continue
		}
		if s, ok := d.String(models.DataKeyJWTToken); !ok || s == "" {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// ── billing / captcha plumbing ──────────────────────────────────────────────

// billingHeaders reproduces `_auth_headers`: the billing family's fingerprint
// headers, derived from the ACCOUNT's profile (each account is its own device).
func (a *API) billingHeaders(acc models.Account, appVersion string) http.Header {
	if appVersion == "" {
		appVersion = billingAppVersion
	}
	profile := a.profileFor(acc)
	h := http.Header{
		"Content-Type":       {"application/json"},
		"User-Agent":         {"ZCode/" + appVersion},
		"HTTP-Referer":       {zcodeOrigin},
		"X-Title":            {billingTitle},
		"X-Zcode-App-Version": {appVersion},
		"X-Platform":         {profile.Platform + "-" + profile.Arch},
		"X-Release-Channel":  {billingReleaseCh},
		"X-Client-Language":  {profile.Language},
		"X-Client-Timezone":  {profile.Timezone},
		"X-Os-Category":      {osCategory(profile.Platform)},
		"X-Os-Version":       {profile.OSVersion},
		"X-Device-Mid":       {profile.DeviceMid},
		"X-Request-Id":       {newUUIDv4()},
	}
	if s, ok := a.jwtToken(acc); ok && s != "" {
		h.Set("Authorization", "Bearer "+s)
	} else if k, ok := a.apiKey(acc); ok && k != "" {
		h.Set("X-Api-Key", k)
	}
	return h
}

// claimHeaders reproduces `_claim_headers`: billing headers plus the captcha
// verify param, an optional region, and the explicit version/platform backstop
// (measured: a missing version/platform header yields 3007 even with a valid
// captcha).
func (a *API) claimHeaders(acc models.Account, verifyParam, region, appVersion string) http.Header {
	if appVersion == "" {
		appVersion = billingAppVersion
	}
	h := a.billingHeaders(acc, appVersion)
	h.Set(captchaHeader, verifyParam)
	if strings.TrimSpace(region) != "" {
		h.Set("X-Aliyun-Captcha-Verify-Region", strings.TrimSpace(region))
	}
	h.Set("X-Zcode-App-Version", appVersion)
	h.Set("X-Platform", clientPlatform)
	return h
}

// billingRequest reproduces `_billing_request`: a transport failure becomes a
// business error, an upstream 401/403 without captcha wording is an auth
// failure, and a non-JSON body is a business error — never a 500.
func (a *API) billingRequest(ctx context.Context, acc models.Account, method, path string,
	headers http.Header, body map[string]any) (map[string]any, error) {
	var payload []byte
	if body != nil {
		b, err := marshalJSON(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	if headers == nil {
		headers = a.billingHeaders(acc, a.opt.Version)
	}
	res, err := a.client.Send(ctx, &upstream.Request{
		Method:  method,
		URL:     a.opt.ZaiBillingBase + path,
		Header:  headers,
		Body:    payload,
		Timeout: 25 * time.Second,
	})
	if err != nil {
		return nil, &claimError{msg: "上游网络错误: " + err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		text := strings.ToLower(readBodyText(res.Body))
		if !strings.Contains(text, "captcha") && !strings.Contains(text, "verify") {
			return nil, &claimError{msg: "鉴权失败 HTTP " + itoa(res.StatusCode)}
		}
		// A captcha-bearing 401/403 still needs the body parsed, so re-read from
		// the captured text.
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return nil, &claimError{msg: "上游响应非 JSON HTTP " + itoa(res.StatusCode)}
		}
		return parsed, nil
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, &claimError{msg: "上游响应非 JSON HTTP " + itoa(res.StatusCode)}
	}
	return out, nil
}

// reportActivationEvents reproduces `report_activation_events`: report the two
// desktop activation elements, returning an error string (empty = success).
// The event endpoint does not check Authorization.
func (a *API) reportActivationEvents(ctx context.Context, acc models.Account) string {
	userID := jwtUserID(acc)
	if userID == "" {
		return "JWT 无 user_id，跳过激活上报"
	}
	profile := a.profileFor(acc)
	for _, element := range []string{"app_launch", "app_daily_active"} {
		if err := a.postActivationEvent(ctx, profile, userID, element); err != nil {
			return "激活事件 " + element + " 上报失败: " + err.Error()
		}
	}
	return ""
}

func (a *API) postActivationEvent(ctx context.Context, p *fingerprintView, userID, element string) error {
	payload, err := marshalJSON(map[string]any{
		"element":           element,
		"user_id":           userID,
		"app_version":       a.opt.Version,
		"platform":          p.Platform + "-" + p.Arch,
		"os_version":        p.OSVersion,
		"language":          p.Language,
		"timezone":          p.Timezone,
		"screen_resolution": p.Screen,
		"device_mid":        p.DeviceMid,
	})
	if err != nil {
		return err
	}
	res, err := a.client.Send(ctx, &upstream.Request{
		Method:  http.MethodPost,
		URL:     eventReportURL,
		Header:  http.Header{"Content-Type": {"application/json"}},
		Body:    payload,
		Timeout: 20 * time.Second,
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New("HTTP " + itoa(res.StatusCode))
	}
	return nil
}

// jwtUserID reproduces `jwt_user_id`: decode the JWT payload and take
// `user_id`, falling back to `sub`.
func jwtUserID(acc models.Account) string {
	token, ok := a0JWT(acc)
	if !ok {
		return ""
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	if pad := len(seg) % 4; pad != 0 {
		seg += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	for _, k := range []string{"user_id", "sub"} {
		if s, ok := payload[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// solveCaptcha obtains a verify param. Without a configured solver this is a
// business failure (the legacy service would raise CaptchaSolveError, which the
// route layer reports as `ok:false` rather than a 500).
func (a *API) solveCaptcha(ctx context.Context) (string, string, error) {
	if a.solver == nil {
		return "", "", &claimError{msg: "验证码求解失败: 求解器不可用"}
	}
	param, err := a.solver.Solve(ctx)
	if err != nil {
		return "", "", &claimError{msg: "验证码求解失败: " + err.Error()}
	}
	return param, "", nil
}

// invalidateCaptcha drops any pooled token after a 3007 rejection. The Go solver
// holds no pool, so this is a documented no-op that keeps the retry semantics
// (a second, freshly solved token) intact.
func (a *API) invalidateCaptcha() {}

// profileFor reproduces `profile_for`: use the stored profile when it is a
// complete dict, else assign the host profile (idempotent, in-memory).
func (a *API) profileFor(acc models.Account) *fingerprintView {
	if d, err := acc.ParseData(); err == nil {
		if obj, ok := d.Object(models.DataKeyFingerprint); ok {
			fp := &fingerprintView{
				Platform:  rawString(obj, "platform"),
				Arch:      rawString(obj, "arch"),
				OSVersion: rawString(obj, "os_version"),
				Language:  rawString(obj, "language"),
				Timezone:  rawString(obj, "timezone"),
				Screen:    rawString(obj, "screen"),
				DeviceMid: rawString(obj, "device_mid"),
			}
			if fp.DeviceMid != "" {
				return fp
			}
		}
	}
	return hostProfile()
}

// osCategory maps the client platform onto the `X-Os-Category` value.
func osCategory(platform string) string {
	switch platform {
	case "darwin", "macos":
		return "macos"
	case "win32", "windows":
		return "windows"
	default:
		return "linux"
	}
}

// readBodyText drains a bounded response body as text.
func readBodyText(r interface{ Read([]byte) (int, error) }) string {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < 1<<20 {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}

// firstString returns the first key whose value is a non-empty string.
func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// str renders a JSON value as a string, treating null as empty.
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
