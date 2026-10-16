package upstream

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The production client must satisfy the seam the gateway mocks.
var _ Client = New()

// ---------------------------------------------------------------------------
// HTTP statuses are RESULTS, never transport errors. The gateway's whole
// error-shape mapping (CR-33: chat translates an HTTP-200 business error to
// 502, while an upstream 4xx passes through verbatim) depends on this split.
// ---------------------------------------------------------------------------

func TestHTTPStatusesAreResultsNotErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 429, 500, 502, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const upstreamBody = `{"code":3006,"msg":"model not allowed","logid":"abc"}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, upstreamBody)
			}))
			defer srv.Close()

			res, err := New().Send(context.Background(), &Request{
				Method: http.MethodPost,
				URL:    srv.URL,
			})
			if err != nil {
				t.Fatalf("status %d must come back as a result, got transport error: %v", status, err)
			}
			if res.StatusCode != status {
				t.Errorf("StatusCode = %d, want %d", res.StatusCode, status)
			}
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			_ = res.Body.Close()
			if string(body) != upstreamBody {
				t.Errorf("body = %s, want the upstream body verbatim", body)
			}
			// The upstream content-type travels with the response so the
			// pass-through path can reproduce it exactly (shape C).
			if ct := res.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("content-type = %q, want the upstream value verbatim", ct)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transport failures -> typed *Error, classified
// ---------------------------------------------------------------------------

func TestConnectFailureIsKindConnect(t *testing.T) {
	// Port 1 is reserved and never listening: a genuine connection refusal.
	_, err := New().Send(context.Background(), &Request{
		Method: http.MethodGet,
		URL:    "http://127.0.0.1:1/nothing-here",
	})
	if err == nil {
		t.Fatal("expected a transport error for a refused connection")
	}
	if !IsConnect(err) {
		t.Errorf("IsConnect = false for %v", err)
	}
	if IsTimeout(err) {
		t.Errorf("a refused connection must not be classified as a timeout: %v", err)
	}
	var ue *Error
	if !errors.As(err, &ue) {
		t.Fatalf("error is not a *upstream.Error: %T", err)
	}
	if ue.Kind != KindConnect {
		t.Errorf("Kind = %v, want KindConnect", ue.Kind)
	}
	if ue.Unwrap() == nil {
		t.Error("Unwrap() = nil, the underlying cause must stay reachable")
	}
}

func TestTimeoutIsKindTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the response open past the client's deadline
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	start := time.Now()
	_, err := New().Send(context.Background(), &Request{
		Method:  http.MethodPost,
		URL:     srv.URL,
		Timeout: 120 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !IsTimeout(err) {
		t.Errorf("IsTimeout = false for %v", err)
	}
	var ue *Error
	if !errors.As(err, &ue) || ue.Kind != KindTimeout {
		t.Errorf("Kind = %v, want KindTimeout", ue.Kind)
	}
	// The deadline must actually fire, not merely be recorded.
	if elapsed > 2*time.Second {
		t.Errorf("timeout took %v, want ~120ms", elapsed)
	}
}

// The legacy gateway forwards with NO upstream timeout, so the zero value must
// mean "no deadline" rather than "immediately expired".
func TestZeroTimeoutMeansNoDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	res, err := New().Send(context.Background(), &Request{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Timeout: 0,
	})
	if err != nil {
		t.Fatalf("Timeout=0 must not expire: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", res.StatusCode)
	}
	_ = res.Body.Close()
}

func TestClassifyTransportError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{"ctx deadline", context.DeadlineExceeded, KindTimeout},
		{"ctx canceled (client hung up)", context.Canceled, KindConnect},
		{"net.Error timeout", &fakeNetError{timeout: true}, KindTimeout},
		{"net.Error not timeout", &fakeNetError{timeout: false}, KindConnect},
		{"generic", errors.New("unexpected EOF"), KindConnect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyTransportError(tc.err)
			if got.Kind != tc.want {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.want)
			}
			if !errors.Is(got, tc.err) {
				t.Error("the original error must stay wrapped for errors.Is")
			}
		})
	}
}

// The predicates must not mis-fire on errors from other packages.
func TestPredicatesAreFalseForForeignErrors(t *testing.T) {
	err := errors.New("plain")
	if IsTimeout(err) || IsConnect(err) {
		t.Error("foreign errors must classify as neither timeout nor connect")
	}
}

// ---------------------------------------------------------------------------
// Forwarding fidelity: headers, body, and STREAMING (no buffering)
// ---------------------------------------------------------------------------

func TestHeadersAndBodyAreForwardedVerbatim(t *testing.T) {
	var gotHeader http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := http.Header{}
	// Placeholder credential shape only - never a real value (t6 secrets rule).
	h.Set("Authorization", "Bearer test-placeholder")
	h.Set("x-api-key", "test-placeholder")
	h.Set("x-zcode-device-mid", "mid-1")

	payload := []byte(`{"model":"GLM-5.3-Flash","messages":[{"role":"user","content":"hi"}]}`)
	res, err := New().Send(context.Background(), &Request{
		Method: http.MethodPost,
		URL:    srv.URL,
		Header: h,
		Body:   payload,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = res.Body.Close()

	if got := gotHeader.Get("Authorization"); got != "Bearer test-placeholder" {
		t.Errorf("Authorization = %q, want it forwarded verbatim", got)
	}
	if got := gotHeader.Get("x-zcode-device-mid"); got != "mid-1" {
		t.Errorf("identity header = %q, want it forwarded verbatim", got)
	}
	if string(gotBody) != string(payload) {
		t.Errorf("body = %s, want %s", gotBody, payload)
	}
}

// SSE depends on the response body arriving incrementally: if this client
// buffered the whole response, /v1/chat/completions streaming would stall.
func TestResponseBodyStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		if fl != nil {
			fl.Flush()
		}
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	res, err := New().Send(context.Background(), &Request{Method: http.MethodPost, URL: srv.URL})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer res.Body.Close()

	start := time.Now()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("read first line: %v", err)
	}
	if line != "data: first\n" {
		t.Errorf("first line = %q, want %q", line, "data: first\n")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("first frame took %v - the body was buffered, not streamed", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Transport configuration invariants (CR-22 lifeline + legacy timeout shape)
// ---------------------------------------------------------------------------

func TestTransportHonoursProxyEnvironment(t *testing.T) {
	rc, ok := New().(*realClient)
	if !ok {
		t.Fatal("New() must return the production client")
	}
	tr, ok := rc.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", rc.http.Transport)
	}
	// CR-22: the systemd unit's NO_PROXY is what lets the gateway reach the
	// upstream directly. Replacing this hook with a custom proxy function (or
	// nil) silently breaks every upstream call in production.
	if tr.Proxy == nil {
		t.Fatal("Transport.Proxy is nil - NO_PROXY/no_proxy would stop working (CR-22)")
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if proxy, err := tr.Proxy(req); err != nil || proxy != nil {
		t.Errorf("loopback must bypass any proxy, got %v (err %v)", proxy, err)
	}
}

func TestNoClientLevelTimeoutsByDefault(t *testing.T) {
	rc := New().(*realClient)
	tr := rc.http.Transport.(*http.Transport)
	if rc.http.Timeout != 0 {
		t.Errorf("client Timeout = %v, want 0 (a client-wide timeout would cap SSE streams)", rc.http.Timeout)
	}
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want 0 (the legacy gateway forwards without an upstream timeout)", tr.ResponseHeaderTimeout)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// fakeNetError implements net.Error so the timeout branch is reachable.
type fakeNetError struct{ timeout bool }

func (e *fakeNetError) Error() string   { return "fake net error" }
func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return false }

var _ net.Error = (*fakeNetError)(nil)
var _ = strings.TrimSpace // keep strings available for future assertions
