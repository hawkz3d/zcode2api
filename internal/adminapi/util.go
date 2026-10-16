package adminapi

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode"

	"zhipucode2api/internal/models"
)

// itoa is the integer formatter used in upstream error messages. Python builds
// those strings with f-strings, so the Go equivalent must not differ in any way
// a client could observe (`鉴权失败 HTTP 401`).
func itoa(n int) string { return strconv.Itoa(n) }

// marshalJSON is json.Marshal with HTML escaping turned OFF, which is the only
// way to match FastAPI byte-for-byte.
//
// encoding/json escapes `<`, `>` and `&` as `\u003c`, `\u003e` and `\u0026` by
// default; Python's json.dumps (and therefore Starlette's JSONResponse) does
// not. The difference is directly observable: B-11's `authorize_url` carries
// three `&` separators, so the default encoder emits 15 bytes more than the
// baseline and the whole response stops matching. Measured:
//
//	base {"flow_id":...,"authorize_url":"...?a=1&b=2&c=3&d=4",...}  279 B
//	cand ...same URL with & written as \u0026                       294 B
//
// Every serialisation point in this package must go through this helper —
// including the ones feeding `Set`/persistence, because a `\u0026` written into
// `data` would also diverge from the legacy SQLite rows.
//
// Encoder.Encode appends a newline; it is trimmed so the output is byte-equal
// to json.Marshal's.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// errString is a constant error whose text is a plain string.
type errString string

func (e errString) Error() string { return string(e) }

// parseIntStrict reproduces Python's `int(<str>)` for the strict cases the
// upstream business codes need: surrounding whitespace is allowed, an empty
// string is not a number, and a fractional literal is rejected (Python would
// raise on `int("1.5")`).
func parseIntStrict(s string) (int, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, errString("empty string is not a number")
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, errString("invalid literal for int(): " + trimmed)
	}
	return n, nil
}

// nowUnix is the wall-clock epoch seconds used when no clock has been injected.
// It exists as a free function because `newAccount` is called from contexts
// (import, login) that have no *API receiver.
func nowUnix() int64 { return time.Now().Unix() }

// isAlnum is Python's `str.isalnum()`: true for any Unicode letter or digit.
// Using bytes here would silently mangle CJK account names into dashes, and
// since the id becomes the account's primary key that is a data-loss bug, not a
// cosmetic one.
func isAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// asObject decodes a raw member of the account data into a generic JSON object.
// AccountData.Object returns map[string]json.RawMessage (its storage shape),
// while the billing/entitlement code needs the Python-shaped map[string]any.
func asObject(d *models.AccountData, key string) (map[string]any, bool) {
	raw, ok := d.Raw(key)
	if !ok || d.IsNull(key) {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// asArray decodes a raw member into a generic JSON array.
func asArray(d *models.AccountData, key string) ([]any, bool) {
	raw, ok := d.Raw(key)
	if !ok || d.IsNull(key) {
		return nil, false
	}
	var a []any
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, false
	}
	return a, true
}
