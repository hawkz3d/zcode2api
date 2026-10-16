package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// ---------------------------------------------------------------------------
// Test doubles: the upstream client is injectable, which is what makes the
// error-path coverage below possible (t6 requires it).
// ---------------------------------------------------------------------------

// fakeClient returns a canned response or a canned transport failure.
type fakeClient struct {
	res  *upstream.Response
	err  error
	reqs []*upstream.Request
	body []byte // captured request body of the last call
}

func (f *fakeClient) Send(_ context.Context, req *upstream.Request) (*upstream.Response, error) {
	f.reqs = append(f.reqs, req)
	if req.Body != nil {
		f.body = req.Body
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.res, nil
}

// jsonResponse builds a 200 upstream reply carrying body.
func jsonResponse(status int, contentType, body string) *upstream.Response {
	return &upstream.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// fakeAccounts satisfies AccountSource.
type fakeAccounts struct {
	accs []models.Account
	err  error
}

func (f *fakeAccounts) ListAccounts(context.Context) ([]models.Account, error) {
	return f.accs, f.err
}

// fakeSettings satisfies SettingSource.
type fakeSettings struct {
	key   string
	found bool
	err   error
}

func (f *fakeSettings) GetSetting(context.Context, string) (string, bool, error) {
	return f.key, f.found, f.err
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// accountJSON builds a minimal selectable account for the given provider/
// mode/status, carrying the credential the mode names (jwt_token vs api_key)
// so it passes the I-9 channel candidate filter. Default-channel (no
// x-provider) tests need mode="jwt"; subscription tests use "apiKey".
func accountJSON(t *testing.T, id, provider, mode, status string) models.Account {
	t.Helper()
	cred := `"api_key":"sk-test-not-a-real-credential"`
	if mode == "jwt" {
		cred = `"jwt_token":"zjwt-test-not-a-real-credential"`
	}
	raw := `{"id":"` + id + `","provider":"` + provider + `","mode":"` + mode +
		`","status":"` + status + `","enabled":true,` + cred + `}`
	a := models.Account{
		ID:       id,
		Provider: provider,
		Enabled:  1,
		Data:     json.RawMessage(raw),
	}
	modeCopy, statusCopy := mode, status
	a.Mode, a.Status = &modeCopy, &statusCopy
	return a
}

func newTestGateway(t *testing.T, client upstream.Client, accounts []models.Account, gatewayKey string) *Gateway {
	t.Helper()
	settings := &fakeSettings{}
	if gatewayKey != "" {
		settings.key, settings.found = gatewayKey, true
	}
	return New(&fakeAccounts{accs: accounts}, settings, client, Options{
		ZAIPlanURL:  "https://plan.invalid/anthropic/v1/messages",
		ZAIKeyURL:   "https://key.invalid/anthropic/v1/messages",
		BigModelURL: "https://bigmodel.invalid/api/anthropic/v1/messages",
		AppVersion:  "3.15.3",
	})
}

// do runs one request against the gateway handler.
func do(t *testing.T, g *Gateway, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	return w
}

// ---------------------------------------------------------------------------
// A-01 /v1/models - the static list, byte-for-byte, and the SHARED gate
// ---------------------------------------------------------------------------

func TestModelsListIsTheMeasuredBody(t *testing.T) {
	g := newTestGateway(t, &fakeClient{}, nil, "")
	w := do(t, g, http.MethodGet, "/v1/models", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// 224 bytes is the measured live content-length. This catches accidental
	// pretty-printing, an extra field, or a reordered struct.
	want := `{"object":"list","data":[` +
		`{"id":"GLM-5.3-Flash","type":"model","display_name":"GLM-5.3-Flash","created_at":"2025-01-01T00:00:00Z"},` +
		`{"id":"GLM-5.3","type":"model","display_name":"GLM-5.3","created_at":"2025-01-01T00:00:00Z"}]}`
	if got := w.Body.String(); got != want {
		t.Errorf("body mismatch\n got: %s\nwant: %s", got, want)
	}
	if n := len(w.Body.String()); n != 224 {
		t.Errorf("body length = %d, want the measured 224", n)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json without charset", ct)
	}
}

// The gate covers /v1/models too (endpoints.md §1.1 scopes the key to /v1/*;
// black-box test_models_endpoint_gated_too asserts 401). Today's live 200 is
// only because meta.gateway_key is EMPTY - so this test pins BOTH branches.
func TestModelsSharesTheGatewayGate(t *testing.T) {
	t.Run("key configured, no credential -> 401", func(t *testing.T) {
		g := newTestGateway(t, &fakeClient{}, nil, "sk-configured")
		w := do(t, g, http.MethodGet, "/v1/models", "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
		assertShapeB(t, w, "authentication_error")
	})

	t.Run("key configured, x-api-key accepted -> 200", func(t *testing.T) {
		g := newTestGateway(t, &fakeClient{}, nil, "sk-configured")
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("x-api-key", "sk-configured")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	})

	t.Run("no key configured -> fully open (production state)", func(t *testing.T) {
		g := newTestGateway(t, &fakeClient{}, nil, "")
		if w := do(t, g, http.MethodGet, "/v1/models", ""); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		// A wrong credential must ALSO be 200 while the key is empty.
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer wrong-key")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("wrong key with empty config: status = %d, want 200", w.Code)
		}
	})
}

func TestAuthWrongCredentialIs403(t *testing.T) {
	g := newTestGateway(t, &fakeClient{}, nil, "sk-configured")
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer sk-wrong")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	assertShapeB(t, w, "authentication_error")
}

// ---------------------------------------------------------------------------
// The three error shapes must never be unified (CR-02)
// ---------------------------------------------------------------------------

// assertShapeB checks the envelope AND the measured key order (message first).
func assertShapeB(t *testing.T, w *httptest.ResponseRecorder, wantType string) {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body := w.Body.String()
	// Key order is contract: `"message"` must precede `"type"`.
	mi, ti := strings.Index(body, `"message"`), strings.Index(body, `"type"`)
	if mi < 0 || ti < 0 {
		t.Fatalf("body lacks message/type members: %s", body)
	}
	if mi > ti {
		t.Errorf("key order wrong: message at %d, type at %d (want message first): %s", mi, ti, body)
	}
	var got shapeB
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal shape B: %v (%s)", err, body)
	}
	if got.Error.Type != wantType {
		t.Errorf("error.type = %q, want %q", got.Error.Type, wantType)
	}
	if got.Error.Message == "" {
		t.Error("error.message is empty")
	}
}

// The two endpoints use DIFFERENT type strings for the SAME malformed-JSON
// semantics. Unifying them fails conformance (CR-01/CR-02).
func TestMalformedJSONTypeDiffersPerEndpoint(t *testing.T) {
	g := newTestGateway(t, &fakeClient{}, nil, "")

	w := do(t, g, http.MethodPost, "/v1/messages", `{bad`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("/v1/messages status = %d, want 400", w.Code)
	}
	assertShapeB(t, w, TypeInvalidRequest) // no _error suffix

	w = do(t, g, http.MethodPost, "/v1/chat/completions", `{bad`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("/v1/chat/completions status = %d, want 400", w.Code)
	}
	assertShapeB(t, w, TypeInvalidRequestError) // WITH _error suffix
}

// Same scenario, OPPOSITE semantics: a valid non-object body crashes the
// legacy messages path (500 text/plain) but is a clean 400 on chat (CR-32).
func TestNonObjectBodyDiffersPerEndpoint(t *testing.T) {
	g := newTestGateway(t, &fakeClient{}, nil, "")

	for _, body := range []string{`[]`, `null`, `"s"`, `123`, `true`} {
		w := do(t, g, http.MethodPost, "/v1/messages", body)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("messages %s: status = %d, want 500", body, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("messages %s: content-type = %q, want text/plain; charset=utf-8", body, ct)
		}
		if got := w.Body.String(); got != "Internal Server Error" {
			t.Errorf("messages %s: body = %q, want the 21-byte plain text", body, got)
		}

		w = do(t, g, http.MethodPost, "/v1/chat/completions", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("chat %s: status = %d, want 400", body, w.Code)
		}
		assertShapeB(t, w, TypeInvalidRequestError)
	}
}

// Unknown paths and wrong methods are FastAPI shape A (never shape B).
func TestShapeAForUnknownPathAndMethod(t *testing.T) {
	g := newTestGateway(t, &fakeClient{}, nil, "")

	w := do(t, g, http.MethodGet, "/nope", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("/nope status = %d, want 404", w.Code)
	}
	if got := w.Body.String(); got != `{"detail":"Not Found"}` {
		t.Errorf("/nope body = %s", got)
	}

	w = do(t, g, http.MethodPost, "/v1/models", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /v1/models status = %d, want 405", w.Code)
	}
	if got := w.Body.String(); got != `{"detail":"Method Not Allowed"}` {
		t.Errorf("POST /v1/models body = %s", got)
	}
	if allow := w.Header().Get("Allow"); allow != "GET" {
		t.Errorf("allow = %q, want GET", allow)
	}
}

// Trailing slash -> 307 (NOT 302) with an ABSOLUTE Location.
func TestTrailingSlashRedirectsAbsolutely(t *testing.T) {
	g := newTestGateway(t, &fakeClient{}, nil, "")
	for _, path := range []string{"/v1/models/", "/v1/messages/", "/v1/chat/completions/"} {
		w := do(t, g, http.MethodGet, path, "")
		if w.Code != http.StatusTemporaryRedirect {
			t.Errorf("%s: status = %d, want 307", path, w.Code)
			continue
		}
		loc := w.Header().Get("Location")
		if !strings.HasPrefix(loc, "http://") {
			t.Errorf("%s: Location = %q, want an absolute URL", path, loc)
		}
	}
}

// ---------------------------------------------------------------------------
// A-03 chat local validation chain (order and wording are measured)
// ---------------------------------------------------------------------------

func TestChatValidationChain(t *testing.T) {
	g := newTestGateway(t, &fakeClient{}, nil, "")

	cases := []struct {
		name string
		body string
		want string
	}{
		{"non-object", `[]`, msgNotJSONObject},
		{"missing model", `{"messages":[{"role":"user","content":"hi"}]}`, msgMissingModel},
		{"numeric model", `{"model":123,"messages":[{"role":"user","content":"hi"}]}`, msgMissingModel},
		{"empty model", `{"model":"","messages":[{"role":"user","content":"hi"}]}`, msgMissingModel},
		{"messages absent", `{"model":"GLM-5.3-Flash"}`, msgMissingMessages},
		{"messages null", `{"model":"GLM-5.3-Flash","messages":null}`, msgMissingMessages},
		{"messages empty", `{"model":"GLM-5.3-Flash","messages":[]}`, msgMissingMessages},
		{"messages wrong type", `{"model":"GLM-5.3-Flash","messages":"x"}`, msgMissingMessages},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, g, http.MethodPost, "/v1/chat/completions", tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			assertShapeB(t, w, TypeInvalidRequestError)
			var got shapeB
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if got.Error.Message != tc.want {
				t.Errorf("message = %q, want %q", got.Error.Message, tc.want)
			}
		})
	}
}

// /v1/messages must NOT validate the body beyond JSON legality: the missing
// "messages" array is forwarded upstream, and the upstream business error
// comes back as a 200 pass-through (A-02 red note 1).
func TestMessagesForwardsInsteadOfValidating(t *testing.T) {
	upstreamBody := `{"code":1005,"msg":"exceed quota limit","logid":"abc"}`
	client := &fakeClient{res: jsonResponse(200, "application/json; charset=utf-8", upstreamBody)}
	g := newTestGateway(t, client, []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}, "")

	// No messages at all - the legacy service still forwards this.
	w := do(t, g, http.MethodPost, "/v1/messages", `{"model":"GLM-5.3-Flash"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (upstream pass-through, NOT a local 400)", w.Code)
	}
	if got := w.Body.String(); got != upstreamBody {
		t.Errorf("body = %s, want the upstream body verbatim", got)
	}
	if len(client.reqs) != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the body must be forwarded)", len(client.reqs))
	}
}

// ---------------------------------------------------------------------------
// A-02 vs A-03: the SAME upstream business error, two OPPOSITE shapes (CR-33)
// ---------------------------------------------------------------------------

func TestUpstreamBusinessErrorTranslationDiffersPerEndpoint(t *testing.T) {
	upstreamBody := `{"code":1005,"msg":"exceed quota limit","logid":"abc"}`
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}

	t.Run("messages passes through at 200", func(t *testing.T) {
		client := &fakeClient{res: jsonResponse(200, "application/json; charset=utf-8", upstreamBody)}
		g := newTestGateway(t, client, acct, "")
		w := do(t, g, http.MethodPost, "/v1/messages", `{"model":"GLM-5.3-Flash","messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if got := w.Body.String(); got != upstreamBody {
			t.Errorf("body = %s, want upstream verbatim (shape C)", got)
		}
	})

	t.Run("chat translates to 502 upstream_error with the code", func(t *testing.T) {
		client := &fakeClient{res: jsonResponse(200, "application/json", upstreamBody)}
		g := newTestGateway(t, client, acct, "")
		w := do(t, g, http.MethodPost, "/v1/chat/completions",
			`{"model":"GLM-5.3-Flash","messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", w.Code)
		}
		// Measured body, 78 bytes: message carries the upstream msg verbatim.
		want := `{"error":{"message":"exceed quota limit","type":"upstream_error","code":1005}}`
		if got := w.Body.String(); got != want {
			t.Errorf("body mismatch\n got: %s\nwant: %s", got, want)
		}
		if n := len(w.Body.String()); n != 78 {
			t.Errorf("body length = %d, want the measured 78", n)
		}
	})

	t.Run("chat passes an upstream 4xx through untouched", func(t *testing.T) {
		// Measured: unknown model -> 400 {"code":3006,...} verbatim, NOT 502.
		body := `{"code":3006,"msg":"model not allowed","logid":"abc"}`
		client := &fakeClient{res: jsonResponse(400, "application/json", body)}
		g := newTestGateway(t, client, acct, "")
		w := do(t, g, http.MethodPost, "/v1/chat/completions",
			`{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (no translation for upstream 4xx)", w.Code)
		}
		if got := w.Body.String(); got != body {
			t.Errorf("body = %s, want the upstream body verbatim", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Upstream TRANSPORT failures -> 502 upstream_unreachable (both endpoints)
// ---------------------------------------------------------------------------

func TestUpstreamTransportFailureMapping(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}

	cases := []struct {
		name string
		err  error
	}{
		{"connect refused", &upstream.Error{Kind: upstream.KindConnect, Err: errors.New("connection refused")}},
		{"timeout", &upstream.Error{Kind: upstream.KindTimeout, Err: errors.New("context deadline exceeded")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
				client := &fakeClient{err: tc.err}
				g := newTestGateway(t, client, acct, "")
				w := do(t, g, http.MethodPost, path,
					`{"model":"GLM-5.3-Flash","messages":[{"role":"user","content":"hi"}]}`)
				if w.Code != http.StatusBadGateway {
					t.Errorf("%s: status = %d, want 502", path, w.Code)
				}
				assertShapeB(t, w, TypeUpstreamUnreachable)
			}
		})
	}
}

// The timeout is injectable, so the mapping above is reachable in production
// configuration too (Options.UpstreamTimeout is passed to every call).
func TestUpstreamTimeoutIsForwardedToClient(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	client := &fakeClient{res: jsonResponse(200, "application/json", `{"code":1005,"msg":"x","logid":"y"}`)}
	g := newTestGateway(t, client, acct, "")
	g.opt.UpstreamTimeout = 0 // legacy default: no upstream timeout

	_ = do(t, g, http.MethodPost, "/v1/messages", `{"model":"GLM-5.3-Flash"}`)
	if len(client.reqs) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(client.reqs))
	}
	if got := client.reqs[0].Timeout; got != 0 {
		t.Errorf("timeout = %v, want 0 (legacy forwards without an upstream timeout)", got)
	}
}

// ---------------------------------------------------------------------------
// Model normalisation and max_tokens clamping reach the UPSTREAM payload
// (not observable over HTTP, which is why these are unit-level assertions)
// ---------------------------------------------------------------------------

func TestRequestRewritesReachUpstream(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	client := &fakeClient{res: jsonResponse(200, "application/json", `{"code":1005,"msg":"x","logid":"y"}`)}
	g := newTestGateway(t, client, acct, "")

	_ = do(t, g, http.MethodPost, "/v1/messages",
		`{"model":"glm-5.3-flash","max_tokens":"999999","messages":[{"role":"user","content":"hi"}]}`)

	var sent map[string]any
	if err := json.Unmarshal(client.body, &sent); err != nil {
		t.Fatalf("upstream payload is not JSON: %v (%s)", err, client.body)
	}
	if got := sent["model"]; got != "GLM-5.3-Flash" {
		t.Errorf("upstream model = %v, want GLM-5.3-Flash (normalised casing)", got)
	}
	// The string form must be clamped too (it bypasses upstream code 1210).
	switch v := sent["max_tokens"].(type) {
	case float64:
		if v != 131072 {
			t.Errorf("max_tokens = %v, want 131072", v)
		}
	case string:
		if v != "131072" {
			t.Errorf("max_tokens = %q, want the clamped string form 131072", v)
		}
	default:
		t.Errorf("max_tokens has unexpected type %T (%v)", v, v)
	}
}

// Unknown model names are forwarded VERBATIM - the upstream rejects them, not
// a local whitelist (A-02 red note 2).
func TestUnknownModelIsForwardedNotRejected(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	client := &fakeClient{res: jsonResponse(400, "application/json", `{"code":3006,"msg":"model not allowed","logid":"z"}`)}
	g := newTestGateway(t, client, acct, "")

	_ = do(t, g, http.MethodPost, "/v1/messages", `{"model":"no-such-model-xyz"}`)
	if len(client.reqs) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(client.reqs))
	}
	var sent map[string]any
	_ = json.Unmarshal(client.body, &sent)
	if got := sent["model"]; got != "no-such-model-xyz" {
		t.Errorf("upstream model = %v, want the unknown name forwarded verbatim", got)
	}
}

// ---------------------------------------------------------------------------
// Empty pool -> 503 no_available_account (before any upstream call)
// ---------------------------------------------------------------------------

func TestEmptyPoolReturns503(t *testing.T) {
	client := &fakeClient{}
	g := newTestGateway(t, client, nil, "")
	w := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"GLM-5.3-Flash","messages":[{"role":"user","content":"hi"}]}`)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	assertShapeB(t, w, TypeNoAvailableAccount)
	if len(client.reqs) != 0 {
		t.Errorf("upstream calls = %d, want 0 (fail before dispatch)", len(client.reqs))
	}
}

// ---------------------------------------------------------------------------
// SSE: the error frame is a FRAME, not an HTTP status (CR-33 measured)
// ---------------------------------------------------------------------------

func TestChatStreamEmitsErrorFrameNotStatus(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	// The upstream answers 200 with an SSE-ish business error body.
	client := &fakeClient{res: jsonResponse(200, "application/json",
		`{"code":1005,"msg":"exceed quota limit","logid":"abc"}`)}
	g := newTestGateway(t, client, acct, "")

	w := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"GLM-5.3-Flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the error travels inside the stream)", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q, want text/event-stream; charset=utf-8", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("cache-control = %q, want no-cache", cc)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"type": "upstream_error"`) {
		t.Errorf("stream lacks the upstream_error frame: %q", body)
	}
	if !strings.Contains(body, `code=1005`) {
		t.Errorf("stream error message lacks the measured prefix: %q", body)
	}
}

// A successful Anthropic stream translates into chunk frames ending in
// data: [DONE] (the contract pins that terminator).
func TestChatStreamTranslatesAnthropicEvents(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	anthropicSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	client := &fakeClient{res: jsonResponse(200, "text/event-stream", anthropicSSE)}
	g := newTestGateway(t, client, acct, "")

	w := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := strings.TrimRight(w.Body.String(), "\n")
	if !strings.HasSuffix(body, "data: [DONE]") {
		t.Errorf("stream must end with data: [DONE], got tail: %q", tail(body, 120))
	}
	if !strings.Contains(body, `"object": "chat.completion.chunk"`) {
		t.Errorf("frames lack chat.completion.chunk object: %q", body)
	}
	if !strings.Contains(body, `"role": "assistant", "content": ""`) {
		t.Errorf("first frame must carry the measured empty-content delta: %q", body)
	}
	if !strings.Contains(body, `"content": "Hello"`) {
		t.Errorf("text delta was not translated: %q", body)
	}
	// The model echoed to the client is the normalised official name.
	if !strings.Contains(body, `"model": "GLM-5.3-Flash"`) {
		t.Errorf("model not normalised in frames: %q", body)
	}
}

// A stream that dies before message_stop must still be terminated, because the
// contract pins the [DONE] terminator.
func TestChatStreamTerminatesTruncatedUpstream(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	client := &fakeClient{res: jsonResponse(200, "text/event-stream",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}`)}
	g := newTestGateway(t, client, acct, "")

	w := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"GLM-5.3-Flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if !strings.HasSuffix(strings.TrimRight(w.Body.String(), "\n"), "data: [DONE]") {
		t.Errorf("truncated stream was not terminated: %q", w.Body.String())
	}
}

// The failure/validation paths must NEVER set SSE headers even when the client
// asked for stream:true (captain's measured rule).
func TestStreamRequestErrorsStayJSON(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}

	t.Run("local validation failure", func(t *testing.T) {
		g := newTestGateway(t, &fakeClient{}, acct, "")
		w := do(t, g, http.MethodPost, "/v1/chat/completions", `{"model":"GLM-5.3-Flash","stream":true}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json (not SSE)", ct)
		}
	})

	t.Run("upstream transport failure", func(t *testing.T) {
		client := &fakeClient{err: &upstream.Error{Kind: upstream.KindConnect, Err: errors.New("refused")}}
		g := newTestGateway(t, client, acct, "")
		w := do(t, g, http.MethodPost, "/v1/chat/completions",
			`{"model":"GLM-5.3-Flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json (not SSE)", ct)
		}
	})
}

// Upstream 5xx is ALSO passed through verbatim on both endpoints: the
// translation applies only to HTTP-200 bodies carrying an error code, so a
// genuine upstream 500 must reach the client as a 500.
func TestUpstream5xxPassesThroughOnBothEndpoints(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	body := `{"type":"error","error":{"type":"api_error","message":"upstream exploded"}}`

	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			client := &fakeClient{res: jsonResponse(500, "application/json; charset=utf-8", body)}
			g := newTestGateway(t, client, acct, "")
			w := do(t, g, http.MethodPost, path,
				`{"model":"GLM-5.3-Flash","messages":[{"role":"user","content":"hi"}]}`)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (no translation for a real upstream 5xx)", w.Code)
			}
			if got := w.Body.String(); got != body {
				t.Errorf("body = %s, want the upstream body verbatim", got)
			}
		})
	}
}

// The pass-through must preserve the upstream content-type, including its
// charset, because the legacy gateway relays that header verbatim.
func TestUpstreamContentTypeIsRelayed(t *testing.T) {
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	client := &fakeClient{res: jsonResponse(200, "application/json; charset=utf-8",
		`{"code":1005,"msg":"exceed quota limit","logid":"abc"}`)}
	g := newTestGateway(t, client, acct, "")

	w := do(t, g, http.MethodPost, "/v1/messages", `{"model":"GLM-5.3-Flash"}`)
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q, want the upstream value relayed verbatim", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("cache-control = %q, want no-cache (measured on the live gateway)", cc)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
