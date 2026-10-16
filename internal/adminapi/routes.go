package adminapi

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

// maxBodyBytes bounds how much of a request body is buffered. The legacy
// service streams the body into Pydantic with no explicit limit; the only
// bulk endpoint is /admin/api/import (measured ~1 KB), so a generous cap is
// purely an OOM guard and never reachable by contract traffic.
const maxBodyBytes = 16 << 20

// bodyKind says whether a route's request body is mandatory.
//
// This is per-ROUTE and measured, not inferred from the method: POST
// /admin/api/accounts/refresh accepts an absent body (200, measured) while PUT
// /admin/api/accounts/refresh requires one (422, measured). Both resolve to
// different route entries — the PUT is matched by the earlier
// /admin/api/accounts/{account_id} route.
type bodyKind int

const (
	bodyNone     bodyKind = iota // no body is read at all
	bodyRequired                 // absent/empty body -> 422 `missing`
	bodyOptional                 // absent body -> handler defaults
)

// bodyShape is the JSON type the body must decode to.
type bodyShape int

const (
	shapeNone bodyShape = iota
	shapeDict
	shapeList
)

type handlerFunc func(*reqCtx)

// routeEntry is one registered path+method pair. Registration order across the
// slice IS the contract (see the package doc).
//
// One entry carries exactly one method, mirroring Starlette's one-route-per-
// decorator registration. The `Allow` header therefore needs no derivation:
// when this entry is the first PATH match but the request method differs, the
// answer is this entry's own method. Measured: `PATCH /admin/api/accounts` ->
// `allow: GET`, because the GET entry for that path is registered first even
// though DELETE and POST entries exist for the same path.
type routeEntry struct {
	pattern  string
	segments []string
	method   string
	bodyKind bodyKind
	bodyShape bodyShape
	handler  handlerFunc
}

func newRoute(pattern, method string, bk bodyKind, bs bodyShape, h handlerFunc) *routeEntry {
	return &routeEntry{
		pattern:   pattern,
		segments:  splitPath(pattern),
		method:    method,
		bodyKind:  bk,
		bodyShape: bs,
		handler:   h,
	}
}

func splitPath(p string) []string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// matchPath reports whether path matches the route's pattern, returning any
// `{name}` captures. A parameter segment matches Starlette's `[^/]+`, i.e. one
// or more characters with no slash — so an empty segment never matches.
func (e *routeEntry) matchPath(path string) (map[string]string, bool) {
	segs := splitPath(path)
	if len(segs) != len(e.segments) {
		return nil, false
	}
	var params map[string]string
	for i, want := range e.segments {
		got := segs[i]
		if len(want) > 1 && want[0] == '{' && want[len(want)-1] == '}' {
			if got == "" {
				return nil, false
			}
			if params == nil {
				params = make(map[string]string, 2)
			}
			params[want[1:len(want)-1]] = got
			continue
		}
		if want != got {
			return nil, false
		}
	}
	return params, true
}

// reqCtx carries the decoded request into a handler.
type reqCtx struct {
	w      http.ResponseWriter
	r      *http.Request
	params map[string]string
	body   []byte
	doc    map[string]any // decoded object, when bodyShape is shapeDict
	arr    []any          // decoded array, when bodyShape is shapeList
	value  any            // decoded value, whatever its shape
}

// param returns a captured path parameter.
func (c *reqCtx) param(name string) string { return c.params[name] }

// object returns the decoded body as an object, or an empty one when the body
// was optional and absent.
func (c *reqCtx) object() map[string]any {
	if c.doc == nil {
		return map[string]any{}
	}
	return c.doc
}

func (c *reqCtx) str(key string) (string, bool) {
	v, ok := c.object()[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func (c *reqCtx) stringList(key string) ([]string, bool) {
	raw, ok := c.object()[key]
	if !ok || raw == nil {
		return nil, false
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// router is the ordered route table. It implements http.Handler directly so
// the mount in cmd/ is a single Handle call.
type router struct {
	api    *API
	routes []*routeEntry
}

func (rt *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	method := r.Method

	// Starlette walks its route list once: it dispatches the first entry whose
	// PATH and METHOD both match, and only when no entry matches both does it
	// report 405 — with `Allow` naming the method of the FIRST entry whose PATH
	// matched (regardless of whether that entry's method was the requested one).
	//
	// Both halves are measured and neither is derivable from the other:
	//   - `POST /admin/api/accounts` dispatches (the POST entry exists further
	//     down the list), so a first-path-match-only rule would wrongly 405 it;
	//   - `PATCH /admin/api/accounts` is 405 `allow: GET`, so `Allow` must NOT
	//     be the union of the path's methods.
	// Starlette also adds HEAD to every GET route's method set; mirror that.
	var firstPathMatch *routeEntry
	for _, e := range rt.routes {
		params, ok := e.matchPath(path)
		if !ok {
			continue
		}
		if firstPathMatch == nil {
			firstPathMatch = e
		}
		if e.method == method || (method == http.MethodHead && e.method == http.MethodGet) {
			rt.dispatch(w, r, e, params)
			return
		}
	}
	if firstPathMatch != nil {
		w.Header().Set("Allow", firstPathMatch.method)
		writeShapeA(w, http.StatusMethodNotAllowed, detailMethodNotAllowed)
		return
	}

	// Trailing-slash normalisation runs only when nothing matched at all, and
	// BEFORE auth: measured `GET /admin/api/accounts/refresh/` without any
	// credentials is a 307, not a 401. Starlette's rule is single-direction —
	// strip a trailing slash, else append one — and it redirects on a path
	// match regardless of method.
	if path != "/" {
		alt := path + "/"
		if strings.HasSuffix(path, "/") {
			alt = strings.TrimSuffix(path, "/")
		}
		for _, e := range rt.routes {
			if _, ok := e.matchPath(alt); ok {
				redirect307(w, r, alt)
				return
			}
		}
	}

	writeShapeA(w, http.StatusNotFound, detailNotFound)
}

// dispatch enforces auth and then body validation, in that order. The order is
// measured: `PUT /admin/api/accounts/refresh` without credentials is 401 (the
// route matched, auth ran), while the same request WITH credentials and no
// body is 422 (auth passed, validation ran).
func (rt *router) dispatch(w http.ResponseWriter, r *http.Request, e *routeEntry, params map[string]string) {
	key, err := rt.api.adminKey(r.Context())
	if err != nil {
		writeShapeA(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if detail := checkAdminCredential(r, key); detail != "" {
		writeShapeA(w, http.StatusUnauthorized, detail)
		return
	}

	ctx := &reqCtx{w: w, r: r, params: params}
	if !rt.api.decodeBody(ctx, e) {
		return
	}
	e.handler(ctx)
}

// isJSONContentType mirrors FastAPI's body-decoding gate: the body is only
// parsed as JSON when the media type is application/json (or a +json suffix).
// Anything else leaves the RAW TEXT as the value, which is what the measured
// `dict_type`/`list_type` responses carry in `input`.
func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func (a *API) decodeBody(ctx *reqCtx, e *routeEntry) bool {
	if e.bodyKind == bodyNone {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(ctx.r.Body, maxBodyBytes))
	if err != nil {
		writeShapeA(ctx.w, http.StatusInternalServerError, "Internal Server Error")
		return false
	}
	ctx.body = raw
	if len(raw) == 0 {
		if e.bodyKind == bodyRequired {
			writeMissingBody(ctx.w)
			return false
		}
		return true
	}
	if !isJSONContentType(ctx.r.Header.Get("Content-Type")) {
		text := string(raw)
		if e.bodyShape == shapeList {
			writeListType(ctx.w, text)
		} else {
			writeDictType(ctx.w, text)
		}
		return false
	}
	// pyNormalizeJSON accepts exactly the language CPython's json.loads
	// accepts (including bare NaN/Infinity, which encoding/json rejects) and
	// reports JSONDecodeError.pos/.msg verbatim on failure — see pyjson.go.
	norm, off, msg, ok := pyNormalizeJSON(raw)
	if !ok {
		writeJSONInvalid(ctx.w, off, msg)
		return false
	}
	var v any
	if err := json.Unmarshal(norm, &v); err != nil {
		// Unreachable for valid UTF-8 input: the scanner's language is a
		// superset of encoding/json's. Kept as a guard so a future divergence
		// degrades to the generic shape instead of a panic.
		writeJSONInvalid(ctx.w, 0, pyMsgExpectingValue)
		return false
	}
	// Measured: a JSON `null` body is treated as an ABSENT body, not as a type
	// error. On a required-body route it yields `missing` (the same 422 as no
	// body at all); on an optional-body route the handler runs with defaults.
	// Every OTHER scalar (number/bool/string/array) is a genuine type error.
	if v == nil {
		if e.bodyKind == bodyRequired {
			writeMissingBody(ctx.w)
			return false
		}
		return true
	}
	ctx.value = v
	switch e.bodyShape {
	case shapeList:
		arr, ok := v.([]any)
		if !ok {
			writeListType(ctx.w, v)
			return false
		}
		ctx.arr = arr
	case shapeDict:
		doc, ok := v.(map[string]any)
		if !ok {
			writeDictType(ctx.w, v)
			return false
		}
		ctx.doc = doc
	}
	return true
}

// redirect307 emits the trailing-slash redirect. Measured shape: status 307,
// an ABSOLUTE Location URL (scheme + host + path), `content-length: 0`, and NO
// content-type at all — so it must not go through writeJSON.
func redirect307(w http.ResponseWriter, r *http.Request, alt string) {
	loc := requestScheme(r) + "://" + r.Host + alt
	if r.URL.RawQuery != "" {
		loc += "?" + r.URL.RawQuery
	}
	h := w.Header()
	h.Set("Location", loc)
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusTemporaryRedirect)
}

// requestScheme mirrors the gateway helper: TLS or an explicit
// X-Forwarded-Proto wins, otherwise plain http.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p
	}
	return "http"
}
