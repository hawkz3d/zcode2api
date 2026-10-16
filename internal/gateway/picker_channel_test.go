package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// I-9 channel matrix (t18 §六确证一; t24). The ledger criterion is the
// x-provider request header (legacy _detect_provider; memory
// 《ZCode双通道拆分落地实践》step ③) - default = plan/JWT ledger,
// x-provider: bigmodel = subscription ledger (cross-provider apiKey pool +
// exhausted exemption). All mock upstream, zero real traffic.

func doH(t *testing.T, g *Gateway, path string, hdrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	return w
}

func okBody() *upstream.Response {
	return jsonResponse(200, "application/json", `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
}

func poolOf(accs ...models.Account) []models.Account { return accs }

// Defect filter check: the plan (default) channel must NOT poll api-key
// accounts - the legacy mixed-pool cost was hitting 1113-billing accounts
// first (seconds wasted + misleading logs).
func TestChannelPlanIgnoresApiKeyAccounts(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "active")), "")
	w := doH(t, g, "/v1/chat/completions", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (no JWT candidate on plan ledger), body=%s", w.Code, w.Body.String())
	}
	if len(client.reqs) != 0 {
		t.Errorf("apiKey account leaked into the plan channel: %d upstream calls", len(client.reqs))
	}
}

// Defect b: the subscription channel pool spans ALL providers - a zai-minted
// apiKey is selectable for bigmodel traffic (provider field is not the
// ledger criterion, the credential is).
func TestChannelSubTakesZaiApiKeyCrossProvider(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "active")), "")
	w := doH(t, g, "/v1/chat/completions", map[string]string{"x-provider": "bigmodel"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (zai apiKey must serve the subscription ledger), body=%s", w.Code, w.Body.String())
	}
	if len(client.reqs) != 1 || !strings.Contains(client.reqs[0].URL, "key.invalid") {
		t.Errorf("upstream call = %+v, want 1x the zai-key endpoint", client.reqs)
	}
}

// Defect c: exhausted passes on the subscription ledger (free models remain
// usable; the status is decided by paid-model balance)...
func TestChannelSubExhaustedExempt(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "exhausted")), "")
	w := doH(t, g, "/v1/chat/completions", map[string]string{"x-provider": "bigmodel"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (exhausted exempt on subscription), body=%s", w.Code, w.Body.String())
	}
}

// ...but NOT on the plan ledger (there exhausted really means no quota;
// retrying would just burn turns - the legacy design note).
func TestChannelPlanExhaustedNotExempt(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-j1", "zai", "jwt", "exhausted")), "")
	w := doH(t, g, "/v1/chat/completions", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (exhausted stays excluded on plan), body=%s", w.Code, w.Body.String())
	}
}

// invalid/disabled are never selectable on either ledger.
func TestChannelSubStillExcludesInvalid(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "invalid")), "")
	w := doH(t, g, "/v1/chat/completions", map[string]string{"x-provider": "bigmodel"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (invalid never selectable)", w.Code)
	}
}

// Defect a: a candidate that survives the credential filter but whose
// resolve fails (provider/mode combination with no defined transport) is
// SKIPPED - the next candidate serves the request instead of an immediate
// 503 (the legacy continue-on-unusable semantics).
func TestResolveFailureContinuesToNextCandidate(t *testing.T) {
	weird := accountJSON(t, "w-1", "weirdpass-through", "jwt", "active") // candidateFor(plan) passes: jwt!=  ""
	good := accountJSON(t, "z-j1", "zai", "jwt", "active")
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(weird, good), "")
	w := doH(t, g, "/v1/chat/completions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after skipping the unresolvable candidate; body=%s", w.Code, w.Body.String())
	}
	if len(client.reqs) != 1 || !strings.Contains(client.reqs[0].URL, "plan.invalid") {
		t.Errorf("upstream calls = %d, want exactly 1 (the healthy plan endpoint): %+v", len(client.reqs), client.reqs)
	}
}

// When EVERY candidate fails to resolve, the loop ends in the same 503 shape
// (and zero upstream traffic) - the continuation must not fabricate a call.
func TestAllCandidatesUnresolvableStill503(t *testing.T) {
	weird := accountJSON(t, "w-1", "weirdpass-through", "jwt", "active")
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(weird), "")
	w := doH(t, g, "/v1/chat/completions", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if len(client.reqs) != 0 {
		t.Errorf("upstream calls = %d, want 0", len(client.reqs))
	}
}

// The two ledgers keep independent pools and cursors: interleaving requests
// always land on the credential-appropriate account.
func TestLedgersRouteIndependently(t *testing.T) {
	jwtAcc := accountJSON(t, "z-j1", "zai", "jwt", "active")
	keyAcc := accountJSON(t, "z-k1", "zai", "apiKey", "active")
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(jwtAcc, keyAcc), "")

	for i, tc := range []struct {
		hdr    map[string]string
		wantIn string
	}{
		{nil, "plan.invalid"},
		{map[string]string{"x-provider": "bigmodel"}, "key.invalid"},
		{nil, "plan.invalid"},
		{map[string]string{"x-provider": "BIGMODEL"}, "key.invalid"}, // case-insensitive match
	} {
		w := doH(t, g, "/v1/chat/completions", tc.hdr)
		if w.Code != http.StatusOK {
			t.Fatalf("round %d: status %d body=%s", i, w.Code, w.Body.String())
		}
		last := client.reqs[len(client.reqs)-1]
		if !strings.Contains(last.URL, tc.wantIn) {
			t.Errorf("round %d: URL %q, want %q", i, last.URL, tc.wantIn)
		}
	}
}

// The routing header never leaks upstream: outbound headers are freshly
// assembled by accountSel.headers (whitelist), x-provider is consumed
// locally by detectChannel only.
func TestRoutingHeaderNeverLeaksUpstream(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "active")), "")
	w := doH(t, g, "/v1/chat/completions", map[string]string{"x-provider": "bigmodel"})
	// zai apiKey on the subscription ledger: selectable cross-provider.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	req := client.reqs[0]
	if got := req.Header.Get("x-provider"); got != "" {
		t.Errorf("x-provider leaked to upstream: %q", got)
	}
}
