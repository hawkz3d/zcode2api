package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// randRead and hexEncode are the two primitives reqID needs. They are declared
// as package-level vars so tests can inject a deterministic source, and are
// kept in this file rather than a shared util because nothing else in the
// gateway package mints random tokens.
var randRead = func(b []byte) (int, error) { return rand.Read(b) }

func hexEncode(b []byte) string { return hex.EncodeToString(b) }

// ReqLog is the Go equivalent of the legacy `app/reqlog.py`: an in-memory ring
// buffer of gateway requests, read by the admin API's B-21
// (`GET /admin/api/monitoring`) and emptied by B-22 (`POST .../clear`).
//
// It lives in the GATEWAY package, not in adminapi, for dependency-direction
// reasons: the gateway is the only writer, and `internal/adminapi` must not be
// imported by `internal/gateway` (adminapi already depends on gateway-adjacent
// packages). The admin API reaches the ring through its injectable
// `Options.Monitoring` / `Options.MonitoringClear` hooks, wired in cmd — so the
// ring is owned where it is written and merely observed where it is served.
//
// It is deliberately NOT persisted. The legacy module states the design intent
// outright: "仅内存（deque 上限 500 条），重启即清零：定位是运维实时观测而非
// 审计，避免每请求写库的 IO 放大". A fresh process therefore legitimately
// answers `{"entries":[],"keep":500}` after a restart or after `clear`, and it
// does NOT self-heal until new traffic arrives.
//
// The zero value is not usable; construct with NewReqLog.
type ReqLog struct {
	mu       sync.Mutex
	entries  []*reqLogEntry
	inflight map[string]*reqLogEntry
	now      func() time.Time
}

// reqLogEntry mirrors the legacy `begin()` dict FIELD FOR FIELD AND IN THE SAME
// ORDER. The order is contract-visible: Python serialises the dict in insertion
// order and the conformance harness compares responses positionally, so a
// reordered struct would diverge even when every value matched.
//
// Pointer fields model the legacy `None` states: `ok` is tri-state
// (nil = in flight, true = success, false = failure) and `status`, `t_first`,
// `t_total`, `input_tokens`, `output_tokens` are all nullable. Measured on the
// live baseline: a streamed upstream failure carries `"status":null`.
type reqLogEntry struct {
	ReqID        string   `json:"req_id"`
	TS           float64  `json:"ts"`
	Endpoint     string   `json:"endpoint"`
	Model        string   `json:"model"`
	Stream       bool     `json:"stream"`
	Preview      string   `json:"preview"`
	Account      string   `json:"account"`
	Mode         string   `json:"mode"`
	OK           *bool    `json:"ok"`
	Status       *int     `json:"status"`
	Error        string   `json:"error"`
	TFirst       *float64 `json:"t_first"`
	TTotal       *float64 `json:"t_total"`
	InputTokens  *int     `json:"input_tokens"`
	OutputTokens *int     `json:"output_tokens"`
}

// reqLogKeep is the legacy `reqlog.KEEP`: the ring capacity, reported verbatim
// by B-21 as `keep` so the UI can render "n/500".
const reqLogKeep = 500

// NewReqLog builds an empty ring with the contract capacity.
func NewReqLog() *ReqLog { return newReqLog(time.Now) }

func newReqLog(now func() time.Time) *ReqLog {
	return &ReqLog{inflight: map[string]*reqLogEntry{}, now: now}
}

// nowFloat is the legacy `time.time()`: wall-clock seconds as a float64.
func (l *ReqLog) nowFloat() float64 { return float64(l.now().UnixNano()) / 1e9 }

// Begin records a request that passed auth and body parsing, mirroring
// `reqlog.begin()`.
//
// The call site is contract-critical: the legacy invocation sits AFTER
// validation and BEFORE dispatch, which is why 4xx validation failures (missing
// model, malformed JSON, non-object body) never appear in the log. Measured on
// the live baseline: three such requests produced a delta of 0.
func (l *ReqLog) Begin(reqID, endpoint, model string, stream bool, preview string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := &reqLogEntry{
		ReqID:    reqID,
		TS:       l.nowFloat(),
		Endpoint: endpoint,
		Model:    model,
		Stream:   stream,
		Preview:  truncRunes(preview, 80),
	}
	l.entries = append(l.entries, e)
	if len(l.entries) > reqLogKeep {
		l.entries = l.entries[len(l.entries)-reqLogKeep:]
	}
	l.inflight[reqID] = e
	return reqID
}

// MarkAccount records the account that ultimately served the request; on a
// multi-account retry the LAST one wins (legacy `mark_account`).
func (l *ReqLog) MarkAccount(reqID, account, mode string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.inflight[reqID]; e != nil {
		e.Account = account
		e.Mode = mode
	}
}

// FinishOK closes a successful request. `status` of 0 means "unset" and becomes
// 200, exactly like the legacy `status or 200`.
func (l *ReqLog) FinishOK(reqID string, status int, tFirst *float64, inputTokens, outputTokens *int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.inflight[reqID]
	if e == nil {
		return
	}
	delete(l.inflight, reqID)
	ok := true
	e.OK = &ok
	if status == 0 {
		status = 200
	}
	e.Status = &status
	e.TFirst = tFirst
	total := l.nowFloat() - e.TS
	e.TTotal = &total
	e.InputTokens = inputTokens
	e.OutputTokens = outputTokens
}

// FinishError closes a failed request. `status` may be nil — the legacy stream
// path calls `finish_error` without one, which is how a logged entry ends up
// carrying `"status":null` (measured on the live baseline).
func (l *ReqLog) FinishError(reqID, msg string, status *int, tFirst *float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.inflight[reqID]
	if e == nil {
		return
	}
	delete(l.inflight, reqID)
	ok := false
	e.OK = &ok
	e.Status = status
	e.Error = truncRunes(msg, 200)
	e.TFirst = tFirst
	total := l.nowFloat() - e.TS
	e.TTotal = &total
}

// Snapshot returns every entry NEWEST FIRST as raw JSON objects, ready to be
// spliced into the B-21 response. This is the legacy `reversed(_entries)` — the
// direction is a contract the UI depends on (the monitoring page renders the
// first row as the most recent request), and the conformance harness asserts it
// independently through its ORDER layer.
//
// Entries still in flight are included, matching the legacy docstring
// "全部条目，最新在前（含在途）".
func (l *ReqLog) Snapshot() []json.RawMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]json.RawMessage, 0, len(l.entries))
	for i := len(l.entries) - 1; i >= 0; i-- {
		b, err := jsonMarshalCompact(l.entries[i])
		if err != nil {
			continue
		}
		out = append(out, json.RawMessage(b))
	}
	return out
}

// Clear empties both the ring and the in-flight index (legacy `clear`).
func (l *ReqLog) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
	l.inflight = map[string]*reqLogEntry{}
}

// truncRunes is Python's `s[:n]`, which slices by CODE POINT — not by byte and
// not by UTF-16 unit. Slicing bytes would cut a multi-byte character in half
// and emit invalid UTF-8.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// lastUserText is the legacy `_last_user_text`: the text of the LAST
// role=user message, used as the monitoring `preview`.
//
// It walks messages in reverse and returns the first user message it can read
// as text — either a plain string content, or the first `{"type":"text"}` part
// of a content list. A message whose content is neither yields "" and the walk
// CONTINUES (the legacy `if` falls through rather than returning), so a later
// user message with unreadable content does not mask an earlier readable one.
func lastUserText(obj map[string]any) string {
	msgs, _ := obj["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c
		case []any:
			for _, part := range c {
				p, ok := part.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := p["type"].(string); t == "text" {
					s, _ := p["text"].(string)
					return s
				}
			}
		}
	}
	return ""
}

// reqID mints the legacy `secrets.token_hex(8)`: 16 lowercase hex characters.
func reqID() string {
	var b [8]byte
	if _, err := randRead(b[:]); err != nil {
		return "0000000000000000"
	}
	return hexEncode(b[:])
}
