// Package captcha is the Go client for the legacy captcha solver, invoked
// as an external black-box process.
//
// # Provenance (t2 spike, NO-GO ruling)
//
// docs/contract/captcha-spike.md settled: real-Chromium replacement of
// solver.js is a dead end (the official online SDK no longer yields the
// 4-field VERIFY_PARAM, §1.3). The user decision: keep solver.js as a black
// box and call it via os/exec. This package therefore NEVER reads or
// re-implements solver internals - it depends only on the frozen
// process-level contract below.
//
// # Black-box process contract (from t2 measurements, trusted input)
//
//	node <solver.js> <scene> <region> <prefix>   # cwd = solver.js directory
//	success: exit 0, ONE stdout line "VERIFY_PARAM=<value>",
//	        measured value length 280 B (base64 of JSON
//	        {certifyId,sceneId,isSign,securityToken})
//	failure: exit 6 with stdout AND stderr EMPTY (no diagnostics - the
//	        reason - network vs bad params - is unobservable, hence the
//	        retry policy below is load-bearing)
//	exit codes: 0 ok | 2 timeout | 3 init failure | 4 fail | 5 onError |
//	            6 invalid-params/catch-all failure
//
// ⚠ Empty argv makes solver.js succeed with its OWN hardcoded defaults
// (spike §1.6), so Solve always passes the three parameters explicitly -
// production values are scene 11xygtvd / region cn / prefix no8xfe (B-15),
// supplied by the caller, never baked in here.
//
// Timeout (default 40s) and retry count (default 4, per
// ZCODE_CAPTCHA_TIMEOUT / ZCODE_CAPTCHA_RETRIES, defaults per env-contract
// E-13) are implemented on THIS side because the solver can hang silently.
// CR-21 ambiguity: the legacy constant may mean attempts or retries+1;
// Attempts below is pinned to "total attempts per Solve" and documented -
// flip one comparison if a later contract ruling says otherwise.
//
// # Downstream usage (endpoints.md B-15/B-16)
//
// The returned verifyParam is forwarded VERBATIM upstream
// (x-aliyun-captcha-verify-param); region pairs as
// x-aliyun-captcha-verify-region. So Solve must not re-encode/pad/trim the
// value beyond stripping the measured trailing newline.
package captcha
