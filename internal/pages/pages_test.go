package pages

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"zhipucode2api/web"
)

func do(t *testing.T, h http.Handler, method, path string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// ---- pages ----------------------------------------------------------------

func TestPagesServedWithMeasuredHeaders(t *testing.T) {
	h := Handler()
	for _, p := range []string{"/admin/login", "/admin/accounts", "/admin/monitoring", "/admin/settings"} {
		resp := do(t, h, http.MethodGet, p, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("%s status=%d", p, resp.StatusCode)
		}
		if ct := resp.Header.Get("content-type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s content-type=%q", p, ct)
		}
		// ALL pages carry no-store (measured; the original contract listed
		// only /admin/login - the correction static-assets-spec §2 demands).
		if cc := resp.Header.Get("cache-control"); cc != "no-store" {
			t.Errorf("%s cache-control=%q, want no-store", p, cc)
		}
		// Pages have NO etag (measured: etag grep 0 hits on all four).
		if e := resp.Header.Get("etag"); e != "" {
			t.Errorf("%s unexpectedly has etag %q", p, e)
		}
		body := readAll(t, resp.Body)
		if !strings.Contains(string(body), "v=2.5.1") {
			t.Errorf("%s: rendered body lacks v=2.5.1", p)
		}
		if strings.Contains(string(body), "{{APP_VERSION}}") {
			t.Errorf("%s: placeholder leaked into render", p)
		}
	}
}

func TestPagesGETStrict405(t *testing.T) {
	h := Handler()
	for _, m := range []string{http.MethodHead, http.MethodPost, http.MethodOptions} {
		resp := do(t, h, m, "/admin/login", nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /admin/login = %d, want 405 (measured HEAD→405)", m, resp.StatusCode)
		}
		if a := resp.Header.Get("allow"); a != http.MethodGet {
			t.Errorf("%s Allow=%q want GET", m, a)
		}
	}
}

// ---- redirects (measured Location forms!) -----------------------------------

func TestRedirects(t *testing.T) {
	h := Handler()
	cases := []struct{ path, want string }{
		{"/", "/admin"},                           // relative, measured
		{"/admin", "/admin/login"},                // relative, measured
		{"/static", "http://example.com/static/"}, // ABSOLUTE, measured Starlette Mount quirk
		// Trailing slashes: 307 with ABSOLUTE Location (measured 2026-10-15
		// against the legacy service: /admin/login/ -> http://host/admin/login,
		// /admin/ -> http://host/admin). t12 folded this into Register.
		{"/admin/login/", "http://example.com/admin/login"},
		{"/admin/", "http://example.com/admin"},
	}
	for _, c := range cases {
		resp := do(t, h, http.MethodGet, c.path, nil)
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("%s status=%d want 307", c.path, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("location"); got != c.want {
			t.Errorf("%s location=%q want %q", c.path, got, c.want)
		}
	}
}

// ---- static assets ----------------------------------------------------------

func TestAssetServingHeaders(t *testing.T) {
	h := Handler()
	wantCT := map[string]string{
		"/static/css/app.css":  "text/css; charset=utf-8",
		"/static/js/auth.js":   "text/javascript; charset=utf-8",
		"/static/js/header.js": "text/javascript; charset=utf-8",
		"/static/js/toast.js":  "text/javascript; charset=utf-8",
	}
	for path, ct := range wantCT {
		resp := do(t, h, http.MethodGet, path, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("%s status=%d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("content-type"); got != ct {
			t.Errorf("%s content-type=%q want %q", path, got, ct)
		}
		etag := resp.Header.Get("etag")
		if !strings.HasPrefix(etag, `"`) || len(etag) != 34 {
			t.Errorf("%s etag=%q want quoted md5hex", path, etag)
		}
		body := readAll(t, resp.Body)
		if cl := resp.Header.Get("content-length"); cl != itoa(len(body)) {
			t.Errorf("%s content-length=%s actual=%d", path, cl, len(body))
		}
		// no cache-control on assets (measured)
		if cc := resp.Header.Get("cache-control"); cc != "" {
			t.Errorf("%s has cache-control=%q, want none", path, cc)
		}
		// self-consistent ETag == md5(content)
		if a, _ := web.LookupAsset(path); a.ETag != etag {
			t.Errorf("%s etag mismatch served vs computed", path)
		}
	}
}

func TestConditionalRequests(t *testing.T) {
	h := Handler()
	resp := do(t, h, http.MethodGet, "/static/css/app.css", nil)
	etag := resp.Header.Get("etag")
	io.Copy(io.Discard, resp.Body)

	// exact etag -> 304
	r304 := do(t, h, http.MethodGet, "/static/css/app.css", map[string]string{"if-none-match": etag})
	if r304.StatusCode != http.StatusNotModified {
		t.Fatalf("strong match status=%d want 304", r304.StatusCode)
	}
	if r304.Header.Get("content-type") != "" || r304.Header.Get("content-length") != "" {
		t.Errorf("304 must carry neither content-type nor content-length (measured)")
	}
	if r304.Header.Get("etag") != etag {
		t.Errorf("304 must echo etag (measured)")
	}
	if b := readAll(t, r304.Body); len(b) != 0 {
		t.Errorf("304 body=%d bytes, want 0", len(b))
	}
	// weak validator accepts (measured: W/"..." -> 304)
	if r := do(t, h, http.MethodGet, "/static/css/app.css", map[string]string{"if-none-match": "W/" + etag}); r.StatusCode != 304 {
		t.Errorf("weak-prefix match status=%d want 304", r.StatusCode)
	}
	// multi-candidate list
	if r := do(t, h, http.MethodGet, "/static/css/app.css", map[string]string{"if-none-match": `"zz","deadbeef",` + etag}); r.StatusCode != 304 {
		t.Errorf("list match status=%d want 304", r.StatusCode)
	}
	// mismatch -> 200
	if r := do(t, h, http.MethodGet, "/static/css/app.css", map[string]string{"if-none-match": `"nope"`}); r.StatusCode != 200 {
		t.Errorf("mismatch status=%d want 200", r.StatusCode)
	}
}

func TestAssetHeadAllowedAndNotFoundShapes(t *testing.T) {
	h := Handler()
	// static answers HEAD (contrast with pages' 405 - the measured split)
	if r := do(t, h, http.MethodHead, "/static/js/auth.js", nil); r.StatusCode != 200 {
		t.Errorf("HEAD /static/js/auth.js=%d want 200 (StaticFiles answers HEAD)", r.StatusCode)
	}
	for _, p := range []string{"/static/", "/static/nonexist.js", "/static/js/nope.q"} {
		r := do(t, h, http.MethodGet, p, nil)
		if r.StatusCode != 404 {
			t.Errorf("%s=%d want 404", p, r.StatusCode)
			continue
		}
		body := string(readAll(t, r.Body))
		if body != `{"detail":"Not Found"}` {
			t.Errorf("%s body=%q want shape A verbatim", p, body)
		}
		if ct := r.Header.Get("content-type"); ct != "application/json" {
			t.Errorf("%s content-type=%q want application/json", p, ct)
		}
	}
	// unknown admin page shape A 404
	r := do(t, h, http.MethodGet, "/admin/nope", nil)
	if r.StatusCode != 404 || string(readAll(t, r.Body)) != `{"detail":"Not Found"}` {
		t.Errorf("/admin/nope -> %d, want 404 shape A", r.StatusCode)
	}
}

// ---- version wiring ----------------------------------------------------------

func TestVersionPlaceholderSiteCount(t *testing.T) {
	total := 0
	for _, p := range Pages() {
		tmpl, ok := Template(p)
		if !ok {
			t.Fatalf("missing page %s", p)
		}
		total += strings.Count(tmpl, "{{APP_VERSION}}")
	}
	// 15 is the measured legacy template count (adr-006 "共 15 处").
	if total != 15 {
		t.Errorf("placeholder sites total=%d, want 15 (login3+accounts4+monitoring4+settings4)", total)
	}
}

func TestAssetEtagMatchSemantics(t *testing.T) {
	// unit level of the RFC-9110 subset the legacy service shows
	if !web.ETagMatch(`"abc"`, `"abc"`) {
		t.Error("exact strong match failed")
	}
	if !web.ETagMatch(`W/"abc"`, `"abc"`) {
		t.Error("weak client against strong must match (measured)")
	}
	if !web.ETagMatch(`"abc", "def"`, `"def"`) {
		t.Error("list match failed")
	}
	if web.ETagMatch(`"xyz"`, `"abc"`) {
		t.Error("mismatch must not match")
	}
}

// ---- D-group metadata (t20) -------------------------------------------------

func TestMetaExactBytesAndMethodSemantics(t *testing.T) {
	h := Handler()
	resp := do(t, h, "GET", "/meta", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /meta = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("content-type"); ct != "application/json" {
		t.Errorf("content-type=%q want application/json (measured)", ct)
	}
	body := readAll(t, resp.Body)
	if string(body) != `{"version":"2.5.3"}` {
		t.Errorf("body=%q want exactly {\"version\":\"2.5.3\"}", body)
	}
	if len(body) != 19 {
		t.Errorf("len=%d want 19 (measured)", len(body))
	}
	// Measured: /meta is GET-STRICT — HEAD/POST/DELETE -> 405 allow: GET.
	for _, m := range []string{"HEAD", "POST", "DELETE"} {
		r := do(t, h, m, "/meta", nil)
		if r.StatusCode != 405 || r.Header.Get("Allow") != "GET" {
			t.Errorf("%s /meta = %d allow=%q, want 405 allow GET", m, r.StatusCode, r.Header.Get("Allow"))
		}
	}
	// Measured: /meta/ -> 307 with ABSOLUTE Location.
	r := do(t, h, "GET", "/meta/", nil)
	if r.StatusCode != 307 || r.Header.Get("location") != "http://example.com/meta" {
		t.Errorf("/meta/ = %d loc=%q, want 307 http://example.com/meta", r.StatusCode, r.Header.Get("location"))
	}
}

func TestOpenAPIDocumentContract(t *testing.T) {
	h := Handler()
	resp := do(t, h, "GET", "/openapi.json", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /openapi.json = %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("content-type"); ct != "application/json" {
		t.Errorf("content-type=%q want application/json", ct)
	}
	var doc struct {
		OpenAPI string            `json:"openapi"`
		Info    map[string]string `json:"info"`
		Paths   map[string]any    `json:"paths"`
	}
	if err := json.Unmarshal(readAll(t, resp.Body), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi=%q want 3.1.0", doc.OpenAPI)
	}
	// THE observable contract point (captain): title must be the legacy name.
	if doc.Info["title"] != "zcode-hub" {
		t.Errorf("info.title=%q want zcode-hub (NOT zhipucode2api)", doc.Info["title"])
	}
	if doc.Info["version"] != "2.5.3" {
		t.Errorf("info.version=%q want 2.5.3", doc.Info["version"])
	}
	if len(doc.Paths) != 22 {
		t.Errorf("paths=%d want 22 (measured legacy route count)", len(doc.Paths))
	}
	for _, p := range []string{"/v1/messages", "/admin/api/accounts", "/admin/api/settings"} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("missing measured path %s", p)
		}
	}
	// Measured method semantics: HEAD -> 200; POST -> 405 allow "HEAD, GET".
	if r := do(t, h, "HEAD", "/openapi.json", nil); r.StatusCode != 200 {
		t.Errorf("HEAD /openapi.json = %d want 200 (measured)", r.StatusCode)
	}
	if r := do(t, h, "POST", "/openapi.json", nil); r.StatusCode != 405 || r.Header.Get("Allow") != "HEAD, GET" {
		t.Errorf("POST /openapi.json = %d allow=%q want 405 'HEAD, GET'", r.StatusCode, r.Header.Get("Allow"))
	}
}
