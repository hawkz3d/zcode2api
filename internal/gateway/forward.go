package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"zhipucode2api/internal/upstream"
)

// maxUpstreamTimeout is only the ceiling for injected timeouts (0 = none).
const maxBodyBytes = 32 << 20 // 32 MiB; oversized -> shape B request_too_large (04-api-spec §1.6, declared)

// handleMessages implements A-02: forward-only proxy. The ONLY local
// validation is JSON legality:
//
//	malformed JSON         -> 400 shape B invalid_request (measured verbatim)
//	valid non-object JSON  -> 500 text/plain crash-reproduction (measured:
//	                          legacy payload.get() on a non-mapping)
//	object                 -> transform (model-name normalisation,
//	                          max_tokens clamp, plan-channel identity
//	                          injection) and forward; the upstream answer is
//	                          passed through verbatim (status, content-type,
//	                          body) - including HTTP-200 business errors and
//	                          4xx upstream rejections (A-02 measured facts).
//
// Transport failure (connect/timeout) -> 502 shape B upstream_unreachable
// (04-api-spec §1.6; production body unmeasured - declared semantics).
func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(w, r)
	if err != nil {
		return // writer already answered (413)
	}
	var body any
	if json.Unmarshal(raw, &body) != nil {
		writeShapeB(w, http.StatusBadRequest, TypeInvalidRequest, msgMalformedJSON)
		return
	}
	payload, ok := body.(map[string]any)
	if !ok {
		// Measured legacy crash: syntactically valid but non-mapping
		// ([]、null、"s"、123、true) -> 500 "Internal Server Error".
		writeInternalError(w)
		return
	}
	// Monitoring (B-21): the legacy /v1/messages handler calls
	// `reqlog.begin(req_id, "messages", ...)` BEFORE `_dispatch`
	// (gateway.py:202), so this endpoint is recorded exactly like chat -
	// including the 503 no-account case. Measured on the baseline: the
	// monitoring ring carries `endpoint="messages"` entries.
	msgModel := NormalizeModelName(stripLedgerPrefix(rawModelOf(payload)))
	if msgModel == "" {
		msgModel = "-" // legacy `str(body.get("model") or "-")`
	}
	t0 := time.Now()
	var recID string
	if g.reqLog != nil {
		streamFlag, _ := payload["stream"].(bool)
		recID = g.reqLog.Begin(reqID(), "messages", msgModel, streamFlag, lastUserText(payload))
	}

	sel, err := g.pickAccount(r, rawModelOf(payload))
	if err != nil {
		if recID != "" {
			st := http.StatusServiceUnavailable
			g.reqLog.FinishError(recID, "无可用账号 / 额度均已耗尽", &st, nil)
		}
		writeNoAccount(w)
		return
	}
	if recID != "" {
		acctName := ""
		if sel.Account.Name != nil {
			acctName = *sel.Account.Name
		}
		g.reqLog.MarkAccount(recID, acctName, sel.Mode)
	}
	transformAnthropicPayload(payload, sel)
	out, err := jsonMarshalCompact(payload)
	if err != nil {
		if recID != "" {
			g.reqLog.FinishError(recID, "请求体序列化失败: "+err.Error(), nil, nil)
		}
		writeInternalError(w)
		return
	}
	res, uerr := g.client.Send(r.Context(), &upstream.Request{
		Method:  http.MethodPost,
		URL:     sel.URL,
		Header:  sel.headers(g.opt),
		Body:    out,
		Timeout: g.opt.UpstreamTimeout,
	})
	if uerr != nil {
		if recID != "" {
			g.reqLog.FinishError(recID, "上游不可达: "+uerr.Error(), nil, nil)
		}
		writeUpstreamUnreachable(w, uerr)
		return
	}
	defer res.Body.Close()
	// Verbatim pass-through: status + content-type + body (CR-02 shape C).
	// The monitoring log mirrors the legacy behaviour for BOTH outcomes: a
	// non-200 is a FAILURE carrying the response text (gateway.py:612), while
	// the success path closes the entry once the body has been relayed.
	raw, rerr := io.ReadAll(res.Body)
	if rerr != nil {
		if recID != "" {
			g.reqLog.FinishError(recID, "读取上游响应失败: "+rerr.Error(), nil, nil)
		}
		writeInternalError(w)
		return
	}
	if recID != "" {
		tFirst := time.Since(t0).Seconds()
		if res.StatusCode != http.StatusOK {
			st := res.StatusCode
			text := strings.ReplaceAll(string(raw), "\n", " ")
			g.reqLog.FinishError(recID, "HTTP "+itoa(res.StatusCode)+": "+truncRunes(text, 120), &st, &tFirst)
		} else {
			zero := 0
			g.reqLog.FinishOK(recID, res.StatusCode, &tFirst, &zero, &zero)
		}
	}
	writeUpstreamVerbatim(w, res, raw)
}

// writeNoAccount emits the 503 no_available_account shape (black-box
// test_no_account_returns_503 asserts status+type; message text is
// declared-style, unmeasured verbatim).
func writeNoAccount(w http.ResponseWriter) {
	writeShapeB(w, http.StatusServiceUnavailable, TypeNoAvailableAccount, "无可用账号")
}

func writeUpstreamUnreachable(w http.ResponseWriter, uerr error) {
	writeShapeB(w, http.StatusBadGateway, TypeUpstreamUnreachable, "上游不可达: "+uerr.Error())
}

// copyUpstream streams the upstream body out unchanged, flushing per read so
// SSE passthrough never buffers (t6 streaming requirement).
// writeUpstreamVerbatim re-emits an upstream response whose body has ALREADY
// been read (the monitoring log needs the text to build its `error` field, so
// that pass-through cannot stream). Header handling is identical to
// copyUpstream so the two paths stay byte-equivalent on the wire.
func writeUpstreamVerbatim(w http.ResponseWriter, res *upstream.Response, raw []byte) {
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-cache")
	if cl := res.Header.Get("Content-Length"); cl != "" && !isEventStream(res) {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(raw)
}

func copyUpstream(w http.ResponseWriter, res *upstream.Response) {
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Measured on the live gateway: EVERY upstream-relayed response carries
	// cache-control: no-cache, whether it is a 200 business error or a real
	// 4xx. Set unconditionally rather than only when the upstream sends it,
	// because the upstream does not always do so.
	w.Header().Set("Cache-Control", "no-cache")
	if cl := res.Header.Get("Content-Length"); cl != "" && !isEventStream(res) {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func isEventStream(res *upstream.Response) bool {
	return strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream")
}

// transformAnthropicPayload applies the two measured request rewrites:
//
//	model      normalised (04-api-spec §1.1 + measured GLM-5.3-Flash;
//	           unknown names pass through verbatim)
//	max_tokens clamped to 131072 including the string form "999999"
//	           (black-box test_max_tokens_*; values below pass through)
//
// On the zai plan (JWT) channel the official ZCode system identity blocks are
// PREPENDED to any client system; user-provided system ends up as the LAST
// element (black-box: payload["system"][-1] is the user text). Key-channel
// and bigmodel calls are standard Anthropic endpoints - no injection there.
func transformAnthropicPayload(payload map[string]any, sel *accountSel) {
	if m, ok := payload["model"].(string); ok {
		payload["model"] = NormalizeModelName(stripLedgerPrefix(m))
	}
	clampMaxTokens(payload)
	if sel.Channel == ChannelPlan {
		payload["system"] = mergeIdentitySystem(payload["system"])
	}
}

func clampMaxTokens(payload map[string]any) {
	const limit = 131072
	switch v := payload["max_tokens"].(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil && n > limit {
			payload["max_tokens"] = limit
		} else if err == nil {
			payload["max_tokens"] = n // keep as int (avoids 1024 -> 1024.0 drift)
		}
	case float64:
		if v > limit {
			payload["max_tokens"] = limit
		}
	case string:
		// "999999" must clamp too (black-box guards the upstream code-1210
		// bypass); non-numeric strings stay untouched (unknown to us).
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			if n > limit {
				payload["max_tokens"] = limit
			} else if n == float64(int64(n)) {
				payload["max_tokens"] = int64(n)
			}
		}
	}
}

// NormalizeModelName lowercases then maps to the official capitalisation
// (04-api-spec §1.1 + the two live-listed models); unknown names return
// verbatim ("未知名原样透传").
func NormalizeModelName(model string) string {
	lower := strings.ToLower(model)
	if canonical, ok := modelCanonical[lower]; ok {
		return canonical
	}
	return model
}

// rawModelOf extracts the RAW model string (pre-normalization, ledger-prefix
// intact) for detectChannel; absent/non-string yields "".
func rawModelOf(payload map[string]any) string {
	m, _ := payload["model"].(string)
	return m
}

// stripLedgerPrefix consumes the "bigmodel/" subscription marker that the
// legacy gateway strips before forwarding (captain probe 2026-10-16: the
// upstream echo carried the BARE name; "bigmodel/nonexistent" reaching 1211
// upstream proves strip-then-forward). TrimPrefix only ever matches the head
// once - mid-string occurrences pass through verbatim (实现决定〔对拍未锚〕:
// legacy behavior for mid-string "bigmodel/" was never probed; head-only
// mirrors the measured semantics exactly).
func stripLedgerPrefix(model string) string {
	return strings.TrimPrefix(model, ledgerPrefixBigmodel)
}

// modelCanonical: doc table (glm-5.2/5-turbo/turbo/5.1/4.7) plus the two
// measured list members. Keys are lowercase forms.
var modelCanonical = map[string]string{
	"glm-5.2":       "GLM-5.2",
	"glm-5-turbo":   "GLM-5-Turbo",
	"glm-turbo":     "GLM-5-Turbo", // alias (04 §1.1)
	"glm-5.1":       "GLM-5.1",
	"glm-4.7":       "GLM-4.7",
	"glm-5.3-flash": "GLM-5.3-Flash", // measured: /v1/models + black-box chat
	"glm-5.3":       "GLM-5.3",       // measured: /v1/models
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeInternalError(w)
		return nil, err
	}
	if int64(len(raw)) > maxBodyBytes {
		// 413 request_too_large: declared by 04-api-spec §1.6 (the legacy
		// limit is on gzip-decompressed size; our transport does not accept
		// encoded request bodies, so the raw cap is the observable equivalent).
		writeShapeB(w, http.StatusRequestEntityTooLarge, "request_too_large", "请求体过大")
		return nil, errBodyTooLarge
	}
	return raw, nil
}

var errBodyTooLarge = errors.New("request body too large")

func logWarn(format string, args ...any) {
	log.Printf("["+time.Now().Format(time.RFC3339)+"] warning: "+format, args...)
}
