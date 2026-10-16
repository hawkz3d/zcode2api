// Package adminapi implements the 22 legacy `/admin/api/*` endpoints (contract
// §2 B-01..B-22) as a single ordered route table.
//
// The package is deliberately NOT built on Go's ServeMux, because the legacy
// service is Starlette and its routing semantics are observable in the
// response contract:
//
//   - Routes are matched in REGISTRATION order, and the first route whose
//     PATH matches decides. A method mismatch on that route yields 405 with
//     `Allow:` naming only THAT route's method — not the union over every
//     route sharing the path. Measured: `PATCH /admin/api/accounts` -> 405
//     `allow: GET` even though the path also declares DELETE and POST.
//   - Because `/admin/api/accounts/{account_id}` (PUT) is registered before
//     `/admin/api/accounts/refresh` (POST), `GET /admin/api/accounts/refresh`
//     resolves to the `{account_id}` route and answers 405 `Allow: PUT`.
//     Go 1.22's ServeMux would instead pick the literal segment and answer
//     405 `Allow: POST` — a contract-visible difference.
//   - Trailing-slash normalisation (307) runs BEFORE auth, and its Location
//     is an ABSOLUTE URL.
//
// Request handling order is route -> method -> auth -> body validation, all
// measured: `PUT /admin/api/accounts/refresh` without credentials is 401 (the
// route matched), while the same request WITH credentials but no body is 422
// (auth passed, validation ran). An unregistered path is 404 regardless of
// credentials.
package adminapi

import (
	"net/http"
	"strconv"
)

// contentTypeJSON is the exact Content-Type the legacy service emits for every
// locally generated JSON response. Note the absence of a charset parameter:
// measured responses carry bare `application/json`, and the conformance harness
// compares content-type down to the media type only.
const contentTypeJSON = "application/json"

// Shape A — FastAPI HTTPException. Every `/admin/api/*` error and every
// unknown-path 404 uses exactly this one-field body.
type shapeA struct {
	Detail string `json:"detail"`
}

// Shape D — FastAPI/Pydantic request-validation error. This shape is NOT
// documented in endpoints.md (a contract gap: the doc only describes shapes A
// and B) but it is fully measurable, and a client that sends a wrong-typed body
// sees it. `detail` is an ARRAY of issues, not a string, so it cannot share
// shapeA's encoding.
//
// Field order inside each issue is measured and significant:
//
//	{"type":...,"loc":["body"],"msg":...,"input":...}
//
// with an optional trailing `ctx` on `json_invalid`.
type validationIssue struct {
	Type  string   `json:"type"`
	Loc   []any    `json:"loc"`
	Msg   string   `json:"msg"`
	Input any      `json:"input"`
	Ctx   *issueCtx `json:"ctx,omitempty"`
}

type issueCtx struct {
	Error string `json:"error"`
}

type shapeD struct {
	Detail []validationIssue `json:"detail"`
}

// Legacy validation `type` strings, measured verbatim.
const (
	valMissing  = "missing"
	valDictType = "dict_type"
	valListType = "list_type"
	valJSONBad  = "json_invalid"
)

// Legacy validation `msg` strings, measured verbatim.
const (
	msgFieldRequired = "Field required"
	msgNotDict       = "Input should be a valid dictionary"
	msgNotList       = "Input should be a valid list"
	msgJSONDecode    = "JSON decode error"
)

// Legacy `detail` strings for shape A, measured verbatim. These are contract
// text and must never be reworded or translated.
const (
	detailMissingCredential = "缺少鉴权凭证"
	detailBadCredential     = "鉴权凭证无效"
	detailNotFound          = "Not Found"
	detailMethodNotAllowed  = "Method Not Allowed"
	detailAccountMissing    = "账号不存在"
	detailJWTAccountMissing = "JWT 账号不存在"
	detailNoTokens          = "请输入至少一个 Token / API Key"
	detailMissingAccountID  = "缺少 account_id"
)

// writeJSON encodes v with the legacy media type and an explicit
// Content-Length. net/http sets Content-Length itself for small bodies, but
// doing it here keeps the header set identical for every code path.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := marshalJSON(v)
	if err != nil {
		// Marshalling our own response structs cannot fail for any input we
		// produce; if it ever does, the 500 is the honest answer.
		b = []byte(`{"detail":"Internal Server Error"}`)
		status = http.StatusInternalServerError
	}
	h := w.Header()
	h.Set("Content-Type", contentTypeJSON)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// writeShapeA emits `{"detail":"<text>"}`.
func writeShapeA(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, shapeA{Detail: detail})
}

// writeValidation emits the shape-D array body. `input` must be pre-shaped by
// the caller: for a body-less request it is JSON null; for a body rejected
// without a JSON content type it is the RAW BODY TEXT as a JSON string
// (measured: `"input":"{}"`); otherwise it is the decoded value.
func writeValidation(w http.ResponseWriter, issue validationIssue) {
	writeJSON(w, http.StatusUnprocessableEntity, shapeD{Detail: []validationIssue{issue}})
}

// writeMissingBody is the 422 emitted when a REQUIRED body is absent or empty.
func writeMissingBody(w http.ResponseWriter) {
	writeValidation(w, validationIssue{
		Type:  valMissing,
		Loc:   []any{"body"},
		Msg:   msgFieldRequired,
		Input: nil,
	})
}

// writeDictType is the 422 emitted when a body that must be a JSON object
// decodes to something else. `input` is the decoded value (so `[]`, `"x"`, or
// the raw text when there was no JSON content type).
func writeDictType(w http.ResponseWriter, input any) {
	writeValidation(w, validationIssue{
		Type:  valDictType,
		Loc:   []any{"body"},
		Msg:   msgNotDict,
		Input: input,
	})
}

// writeListType is the 422 emitted when a body that must be a JSON array
// decodes to something else. Used by `DELETE /admin/api/accounts`, whose body
// is an array rather than an object.
func writeListType(w http.ResponseWriter, input any) {
	writeValidation(w, validationIssue{
		Type:  valListType,
		Loc:   []any{"body"},
		Msg:   msgNotList,
		Input: input,
	})
}

// writeJSONInvalid is the 422 emitted when the body is present but is not
// parseable JSON. The `loc` index is the byte offset the decoder failed at
// (measured: 0 for a leading syntax error, 1 for `{`), `input` is `{}`, and
// `ctx.error` carries the decoder's own message.
func writeJSONInvalid(w http.ResponseWriter, offset int, decoderMsg string) {
	writeValidation(w, validationIssue{
		Type:  valJSONBad,
		Loc:   []any{"body", offset},
		Msg:   msgJSONDecode,
		Input: map[string]any{},
		Ctx:   &issueCtx{Error: decoderMsg},
	})
}
