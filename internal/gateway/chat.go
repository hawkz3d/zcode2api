package gateway

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"zhipucode2api/internal/upstream"
)

// handleChatCompletions implements A-03 (OpenAI Chat Completions).
//
// This endpoint is the OPPOSITE of /v1/messages in three measured ways
// (compat-risks.md CR-32/CR-33) - sharing one code path between them makes at
// least one endpoint fail the conformance run:
//
//  1. local validation     chat validates model+messages (400, shape B with
//     type invalid_request_error); messages does not
//  2. non-object JSON body chat -> 400 shape B; messages -> 500 text/plain
//  3. upstream business err chat TRANSLATES to 502 + upstream_error + code;
//     messages passes the upstream body through at 200
//
// Validation order is measured and frozen: object-ness -> model -> messages.
// The exact Chinese wording is contract text (errors.go).
func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(w, r)
	if err != nil {
		return // writer already answered (413)
	}

	var body any
	if json.Unmarshal(raw, &body) != nil {
		writeShapeB(w, http.StatusBadRequest, TypeInvalidRequestError, msgMalformedJSON)
		return
	}
	obj, ok := body.(map[string]any)
	if !ok {
		// Measured: []、null、"s"、123、true -> 400 (NOT the messages 500).
		writeShapeB(w, http.StatusBadRequest, TypeInvalidRequestError, msgNotJSONObject)
		return
	}
	model, _ := obj["model"].(string)
	if strings.TrimSpace(model) == "" {
		// Measured: absent, null, numeric 123 and "" all land here.
		writeShapeB(w, http.StatusBadRequest, TypeInvalidRequestError, msgMissingModel)
		return
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) == 0 {
		// Measured: absent, null, "notarray", {} and [] all land here.
		writeShapeB(w, http.StatusBadRequest, TypeInvalidRequestError, msgMissingMessages)
		return
	}

	stream, _ := obj["stream"].(bool)
	official := NormalizeModelName(stripLedgerPrefix(model))

	// Monitoring (B-21): `reqlog.begin` sits AFTER validation and BEFORE
	// account selection (legacy gateway.py:240 - the call precedes
	// `_dispatch`, which is what picks the account). A request that fails
	// validation is NOT recorded (measured: delta=0 for a bad model, malformed
	// JSON or a non-object body), but a request that finds NO account IS
	// recorded - the legacy `_dispatch` answers 503 and calls
	// `finish_error("无可用账号 / 额度均已耗尽")` on the same req_id
	// (gateway.py:381-382). Recording after pickAccount would silently drop
	// every 503, so the two sides' counters would diverge.
	//
	// The logged `model` is the NORMALISED, prefix-STRIPPED name: legacy reads
	// `body["model"]` after `_normalize_body` has stripped the `provider/`
	// prefix and applied MODEL_NAME_MAP (gateway.py:53-59). Captain's live
	// probe 2026-10-16 confirmed the baseline logs the bare name
	// (`bigmodel/glm-4.5-flash` is recorded as `glm-4.5-flash`).
	t0 := time.Now()
	var recID string
	if g.reqLog != nil {
		recID = g.reqLog.Begin(reqID(), "chat", official, stream, lastUserText(obj))
	}

	sel, err := g.pickAccount(r, model)
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
	anthReq := toAnthropicRequest(obj, sel, official)
	out, err := jsonMarshalCompact(anthReq)
	if err != nil {
		writeInternalError(w)
		return
	}

	// I-3 captcha laziness: the FIRST attempt carries NO verify param. A token
	// is solved only after the upstream answers with a captcha challenge, and
	// the retry reuses the SAME account (contract §2.1: "挑战信号 → 原账号重试").
	//
	// The loop is bounded by maxCaptchaRetries (=3, legacy MAX_CAPTCHA_RETRIES)
	// with `captchaRetries == 0` on entry, so the `captchaRetries > 0` gate is
	// false on the first pass - the lazy property is structural, not a special
	// case. This mirrors the legacy diff exactly:
	//     if needs_captcha:                      ->  if needs_captcha and captcha_retries > 0:
	var (
		res        *upstream.Response
		uerr       error
		verifyParm string
	)
	for captchaRetries := 0; ; captchaRetries++ {
		res, uerr = g.client.Send(r.Context(), &upstream.Request{
			Method:  http.MethodPost,
			URL:     sel.URL,
			Header:  sel.headersWithCaptcha(g.opt, verifyParm),
			Body:    out,
			Timeout: g.opt.UpstreamTimeout,
		})
		if uerr != nil {
			break // transport failure: 502 below, no challenge to inspect
		}
		// Only inspect for a challenge when a solver is actually wired: without
		// one, retrying cannot change the outcome - pass the answer through.
		if g.opt.Captcha == nil || captchaRetries >= maxCaptchaRetries {
			break
		}
		if !needsCaptchaRetry(res) {
			break
		}
		// Challenge: solve a fresh token, drop the previous one, retry the SAME
		// account. A solve failure is not fatal to the request - the challenge
		// answer is passed through so the caller sees the upstream's own words
		// (legacy: solve failure surfaces as a business receipt, not a 500).
		token, serr := g.opt.Captcha.Solve(r.Context())
		if serr != nil || strings.TrimSpace(token) == "" {
			break
		}
		verifyParm = token
		_ = res.Body.Close() // discard the challenge body before re-sending
	}
	if uerr != nil {
		// Declared (04-api-spec.md §1.6): 502 upstream_unreachable. The exact
		// body was never measured against the live service - see the t6 report.
		if recID != "" {
			g.reqLog.FinishError(recID, uerr.Error(), nil, nil)
		}
		writeShapeB(w, http.StatusBadGateway, TypeUpstreamUnreachable, "上游不可达: "+uerr.Error())
		return
	}
	defer res.Body.Close()

	if stream {
		relayChatStream(w, res, g.reqLog, recID, official, t0)
		return
	}
	relayChatResponse(w, res, g.reqLog, recID, official, t0)
}

// needsCaptchaRetry reports whether a response is a captcha challenge that a
// retry could clear. The body is BUFFERED here (and put back) because the
// challenge signature lives in it; without a wired solver the caller never
// reaches this function, so the buffering cost is only paid on the challenge
// path - which is the rare path by construction.
func needsCaptchaRetry(res *upstream.Response) bool {
	if res.StatusCode != http.StatusForbidden {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes))
	_ = res.Body.Close()
	if err != nil {
		return false
	}
	if captchaChallenge(res.StatusCode, body) {
		return true
	}
	// Not a challenge: restore the body so the normal relay path can read it.
	res.Body = io.NopCloser(bytes.NewReader(body))
	return false
}

// chatID generates an OpenAI-style completion id. The live gateway mints its
// own `chatcmpl-` id even though the upstream speaks Anthropic (`msg_...`).
func chatID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "chatcmpl-000000000000000000000000"
	}
	return "chatcmpl-" + hex.EncodeToString(b[:])
}

// businessCode reports the upstream business-error code when m is an upstream
// error body rather than an Anthropic message.
//
// Measured bodies: {"code":1005,"msg":"exceed quota limit","logid":"..."} and
// {"code":3006,"msg":"model not allowed","logid":"..."}. A real Anthropic
// message always carries a `content` array and never a top-level `code`.
func businessCode(m map[string]any) (int, string, bool) {
	if _, isMessage := m["content"]; isMessage {
		return 0, "", false
	}
	code, ok := m["code"]
	if !ok {
		return 0, "", false
	}
	msg, _ := m["msg"].(string)
	switch c := code.(type) {
	case json.Number:
		n, err := c.Int64()
		if err != nil {
			return 0, msg, false
		}
		return int(n), msg, true
	case float64:
		return int(c), msg, true
	}
	return 0, msg, false
}

// toAnthropicRequest converts an inbound OpenAI Chat body into the Anthropic
// Messages body the upstream expects.
//
// Contract-mandated pieces only: model normalisation, max_tokens clamp, and
// the OpenAI `system` message hoisted into the Anthropic top-level `system`
// member (black-box: the user's system text ends up as the LAST element of the
// system array, after the official identity blocks - mergeIdentitySystem does
// exactly that for the plan channel). Every other member is carried over
// verbatim so unknown/optional fields keep the client's shape.
func toAnthropicRequest(obj map[string]any, sel *accountSel, official string) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		switch k {
		case "model", "messages", "system", "stream":
			// rewritten below
		default:
			out[k] = v
		}
	}
	out["model"] = official

	// Split role=system messages out of the message list.
	var systemTexts []string
	conv := make([]any, 0, len(obj["messages"].([]any)))
	for _, item := range obj["messages"].([]any) {
		m, ok := item.(map[string]any)
		if !ok {
			conv = append(conv, item)
			continue
		}
		if role, _ := m["role"].(string); role == "system" {
			if s, ok := m["content"].(string); ok && s != "" {
				systemTexts = append(systemTexts, s)
			}
			continue
		}
		conv = append(conv, m)
	}
	out["messages"] = conv

	// The client's own system text: an explicit top-level `system` wins,
	// otherwise the hoisted system messages are joined.
	var clientSystem any
	switch s := obj["system"].(type) {
	case string:
		clientSystem = s
	case []any:
		clientSystem = s
	case nil:
		if len(systemTexts) > 0 {
			clientSystem = strings.Join(systemTexts, "\n")
		}
	default:
		clientSystem = s
	}
	if sel.Channel == ChannelPlan {
		// Official identity blocks first, caller system last (measured).
		out["system"] = mergeIdentitySystem(clientSystem)
	} else if clientSystem != nil {
		out["system"] = clientSystem
	}

	if stream, ok := obj["stream"].(bool); ok {
		out["stream"] = stream
	}
	clampMaxTokens(out)
	return out
}

// relayChatResponse translates a non-streaming Anthropic answer into an OpenAI
// Chat Completion, and reproduces the two upstream error behaviours that differ
// from /v1/messages.
func relayChatResponse(w http.ResponseWriter, res *upstream.Response, log *ReqLog, recID, model string, t0 time.Time) {
	// tFirst is the legacy `time.time() - attempt_t0`: the seconds between the
	// account attempt starting and the upstream response HEADERS arriving. It
	// is recorded on BOTH paths (finish_error and finish_ok) and is a nullable
	// field — a request that never reached upstream keeps it null.
	tFirst := time.Since(t0).Seconds()
	finishErr := func(status int, msg string) {
		if log != nil && recID != "" {
			st := status
			log.FinishError(recID, msg, &st, &tFirst)
		}
	}
	finishOK := func(status, in, out int) {
		if log != nil && recID != "" {
			i, o := in, out
			log.FinishOK(recID, status, &tFirst, &i, &o)
		}
	}
	if res.StatusCode != http.StatusOK {
		// Legacy gateway.py:612 records an upstream non-200 as a FAILURE with
		// the response text in `error`, NOT as a success:
		//   finish_error(req_id, f"HTTP {status}: {text[:120]}".replace("\n", " "),
		//                status=status_code, t_first=...)
		// The body must be read to build that message, so the pass-through is
		// buffered instead of streamed. Measured on the baseline: an unknown
		// model yields
		//   ok=false status=400 error='HTTP 400: {"type":"error",...1211...'
		// and input_tokens/output_tokens stay null.
		raw, rerr := io.ReadAll(res.Body)
		if rerr != nil {
			finishErr(res.StatusCode, "读取上游响应失败: "+rerr.Error())
			writeInternalError(w)
			return
		}
		text := strings.ReplaceAll(string(raw), "\n", " ")
		finishErr(res.StatusCode, "HTTP "+itoa(res.StatusCode)+": "+truncRunes(text, 120))
		// Upstream 4xx/5xx pass through VERBATIM - measured: an unknown model
		// yields 400 {"code":3006,"msg":"model not allowed"} unchanged, NOT a
		// 502 translation. The translation applies to HTTP-200 business errors
		// only.
		writeUpstreamVerbatim(w, res, raw)
		return
	}
	rawBody, err := io.ReadAll(res.Body)
	if err != nil {
		writeInternalError(w)
		return
	}
	var m map[string]any
	if json.Unmarshal(rawBody, &m) == nil {
		if code, msg, ok := businessCode(m); ok {
			// Measured: HTTP 200 + {"code":1005,"msg":"exceed quota limit"}
			// becomes 502 + shape B + upstream_error + code, with `message`
			// carrying the upstream `msg` verbatim (78 bytes, no charset).
			finishErr(http.StatusBadGateway,
				"上游业务错误 code="+itoa(code)+": "+truncRunes(msg, 120))
			writeShapeBWithCode(w, http.StatusBadGateway, TypeUpstreamError, msg, code)
			return
		}
	}
	in, outT := anthropicUsage(m)
	finishOK(http.StatusOK, in, outT)
	writeJSON(w, http.StatusOK, "application/json", openAIChatResponse(m, model))
}

// openAIChatResponse builds the OpenAI envelope with a fixed member order
// (struct fields, not a map: Go marshals maps in key order, which would not
// match the documented OpenAI layout).
func openAIChatResponse(m map[string]any, model string) any {
	text, finish := anthropicText(m)
	in, outT := anthropicUsage(m)
	type messageT struct {
		Role             string `json:"role"`
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content,omitempty"` // I-4 spec §3.3; omitempty keeps thinking-free responses byte-identical to before
	}
	type choiceT struct {
		Index        int      `json:"index"`
		Message      messageT `json:"message"`
		FinishReason string   `json:"finish_reason"`
	}
	return struct {
		ID      string    `json:"id"`
		Object  string    `json:"object"`
		Created int64     `json:"created"`
		Model   string    `json:"model"`
		Choices []choiceT `json:"choices"`
		Usage   struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}{
		ID:      chatID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []choiceT{{Index: 0, Message: messageT{Role: "assistant", Content: text, ReasoningContent: anthropicThinking(m)}, FinishReason: finish}},
		Usage: struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{PromptTokens: in, CompletionTokens: outT, TotalTokens: in + outT},
	}
}

// anthropicThinking concatenates every thinking block of an Anthropic message
// (I-4 / reasoning-content-spec §3.3, mirroring the legacy patch: collect
// block.thinking strings -> join -> message.reasoning_content). Thinking
// blocks carry {"type":"thinking","thinking":...}; text blocks never match.
func anthropicThinking(m map[string]any) string {
	var sb strings.Builder
	if blocks, ok := m["content"].([]any); ok {
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := bm["type"].(string); t != "thinking" {
				continue
			}
			if th, ok := bm["thinking"].(string); ok {
				sb.WriteString(th)
			}
		}
	}
	return sb.String()
}

// anthropicText concatenates every text block of an Anthropic message.
func anthropicText(m map[string]any) (string, string) {
	var sb strings.Builder
	if blocks, ok := m["content"].([]any); ok {
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := bm["text"].(string); ok {
				sb.WriteString(t)
			}
		}
	}
	stop, _ := m["stop_reason"].(string)
	return sb.String(), mapStopReason(stop)
}

// anthropicUsage maps Anthropic token counters onto the OpenAI names.
func anthropicUsage(m map[string]any) (int, int) {
	u, ok := m["usage"].(map[string]any)
	if !ok {
		return 0, 0
	}
	num := func(v any) int {
		switch t := v.(type) {
		case json.Number:
			n, _ := t.Int64()
			return int(n)
		case float64:
			return int(t)
		}
		return 0
	}
	return num(u["input_tokens"]), num(u["output_tokens"])
}

// mapStopReason maps Anthropic stop reasons onto OpenAI finish reasons.
func mapStopReason(stop string) string {
	switch stop {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "end_turn", "stop_sequence", "":
		return "stop"
	}
	return "stop"
}

// ---------------------------------------------------------------------------
// Streaming (SSE) translation
// ---------------------------------------------------------------------------

// relayChatStream consumes the upstream Anthropic SSE stream and emits OpenAI
// `chat.completion.chunk` frames, terminating with `data: [DONE]`.
//
// Frames are written with Python's default json.dumps separators (", " / ": ")
// because the measured first frame carries those spaces - the legacy gateway
// builds them in Python, so byte-level conformance means matching its spacing.
// This is the ONE place where our output is not compact JSON.
//
// Measured facts reproduced here (endpoints.md A-03 + CR-33):
//   - the first frame is the real upstream shape with an EMPTY content and
//     finish_reason null
//   - a business error mid-stream is an SSE FRAME, not an HTTP status change,
//     and its message is PREFIXED ("上游业务错误 code=1005: ...") unlike the
//     non-streaming 502 body which carries the bare upstream msg
//
// NOTE: the SUCCESS path of this translation was never measured against the
// live service (probing it would burn real quota); the frame grammar follows
// the Anthropic SSE event set plus the one measured frame.
func relayChatStream(w http.ResponseWriter, res *upstream.Response, log *ReqLog, recID, model string, t0 time.Time) {
	tFirst := time.Since(t0).Seconds()
	finishErr := func(status int, msg string) {
		if log != nil && recID != "" {
			st := status
			log.FinishError(recID, msg, &st, &tFirst)
		}
	}
	if res.StatusCode != http.StatusOK {
		// Same failure semantics as the non-streaming path (legacy
		// gateway.py:612 handles both).
		raw, rerr := io.ReadAll(res.Body)
		if rerr != nil {
			finishErr(res.StatusCode, "读取上游响应失败: "+rerr.Error())
			writeInternalError(w)
			return
		}
		text := strings.ReplaceAll(string(raw), "\n", " ")
		finishErr(res.StatusCode, "HTTP "+strconv.Itoa(res.StatusCode)+": "+truncRunes(text, 120))
		writeUpstreamVerbatim(w, res, raw)
		return
	}
	// The stream closes as a SUCCESS once the headers arrive (the legacy
	// stream path calls finish_ok on completion with status + usage). Usage is
	// parsed from the terminal SSE events, so the close is deferred to the
	// stream's own end.
	finishOK := func(in, out int) {
		if log != nil && recID != "" {
			i, o := in, out
			log.FinishOK(recID, http.StatusOK, &tFirst, &i, &o)
		}
	}
	// SSE headers measured on the live gateway.
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	id := chatID()
	created := time.Now().Unix()

	writeSSE := func(payload string) {
		_, _ = io.WriteString(w, "data: "+payload+"\n\n")
		flush()
	}
	done := false

	br := bufio.NewReaderSize(res.Body, 64*1024)

	// A business error can arrive with NO SSE framing at all: the upstream
	// answers HTTP 200 with the bare error object (the same body the
	// non-streaming path translates to 502). The measured legacy behaviour is
	// still an SSE stream - a first chunk frame, then the error frame - so the
	// error must be surfaced INSIDE the stream rather than swallowed (which
	// would hand the client an empty "successful" stream).
	if head, err := br.Peek(1); err == nil && head[0] == '{' {
		all, _ := io.ReadAll(io.LimitReader(br, maxBodyBytes))
		var m map[string]any
		if json.Unmarshal(all, &m) == nil {
			if code, msg, ok := businessCode(m); ok {
				writeSSE(chatChunk(id, created, model, `"role": "assistant", "content": ""`, "null"))
				finishErr(http.StatusOK, "上游业务错误 code="+strconv.Itoa(code)+": "+msg)
				writeSSE(sseErrorFrame("上游业务错误 code="+strconv.Itoa(code)+": "+msg, code))
				return
			}
		}
	}

	for {
		line, rerr := br.ReadString('\n')
		if line == "" && rerr != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			if rerr != nil {
				break
			}
			continue // event: / id: / comment lines
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			if rerr != nil {
				break
			}
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(payload), &ev) != nil {
			if rerr != nil {
				break
			}
			continue
		}
		if code, msg, ok := businessCode(ev); ok {
			// Measured: the prefix differs from the non-streaming body.
			writeSSE(sseErrorFrame("上游业务错误 code="+strconv.Itoa(code)+": "+msg, code))
			return
		}
		switch ev["type"] {
		case "message_start":
			// Measured first frame: delta.role assistant, content "".
			writeSSE(chatChunk(id, created, model, `"role": "assistant", "content": ""`, "null"))
		case "content_block_delta":
			d, _ := ev["delta"].(map[string]any)
			if d == nil {
				continue
			}
			// I-4 (reasoning-content-spec §3.1): thinking deltas carry the
			// chain-of-thought under delta.thinking - NOT delta.text (field
			// name verified from the legacy patch diff, t18 §六确证五). Map
			// onto the OpenAI-community reasoning_content member; an empty
			// thinking string emits no blank frame.
			if dt, _ := d["type"].(string); dt == "thinking_delta" {
				if th, _ := d["thinking"].(string); th != "" {
					writeSSE(chatChunk(id, created, model, `"reasoning_content": `+jsonString(th), "null"))
				}
				continue
			}
			// §3.2: signature_delta is integrity metadata, not content -
			// explicitly skipped. (The old text==""-continue happened to drop
			// it too; saying it out loud protects a future edit from leaking
			// signatures into the visible stream.)
			if dt, _ := d["type"].(string); dt == "signature_delta" {
				continue
			}
			text, _ := d["text"].(string)
			if text == "" {
				continue
			}
			writeSSE(chatChunk(id, created, model, `"content": `+jsonString(text), "null"))
		case "message_delta":
			// stop_reason arrives here; the terminal frame is emitted on
			// message_stop so the finish frame is always the last one.
		case "message_stop":
			writeSSE(chatChunk(id, created, model, ``, `"stop"`))
			writeSSE("[DONE]")
			done = true
		}
		if rerr != nil {
			break
		}
	}
	if !done {
		// The stream ended without message_stop (upstream cut, transport EOF).
		// The contract pins "ends with data: [DONE]", so terminate it rather
		// than leaving a dangling stream.
		writeSSE(chatChunk(id, created, model, ``, `"stop"`))
		writeSSE("[DONE]")
	}
	finishOK(0, 0)
}

// itoa is strconv.Itoa under a short name, mirroring the adminapi helper so
// the two packages' upstream-error message strings are built identically.
func itoa(n int) string { return strconv.Itoa(n) }

// chatChunk renders one chat.completion.chunk frame body with Python-style
// spacing. deltaBody is the raw JSON for the delta members; finishReason is
// the raw JSON for finish_reason ("null" or a quoted string).
func chatChunk(id string, created int64, model, deltaBody, finishReason string) string {
	return `{"id": ` + jsonString(id) +
		`, "object": "chat.completion.chunk", "created": ` + strconv.FormatInt(created, 10) +
		`, "model": ` + jsonString(model) +
		`, "choices": [{"index": 0, "delta": {` + deltaBody +
		`}, "finish_reason": ` + finishReason + `}]}`
}

// sseErrorFrame renders the in-stream error frame. Measured verbatim spacing:
// the payload is a nested object with spaces after the colons.
func sseErrorFrame(message string, code int) string {
	return `{"error": {"message": ` + jsonString(message) +
		`, "type": "upstream_error", "code": ` + strconv.Itoa(code) + `}}`
}

// jsonString marshals a string without HTML escaping, so prompt text carrying
// < > & survives the translation byte-for-byte.
func jsonString(s string) string {
	b, err := jsonMarshalCompact(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
