package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"zhipucode2api/internal/gateway"
	"zhipucode2api/internal/models"
	"zhipucode2api/internal/upstream"
)

// ---- fakes ------------------------------------------------------------------

type fakeAccounts struct{}

func (fakeAccounts) ListAccounts(context.Context) ([]models.Account, error) {
	return []models.Account{{ID: "wiring-probe-0001", Provider: models.ProviderZai, Enabled: 1}}, nil
}

type fakeSettings struct{}

func (fakeSettings) GetSetting(context.Context, string) (string, bool, error) {
	return "", true, nil // no gateway key -> authGate lets requests through
}

// fakeAdminAccounts / fakeAdminSettings satisfy adminapi's wider source
// interfaces. The admin key is left empty so the adminapi router answers its
// own 401 rather than needing a credential in every wiring probe.
type fakeAdminAccounts struct{}

func (fakeAdminAccounts) ListAccounts(context.Context) ([]models.Account, error) {
	return nil, nil
}
func (fakeAdminAccounts) GetAccount(context.Context, string) (models.Account, bool, error) {
	return models.Account{}, false, nil
}
func (fakeAdminAccounts) UpsertAccount(context.Context, models.Account) error { return nil }
func (fakeAdminAccounts) DeleteAccount(context.Context, string) (bool, error) { return false, nil }
func (fakeAdminAccounts) SetEnabled(context.Context, string, bool) (bool, error) {
	return false, nil
}

type fakeAdminSettings struct{}

func (fakeAdminSettings) GetSetting(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (fakeAdminSettings) SetSetting(context.Context, string, string) error { return nil }

func testRootMux() http.Handler {
	reqLog := gateway.NewReqLog()
	gw := gateway.New(fakeAccounts{}, fakeSettings{}, upstream.New(), gateway.Options{ReqLog: reqLog})
	return newRootMux(gw.Handler(), fakeAdminAccounts{}, fakeAdminSettings{}, reqLog, nil)
}

func get(t *testing.T, h http.Handler, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// ---- the t12 contract: both surfaces live on ONE mux, neither bleeds --------

func TestWiringPagesRoutesServed(t *testing.T) {
	h := testRootMux()
	for _, p := range []string{"/admin/login", "/admin/accounts", "/admin/monitoring", "/admin/settings"} {
		resp, _ := get(t, h, p)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d, want 200 (page routed to pages? root mux?)", p, resp.StatusCode)
		}
		if ct := resp.Header.Get("content-type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s content-type=%q", p, ct)
		}
		if cc := resp.Header.Get("cache-control"); cc != "no-store" {
			t.Errorf("%s cache-control=%q want no-store", p, cc)
		}
	}
	resp, body := get(t, h, "/static/css/app.css")
	if resp.StatusCode != 200 || resp.Header.Get("content-type") != "text/css; charset=utf-8" {
		t.Fatalf("/static/css/app.css => %d %q", resp.StatusCode, resp.Header.Get("content-type"))
	}
	if len(body) != 23788 {
		t.Errorf("app.css bytes=%d, want 23788 (asset served verbatim)", len(body))
	}
}

func TestWiringGatewayStillWorks(t *testing.T) {
	h := testRootMux()
	resp, body := get(t, h, "/v1/models")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/models status=%d body=%q, want 200 via gateway", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("content-type"); ct != "application/json" {
		t.Errorf("/v1/models content-type=%q", ct)
	}
}

// TestWiringNoRouteInterference is the acceptance item 4: pages' "/"
// catch-all must not swallow /v1/*, and the gateway's own catch-alls must
// not eat pages paths. Both 404 fallbacks answer shape A, but through
// DIFFERENT owners - the /v1/ one carries the gateway's slashRedirect/
// auth behavior, proved by the 307 and by the auth-free models body.
func TestWiringNoRouteInterference(t *testing.T) {
	h := testRootMux()

	// unknown paths: shape A from the pages catch-all
	for _, p := range []string{"/health", "/nope", "/adminapi/x", "/admin/unknown"} {
		resp, body := get(t, h, p)
		if resp.StatusCode != http.StatusNotFound || body != `{"detail":"Not Found"}` {
			t.Errorf("%s => %d %q, want 404 shape A", p, resp.StatusCode, body)
		}
	}
	// /v1/* non-routes stay INSIDE the gateway subtree (its /v1/ catch-all)
	resp, body := get(t, h, "/v1/nope")
	if resp.StatusCode != http.StatusNotFound || body != `{"detail":"Not Found"}` {
		t.Errorf("/v1/nope => %d %q, want 404 shape A from gateway", resp.StatusCode, body)
	}
	// gateway slashRedirect (absolute 307) still reachable -> the mount kept
	// the whole gateway handler, not individual paths.
	resp, _ = get(t, h, "/v1/models/")
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("/v1/models/ => %d, want 307 from gateway slashRedirect", resp.StatusCode)
	}
	if loc := resp.Header.Get("location"); loc != "http://example.com/v1/models" {
		t.Errorf("/v1/models/ location=%q, want absolute", loc)
	}
	// pages trailing slash on the integrated mux (Register carries it now)
	resp, _ = get(t, h, "/admin/login/")
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("/admin/login/ => %d, want 307", resp.StatusCode)
	}
	if loc := resp.Header.Get("location"); loc != "http://example.com/admin/login" {
		t.Errorf("/admin/login/ location=%q, want absolute per measurement", loc)
	}
	// root redirects belong to pages
	resp, _ = get(t, h, "/")
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("location") != "/admin" {
		t.Errorf(`/ => %d %q, want 307 rel /admin`, resp.StatusCode, resp.Header.Get("location"))
	}
	resp, _ = get(t, h, "/admin")
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("location") != "/admin/login" {
		t.Errorf(`/admin => %d %q, want 307 rel /admin/login`, resp.StatusCode, resp.Header.Get("location"))
	}
}
