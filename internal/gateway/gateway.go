// Package gateway implements the three public API endpoints of
// zhipucode2api (docs/contract/endpoints.md group A: A-01/A-02/A-03).
//
// # Error shapes (compat-risks.md CR-02) - three, never unified
//
//	shape A {"detail":"..."}               FastAPI layer: unknown paths, 405.
//	shape B {"error":{"message","type"}}   local gateway validation with the
//	      endpoint-dependent type: invalid_request on /v1/messages,
//	      invalid_request_error on /v1/chat/completions (message-first key
//	      order, measured; writers live in errors.go).
//	shape C upstream body verbatim         everything the upstream answers,
//	      including HTTP-200 business errors (A-02 measured facts).
//
// # Non-object JSON bodies - a measured asymmetry to reproduce
//
//	POST /v1/messages       body valid JSON but not object -> 500
//	                        "Internal Server Error" text/plain (legacy crash)
//	POST /v1/chat/completions same -> 400 shape B 请求体必须是 JSON 对象
//
// # Scope notes (t6 contract)
//
//   - /v1/messages performs NO Anthropic body validation beyond JSON
//     legality (A-02: missing "messages" is forwarded upstream, not 400).
//   - Account selection here is the contract-minimal rotation over
//     selectable accounts; full pool semantics (failover by class,
//     cooling/exhausted writes, captcha retry) are a later task - the seam
//     is picker.go's single function.
//   - Local JSON bodies carry content-type application/json WITHOUT
//     charset; passthrough keeps the upstream header (CR-10). No
//     Server-header spoofing (the harness exempts it).
package gateway

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// AccountSource lists persisted accounts (satisfied by *store.Store).
type AccountSource interface {
	ListAccounts(ctx context.Context) ([]models.Account, error)
}

// SettingSource reads meta KV (satisfied by *store.Store).
type SettingSource interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
}

// Options bundles tunables; zero fields fall back to contract defaults in New.
type Options struct {
	// UpstreamTimeout is the per-request timeout. Default 0: the legacy
	// gateway forwards WITHOUT an upstream timeout (04-api-spec.md §3 "网关
	// 转发无上游超时"), and long SSE streams must not be cut. Tests inject a
	// positive value to exercise the timeout mapping (t6 requires the
	// capability).
	UpstreamTimeout time.Duration
	// AppVersion feeds User-Agent "ZCode/<ver>" and X-ZCode-App-Version.
	// Default "3.15.3" = live data/release_version.json runtime artifact
	// (measured 2026-10-15; ADR-001: measurement over stale doc constants).
	AppVersion string
	// Upstream URLs. Env ZAI_UPSTREAM_URL / ZAI_FALLBACK_URL /
	// BIGMODEL_UPSTREAM_URL overrides (env-contract.md §3, test injection).
	ZAIPlanURL, ZAIKeyURL, BigModelURL string
	// ReqLog records every forwarded request for the admin API's B-21
	// (`GET /admin/api/monitoring`). nil means "do not record" - tests that do
	// not care about monitoring stay unaffected, and cmd installs the ring.
	ReqLog *ReqLog
	// Captcha solves a verify param when (and only when) the upstream answers
	// with a captcha challenge - I-3 laziness. nil means "not wired": the
	// challenge branch then passes the upstream answer through unchanged
	// instead of retrying, so a deployment without a configured solver keeps
	// working. cmd installs a real *captcha.Solver from the B-15 config.
	Captcha CaptchaSolver
}

// Gateway wires deps to routes.
type Gateway struct {
	accounts   AccountSource
	settings   SettingSource
	client     upstream.Client
	opt        Options
	planCursor uint64 // per-ledger rotation cursors (I-9; in-memory only,
	subCursor  uint64 // cursor persistence unverified per account-model §3.4)
	reqLog     *ReqLog
}

// New resolves contract defaults (including env overrides) into a Gateway.
func New(accounts AccountSource, settings SettingSource, client upstream.Client, opt Options) *Gateway {
	if opt.AppVersion == "" {
		opt.AppVersion = "3.15.3"
	}
	if opt.ZAIPlanURL == "" {
		opt.ZAIPlanURL = envOr("ZAI_UPSTREAM_URL", "https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages")
	}
	if opt.ZAIKeyURL == "" {
		opt.ZAIKeyURL = envOr("ZAI_FALLBACK_URL", "https://api.z.ai/api/anthropic/v1/messages")
	}
	if opt.BigModelURL == "" {
		opt.BigModelURL = envOr("BIGMODEL_UPSTREAM_URL", "https://open.bigmodel.cn/api/anthropic/v1/messages")
	}
	return &Gateway{accounts: accounts, settings: settings, client: client, opt: opt, reqLog: opt.ReqLog}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Handler returns the ready HTTP handler (used by cmd and by tests).
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	// All three /v1/* endpoints sit behind the SAME gateway gate: the contract
	// scopes the key to `/v1/*` (endpoints.md §1.1), and the black-box test
	// test_models_endpoint_gated_too asserts GET /v1/models -> 401 once a key
	// is configured (it is 200 today only because meta.gateway_key is EMPTY).
	mux.HandleFunc("/v1/models", g.enforceMethod(http.MethodGet, g.authGate(g.handleModels)))
	mux.HandleFunc("/v1/messages", g.enforceMethod(http.MethodPost, g.authGate(g.handleMessages)))
	mux.HandleFunc("/v1/chat/completions", g.enforceMethod(http.MethodPost, g.authGate(g.handleChatCompletions)))
	// /v1/ subtree catch-all + everything else: shape A 404 (measured:
	// /v1/nope and /health both -> {"detail":"Not Found"}).
	mux.HandleFunc("/v1/", shapeNotFound)
	mux.HandleFunc("/", shapeNotFound)
	return slashRedirect(mux)
}

func shapeNotFound(w http.ResponseWriter, r *http.Request) {
	writeShapeA(w, http.StatusNotFound, "Not Found")
}

// enforceMethod reproduces FastAPI's 405 (measured verbatim): shape A body
// + allow header, on any wrong method including OPTIONS.
func (g *Gateway) enforceMethod(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeShapeA(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return
		}
		h(w, r)
	}
}

// authGate implements the gateway key gate (endpoints.md §1.1, CR-07,
// black-box test_gateway_auth):
//   - meta gateway_key empty -> full passthrough (current production state)
//   - configured: no credentials -> 401; wrong credentials -> 403
//   - Authorization: Bearer <key> and x-api-key are equivalent channels
//
// Body: shape B, type authentication_error (04-api-spec §1.6 - declared; the
// live key is empty so production cannot observe the wording).
func (g *Gateway) authGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, _, err := g.settings.GetSetting(r.Context(), "gateway_key")
		if err != nil {
			// The legacy in-memory pool cannot fail this way; failing closed
			// would 500 every request on a transient read error. Allow and
			// keep the failure visible server-side.
			logWarn("gateway: read gateway_key: %v", err)
		}
		if strings.TrimSpace(key) == "" {
			next(w, r)
			return
		}
		provided := ""
		if auth := r.Header.Get("Authorization"); auth != "" {
			if scheme, token, ok := strings.Cut(auth, " "); ok && strings.EqualFold(scheme, "Bearer") {
				provided = strings.TrimSpace(token)
			}
		}
		if provided == "" {
			provided = r.Header.Get("x-api-key")
		}
		switch provided {
		case "":
			writeShapeB(w, http.StatusUnauthorized, "authentication_error", "缺少鉴权凭证")
		case key:
			next(w, r)
		default:
			writeShapeB(w, http.StatusForbidden, "authentication_error", "鉴权凭证无效")
		}
	}
}

// handleModels emits the measured static list byte-for-byte (A-01). Struct
// field order IS the contract byte order (object, data, then per item
// id, type, display_name, created_at); created_at is a placeholder constant.
func (g *Gateway) handleModels(w http.ResponseWriter, _ *http.Request) {
	type modelItem struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
	}
	writeJSON(w, http.StatusOK, "application/json", struct {
		Object string      `json:"object"`
		Data   []modelItem `json:"data"`
	}{Object: "list", Data: []modelItem{
		{ID: "GLM-5.3-Flash", Type: "model", DisplayName: "GLM-5.3-Flash", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "GLM-5.3", Type: "model", DisplayName: "GLM-5.3", CreatedAt: "2025-01-01T00:00:00Z"},
	}})
}

// slashRedirect reproduces FastAPI redirect_slashes (endpoints.md §1.5 +
// §4 red note): trailing slash on a registered path -> 307 (NOT 302) with
// an ABSOLUTE Location. Unregistered trailing paths fall through to the
// subtree 404 exactly like the legacy app.
func slashRedirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasSuffix(p, "/") && len(p) > 1 {
			clean := strings.TrimRight(p, "/")
			switch clean {
			case "/v1/models", "/v1/messages", "/v1/chat/completions":
				w.Header().Set("Location", requestScheme(r)+"://"+r.Host+clean)
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
