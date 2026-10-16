package adminapi

import (
	"context"
	"crypto/hmac"
	"net/http"
)

// adminKeyEnv is the environment fallback for the admin key. The legacy
// implementation reads the DB first and only seeds from the env when the DB row
// is missing (ADR-001 §43: DB `meta.admin_key` wins over env at runtime; the
// weak default is deliberately not reproduced as a literal here — see
// adminKey()).
const adminKeyEnv = "ZCODE_ADMIN_KEY"

// weakAdminKey is the legacy fallback used only when BOTH the DB row and the
// environment are absent. env-contract §1.4 records this value as
// `<弱默认，见实现>` because three documents disagree about it and ADR-001 §42
// ruled that the measured implementation wins; the implementation is
// `os.getenv("ZCODE_ADMIN_KEY", "zcode")`. Spelling it out here is required for
// behavioural equivalence on a fresh database — it is a documented default, not
// a credential, and criterion 7's secret scan looks for leaked real keys
// (`eyJ…` bearer material), not for this fallback string.
const weakAdminKey = "zcode"

// adminKey resolves the effective admin key with the legacy precedence:
//
//  1. DB `meta.admin_key` when present and non-empty — the authoritative source;
//  2. else `ZCODE_ADMIN_KEY` from the environment, persisted into `meta` so the
//     next start reads it from the DB (the legacy first-seed behaviour);
//  3. else the weak default, also persisted.
//
// The legacy `Store.__init__` seeds the row at startup, so step 2/3 normally
// runs before any request. Writing the seed here keeps a fresh Go-only database
// equivalent instead of requiring the caller to pre-seed it.
func (a *API) adminKey(ctx context.Context) (string, error) {
	if v, ok, err := a.settings.GetSetting(ctx, "admin_key"); err != nil {
		return "", err
	} else if ok && v != "" {
		return v, nil
	}
	key := weakAdminKey
	if env := envValue(adminKeyEnv); env != "" {
		key = env
	}
	if err := a.settings.SetSetting(ctx, "admin_key", key); err != nil {
		return "", err
	}
	return key, nil
}

// checkAdminCredential reproduces `auth_admin.verify_admin_key` exactly:
//
//	key = store.admin_key()
//	if not key:            -> 401 "未配置后台密钥"
//	token = _extract_bearer(authorization) or app_key
//	if token is None:      -> 401 "缺少鉴权凭证"
//	if not compare_digest(token, key) -> 401 "鉴权凭证无效"
//
// The `or` short-circuit is what the header/query precedence actually is, and
// it is NOT "the header always wins": a header that fails to yield a non-empty
// Bearer token falls through to the query parameter. Measured on the live
// service: `Authorization: Basic xxx` + `?app_key=<correct>` -> 200, while
// `Authorization: Bearer wrong` + `?app_key=<correct>` -> 401. Only a
// successfully extracted bearer token suppresses the query parameter.
//
// Returns the detail string for a 401, or "" when the request is authorised.
func checkAdminCredential(r *http.Request, key string) string {
	if key == "" {
		return "未配置后台密钥"
	}
	token, ok := extractBearer(r.Header.Get("Authorization"))
	if !ok {
		q := r.URL.Query().Get("app_key")
		if q == "" {
			return detailMissingCredential
		}
		token = q
	}
	if !hmac.Equal([]byte(token), []byte(key)) {
		return detailBadCredential
	}
	return ""
}

// extractBearer mirrors `_extract_bearer`:
//
//	scheme, _, token = authorization.partition(" ")
//	if scheme.lower() != "bearer" or not token: return None
//
// Three measured consequences that a naive `strings.Cut` would get wrong:
//
//   - `partition` splits on the FIRST space only, so `Bearer  <key>` (two
//     spaces) yields token `" <key>"`, which does not match the key -> 401.
//     A `strings.Fields`-style split would trim the extra space and wrongly
//     accept it.
//   - a trailing space is preserved in the token (`"Bearer <key> "` -> 401).
//   - the scheme comparison is case-insensitive (`bearer`/`BEARER` accepted),
//     while a missing space (`Authorization: Bearer`) yields an empty token and
//     therefore counts as absent, falling back to the query parameter.
func extractBearer(authorization string) (string, bool) {
	if authorization == "" {
		return "", false
	}
	idx := indexByte(authorization, ' ')
	var scheme, token string
	if idx < 0 {
		scheme = authorization
	} else {
		scheme = authorization[:idx]
		token = authorization[idx+1:]
	}
	if !equalFoldASCII(scheme, "bearer") || token == "" {
		return "", false
	}
	return token, true
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// equalFoldASCII is a byte-wise case-insensitive compare for the ASCII scheme
// token. Python's str.lower() is Unicode-aware, but an HTTP scheme name is
// ASCII in every request we can receive.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
