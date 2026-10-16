package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// loginFlowTTL is the legacy `LOGIN_FLOW_TTL` (300.0 s): the window in which a
// started OAuth flow may still be polled.
const loginFlowTTL = 300 * time.Second

// loginExchangeTimeout is the legacy `LOGIN_EXCHANGE_TIMEOUT` (60.0 s) applied
// to the access_token -> API key exchange.
const loginExchangeTimeout = 60 * time.Second

// loginFlow is one in-flight OAuth authorisation, held in memory only (the
// legacy service never persists these; a restart drops them, which is why an
// unknown flow_id answers `expired` rather than 404).
type loginFlow struct {
	id        string
	label     string
	created   time.Time
	pollToken string
	// done marks the flow as consumed. The legacy code POPS the entry before
	// exchanging credentials, so a second poll of a completed flow must see
	// `expired` and the exchange must run at most once even under concurrent
	// polls (test_oauth_login.py asserts exactly this).
	done bool
}

// loginStore holds the in-flight flows with the TTL GC the legacy `_login_gc`
// performs on every start/poll.
type loginStore struct {
	mu    sync.Mutex
	flows map[string]*loginFlow
	now   func() time.Time
}

func newLoginStore(now func() time.Time) *loginStore {
	return &loginStore{flows: make(map[string]*loginFlow), now: now}
}

// gc drops flows older than the TTL. Called at the top of both login handlers.
func (s *loginStore) gc() {
	cutoff := s.now().Add(-loginFlowTTL)
	for id, f := range s.flows {
		if f.created.Before(cutoff) {
			delete(s.flows, id)
		}
	}
}

func (s *loginStore) put(f *loginFlow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flows[f.id] = f
}

// take atomically removes a flow, reporting whether it existed and had not
// already been consumed. This is the reentry guard: the legacy code pops before
// the exchange, so a concurrent second poll finds nothing and answers
// `expired`.
func (s *loginStore) take(id string) (*loginFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[id]
	if !ok {
		return nil, false
	}
	delete(s.flows, id)
	return f, true
}

// peek reports whether a flow exists without consuming it.
func (s *loginStore) peek(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.flows[id]
	return ok
}

// ── B-11 POST /admin/api/login/start ─────────────────────────────────────────

type loginStartBody struct {
	FlowID       string `json:"flow_id"`
	AuthorizeURL string `json:"authorize_url"`
	ExpiresIn    int    `json:"expires_in"`
}

func (a *API) handleLoginStart(c *reqCtx) {
	label := ""
	if s, ok := c.str("label"); ok {
		label = strings.TrimSpace(s)
	}
	if len(label) > 32 {
		label = label[:32]
	}

	a.logins.mu.Lock()
	a.logins.gc()
	a.logins.mu.Unlock()

	pollToken, err := randomHex(32)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	flowID, authorizeURL, err := a.oauthInit(c.r.Context(), pollToken)
	if err != nil {
		// Measured legacy behaviour: an init failure surfaces as
		// `502 登录初始化失败: <err>` (a raise_for_status / transport error is
		// NOT mapped onto a 500).
		writeShapeA(c.w, http.StatusBadGateway, "登录初始化失败: "+err.Error())
		return
	}
	a.logins.put(&loginFlow{
		id:        flowID,
		label:     label,
		created:   a.now(),
		pollToken: pollToken,
	})
	writeJSON(c.w, http.StatusOK, loginStartBody{
		FlowID:       flowID,
		AuthorizeURL: authorizeURL,
		ExpiresIn:    int(loginFlowTTL / time.Second),
	})
}

// ── B-12 GET /admin/api/login/poll/{flow_id} ────────────────────────────────

// loginPollBody is the poll envelope. `status` decides which of the optional
// members are present, so they are all omitempty.
type loginPollBody struct {
	Status  string       `json:"status"`
	Message string       `json:"message,omitempty"`
	Account *accountView `json:"account,omitempty"`
}

func (a *API) handleLoginPoll(c *reqCtx) {
	flowID := c.param("flow_id")
	a.logins.mu.Lock()
	a.logins.gc()
	a.logins.mu.Unlock()

	if !a.logins.peek(flowID) {
		// An unknown flow is NOT a 404: the legacy handler returns 200
		// `{"status":"expired"}` (measured, and asserted by the integration
		// test for a never-created flow id).
		writeJSON(c.w, http.StatusOK, loginPollBody{Status: "expired"})
		return
	}

	f, ok := a.logins.take(flowID)
	if !ok {
		writeJSON(c.w, http.StatusOK, loginPollBody{Status: "expired"})
		return
	}
	ctx := c.r.Context()
	data, err := a.oauthPoll(ctx, f.pollToken, flowID)
	if err != nil {
		// A transport failure during polling is `pending`, not an error: the
		// user may still be mid-authorisation.
		writeJSON(c.w, http.StatusOK, loginPollBody{Status: "pending"})
		return
	}
	switch state, _ := data["state"].(string); state {
	case "failed":
		reason := firstNonEmptyString(data, "message", "reason")
		if reason == "" {
			reason = "授权失败或被拒绝"
		}
		writeJSON(c.w, http.StatusOK, loginPollBody{Status: "failed", Message: reason})
		return
	case "ready":
		// fall through to the exchange below
	default:
		// Any other state (including "pending") keeps the flow alive for the
		// next poll.
		a.logins.put(&loginFlow{id: flowID, label: f.label, created: f.created, pollToken: f.pollToken})
		writeJSON(c.w, http.StatusOK, loginPollBody{Status: "pending"})
		return
	}

	// state == "ready". The entry has already been consumed above, which is the
	// legacy reentry protection: a concurrent second poll finds nothing and
	// answers `expired`, and the exchange runs exactly once.
	acc, err := a.completeLogin(ctx, f, data)
	if err != nil {
		writeJSON(c.w, http.StatusOK, loginPollBody{Status: "failed", Message: err.Error()})
		return
	}
	v, err := viewOf(acc)
	if err != nil {
		writeShapeA(c.w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	writeJSON(c.w, http.StatusOK, loginPollBody{Status: "ready", Account: &v})
}

// completeLogin reproduces the ready branch of `login_poll`: prefer the ZCode
// JWT, otherwise exchange the Z.ai access_token for an API key.
func (a *API) completeLogin(ctx context.Context, f *loginFlow, data map[string]any) (models.Account, error) {
	zcodeJWT, _ := data["token"].(string)
	var accessToken string
	if zai, ok := data["zai"].(map[string]any); ok {
		accessToken, _ = zai["access_token"].(string)
	}
	label := f.label
	if label == "" {
		label = "oauth-login"
	}

	if zcodeJWT != "" {
		acc, err := newAccount(models.ProviderZai, label, zcodeJWT)
		if err != nil {
			return models.Account{}, err
		}
		if err := a.accounts.UpsertAccount(ctx, acc); err != nil {
			return models.Account{}, err
		}
		// A JWT login is refreshed immediately so the pool has live quota data.
		if _, err := a.fetchQuota(ctx, acc); err != nil {
			// The legacy code refreshes after adding; a refresh failure is not
			// fatal to the login itself.
			_ = err
		}
		fresh, _, err := a.accounts.GetAccount(ctx, acc.ID)
		if err != nil {
			return models.Account{}, err
		}
		return fresh, nil
	}

	if accessToken == "" {
		return models.Account{}, errors.New("未能从授权结果中获取凭证")
	}

	exCtx, cancel := context.WithTimeout(ctx, loginExchangeTimeout)
	defer cancel()
	secret, err := a.exchangeAPIKey(exCtx, accessToken)
	if err != nil {
		// Measured: an exchange failure is swallowed and the handler then
		// reports the generic "no credential" failure rather than the
		// underlying error text.
		return models.Account{}, errors.New("未能从授权结果中获取凭证")
	}
	acc, err := newAccount(models.ProviderZai, label, secret)
	if err != nil {
		return models.Account{}, err
	}
	if err := a.accounts.UpsertAccount(ctx, acc); err != nil {
		return models.Account{}, err
	}
	return acc, nil
}

// ── OAuth upstream calls ─────────────────────────────────────────────────────

// oauthInit performs `POST {oauth_base}/oauth/cli/init`.
func (a *API) oauthInit(ctx context.Context, pollToken string) (string, string, error) {
	body, err := marshalJSON(map[string]string{"provider": "zai"})
	if err != nil {
		return "", "", err
	}
	res, err := a.client.Send(ctx, &upstream.Request{
		Method: http.MethodPost,
		URL:    a.opt.ZaiOAuthBase + "/oauth/cli/init",
		Header: http.Header{
			"Authorization": {"Bearer " + pollToken},
			"Content-Type":  {"application/json"},
		},
		Body:    body,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", "", errors.New("HTTP " + itoa(res.StatusCode))
	}
	var envelope struct {
		Data struct {
			FlowID       string `json:"flow_id"`
			AuthorizeURL string `json:"authorize_url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		return "", "", err
	}
	if envelope.Data.FlowID == "" || envelope.Data.AuthorizeURL == "" {
		return "", "", errors.New("返回的 OAuth 流程数据不完整")
	}
	return envelope.Data.FlowID, envelope.Data.AuthorizeURL, nil
}

// oauthPoll performs `GET {oauth_base}/oauth/cli/poll/{flow_id}` and returns
// the `data` object.
func (a *API) oauthPoll(ctx context.Context, pollToken, flowID string) (map[string]any, error) {
	res, err := a.client.Send(ctx, &upstream.Request{
		Method: http.MethodGet,
		URL:    a.opt.ZaiOAuthBase + "/oauth/cli/poll/" + url.PathEscape(flowID),
		Header: http.Header{"Authorization": {"Bearer " + pollToken}},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("HTTP " + itoa(res.StatusCode))
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Data == nil {
		envelope.Data = map[string]any{}
	}
	return envelope.Data, nil
}

// exchangeAPIKey reproduces `ZaiAuthFlow.exchange_api_key`: access_token ->
// business token -> default org/project -> `zcode-api-key`, then decrypt the
// secret key. The result is `"{apiKey}.{secretKey}"`.
func (a *API) exchangeAPIKey(ctx context.Context, accessToken string) (string, error) {
	origin := a.opt.ZaiAPIBase
	origin = strings.TrimSuffix(origin, "/api")

	loginBody, err := marshalJSON(map[string]string{"token": accessToken})
	if err != nil {
		return "", err
	}
	res, err := a.client.Send(ctx, &upstream.Request{
		Method: http.MethodPost,
		URL:    origin + "/api/auth/z/login",
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   loginBody, Timeout: 30 * time.Second,
	})
	if err != nil {
		return "", err
	}
	var loginEnv struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			AccessToken2 string `json:"accessToken"`
		} `json:"data"`
	}
	err = json.NewDecoder(res.Body).Decode(&loginEnv)
	res.Body.Close()
	if err != nil {
		return "", err
	}
	bizToken := loginEnv.Data.AccessToken
	if bizToken == "" {
		bizToken = loginEnv.Data.AccessToken2
	}
	if bizToken == "" {
		return "", errors.New("返回数据中不含业务凭证")
	}

	infoRes, err := a.client.Send(ctx, &upstream.Request{
		Method: http.MethodGet,
		URL:    origin + "/api/biz/customer/getCustomerInfo",
		Header: http.Header{"Authorization": {"Bearer " + bizToken}},
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return "", err
	}
	var infoEnv struct {
		Data struct {
			Organizations []struct {
				OrganizationID   json.Number `json:"organizationId"`
				OrganizationName string      `json:"organizationName"`
				Projects         []struct {
					ProjectID   json.Number `json:"projectId"`
					ProjectName string      `json:"projectName"`
				} `json:"projects"`
			} `json:"organizations"`
		} `json:"data"`
	}
	err = json.NewDecoder(infoRes.Body).Decode(&infoEnv)
	infoRes.Body.Close()
	if err != nil {
		return "", err
	}
	orgs := infoEnv.Data.Organizations
	if len(orgs) == 0 {
		return "", errors.New("找不到可用的机构")
	}
	org := orgs[0]
	for _, o := range orgs {
		if strings.Contains(o.OrganizationName, "默认机构") {
			org = o
			break
		}
	}
	if len(org.Projects) == 0 {
		return "", errors.New("找不到可用的项目")
	}
	proj := org.Projects[0]
	for _, p := range org.Projects {
		if strings.Contains(p.ProjectName, "默认项目") {
			proj = p
			break
		}
	}

	keyURL := origin + "/api/biz/v1/organization/" + org.OrganizationID.String() +
		"/projects/" + proj.ProjectID.String() + "/api_keys"
	authHeader := http.Header{"Authorization": {"Bearer " + bizToken}}

	keysRes, err := a.client.Send(ctx, &upstream.Request{
		Method: http.MethodGet, URL: keyURL, Header: authHeader, Timeout: 30 * time.Second,
	})
	if err != nil {
		return "", err
	}
	var keysEnv struct {
		Data []struct {
			Name   string `json:"name"`
			APIKey string `json:"apiKey"`
		} `json:"data"`
	}
	err = json.NewDecoder(keysRes.Body).Decode(&keysEnv)
	keysRes.Body.Close()
	if err != nil {
		return "", err
	}
	var apiKey string
	for _, k := range keysEnv.Data {
		if k.Name == "zcode-api-key" {
			apiKey = k.APIKey
			break
		}
	}
	if apiKey == "" {
		createBody, _ := marshalJSON(map[string]string{"name": "zcode-api-key"})
		h := http.Header{"Authorization": {"Bearer " + bizToken}, "Content-Type": {"application/json"}}
		createRes, err := a.client.Send(ctx, &upstream.Request{
			Method: http.MethodPost, URL: keyURL, Header: h,
			Body: createBody, Timeout: 30 * time.Second,
		})
		if err != nil {
			return "", err
		}
		var createEnv struct {
			Data struct {
				APIKey string `json:"apiKey"`
			} `json:"data"`
		}
		err = json.NewDecoder(createRes.Body).Decode(&createEnv)
		createRes.Body.Close()
		if err != nil {
			return "", err
		}
		apiKey = createEnv.Data.APIKey
	}
	if apiKey == "" {
		return "", errors.New("获取 API Key 失败")
	}

	copyRes, err := a.client.Send(ctx, &upstream.Request{
		Method: http.MethodGet, URL: keyURL + "/copy/" + apiKey,
		Header: authHeader, Timeout: 30 * time.Second,
	})
	if err != nil {
		return "", err
	}
	var copyEnv struct {
		Data struct {
			SecretKey string `json:"secretKey"`
		} `json:"data"`
	}
	err = json.NewDecoder(copyRes.Body).Decode(&copyEnv)
	copyRes.Body.Close()
	if err != nil {
		return "", err
	}
	if copyEnv.Data.SecretKey == "" {
		return "", errors.New("未能解密 Secret Key")
	}
	return apiKey + "." + copyEnv.Data.SecretKey, nil
}

// randomHex is the Go equivalent of `secrets.token_hex(n)`.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
