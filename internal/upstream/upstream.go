// Package upstream is the HTTP client layer for the three legacy upstreams
// (zcode.z.ai plan channel, api.z.ai key channel, open.bigmodel.cn).
//
// Design rules frozen by docs/contract:
//
//   - Only transport-level failures are errors (connection refused/DNS/TLS,
//     timeout). Any HTTP status — including upstream business errors like
//     {"code":1005,...} returned under HTTP 200 — is a SUCCESSFUL response:
//     the gateway passes status, content-type and body through verbatim
//     (endpoints.md A-02 measured facts; compat-risks.md CR-02 form C).
//   - Direct connections: the Go standard library honours NO_PROXY from the
//     systemd unit (compat-risks.md CR-22 - the transport keeps the default
//     ProxyFromEnvironment; the deployment unit MUST keep NO_PROXY, which
//     scripts/zhipucode2api.service does).
//   - The legacy gateway applies NO upstream timeout (04-api-spec.md §3:
//     "网关转发无上游超时"); Timeout 0 means none. Callers may still set one
//     and tests rely on that capability (t6 requirement "必须支持：超时").
//   - Client disconnect must cancel the upstream stream: Send derives the
//     request context from the caller's ctx.
package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// ErrorKind classifies transport-level failures per the contract's mapping
// table (endpoints.md §1.2 "未实测, 文档声明" group: upstream_unreachable=502).
type ErrorKind int

const (
	// KindConnect is any connection-level failure (DNS, refused, TLS, abrupt
	// close before response headers) - legacy httpx.HTTPError branch that the
	// pool marks cooling (account-model.md §3.2).
	KindConnect ErrorKind = iota
	// KindTimeout is a per-request timeout or a cancelled/deadline ctx.
	KindTimeout
)

// Error is a transport failure. Upstream HTTP statuses are NOT errors here.
type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("upstream: %v: %v", e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func (k ErrorKind) String() string {
	switch k {
	case KindConnect:
		return "connect"
	case KindTimeout:
		return "timeout"
	}
	return "unknown"
}

// Request describes one upstream call.
type Request struct {
	Method  string
	URL     string
	Header  http.Header // sent as-is (caller builds the identity header set)
	Body    []byte
	Timeout time.Duration // 0 = none (legacy default)
}

// Response is a received upstream reply. Body streams the payload; the
// caller MUST close it. Header carries the upstream content-type verbatim
// (pass-through fidelity, CR-02 form C).
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// Client performs upstream requests. Mocked in gateway tests.
type Client interface {
	Send(ctx context.Context, req *Request) (*Response, error)
}

// realClient is the net/http implementation.
type realClient struct {
	http *http.Client
}

// New returns the production client. The transport is the standard
// ProxyFromEnvironment one so the systemd NO_PROXY continues to force direct
// connections (CR-22); idle connections are reused.
func New() Client {
	return &realClient{http: &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConnsPerHost:   8,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 0, // legacy: none
		},
	}}
}

// Send performs the request. Transport failures come back as *Error with
// KindConnect/KindTimeout; HTTP statuses are results, not errors.
func (c *realClient) Send(ctx context.Context, req *Request) (*Response, error) {
	callCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	body := io.Reader(nil)
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	hreq, err := http.NewRequestWithContext(callCtx, req.Method, req.URL, body)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, &Error{Kind: KindConnect, Err: err}
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			hreq.Header.Add(k, v)
		}
	}
	res, err := c.http.Do(hreq)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, classifyTransportError(err)
	}
	var outBody io.ReadCloser = res.Body
	if cancel != nil {
		// Release the timer ctx when the caller drains/closes the body, so
		// streaming responses keep their timeout scope until done.
		outBody = &cancelOnClose{ReadCloser: res.Body, cancel: cancel}
	}
	return &Response{StatusCode: res.StatusCode, Header: res.Header, Body: outBody}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func classifyTransportError(err error) *Error {
	// The request ctx deadline (our Timeout) surfaces as DeadlineExceeded;
	// a client disconnect surfaces as Canceled - the legacy service treats
	// abrupt closes as transport failures, not upstream health signals.
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Kind: KindTimeout, Err: err}
	case errors.Is(err, context.Canceled):
		return &Error{Kind: KindConnect, Err: err}
	case errors.As(err, &netErr) && netErr.Timeout():
		return &Error{Kind: KindTimeout, Err: err}
	default:
		return &Error{Kind: KindConnect, Err: err}
	}
}

// IsTimeout reports whether err is a transport *Error of KindTimeout.
func IsTimeout(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindTimeout
}

// IsConnect reports whether err is a transport *Error of KindConnect.
func IsConnect(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == KindConnect
}
