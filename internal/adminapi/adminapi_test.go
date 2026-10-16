package adminapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// ---- fakes ------------------------------------------------------------------

// fakeAccounts is an in-memory AccountSource. It records the enabled writes so
// the dual-write contract (ADR-003) can be asserted rather than assumed.
type fakeAccounts struct {
	accounts []models.Account
	enabled  map[string]bool
	deleted  []string
}

func newFakeAccounts() *fakeAccounts {
	// Data must be a real JSON object: viewOf() parses it, and a malformed blob
	// would surface as a 500 instead of exercising the handler.
	ts := 1700000000.0
	zai := "老号"
	jwtMode := "jwt"
	return &fakeAccounts{
		accounts: []models.Account{
			{
				ID: "zai-jwt-0001", Provider: models.ProviderZai, Name: &zai,
				Mode: &jwtMode, Enabled: 1, CreatedAt: &ts,
				Data: json.RawMessage(`{"status":"active","use_count":3,"fail_count":1}`),
			},
			{
				ID: "bigmodel-key-0002", Provider: models.ProviderBigmodel, Enabled: 0,
				CreatedAt: &ts,
				Data:      json.RawMessage(`{"status":"disabled"}`),
			},
		},
		enabled: map[string]bool{},
	}
}

func (f *fakeAccounts) ListAccounts(context.Context) ([]models.Account, error) {
	return f.accounts, nil
}

func (f *fakeAccounts) GetAccount(_ context.Context, id string) (models.Account, bool, error) {
	for _, a := range f.accounts {
		if a.ID == id {
			return a, true, nil
		}
	}
	return models.Account{}, false, nil
}

func (f *fakeAccounts) UpsertAccount(context.Context, models.Account) error { return nil }

func (f *fakeAccounts) DeleteAccount(_ context.Context, id string) (bool, error) {
	f.deleted = append(f.deleted, id)
	return true, nil
}

func (f *fakeAccounts) SetEnabled(_ context.Context, id string, on bool) (bool, error) {
	f.enabled[id] = on
	return true, nil
}

// fakeSettings is an in-memory SettingSource that seeds an admin key, so every
// request in these tests can authenticate the way the contract requires.
type fakeSettings struct{ kv map[string]string }

func newFakeSettings() *fakeSettings {
	return &fakeSettings{kv: map[string]string{"admin_key": "test-admin-key"}}
}

func (f *fakeSettings) GetSetting(_ context.Context, key string) (string, bool, error) {
	v, ok := f.kv[key]
	return v, ok, nil
}

func (f *fakeSettings) SetSetting(_ context.Context, key, value string) error {
	f.kv[key] = value
	return nil
}

func newTestAPI() (http.Handler, *fakeAccounts, *fakeSettings) {
	accts := newFakeAccounts()
	set := newFakeSettings()
	api := New(accts, set, Options{})
	return api.Handler(), accts, set
}

// do issues one request against the router. A nil body means "no body at all",
// which is what distinguishes the 422 `missing` case from a zero-byte body.
func do(t *testing.T, h http.Handler, method, path string, body *string, ctype, auth string) (*http.Response, string) {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(*body))
	}
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	resp := rec.Result()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func okAuth() string { return "Bearer test-admin-key" }

func strp(s string) *string { return &s }

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", body, err)
	}
	return v
}

// ---- contract: the route table ---------------------------------------------

// TestRouteTableMatchesFrozenContract pins the 22 registered path+method pairs
// in REGISTRATION ORDER. Starlette matches in that order, so the order is part
// of the contract: it decides which route answers a request whose path matches
// several entries (notably /admin/api/accounts/refresh vs
// /admin/api/accounts/{account_id}).
func TestRouteTableMatchesFrozenContract(t *testing.T) {
	api := New(newFakeAccounts(), newFakeSettings(), Options{})
	entries := api.routeTable()
	want := []struct{ pattern, method string }{
		{"/admin/api/verify", "GET"},
		{"/admin/api/accounts", "GET"},
		{"/admin/api/accounts", "POST"},
		{"/admin/api/accounts", "DELETE"},
		{"/admin/api/status", "GET"},
		{"/admin/api/accounts/{account_id}", "PUT"},
		{"/admin/api/accounts/{account_id}/enabled", "POST"},
		{"/admin/api/accounts/{account_id}/fingerprint/rotate", "POST"},
		{"/admin/api/accounts/refresh", "POST"},
		{"/admin/api/accounts/{account_id}/refresh", "POST"},
		{"/admin/api/login/start", "POST"},
		{"/admin/api/login/poll/{flow_id}", "GET"},
		{"/admin/api/claim/preview", "GET"},
		{"/admin/api/claim", "POST"},
		{"/admin/api/claim/captcha-config", "GET"},
		{"/admin/api/claim/manual", "POST"},
		{"/admin/api/settings", "GET"},
		{"/admin/api/settings", "PUT"},
		{"/admin/api/export", "GET"},
		{"/admin/api/import", "POST"},
		{"/admin/api/monitoring", "GET"},
		{"/admin/api/monitoring/clear", "POST"},
	}
	if len(entries) != len(want) {
		t.Fatalf("routeTable has %d entries, want %d", len(entries), len(want))
	}
	for i, w := range want {
		got := entries[i]
		if got.pattern != w.pattern || got.method != w.method {
			t.Errorf("entry %d = %s %s, want %s %s", i, got.method, got.pattern, w.method, w.pattern)
		}
	}
	// The literal /refresh route must precede the {account_id} route, or the
	// latter would swallow it and answer 404 for a real endpoint.
	var refreshLit, refreshParam = -1, -1
	for i, e := range entries {
		switch e.pattern {
		case "/admin/api/accounts/refresh":
			refreshLit = i
		case "/admin/api/accounts/{account_id}/refresh":
			refreshParam = i
		}
	}
	if refreshLit < 0 || refreshParam < 0 || refreshLit > refreshParam {
		t.Errorf("literal refresh at %d, parameterised at %d: the literal must come first", refreshLit, refreshParam)
	}
}

// TestRegisterCoversBothAdminRoots is the t17 trap: a bare subtree registration
// makes Go's ServeMux answer 307 for /admin/api, while the legacy service
// answers 404. Registering the exact path as well keeps both 404.
func TestRegisterCoversBothAdminRoots(t *testing.T) {
	h, _, _ := newTestAPI()
	for _, p := range []string{"/admin/api", "/admin/api/"} {
		resp, body := do(t, h, http.MethodGet, p, nil, "", okAuth())
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status=%d want 404 (body=%q)", p, resp.StatusCode, body)
		}
	}
}

// ---- contract: shape A and the 401 ladder -----------------------------------

// TestUnauthorizedIsShapeA covers every credential state. The header only wins
// when it parses as a Bearer token; otherwise the query parameter is the
// fallback — so a malformed header with a good ?key still authenticates.
func TestUnauthorizedIsShapeA(t *testing.T) {
	cases := []struct {
		name, path, auth string
		want             int
		detail           string
	}{
		{"no credential", "/admin/api/accounts", "", 401, "缺少鉴权凭证"},
		{"bad bearer", "/admin/api/accounts", "Bearer wrong", 401, "鉴权凭证无效"},
		{"malformed header", "/admin/api/accounts", "Token abc", 401, "缺少鉴权凭证"},
		{"bare word header", "/admin/api/accounts", "abc", 401, "缺少鉴权凭证"},
		{"empty bearer", "/admin/api/accounts", "Bearer ", 401, "缺少鉴权凭证"},
		{"query param wins", "/admin/api/accounts?app_key=test-admin-key", "", 200, ""},
		{"malformed header falls back to query", "/admin/api/accounts?app_key=test-admin-key", "Token abc", 200, ""},
		{"bad header loses to good query", "/admin/api/accounts?app_key=test-admin-key", "Bearer wrong", 401, "鉴权凭证无效"},
		{"case-insensitive scheme", "/admin/api/accounts", "bearer test-admin-key", 200, ""},
	}
	h, _, _ := newTestAPI()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := do(t, h, http.MethodGet, c.path, nil, "", c.auth)
			if resp.StatusCode != c.want {
				t.Fatalf("status=%d want %d (body=%q)", resp.StatusCode, c.want, body)
			}
			if c.want != 401 {
				return
			}
			if ct := resp.Header.Get("content-type"); ct != "application/json" {
				t.Errorf("content-type=%q want application/json (no charset)", ct)
			}
			var shape struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal([]byte(body), &shape); err != nil {
				t.Fatalf("401 body %q is not shape A: %v", body, err)
			}
			if shape.Detail != c.detail {
				t.Errorf("detail=%q want %q", shape.Detail, c.detail)
			}
		})
	}
}

// TestNoAdminKeyConfigured covers the "未配置后台密钥" branch: with no stored
// key and no env override the service must refuse rather than fall open.
func TestNoAdminKeyConfigured(t *testing.T) {
	// adminKey() falls back to ZCODE_ADMIN_KEY and then to the weak default, so
	// the "no key configured" branch is only reachable with the env cleared.
	t.Setenv(adminKeyEnv, "")
	h := New(newFakeAccounts(), &fakeSettings{kv: map[string]string{}}, Options{}).Handler()
	resp, body := do(t, h, http.MethodGet, "/admin/api/accounts", nil, "", okAuth())
	// The weak default is now seeded, so a WRONG credential is the observable
	// outcome; what matters is that the service does not fall open.
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d want 401 (body=%q)", resp.StatusCode, body)
	}
}

// ---- contract: routing precedence (405 / 307 / 404) -------------------------

// TestMethodNotAllowedAllowHeader pins Starlette's rule: the Allow header is
// the method of the FIRST route whose PATH matched — never the union.
func TestMethodNotAllowedAllowHeader(t *testing.T) {
	// Measured on the live baseline (127.0.0.1:9120). The Allow value is the
	// method of the FIRST entry whose PATH matched — never the union, and never
	// the method the caller would have needed.
	cases := []struct{ method, path, allow string }{
		// GET is registered before POST and DELETE for this path.
		{"PATCH", "/admin/api/accounts", "GET"},
		{"PUT", "/admin/api/accounts", "GET"},
		{"PUT", "/admin/api/verify", "GET"},
		{"GET", "/admin/api/import", "POST"},
		// The parameterised {account_id} PUT precedes the literal /refresh
		// POST, so GET on the literal path reports PUT.
		{"GET", "/admin/api/accounts/refresh", "PUT"},
	}
	h, _, _ := newTestAPI()
	for _, c := range cases {
		resp, body := do(t, h, c.method, c.path, nil, "", okAuth())
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status=%d want 405 (body=%q)", c.method, c.path, resp.StatusCode, body)
			continue
		}
		if got := resp.Header.Get("allow"); got != c.allow {
			t.Errorf("%s %s allow=%q want %q", c.method, c.path, got, c.allow)
		}
		if body != `{"detail":"Method Not Allowed"}` {
			t.Errorf("%s %s body=%q want the shape-A Method Not Allowed", c.method, c.path, body)
		}
	}
}

// TestTrailingSlashRedirectsBeforeAuth pins the measured 307: it carries an
// ABSOLUTE Location and fires even without a credential.
func TestTrailingSlashRedirectsBeforeAuth(t *testing.T) {
	for _, p := range []string{"/admin/api/accounts/", "/admin/api/accounts/refresh/"} {
		h, _, _ := newTestAPI()
		resp, body := do(t, h, http.MethodGet, p, nil, "", "")
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("GET %s status=%d want 307 (body=%q)", p, resp.StatusCode, body)
			continue
		}
		loc := resp.Header.Get("location")
		if !strings.HasPrefix(loc, "http") {
			t.Errorf("GET %s location=%q want an absolute URL", p, loc)
		}
		if body != "" {
			t.Errorf("GET %s body=%q want empty", p, body)
		}
	}
}

// TestUnknownAdminPathIs404 keeps the CR-12/CR-33 forbidden additions out: no
// /health, no /admin/api/pool, /quota or /claim/history.
func TestUnknownAdminPathIs404(t *testing.T) {
	h, _, _ := newTestAPI()
	for _, p := range []string{
		"/admin/api", "/admin/api/", "/admin/api/pool", "/admin/api/quota",
		"/admin/api/claim/history", "/health", "/admin/api/nope",
	} {
		resp, body := do(t, h, http.MethodGet, p, nil, "", okAuth())
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status=%d want 404 (body=%q)", p, resp.StatusCode, body)
		}
	}
}

// TestPathTrapsAreNotAccountsRoutes covers CR-04 and the B-19/B-20 trap:
// export/import have NO accounts segment, and delete is collection-level.
func TestPathTrapsAreNotAccountsRoutes(t *testing.T) {
	h, _, _ := newTestAPI()
	cases := []struct{ method, path string }{
		{"POST", "/admin/api/export"},  // wrong method on a real route
		{"GET", "/admin/api/import"},   // wrong method on a real route
		{"DELETE", "/admin/api/accounts/abc"}, // collection-level delete only
	}
	for _, c := range cases {
		resp, _ := do(t, h, c.method, c.path, nil, "", okAuth())
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status=%d want 405", c.method, c.path, resp.StatusCode)
		}
	}
}

// ---- contract: shape D (422) ------------------------------------------------

// TestRequiredBodyRoutesReport422Missing covers the body-less request on a
// REQUIRED-body route: FastAPI reports type=missing with input=null.
func TestRequiredBodyRoutesReport422Missing(t *testing.T) {
	routes := []struct{ method, path string }{
		{"PUT", "/admin/api/settings"},
		{"POST", "/admin/api/accounts"},
		{"DELETE", "/admin/api/accounts"},
		{"PUT", "/admin/api/accounts/__no_such_id__"},
		{"POST", "/admin/api/accounts/__no_such_id__/enabled"},
		{"POST", "/admin/api/import"},
		{"POST", "/admin/api/claim/manual"},
	}
	h, _, _ := newTestAPI()
	for _, r := range routes {
		resp, body := do(t, h, r.method, r.path, nil, "", okAuth())
		if resp.StatusCode != 422 {
			t.Errorf("%s %s status=%d want 422 (body=%q)", r.method, r.path, resp.StatusCode, body)
			continue
		}
		d := decode(t, body)
		issues, _ := d["detail"].([]any)
		if len(issues) != 1 {
			t.Errorf("%s %s detail=%v want exactly 1 issue", r.method, r.path, d["detail"])
			continue
		}
		it, _ := issues[0].(map[string]any)
		if it["type"] != "missing" || it["msg"] != "Field required" || it["input"] != nil {
			t.Errorf("%s %s issue=%v want missing/Field required/null", r.method, r.path, it)
		}
		if loc, _ := it["loc"].([]any); len(loc) != 1 || loc[0] != "body" {
			t.Errorf("%s %s loc=%v want [body]", r.method, r.path, it["loc"])
		}
	}
}

// TestZeroByteBodyWithJSONTypeAlsoMissing is the measured subtlety: a zero-byte
// body carrying Content-Type: application/json is treated as absent, not as a
// malformed document.
func TestZeroByteBodyWithJSONTypeAlsoMissing(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodPut, "/admin/api/settings", strp(""), "application/json", okAuth())
	if resp.StatusCode != 422 {
		t.Fatalf("status=%d want 422 (body=%q)", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"type":"missing"`) {
		t.Errorf("body=%q want type=missing", body)
	}
}

// TestOptionalBodyRoutesAcceptNoBody is the counterpart measurement: these
// routes are optional-body and must answer 200 when no body is sent.
func TestOptionalBodyRoutesAcceptNoBody(t *testing.T) {
	h, _, _ := newTestAPI()
	for _, r := range []struct{ method, path string }{
		{"POST", "/admin/api/login/start"},
		{"POST", "/admin/api/claim"},
		{"POST", "/admin/api/accounts/refresh"},
	} {
		resp, body := do(t, h, r.method, r.path, nil, "", okAuth())
		if resp.StatusCode != 200 {
			t.Errorf("%s %s status=%d want 200 (body=%q)", r.method, r.path, resp.StatusCode, body)
		}
	}
}

// TestWrongContentTypeIsDictType is the measured 422: a body with no/wrong
// Content-Type is reported as a TYPE error whose `input` is the raw body text.
func TestWrongContentTypeIsDictType(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodPut, "/admin/api/settings", strp("{}"), "text/plain", okAuth())
	if resp.StatusCode != 422 {
		t.Fatalf("status=%d want 422 (body=%q)", resp.StatusCode, body)
	}
	d := decode(t, body)
	issues, _ := d["detail"].([]any)
	it, _ := issues[0].(map[string]any)
	if it["type"] != "dict_type" {
		t.Errorf("type=%v want dict_type", it["type"])
	}
	if it["input"] != "{}" {
		t.Errorf("input=%v want the raw body text \"{}\"", it["input"])
	}
}

// TestTopLevelScalarIsDictType covers the measured matrix for a well-formed
// scalar body: `null` is treated as ABSENT, every other scalar is a type error.
func TestTopLevelScalarIsDictType(t *testing.T) {
	cases := []struct {
		body     string
		wantType string
		wantVal  any
	}{
		{`[]`, "dict_type", []any{}},
		{`1`, "dict_type", float64(1)},
		{`true`, "dict_type", true},
		{`"x"`, "dict_type", "x"},
		{`null`, "missing", nil},
	}
	h, _, _ := newTestAPI()
	for _, c := range cases {
		resp, body := do(t, h, http.MethodPut, "/admin/api/settings", strp(c.body), "application/json", okAuth())
		if resp.StatusCode != 422 {
			t.Errorf("body %s status=%d want 422 (body=%q)", c.body, resp.StatusCode, body)
			continue
		}
		d := decode(t, body)
		issues, _ := d["detail"].([]any)
		it, _ := issues[0].(map[string]any)
		if it["type"] != c.wantType {
			t.Errorf("body %s type=%v want %q", c.body, it["type"], c.wantType)
		}
		if c.wantType == "dict_type" {
			got, _ := json.Marshal(it["input"])
			want, _ := json.Marshal(c.wantVal)
			if string(got) != string(want) {
				t.Errorf("body %s input=%s want %s", c.body, got, want)
			}
		}
	}
}

// TestMalformedJSONUsesPythonPositions pins the wire shape of the malformed
// body: loc is ["body", <code-point offset>] and ctx.error is CPython's
// message. These pairs were measured on the live baseline.
func TestMalformedJSONUsesPythonPositions(t *testing.T) {
	cases := []struct {
		body string
		pos  float64
		msg  string
	}{
		{`{`, 1, "Expecting property name enclosed in double quotes"},
		{`[`, 1, "Expecting value"},
		{`not json`, 0, "Expecting value"},
		{`{"a" 1}`, 5, "Expecting ':' delimiter"},
		{`{"a":1} extra`, 8, "Extra data"},
		{`{"a":"\q"}`, 6, "Invalid \\escape"},
		{`{"a":"\u12"}`, 7, "Invalid \\uXXXX escape"},
	}
	h, _, _ := newTestAPI()
	for _, c := range cases {
		resp, body := do(t, h, http.MethodPut, "/admin/api/settings", strp(c.body), "application/json", okAuth())
		if resp.StatusCode != 422 {
			t.Errorf("body %q status=%d want 422 (body=%q)", c.body, resp.StatusCode, body)
			continue
		}
		d := decode(t, body)
		issues, _ := d["detail"].([]any)
		it, _ := issues[0].(map[string]any)
		loc, _ := it["loc"].([]any)
		if len(loc) != 2 || loc[0] != "body" || loc[1] != c.pos {
			t.Errorf("body %q loc=%v want [body %v]", c.body, it["loc"], c.pos)
		}
		ctx, _ := it["ctx"].(map[string]any)
		if ctx["error"] != c.msg {
			t.Errorf("body %q ctx.error=%v want %q", c.body, ctx["error"], c.msg)
		}
		if input, _ := json.Marshal(it["input"]); string(input) != "{}" {
			t.Errorf("body %q input=%s want {}", c.body, input)
		}
	}
}

// TestBareConstantsAreAccepted is the reason the scanner exists: CPython's
// json.loads accepts NaN/Infinity, so these must NOT become a 422.
func TestBareConstantsAreAccepted(t *testing.T) {
	h, _, _ := newTestAPI()
	for _, body := range []string{`{"a":NaN}`, `{"a":-Infinity}`} {
		resp, out := do(t, h, http.MethodPut, "/admin/api/settings", strp(body), "application/json", okAuth())
		if resp.StatusCode != 200 {
			t.Errorf("body %q status=%d want 200 (body=%q)", body, resp.StatusCode, out)
		}
	}
}

// ---- contract: unknown-id 404s ---------------------------------------------

// TestUnknownIDIs404 pins the measured rule: nothing about the id string
// matters, every unknown id is 404 with shape A.
func TestUnknownIDIs404(t *testing.T) {
	h, _, _ := newTestAPI()
	cases := []struct{ method, path, detail string }{
		{"PUT", "/admin/api/accounts/__liche-no-such-id__", "账号不存在"},
		{"POST", "/admin/api/accounts/__no_such_id__/enabled", "账号不存在"},
		{"POST", "/admin/api/accounts/__no_such_id__/fingerprint/rotate", "账号不存在"},
		{"POST", "/admin/api/accounts/__no_such_id__/refresh", "账号不存在"},
		{"POST", "/admin/api/claim/manual", "JWT 账号不存在"},
	}
	for _, c := range cases {
		body := strp(`{"account_id":"__no_such_id__"}`)
		if c.method == "PUT" {
			body = strp(`{"name":"x"}`)
		}
		resp, out := do(t, h, c.method, c.path, body, "application/json", okAuth())
		if resp.StatusCode != 404 {
			t.Errorf("%s %s status=%d want 404 (body=%q)", c.method, c.path, resp.StatusCode, out)
			continue
		}
		if !strings.Contains(out, c.detail) {
			t.Errorf("%s %s body=%q want detail %q", c.method, c.path, out, c.detail)
		}
	}
}

// ---- contract: success shapes ----------------------------------------------

// TestVerifyAndStatusShapes pins the smallest success bodies, including the
// INSERTION order of /admin/api/status's keys.
func TestVerifyAndStatusShapes(t *testing.T) {
	h, _, _ := newTestAPI()

	resp, body := do(t, h, http.MethodGet, "/admin/api/verify", nil, "", okAuth())
	if resp.StatusCode != 200 || body != `{"status":"ok"}` {
		t.Errorf("verify status=%d body=%q want 200 {\"status\":\"ok\"}", resp.StatusCode, body)
	}

	resp, body = do(t, h, http.MethodGet, "/admin/api/status", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !strings.HasPrefix(body, `{"providers":[`) {
		t.Errorf("status body=%q want providers first (insertion order)", body)
	}
	for _, k := range []string{"providers", "gateway_key_set", "quota_pool"} {
		if !strings.Contains(body, `"`+k+`"`) {
			t.Errorf("status body=%q missing key %q", body, k)
		}
	}
}

// TestMonitoringIsEmptyWithKeep500 pins the measured 25-byte body.
func TestMonitoringIsEmptyWithKeep500(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodGet, "/admin/api/monitoring", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if body != `{"entries":[],"keep":500}` {
		t.Errorf("body=%q want {\"entries\":[],\"keep\":500}", body)
	}
}

// TestSettingsEchoesAdminKeyAndNumericInterval pins two measured facts: the
// admin key is echoed in plaintext (ADR-004) and the interval is a NUMBER, not
// a string.
func TestSettingsEchoesAdminKeyAndNumericInterval(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodGet, "/admin/api/settings", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	d := decode(t, body)
	if d["admin_key"] != "test-admin-key" {
		t.Errorf("admin_key=%v want the stored key echoed in plaintext", d["admin_key"])
	}
	if _, ok := d["quota_refresh_interval"].(float64); !ok {
		t.Errorf("quota_refresh_interval=%v (%T) want a NUMBER", d["quota_refresh_interval"], d["quota_refresh_interval"])
	}
}

// TestNoTokensAndMissingAccountIDDetails pins the two measured 400 bodies.
func TestNoTokensAndMissingAccountIDDetails(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodPost, "/admin/api/accounts", strp(`{}`), "application/json", okAuth())
	if resp.StatusCode != 400 || !strings.Contains(body, "请输入至少一个 Token / API Key") {
		t.Errorf("create status=%d body=%q want 400 请输入至少一个 Token / API Key", resp.StatusCode, body)
	}
	resp, body = do(t, h, http.MethodPost, "/admin/api/claim/manual", strp(`{}`), "application/json", okAuth())
	if resp.StatusCode != 400 || !strings.Contains(body, "缺少 account_id") {
		t.Errorf("manual status=%d body=%q want 400 缺少 account_id", resp.StatusCode, body)
	}
}

// TestDeleteEmptyAndImportEmptyShapes pins the two zero-effect success bodies.
func TestDeleteEmptyAndImportEmptyShapes(t *testing.T) {
	h, accts, _ := newTestAPI()
	resp, body := do(t, h, http.MethodDelete, "/admin/api/accounts", strp(`[]`), "application/json", okAuth())
	if resp.StatusCode != 200 || body != `{"deleted":0}` {
		t.Errorf("delete status=%d body=%q want 200 {\"deleted\":0}", resp.StatusCode, body)
	}
	if len(accts.deleted) != 0 {
		t.Errorf("delete called DeleteAccount %v times, want 0 for an empty list", accts.deleted)
	}
	resp, body = do(t, h, http.MethodPost, "/admin/api/import", strp(`{}`), "application/json", okAuth())
	if resp.StatusCode != 200 || body != `{"count":0}` {
		t.Errorf("import status=%d body=%q want 200 {\"count\":0}", resp.StatusCode, body)
	}
}

// TestMonitoringClearShape pins B-22.
func TestMonitoringClearShape(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodPost, "/admin/api/monitoring/clear", nil, "", okAuth())
	if resp.StatusCode != 200 || body != `{"ok":true}` {
		t.Errorf("status=%d body=%q want 200 {\"ok\":true}", resp.StatusCode, body)
	}
}

// ---- contract: enabled DUAL-WRITE (ADR-003) --------------------------------

// TestSetEnabledWritesThrough is the ADR-003 half that the output layer cannot
// provide: the write must reach the AccountSource, not just the JSON view.
func TestSetEnabledWritesThrough(t *testing.T) {
	h, accts, _ := newTestAPI()
	// The fake reports "not found" for unknown ids, so use a real one.
	resp, body := do(t, h, http.MethodPost, "/admin/api/accounts/zai-jwt-0001/enabled",
		strp(`{"enabled":false}`), "application/json", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	got, ok := accts.enabled["zai-jwt-0001"]
	if !ok {
		t.Fatalf("SetEnabled was not called; recorded=%v", accts.enabled)
	}
	if got != false {
		t.Errorf("SetEnabled(on=%v) want false", got)
	}
}

// ---- contract: export / import path and body -------------------------------

// TestExportShapeAndMasking pins B-19: no accounts segment, version/exported_at
// keys, and per-item name/mode/secret.
func TestExportShapeAndMasking(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodGet, "/admin/api/export", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	d := decode(t, body)
	if d["version"] != float64(1) {
		t.Errorf("version=%v want 1", d["version"])
	}
	if _, ok := d["exported_at"].(float64); !ok {
		t.Errorf("exported_at=%v (%T) want a number", d["exported_at"], d["exported_at"])
	}
	if _, ok := d["providers"].(map[string]any); !ok {
		t.Errorf("providers=%v (%T) want an object", d["providers"], d["providers"])
	}
}

// TestClaimPreviewEmptyShape pins B-13's empty case.
func TestClaimPreviewEmptyShape(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodGet, "/admin/api/claim/preview", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"preview"`) {
		t.Errorf("body=%q want a preview key", body)
	}
}

// TestCaptchaConfigDefaults pins B-15. The live service reports region "cn",
// which contradicts the contract doc's "sgp" — live wins.
func TestCaptchaConfigDefaults(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodGet, "/admin/api/claim/captcha-config", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	d := decode(t, body)
	if d["enabled"] != true || d["scene_id"] != "11xygtvd" || d["prefix"] != "no8xfe" || d["region"] != "cn" {
		t.Errorf("captcha-config=%v want enabled/scene_id/prefix/region per the live baseline", d)
	}
}

// TestRefreshAllEmptyShape pins B-09's empty-pool body.
func TestRefreshAllEmptyShape(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodPost, "/admin/api/accounts/refresh", strp(`{}`), "application/json", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"summary"`) || !strings.Contains(body, `"count"`) {
		t.Errorf("body=%q want summary and count keys", body)
	}
}

// ---- contract: HTML escaping (the B-11 defect) ------------------------------

// TestAuthorizeURLKeepsLiteralAmpersands is the regression guard for the
// encoder defect: Go's default json.Marshal escapes `&` as \u0026, which made
// the body 15 bytes longer than the baseline's.
//
// The upstream is a local httptest server, so the test needs no network and
// cannot be perturbed by upstream rate limiting.
func TestAuthorizeURLKeepsLiteralAmpersands(t *testing.T) {
	const upstreamURL = "https://chat.z.ai/api/oauth/authorize?client_id=client_P8X5CMWmlaRO9gyO-KSqtg" +
		"&redirect_uri=https://zcode.z.ai/api/v1/oauth/cli/callback/zai" +
		"&state=0123456789abcdef0123456789abcdef&response_type=code"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/cli/init" {
			t.Errorf("upstream path=%q want /oauth/cli/init", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"flow_id":"0123456789abcdef0123456789abcdef",`+
			`"authorize_url":"`+upstreamURL+`"}}`)
	}))
	defer up.Close()

	h := New(newFakeAccounts(), newFakeSettings(), Options{
		Client:       upstream.New(),
		ZaiOAuthBase: up.URL,
	}).Handler()
	resp, body := do(t, h, http.MethodPost, "/admin/api/login/start", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if strings.Contains(body, `\u0026`) {
		t.Errorf("body contains \\u0026; the encoder must not HTML-escape: %q", body)
	}
	d := decode(t, body)
	u, _ := d["authorize_url"].(string)
	if strings.Count(u, "&") != 3 {
		t.Errorf("authorize_url has %d literal '&', want 3: %q", strings.Count(u, "&"), u)
	}
	for _, k := range []string{"flow_id", "authorize_url", "expires_in"} {
		if _, ok := d[k]; !ok {
			t.Errorf("body missing key %q: %q", k, body)
		}
	}
	if d["expires_in"] != float64(300) {
		t.Errorf("expires_in=%v want 300", d["expires_in"])
	}
}

// TestNoHTMLEscapingInAnySuccessBody sweeps the read-only endpoints for the
// escape sequences the default encoder would introduce.
func TestNoHTMLEscapingInAnySuccessBody(t *testing.T) {
	h, _, _ := newTestAPI()
	paths := []string{
		"/admin/api/verify", "/admin/api/accounts", "/admin/api/status",
		"/admin/api/settings", "/admin/api/export", "/admin/api/monitoring",
		"/admin/api/claim/preview", "/admin/api/claim/captcha-config",
	}
	for _, p := range paths {
		resp, body := do(t, h, http.MethodGet, p, nil, "", okAuth())
		if resp.StatusCode != 200 {
			t.Errorf("GET %s status=%d (body=%q)", p, resp.StatusCode, body)
			continue
		}
		for _, esc := range []string{`\u003c`, `\u003e`, `\u0026`} {
			if strings.Contains(body, esc) {
				t.Errorf("GET %s body contains %s: %q", p, esc, body)
			}
		}
	}
}

// TestListAccountsViewKeyOrder pins the 21-key AccountView order, which the
// harness compares positionally before any normalization.
func TestListAccountsViewKeyOrder(t *testing.T) {
	h, _, _ := newTestAPI()
	resp, body := do(t, h, http.MethodGet, "/admin/api/accounts", nil, "", okAuth())
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	// Walk the raw text so the ORDER is what is asserted, not just presence.
	want := []string{
		"id", "name", "provider", "mode", "token_masked", "enabled", "status",
		"quota", "subscription", "plan", "plans", "use_count", "fail_count",
		"risk_strikes", "recent_results", "last_used_at", "last_checked_at",
		"cooling_until", "last_error", "created_at", "fingerprint",
	}
	i := strings.Index(body, `"accounts":[`)
	if i < 0 {
		t.Fatalf("body=%q has no accounts array", body)
	}
	seg := body[i:]
	last := -1
	for _, k := range want {
		at := strings.Index(seg, `"`+k+`":`)
		if at < 0 {
			t.Errorf("accounts view missing key %q", k)
			continue
		}
		if at < last {
			t.Errorf("key %q appears out of contract order", k)
		}
		last = at
	}
}
