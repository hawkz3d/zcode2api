package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Package-level captcha wiring for the DIALOGUE gateway path.
//
// I-3 (captcha laziness): the legacy gateway applies the captcha header
// CONDITIONALLY - `if needs_captcha and captcha_retries > 0:` - so the FIRST
// upstream attempt never carries `x-aliyun-captcha-verify-param`. A token is
// solved only after the upstream answers with a captcha challenge, and the
// retry uses the SAME account.
//
// This is deliberately NOT pooling: the legacy `CAPTCHA_POOL_MIN` governs a
// background pre-solve pool (a latency feature), while laziness governs WHEN a
// token is fetched (a behavioural contract). The two are orthogonal - see the
// frozen contract docs/contract/review-evidence/i3-captcha-lazy-contract.md
// §2.3. Go has no pool, so `CAPTCHA_POOL_MIN` degrades to a config-compat
// no-op (gap G-7) while the lazy gate below is implemented in full.
//
// Falsifiability is the whole point (contract §2.1 G-1/G-2 - a single-sided
// criterion is half a criterion):
//
//	G-1  no-challenge path  -> solver call count MUST stay 0
//	G-2  challenge path     -> solver call count MUST become 1, same account
//
// Both are asserted in captcha_test.go against a counting fake solver.

// CaptchaSolver solves one Aliyun verify param. *captcha.Solver satisfies it;
// tests inject a counting fake. nil means "captcha not wired" - the challenge
// branch then degrades to the previous pass-through behaviour rather than
// crashing (a deployment without ZCODE_NODE_PATH must still serve traffic).
type CaptchaSolver interface {
	Solve(ctx context.Context) (string, error)
}

// maxCaptchaRetries is the legacy `MAX_CAPTCHA_RETRIES` (account-model §3.3,
// `gateway.py` L441): the gateway re-tries a challenged request at most this
// many times, and every retry is on the ORIGINAL account.
//
// Not to be confused with `ZCODE_CAPTCHA_RETRIES` (=4), which bounds the
// solver's internal attempts inside one Solve call. CR-21 is closed by this
// distinction (see account-model.md §427).
const maxCaptchaRetries = 3

// captchaChallenge detects the upstream's captcha challenge in a response.
//
// Three shapes are reachable per the legacy `_detect_captcha_challenge`; the
// contract freezes the OBSERVABLE ones (endpoints.md L649 "403 验证码挑战 →
// 原账号重试"). Detection is intentionally conservative: it fires on a 403
// whose body mentions captcha/verify, on the documented `captcha_error` type,
// and on an explicit challenge marker in the body. A 403 WITHOUT any captcha
// marker is NOT a challenge (measured routing: 401/403 non-captcha -> the
// account is marked `invalid`, a different branch this task does not own).
func captchaChallenge(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	s := strings.ToLower(string(body))
	if s == "" {
		// A bare 403 with no body carries no evidence of a captcha challenge.
		// Treating it as one would make the lazy gate fire on ordinary
		// auth/risk failures - the exact "always take" behaviour I-3 removed.
		return false
	}
	for _, marker := range []string{
		"captcha",      // covers x-aliyun-captcha-verify-param / captcha_error
		"verify_param", // the header's own name, sans hyphens
		"verify-param", //
		"验证码",          // zh-Hans upstream text (measured on the live service)
		"aliyun",       // the third-party provider name
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	// A JSON body may carry the challenge under a structured field instead of
	// prose. Accept the documented `captcha_error` type as a challenge.
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if t, _ := m["type"].(string); strings.EqualFold(t, "captcha_error") {
			return true
		}
		if c, _ := m["code"]; c != nil {
			if s, ok := c.(string); ok && strings.EqualFold(s, "captcha_error") {
				return true
			}
		}
	}
	return false
}
