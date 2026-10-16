// Package web carries the four admin-console static assets and the
// console version constant. Assets were obtained via BLACK-BOX HTTP
// observation of the legacy service on 2026-10-15 (docs/contract/
// static-assets-spec.md §three, which explicitly sanctions this channel:
// "可以（通过 HTTP 请求老服务，这是黑盒观测）"). No file was read from
// zcode2api/frontend/. They must stay byte-identical to the legacy
// responses because the diff harness compares static bodies at L3 and the
// ST-* cases will pin content-type + ETag + 304 semantics.
package web

import (
	"crypto/md5"
	"embed"
	"encoding/hex"
	"strings"
)

// Version is the console asset-cache-busting version, value observed in the
// legacy responses (= content of the old frontend/version file): "2.5.1".
// Pages render {{APP_VERSION}} placeholders to exactly this string.
// (Not to be confused with the /meta service version "2.5.3" - the real
// inconsistency frozen in endpoints.md C-group notes.)
const Version = "2.5.1"

//go:embed static/css/app.css static/js/auth.js static/js/header.js static/js/toast.js
var files embed.FS

// Asset is one served static file with precomputed ETag.
type Asset struct {
	Path        string // URL path under /static/
	ContentType string
	ETag        string // quoted md5 of content; self-consistent (harness does not compare etag VALUES, only the 304 behavior)
	Data        []byte
}

var assets = buildAssets()

func buildAssets() map[string]Asset {
	specs := []struct {
		file string
		path string
		ct   string
	}{
		{"static/css/app.css", "/static/css/app.css", "text/css; charset=utf-8"},
		{"static/js/auth.js", "/static/js/auth.js", "text/javascript; charset=utf-8"},
		{"static/js/header.js", "/static/js/header.js", "text/javascript; charset=utf-8"},
		{"static/js/toast.js", "/static/js/toast.js", "text/javascript; charset=utf-8"},
	}
	out := make(map[string]Asset, len(specs))
	for _, s := range specs {
		b, err := files.ReadFile(s.file)
		if err != nil {
			panic("web: embedded asset missing: " + err.Error())
		}
		sum := md5.Sum(b)
		out[s.path] = Asset{
			Path: s.path, ContentType: s.ct, Data: b,
			ETag: `"` + hex.EncodeToString(sum[:]) + `"`,
		}
	}
	return out
}

// LookupAsset returns the embedded file for a /static/ URL path.
func LookupAsset(path string) (Asset, bool) {
	a, ok := assets[path]
	return a, ok
}

// Paths lists all served asset paths (for tests and diagnostics).
func Paths() []string {
	out := make([]string, 0, len(assets))
	for p := range assets {
		out = append(out, p)
	}
	return out
}

// ETagMatch implements RFC 9110 If-None-Match evaluation as observed
// against the legacy service: strong etag matches, the weak "W/" prefix
// matches either way, and "*" matches when an etag exists.
func ETagMatch(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "*" {
		return etag != ""
	}
	want := unW(etag)
	for _, cand := range strings.Split(header, ",") {
		if c := unW(strings.TrimSpace(cand)); c != "" && c == want {
			return true
		}
	}
	return false
}

func unW(s string) string {
	s = strings.TrimPrefix(strings.ToLower(s), "w/")
	return strings.Trim(s, `"`)
}
