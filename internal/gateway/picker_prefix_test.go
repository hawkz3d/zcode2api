package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ledger-prefix tests (captain ruling 2026-10-16 on t24 follow-up): the
// legacy service consumes a "bigmodel/" model-name prefix - routes it to the
// subscription ledger AND strips it before forwarding (probe evidence:
// bigmodel/zzz -> 1211 upstream [stripped, reached bigmodel], bare zzz ->
// 3006 local plan rejection, bigmodel/glm-4.5-flash -> 200 echoing the BARE
// name). All mocked, zero real upstream traffic.

// forwardedModel unmarshals the captured upstream request body.
func forwardedModel(t *testing.T, client *fakeClient) string {
	t.Helper()
	if len(client.reqs) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(client.reqs))
	}
	var sent map[string]any
	if err := json.Unmarshal(client.reqs[0].Body, &sent); err != nil {
		t.Fatalf("upstream body not JSON: %v", err)
	}
	m, _ := sent["model"].(string)
	return m
}

// Core pair: prefix selects the subscription ledger (pool has NO jwt at all)
// AND the forwarded name is stripped.
func TestPrefixRoutesSubscriptionAndStrips(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "active")), "")
	w := doH2(t, g, "/v1/chat/completions",
		`{"model":"bigmodel/glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`, nil) // NO x-provider: prefix alone must route
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := forwardedModel(t, client); got != "glm-4.5-flash" {
		t.Errorf("forwarded model = %q, want stripped %q", got, "glm-4.5-flash")
	}
	if strings.Contains(w.Body.String(), "bigmodel/") {
		t.Errorf("response echo must carry the bare name: %s", w.Body.String())
	}
}

// Contradiction ruling (see detectChannel comment): prefix WINS over an
// opposing x-provider header - the routable reading succeeds where honoring
// the header would pin a guaranteed-failure request to the plan ledger.
func TestPrefixBeatsContradictingHeader(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "active")), "")
	w := doH2(t, g, "/v1/chat/completions", `{"model":"bigmodel/glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"x-provider": "zcode"})
	if w.Code != http.StatusOK {
		t.Fatalf("prefix must outrank x-provider:zcode: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := forwardedModel(t, client); got != "glm-4.5-flash" {
		t.Errorf("forwarded model = %q, want stripped", got)
	}
}

// A mid-string "bigmodel/" is NOT a ledger prefix: exact head match only;
// plan ledger keeps the name verbatim (实现决定〔对拍未锚〕 recorded in
// stripLedgerPrefix's comment - TrimPrefix mirrors the measured head-only
// semantics).
func TestMidStringPrefixUntouched(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-j1", "zai", "jwt", "active")), "")
	w := doH2(t, g, "/v1/chat/completions", `{"model":"x/bigmodel/y","messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := forwardedModel(t, client); got != "x/bigmodel/y" {
		t.Errorf("mid-string prefix must pass through verbatim, got %q", got)
	}
	if !strings.Contains(client.reqs[0].URL, "plan.invalid") {
		t.Errorf("non-prefixed model must stay on plan ledger: %s", client.reqs[0].URL)
	}
}

// The messages endpoint strips too - its rewrite lives in a different
// function (transformAnthropicPayload), so it gets its own guard.
func TestMessagesPrefixStrips(t *testing.T) {
	client := &fakeClient{res: okBody()}
	g := newTestGateway(t, client, poolOf(accountJSON(t, "z-k1", "zai", "apiKey", "active")), "")
	body := `{"model":"bigmodel/glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`
	w := doH2(t, g, "/v1/messages", body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := forwardedModel(t, client); got != "glm-4.5-flash" {
		t.Errorf("messages forwarded model = %q, want stripped", got)
	}
}

// doH2 is doH with an explicit body (messages shape differs from chat).
func doH2(t *testing.T, g *Gateway, path, body string, hdrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	return w
}
