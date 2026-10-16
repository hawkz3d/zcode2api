package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// countingSolver records how many times Solve was called and what it returned.
// It is the instrument for contract §2.1 G-1/G-2 - a single-sided criterion is
// half a criterion, so both directions are asserted here.
type countingSolver struct {
	calls  int32
	token  string
	err    error
	onCall func()
}

func (c *countingSolver) Solve(context.Context) (string, error) {
	atomic.AddInt32(&c.calls, 1)
	if c.onCall != nil {
		c.onCall()
	}
	return c.token, c.err
}

func (c *countingSolver) count() int { return int(atomic.LoadInt32(&c.calls)) }

// scriptedClient answers with a scripted sequence of responses and records the
// verify param each request carried. `challenges` = how many leading 403
// challenge responses to emit before the final answer.
type scriptedClient struct {
	challenges int
	final      func() *upstream.Response
	seenParams []string
	calls      int
}

func (s *scriptedClient) Send(_ context.Context, req *upstream.Request) (*upstream.Response, error) {
	s.calls++
	s.seenParams = append(s.seenParams, req.Header.Get("x-aliyun-captcha-verify-param"))
	if s.calls <= s.challenges {
		body := `{"type":"error","error":{"type":"captcha_error","message":"验证码挑战: 请提供 x-aliyun-captcha-verify-param"}}`
		return &upstream.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	if s.final != nil {
		return s.final(), nil
	}
	return jsonResponse(200, "application/json",
		`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":2}}`), nil
}

// newCaptchaGateway builds a gateway with a plan account whose credential can
// serve the default channel, plus the injected captcha solver.
func newCaptchaGateway(t *testing.T, client upstream.Client, solver CaptchaSolver) *Gateway {
	t.Helper()
	settings := &fakeSettings{}
	g := New(&fakeAccounts{accs: []models.Account{
		accountJSON(t, "acct-plan", "zai", "jwt", "active"),
	}}, settings, client, Options{
		ZAIPlanURL:  "https://plan.invalid/anthropic/v1/messages",
		ZAIKeyURL:   "https://key.invalid/anthropic/v1/messages",
		BigModelURL: "https://bigmodel.invalid/api/anthropic/v1/messages",
		AppVersion:  "3.15.3",
		Captcha:     solver,
	})
	g.opt.Captcha = solver // New copies opts; keep the seam explicit
	return g
}

// ── G-1: no challenge -> solver MUST NOT be called ───────────────────────────
//
// Falsifiable: wire the solver, make the upstream answer 200, and assert the
// count stays 0. If a future edit makes the gate unconditional ("always
// take"), this test goes red - which is exactly the regression I-3 removed.
func TestG1_NoChallenge_NoSolve(t *testing.T) {
	cs := &countingSolver{token: "should-not-be-solved"}
	cl := &scriptedClient{}
	g := newCaptchaGateway(t, cl, cs)

	rec := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := cs.count(); got != 0 {
		t.Fatalf("G-1 red: solver called %d times on a challenge-free request, want 0", got)
	}
	if n := len(cl.seenParams); n != 1 {
		t.Fatalf("G-1 red: %d upstream attempts, want exactly 1", n)
	}
	if p := cl.seenParams[0]; p != "" {
		t.Fatalf("G-1 red: first attempt carried verify param %q, want empty (laziness)", p)
	}
}

// ── G-2: challenge -> solver MUST be called once, retry carries the token ────
//
// The counterpart of G-1. Without it, a "challenge branch never wired" defect
// (solver count 0 forever) would pass G-1 silently. Asserts the three-argument
// token reaches the upstream on the retry.
func TestG2_Challenge_SolvesOnceAndRetriesWithToken(t *testing.T) {
	cs := &countingSolver{token: "fresh-token-from-solver"}
	cl := &scriptedClient{challenges: 1}
	g := newCaptchaGateway(t, cl, cs)

	rec := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`)

	if got := cs.count(); got != 1 {
		t.Fatalf("G-2 red: solver called %d times after one challenge, want exactly 1", got)
	}
	if len(cl.seenParams) != 2 {
		t.Fatalf("G-2 red: %d upstream attempts, want 2 (challenge + retry)", len(cl.seenParams))
	}
	if cl.seenParams[0] != "" {
		t.Fatalf("G-2 red: first attempt carried %q, want empty", cl.seenParams[0])
	}
	if cl.seenParams[1] != "fresh-token-from-solver" {
		t.Fatalf("G-2 red: retry carried %q, want the solver token", cl.seenParams[1])
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("G-2 red: final status = %d, want 200 (retry should succeed)", rec.Code)
	}
}

// ── G-3: the retry uses the ORIGINAL account ────────────────────────────────
//
// Contract §2.1: "挑战信号 → invalidate() 旧池 → 原账号重试". A retry that
// switched accounts would still pass G-1/G-2 - so the account identity is
// asserted separately by counting pickAccount resolutions.
func TestG3_ChallengeRetryKeepsSameAccount(t *testing.T) {
	cs := &countingSolver{token: "tok"}
	cl := &scriptedClient{challenges: 1}
	g := newCaptchaGateway(t, cl, cs)

	before := atomic.LoadUint64(&g.planCursor)
	rec := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`)
	after := atomic.LoadUint64(&g.planCursor)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// One pick, then one retry on the SAME sel: the rotation cursor advances
	// exactly once. A second pick would advance it twice.
	if delta := after - before; delta != 1 {
		t.Fatalf("G-3 red: cursor advanced %d times, want exactly 1 (retry must not re-pick)", delta)
	}
}

// ── Boundary: retries are bounded, and a solve failure passes the answer through
func TestCaptchaRetryBound(t *testing.T) {
	cs := &countingSolver{token: "tok"}
	cl := &scriptedClient{challenges: 99} // never stops challenging
	g := newCaptchaGateway(t, cl, cs)

	rec := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`)

	if got := cs.count(); got != maxCaptchaRetries {
		t.Fatalf("solver calls = %d, want maxCaptchaRetries (%d)", got, maxCaptchaRetries)
	}
	if cl.calls != maxCaptchaRetries+1 {
		t.Fatalf("upstream attempts = %d, want %d", cl.calls, maxCaptchaRetries+1)
	}
	// Exhausted: the last challenge answer is relayed verbatim (403 pass-through).
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want the upstream 403 passed through", rec.Code)
	}
}

func TestCaptchaSolveFailurePassesThrough(t *testing.T) {
	cs := &countingSolver{err: errors.New("solver exploded")}
	cl := &scriptedClient{challenges: 1}
	g := newCaptchaGateway(t, cl, cs)

	rec := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-4.5-flash","messages":[{"role":"user","content":"hi"}]}`)

	if cs.count() != 1 {
		t.Fatalf("solver calls = %d, want 1 (attempted once)", cs.count())
	}
	if cl.calls != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no retry without a token)", cl.calls)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (upstream answer relayed, not a 500)", rec.Code)
	}
}

// ── captchaChallenge: the detector itself must not over-fire ─────────────────
//
// A false positive here would make the gate "always take" on ordinary 403s -
// the exact behaviour I-3 removed. Each negative case is a real 403 that must
// NOT be treated as a challenge.
func TestCaptchaChallengeDetection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"challenge with captcha marker", 403, `{"error":"请提供 captcha 参数"}`, true},
		{"challenge zh-hans", 403, `{"message":"验证码校验失败"}`, true},
		{"challenge via header name", 403, `{"error":"missing x-aliyun-captcha-verify-param"}`, true},
		{"challenge via documented type", 403, `{"type":"captcha_error"}`, true},
		{"plain 403 no body", 403, ``, false},
		{"plain 403 unrelated text", 403, `{"error":"forbidden"}`, false},
		{"401 is never a challenge", 401, `{"error":"captcha required"}`, false},
		{"200 with captcha word is not a challenge", 200, `{"error":"captcha"}`, false},
		{"500 is never a challenge", 500, `{"error":"captcha"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := captchaChallenge(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("captchaChallenge(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}
