package gateway

import (
	"encoding/json"
	"net/http"
)

// The gateway must emit THREE distinct error shapes. Unifying them into one
// struct makes all three wrong (contract CR-02, IMPLEMENTER-CHEATSHEET §1).
//
//	Shape A — FastAPI HTTPException: {"detail":"<text>"}
//	          used by /admin/api/* (not this task) and by unknown paths /
//	          unsupported methods.
//	Shape B — gateway pre-check:     {"error":{"message":M,"type":T}}
//	          used by /v1/* local validation. The two gateway endpoints use
//	          DIFFERENT T values for the same semantics:
//	            /v1/messages          -> "invalid_request"
//	            /v1/chat/completions  -> "invalid_request_error"
//	Shape C — upstream pass-through: the upstream body verbatim
//	          ({"code":N,"msg":...,"logid":...}) — /v1/messages only.
//
// Key order is observable and frozen: shape B is measured as `message` first,
// `type` second (endpoints.md §1.2 red note). The upstream_error variant
// appends `code` third. The struct field order below is therefore part of the
// contract, not a style choice.

// shapeA is the FastAPI {"detail":...} envelope.
type shapeA struct {
	Detail string `json:"detail"`
}

// shapeB is the gateway's own {"error":{...}} envelope.
type shapeB struct {
	Error shapeBInner `json:"error"`
}

type shapeBInner struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	// Code is present only on the upstream_error variant. omitempty keeps it
	// out of the invalid_request / invalid_request_error bodies.
	Code int `json:"code,omitempty"`
}

// Error type strings, frozen by endpoints.md §1.2 / CR-02.
const (
	// TypeInvalidRequest is the /v1/messages local-validation type. Note the
	// ABSENCE of the `_error` suffix — this is the legacy inconsistency the
	// contract requires us to reproduce, not fix.
	TypeInvalidRequest = "invalid_request"
	// TypeInvalidRequestError is the /v1/chat/completions local-validation
	// type, WITH the `_error` suffix.
	TypeInvalidRequestError = "invalid_request_error"
	// TypeUpstreamError is the /v1/chat/completions translation of an
	// upstream business error carried on an HTTP 200 (measured: quota
	// exhaustion surfaces as 502 + this type + the upstream code).
	TypeUpstreamError = "upstream_error"
	// TypeUpstreamUnreachable is the /v1/chat/completions translation of a
	// transport failure. Declared by 04-api-spec.md §1.6 (502); the exact
	// body was NOT measured against the live service (see §8 unverified).
	TypeUpstreamUnreachable = "upstream_unreachable"
	// TypeNoAvailableAccount is declared for the exhausted-pool case. Also
	// declared-but-unmeasured (04-api-spec.md §1.6, 503).
	TypeNoAvailableAccount = "no_available_account"
)

// Measured local-validation messages, verbatim from the live service.
// The Chinese wording is contract text — do not translate or reword it.
const (
	msgMalformedJSON   = "请求体不是合法 JSON"
	msgNotJSONObject   = "请求体必须是 JSON 对象"
	msgMissingModel    = "必须提供 model 参数"
	msgMissingMessages = "必须提供 messages 数组"
)

// writeShapeA writes {"detail":<text>} with the legacy content-type.
//
// The legacy service emits `application/json` with NO charset on these
// responses (endpoints.md §1.4 / CR-10); Go's net/http would otherwise pick
// `text/plain` for a bare WriteHeader, so the header is set explicitly.
func writeShapeA(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, "application/json", shapeA{Detail: detail})
}

// writeShapeB writes {"error":{"message":M,"type":T}}.
func writeShapeB(w http.ResponseWriter, status int, typ, message string) {
	writeShapeBWithCode(w, status, typ, message, 0)
}

// writeShapeBWithCode writes the upstream_error variant, which carries the
// upstream business code as a third member.
func writeShapeBWithCode(w http.ResponseWriter, status int, typ, message string, code int) {
	writeJSON(w, status, "application/json", shapeB{Error: shapeBInner{
		Message: message, Type: typ, Code: code,
	}})
}

// writeJSON serialises v and writes it with the given status and
// content-type. The body is a single Write so Content-Length is set by
// net/http (matching the measured content-length on the legacy service).
func writeJSON(w http.ResponseWriter, status int, contentType string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		// Marshal of these fixed structs cannot fail; if it somehow does,
		// fall back to the FastAPI 500 shape rather than panicking.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// writeInternalError reproduces the legacy unhandled-exception response
// byte-for-byte: 500, `text/plain; charset=utf-8`, and the 21-byte body
// "Internal Server Error" with NO trailing newline.
//
// Measured: POST /v1/messages with a syntactically valid but non-object JSON
// body ([], null, "s", 123, true) returns exactly this. It is a legacy crash
// on `payload.get(...)` against a non-mapping, and the contract requires us to
// reproduce the observable result rather than improve it (ADR-001).
func writeInternalError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte("Internal Server Error"))
}
