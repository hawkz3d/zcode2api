package muxprobe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pagesHandler mimics the REAL t12 pages routeAdmin: exact pages 200,
// page-level shape-A 404, trailing-slash 307 with ABSOLUTE Location.
func pagesHandler() http.Handler {
	rendered := map[string]bool{"/admin/login": true, "/admin/accounts": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/admin" {
			w.Header().Set("location", "/admin/login")
			w.WriteHeader(307)
			_, _ = io.WriteString(w, "PAGES-307-rel:/admin/login")
			return
		}
		if p == "/admin/" {
			w.Header().Set("location", "http://"+r.Host+"/admin")
			w.WriteHeader(307)
			_, _ = io.WriteString(w, "PAGES-307-abs->/admin")
			return
		}
		if rendered[p] {
			_, _ = io.WriteString(w, "PAGES-200")
			return
		}
		if clean := strings.TrimSuffix(p, "/"); clean != p && rendered[clean] {
			w.Header().Set("location", "http://"+r.Host+clean)
			w.WriteHeader(307)
			_, _ = io.WriteString(w, "PAGES-307-abs-slash")
			return
		}
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"detail":"Not Found"}@PAGES`)
	})
}

// apiHandler mimics Catherine's adminapi: internal FULL-PATH exact routes +
// subtree catch-all shape-A 404. withSlash toggles a FastAPI-style
// redirect_slashes wrapper (needed for /admin/api/status/ -> 307 to match
// the legacy measurement).
func apiHandler(withSlash bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/api/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "API-200@full-path")
	})
	mux.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"detail":"Not Found"}@API-catchall`)
	})
	if !withSlash {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			if clean := strings.TrimSuffix(r.URL.Path, "/"); clean == "/admin/api/status" {
				w.Header().Set("location", "http://"+r.Host+clean)
				w.WriteHeader(307)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// buildRoot assembles the t12 newRootMux (gateway + pages) and optionally
// mounts adminapi. mode: "none" (pre-t13), "mount", "register". block adds
// the anti-301 exact "/admin/api" -> 404. slash enables api redirect_slashes.
func buildRoot(mode string, block, slash bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("location", "/admin")
			w.WriteHeader(307)
			_, _ = io.WriteString(w, "PAGES-307-rel:/admin")
			return
		}
		pagesHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("location", "/admin/login")
		w.WriteHeader(307)
		_, _ = io.WriteString(w, "PAGES-307-rel:/admin/login")
	})
	mux.Handle("/admin/", pagesHandler())
	mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "GW-200")
	}))
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "STATIC-200")
	}))
	switch mode {
	case "mount":
		mux.Handle("/admin/api/", apiHandler(slash))
	case "register":
		mux.HandleFunc("/admin/api/status", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "API-200@flat")
		})
		mux.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"detail":"Not Found"}@API-catchall`)
		})
	}
	if block {
		mux.HandleFunc("/admin/api", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"detail":"Not Found"}@API-block`)
		})
	}
	return mux
}

func TestMuxPriority(t *testing.T) {
	paths := []string{
		"/admin/api", "/admin/api/", "/admin/api/status", "/admin/api/status/",
		"/admin/api/nonexistent", "/admin/api/nonexistent/",
		"/admin/login", "/admin/login/", "/admin/unknown", "/admin/", "/admin",
		"/", "/v1/models", "/static/css/app.css",
	}
	scenarios := []struct {
		name, mode   string
		block, slash bool
	}{
		{"A. PAGES-ONLY (pre-t13 today)", "none", false, false},
		{"B. MOUNT \"/admin/api/\" NO block", "mount", false, false},
		{"C. MOUNT \"/admin/api/\" +BLOCK +slash", "mount", true, true},
		{"D. REGISTER flat +BLOCK +slash", "register", true, true},
	}
	for _, sc := range scenarios {
		mux := buildRoot(sc.mode, sc.block, sc.slash)
		t.Logf("\n### %s", sc.name)
		for _, path := range paths {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)
			owner := string(body)
			if len(owner) > 30 {
				owner = owner[:30]
			}
			t.Logf("%-24s -> %d  loc=%-34q body=%s", path, resp.StatusCode, resp.Header.Get("location"), owner)
		}
	}
}
