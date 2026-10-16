// Package pages serves the four admin console pages and the /static
// assets, reproducing the legacy FastAPI+Jinja2 behaviour per
// docs/contract/adr-006-frontend-rewrite.md (option C: rewritten pages,
// structure-equivalent DOM) and static-assets-spec.md (measured headers).
//
// Measured contract facts encoded here (2026-10-15 probes against 9120):
//
//   - pages: 200, content-type "text/html; charset=utf-8",
//     cache-control "no-store" on ALL four pages (the original contract only
//     listed login; measurement shows every page carries it), NO etag.
//   - pages are GET-strict: HEAD /admin/login -> 405 (Starlette GET routes
//     do not match HEAD). Static files DO answer HEAD (StaticFiles).
//   - 405/404 bodies are FastAPI shape A {"detail":"..."}.
//   - redirects 307: "/" -> "/admin", "/admin" -> "/admin/login" with
//     RELATIVE Location (measured - the contract's "absolute URL" note was
//     httpx following the chain, not the header); "/static" ->
//     "/static/" with ABSOLUTE Location (Starlette Mount quirk, measured).
//   - "/static/" (directory) -> 404 shape A (no listing).
//   - assets: content-type per type, quoted etag, If-None-Match -> 304
//     (weak "W/" accepted, measured), no cache-control header.
//   - {{APP_VERSION}} renders to web.Version "2.5.1" at 15 sites across
//     the four templates (count pinned by the structure test).
package pages

import (
	"embed"
	"net/http"
	"strings"
	"sync"

	"zhipucode2api/web"
)

//go:embed templates/*.html
var tplFS embed.FS

// metaBody is the D-01 /meta response. Measured EXACTLY 19 bytes with no
// whitespace: {"version":"2.5.3"}. Note the value "2.5.3" is a DIFFERENT
// version than the asset cache-bust "2.5.1" (web.Version) — that mismatch is
// real and copied verbatim per contract (endpoints.md §5 D-01). It is a byte
// constant, not json.Marshal, so no space sneaks in after the colon.
const metaBody = `{"version":"2.5.3"}`

// openapiBody is the D-02 /openapi.json document (hand-written, valid
// OpenAPI 3.1.0; title=zcode-hub + version=2.5.3, paths cover the 22 measured
// legacy routes). Not byte-equal to FastAPI's generated 19913B — per captain,
// conformance is L1+assertion (title/version are the observable contract).
//
//go:embed assets/openapi.json
var openapiBody []byte

// pageFile maps the URL path to its embedded template file.
var pageFile = map[string]string{
	"/admin/login":      "templates/login.html",
	"/admin/accounts":   "templates/accounts.html",
	"/admin/monitoring": "templates/monitoring.html",
	"/admin/settings":   "templates/settings.html",
}

// rendered caches each page after {{APP_VERSION}} substitution: content is
// static (measured stable across requests), so render once at init.
var rendered = func() map[string][]byte {
	out := make(map[string][]byte, len(pageFile))
	for path, file := range pageFile {
		out[path] = RenderTemplate(mustTemplate(file))
	}
	return out
}()

// Register installs the full pages+static+redirect surface on an existing
// mux. "/admin" and "/" are registered as EXACT patterns on purpose: without
// them Go's ServeMux would issue its own 301 canonical redirect (subtree
// "/admin/"), breaking the measured 307 semantics.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("/", handleRoot)           // catch-all + "/" 307
	mux.HandleFunc("/admin", handleAdminRoot) // 307 -> /admin/login
	mux.HandleFunc("/admin/", routeAdmin)     // 4 pages + page 404/405
	mux.HandleFunc("/static", handleStaticRoot)
	mux.HandleFunc("/static/", handleStatic)
	// D-group metadata (t20). EXACT patterns -> win over the "/" catch-all.
	// Registered WITHOUT a method prefix on purpose: /meta is GET-STRICT
	// (measured HEAD /meta -> 405 allow:GET), and Go's "GET /meta" form would
	// mirror HEAD to 200 (wrong). /openapi.json accepts GET+HEAD but not POST.
	mux.HandleFunc("/meta", handleMeta)
	mux.HandleFunc("/meta/", handleMetaSlash) // /meta/ -> 307 absolute (measured)
	mux.HandleFunc("/openapi.json", handleOpenAPI)
}

// handleMeta serves D-01 GET /meta -> 200 application/json, 19-byte body.
// Measured: only GET is allowed (HEAD/POST/DELETE -> 405 allow:GET, body
// {"detail":"Method Not Allowed"} shape A).
func handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeDetail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	h := w.Header()
	h.Set("content-type", "application/json")
	h.Set("content-length", itoa(len(metaBody)))
	_, _ = w.Write([]byte(metaBody))
}

// handleMetaSlash serves the trailing-slash form: measured /meta/ -> 307 with
// an ABSOLUTE Location to /meta (Starlette redirect_slashes, matches pages/
// and /static behaviour). Deeper paths fall through to shape-A 404.
func handleMetaSlash(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/meta/" {
		redirect307(w, r, "/meta", true)
		return
	}
	writeDetail(w, http.StatusNotFound, "Not Found")
}

// handleOpenAPI serves D-02 GET/HEAD /openapi.json -> 200 application/json.
// Measured: POST -> 405 allow:"HEAD, GET". Body is the hand-written asset.
func handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "HEAD, GET")
		writeDetail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	h := w.Header()
	h.Set("content-type", "application/json")
	h.Set("content-length", itoa(len(openapiBody)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(openapiBody)
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		redirect307(w, r, "/admin", false)
		return
	}
	writeDetail(w, http.StatusNotFound, "Not Found")
}

func handleAdminRoot(w http.ResponseWriter, r *http.Request) {
	redirect307(w, r, "/admin/login", false)
}

// Handler returns the complete standalone handler (root redirects included),
// used by tests and usable directly by the cmd layer.
func Handler() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

// defaultHandler memoizes Handler() for the package-level ServeHTTP.
var defaultHandler = sync.OnceValue(Handler)

// ServeHTTP implements http.Handler for the whole surface.
func ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defaultHandler().ServeHTTP(w, r)
}

func routeAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin" { // ServeMux can also deliver this exact path
		redirect307(w, r, "/admin/login", false)
		return
	}
	body, ok := rendered[r.URL.Path]
	if !ok {
		// Measured trailing-slash form (2026-10-15): /admin/login/, /admin/
		// etc. answer 307 with an ABSOLUTE Location (Starlette
		// redirect_slashes) - the standalone wrapper this replaced did the
		// same; Register is now self-sufficient for composition (t12).
		if clean := strings.TrimSuffix(r.URL.Path, "/"); clean != "" && clean != r.URL.Path {
			if _, isPage := rendered[clean]; isPage || clean == "/admin" {
				redirect307(w, r, clean, true)
				return
			}
		}
		writeDetail(w, http.StatusNotFound, "Not Found")
		return
	}
	if r.Method != http.MethodGet {
		// Measured: HEAD /admin/login -> 405 (GET-strict route).
		w.Header().Set("Allow", http.MethodGet)
		writeDetail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	h := w.Header()
	h.Set("cache-control", "no-store")
	h.Set("content-type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func handleStaticRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/static" {
		// Measured: absolute Location (Starlette Mount normalisation).
		redirect307(w, r, "/static/", true)
		return
	}
	handleStatic(w, r)
}

func handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/static/" {
		writeDetail(w, http.StatusNotFound, "Not Found") // no listing (measured)
		return
	}
	a, ok := web.LookupAsset(r.URL.Path)
	if !ok {
		writeDetail(w, http.StatusNotFound, "Not Found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeDetail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	if web.ETagMatch(r.Header.Get("If-None-Match"), a.ETag) {
		// Measured 304 carries etag only - no content-type, no length.
		w.Header().Set("etag", a.ETag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h := w.Header()
	h.Set("etag", a.ETag)
	h.Set("content-type", a.ContentType)
	h.Set("content-length", itoa(len(a.Data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return // Go suppresses the body anyway; explicit for clarity.
	}
	_, _ = w.Write(a.Data)
}

func redirect307(w http.ResponseWriter, r *http.Request, target string, absolute bool) {
	if absolute {
		target = requestBase(r) + target
	}
	w.Header().Set("location", target)
	w.WriteHeader(http.StatusTemporaryRedirect)
}

func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func writeDetail(w http.ResponseWriter, status int, detail string) {
	body, _ := marshalDetail(detail)
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// RenderTemplate applies the single legacy template variable ({{APP_VERSION}})
// by string replacement - the measured "no control structures" simplification
// from adr-006.
func RenderTemplate(raw string) []byte {
	return []byte(strings.ReplaceAll(raw, "{{APP_VERSION}}", web.Version))
}

func mustTemplate(file string) string {
	b, err := tplFS.ReadFile(file)
	if err != nil {
		panic("pages: missing embedded template " + file + ": " + err.Error())
	}
	return string(b)
}

// Template returns a page template as authored (placeholder intact), for
// the structure checker.
func Template(page string) (string, bool) {
	f, ok := pageFile[page]
	if !ok {
		return "", false
	}
	return mustTemplate(f), true
}

// Pages lists the served page paths.
func Pages() []string {
	out := make([]string, 0, len(pageFile))
	for p := range pageFile {
		out = append(out, p)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
