// Package adminapi implements the 22 legacy `/admin/api/*` endpoints
// (endpoints.md §2 B-01..B-22).
//
// See errors.go for the response-shape contract (shape A for every admin error,
// shape D for Pydantic validation), routes.go for the ordered route table that
// reproduces Starlette's first-path-match semantics, and auth.go for the admin
// credential gate.
package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"zhipucode2api/internal/captcha"
	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// AccountSource is the account-pool read/write surface the admin API needs.
// It is a superset of the gateway's read-only source, because several B
// endpoints mutate the pool (add/delete/edit/enable/fingerprint/import).
type AccountSource interface {
	ListAccounts(ctx context.Context) ([]models.Account, error)
	GetAccount(ctx context.Context, id string) (models.Account, bool, error)
	UpsertAccount(ctx context.Context, a models.Account) error
	DeleteAccount(ctx context.Context, id string) (bool, error)
	SetEnabled(ctx context.Context, id string, enabled bool) (bool, error)
}

// SettingSource is the meta KV surface.
type SettingSource interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Options carries the injectable collaborators and the upstream coordinates the
// login/claim endpoints talk to. Zero values resolve to the contract defaults,
// mirroring gateway.New.
type Options struct {
	// Client performs upstream HTTP. Defaults to upstream.New().
	Client upstream.Client
	// Solver solves the Aliyun captcha for claim endpoints. Optional: when nil,
	// `POST /admin/api/claim/manual` still forwards a caller-supplied verify
	// param (measured: solver invoked 0 times) and `POST /admin/api/claim`
	// reports a business failure rather than 500.
	Solver *captcha.Solver

	// Upstream base URLs. Empty values resolve to the legacy defaults.
	// ZaiOAuthBase is `settings.OAUTH_API_BASE`, i.e. the ORIGIN the OAuth CLI
	// routes hang off — NOT the `/oauth` prefix itself. Legacy value is
	// `constants.ZCODE_ORIGIN + "/api/v1"`; the `/oauth/cli/init` suffix is
	// appended by oauthInit/oauthPoll. Writing the prefix here too would yield
	// `.../api/v1/oauth/oauth/cli/init` → upstream 404 → B-11 502.
	ZaiOAuthBase string // zcode.z.ai/api/v1
	ZaiPlanBase  string // zcode.z.ai/api/v1/zcode-plan
	ZaiBillingBase string // zcode.z.ai/api/v1/zcode-plan
	ZaiAPIBase   string // api.z.ai/api
	BigModelBase string

	// Version is the client app version sent on claim requests.
	Version string

	// Monitoring returns the gateway's request-log ring (newest last) and
	// MonitoringClear empties it. They are injectable rather than hard-wired so
	// that this package never has to import internal/gateway: B-21/B-22 stay
	// contract-shaped whether or not the gateway instrumentation exists yet.
	Monitoring      func() []json.RawMessage
	MonitoringClear func()

	// Now overrides the clock in tests.
	Now func() time.Time
}

// API is the assembled admin API. Construct with New, mount with Register.
type API struct {
	accounts AccountSource
	settings SettingSource
	client   upstream.Client
	solver   *captcha.Solver
	opt      Options
	now      func() time.Time

	// logins is the in-memory OAuth flow table for B-11/B-12. Flows are
	// deliberately NOT persisted: a restart invalidates a half-finished login,
	// which is the legacy behaviour (a 300 s TTL in a process-local dict).
	logins *loginStore

	// captcha cache for B-15: the legacy `CaptchaManager.fetch_config()` caches
	// the client configs for CAPTCHA_CONFIG_CACHE_TTL (600 s), so the admin UI
	// polling this endpoint must not hammer zcode.z.ai once per request.
	captchaMu     sync.Mutex
	captchaCache  captchaConfigBody
	captchaCached time.Time
}

// New builds the admin API. `settings` is consulted for the admin key and the
// gateway key; `accounts` for the pool.
func New(accounts AccountSource, settings SettingSource, opt Options) *API {
	if opt.Client == nil {
		opt.Client = upstream.New()
	}
	if opt.ZaiOAuthBase == "" {
		opt.ZaiOAuthBase = envOr("ZAI_OAUTH_BASE", "https://zcode.z.ai/api/v1")
	}
	if opt.ZaiPlanBase == "" {
		opt.ZaiPlanBase = envOr("ZAI_PLAN_BASE", "https://zcode.z.ai/api/v1/zcode-plan")
	}
	if opt.ZaiBillingBase == "" {
		opt.ZaiBillingBase = envOr("ZAI_BILLING_BASE", "https://zcode.z.ai/api/v1/zcode-plan")
	}
	if opt.ZaiAPIBase == "" {
		opt.ZaiAPIBase = envOr("ZAI_API_BASE", "https://api.z.ai/api")
	}
	if opt.BigModelBase == "" {
		opt.BigModelBase = envOr("BIGMODEL_BASE", "https://open.bigmodel.cn/api")
	}
	if opt.Version == "" {
		opt.Version = envOr("ZCODE_CLIENT_APP_VERSION", "3.10.2")
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	return &API{accounts: accounts, settings: settings, client: opt.Client,
		solver: opt.Solver, opt: opt, now: now, logins: newLoginStore(now)}
}

// Register mounts the admin API on mux.
//
// Two patterns, not one, because of a measured ServeMux trap (t17): under a
// bare subtree registration Go answers `GET /admin/api` (no trailing slash)
// with a 307 to `/admin/api/`, and `/admin/api/` itself with a 200. The legacy
// service answers **404 for both**. Registering the exact path makes the more
// specific pattern win, so no redirect is ever emitted and the router reports
// its own shape-A 404 — the measured body for `/admin/api` and `/admin/api/`.
//
// Exported so the cmd wiring (t12) mounts both suites through one call site.
func Register(mux *http.ServeMux, accounts AccountSource, settings SettingSource, opt Options) *API {
	api := New(accounts, settings, opt)
	h := api.Handler()
	mux.Handle("/admin/api/", h)
	mux.Handle("/admin/api", h)
	return api
}

// Handler returns the ordered-route HTTP handler (also used by tests).
func (a *API) Handler() http.Handler {
	rt := &router{api: a}
	rt.routes = a.routeTable()
	return rt
}

// routeTable is the route registration order, transcribed from the legacy
// `admin_api.py` decorator order (verified against openapi.json, whose `paths`
// preserve Starlette's insertion order).
//
// Each entry carries exactly ONE method because Starlette registers one route
// per decorator: `GET /admin/api/accounts` and `POST /admin/api/accounts` are
// two separate entries. That is what makes the measured `Allow` semantics fall
// out for free — the first PATH-matching entry names its own method
// (`PATCH /admin/api/accounts` -> `allow: GET`), and it is why
// `/admin/api/accounts/refresh` answers `allow: PUT`: the `{account_id}` entry
// sits earlier in this list.
//
// Multi-method paths therefore appear as consecutive entries with the SAME
// pattern and a per-entry method, in the legacy declaration order.
func (a *API) routeTable() []*routeEntry {
	return []*routeEntry{
		// 0
		newRoute("/admin/api/verify", http.MethodGet, bodyNone, shapeNone, a.handleVerify),
		// 1-3: three decorators on the same path (GET, POST, DELETE)
		newRoute("/admin/api/accounts", http.MethodGet, bodyNone, shapeNone, a.handleListAccounts),
		newRoute("/admin/api/accounts", http.MethodPost, bodyRequired, shapeDict, a.handleAddAccounts),
		newRoute("/admin/api/accounts", http.MethodDelete, bodyRequired, shapeList, a.handleDeleteAccounts),
		// 4
		newRoute("/admin/api/status", http.MethodGet, bodyNone, shapeNone, a.handleStatus),
		// 5
		newRoute("/admin/api/accounts/{account_id}", http.MethodPut, bodyRequired, shapeDict, a.handleEditAccount),
		// 6
		newRoute("/admin/api/accounts/{account_id}/enabled", http.MethodPost, bodyRequired, shapeDict, a.handleSetEnabled),
		// 7
		newRoute("/admin/api/accounts/{account_id}/fingerprint/rotate", http.MethodPost, bodyNone, shapeNone, a.handleRotateFingerprint),
		// 8
		newRoute("/admin/api/accounts/refresh", http.MethodPost, bodyOptional, shapeDict, a.handleRefreshAll),
		// 9
		newRoute("/admin/api/accounts/{account_id}/refresh", http.MethodPost, bodyNone, shapeNone, a.handleRefreshOne),
		// 10
		newRoute("/admin/api/login/start", http.MethodPost, bodyOptional, shapeDict, a.handleLoginStart),
		// 11
		newRoute("/admin/api/login/poll/{flow_id}", http.MethodGet, bodyNone, shapeNone, a.handleLoginPoll),
		// 12
		newRoute("/admin/api/claim/preview", http.MethodGet, bodyNone, shapeNone, a.handleClaimPreview),
		// 13
		newRoute("/admin/api/claim", http.MethodPost, bodyOptional, shapeDict, a.handleClaim),
		// 14
		newRoute("/admin/api/claim/captcha-config", http.MethodGet, bodyNone, shapeNone, a.handleCaptchaConfig),
		// 15
		newRoute("/admin/api/claim/manual", http.MethodPost, bodyRequired, shapeDict, a.handleClaimManual),
		// 16-17: GET then PUT
		newRoute("/admin/api/settings", http.MethodGet, bodyNone, shapeNone, a.handleGetSettings),
		newRoute("/admin/api/settings", http.MethodPut, bodyRequired, shapeDict, a.handlePutSettings),
		// 18
		newRoute("/admin/api/export", http.MethodGet, bodyNone, shapeNone, a.handleExport),
		// 19
		newRoute("/admin/api/import", http.MethodPost, bodyRequired, shapeDict, a.handleImport),
		// 20
		newRoute("/admin/api/monitoring", http.MethodGet, bodyNone, shapeNone, a.handleMonitoring),
		// 21
		newRoute("/admin/api/monitoring/clear", http.MethodPost, bodyNone, shapeNone, a.handleMonitoringClear),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envValue reads an environment variable, treating empty as unset (the legacy
// `os.getenv(..., default)` idiom).
func envValue(key string) string { return os.Getenv(key) }
