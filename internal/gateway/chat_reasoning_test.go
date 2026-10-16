package gateway

import (
	"net/http"
	"strings"
	"testing"

	"zhipucode2api/internal/models"
)

// I-4 regression suite (reasoning-content-spec.md; the gap t18 found as the
// "triple zero" blind spot - contract silent, implementation dropped
// thinking deltas on text==""-continue, conformance had no SSE-content case).
// All mocks: fake upstream responses only, ZERO real traffic (quota).

func sse(lines ...string) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString("data: ")
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func streamBody(t *testing.T, sseData string) string {
	t.Helper()
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	client := &fakeClient{res: jsonResponse(200, "text/event-stream", sseData)}
	g := newTestGateway(t, client, acct, "")
	w := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-5.3-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d want 200, body=%s", w.Code, w.Body.String())
	}
	return strings.TrimRight(w.Body.String(), "\n")
}

// §3.1 core: thinking_delta carries its payload in delta.thinking (NOT .text)
// and must surface as delta.reasoning_content.
func TestReasoningStreamThinkingMapped(t *testing.T) {
	body := streamBody(t, sse(
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"abc"}}`,
		`{"type":"message_stop"}`,
	))
	if !strings.Contains(body, `"reasoning_content": "abc"`) {
		t.Errorf("thinking delta not mapped to reasoning_content; body=%q", body)
	}
	// must NOT also leak as content
	if strings.Contains(body, `"content": "abc"`) {
		t.Errorf("thinking leaked into content member: %q", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]") {
		t.Errorf("stream must still terminate [DONE]: %q", tail(body, 80))
	}
}

// Regression guard (acceptance 7): text deltas keep the OLD behavior.
func TestReasoningStreamTextUnchanged(t *testing.T) {
	body := streamBody(t, sse(
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"xyz"}}`,
		`{"type":"message_stop"}`,
	))
	if !strings.Contains(body, `"content": "xyz"`) {
		t.Errorf("text delta regressed: %q", body)
	}
	if strings.Contains(body, "reasoning_content") {
		t.Errorf("pure-text stream must not grow reasoning_content: %q", body)
	}
}

// §3.2: signature_delta is silently skipped - no frame, no error, stream
// completes. (Explicit branch now; previously it only "worked" because its
// text field was empty.)
func TestReasoningStreamSignatureSkipped(t *testing.T) {
	body := streamBody(t, sse(
		`{"type":"content_block_delta","delta":{"type":"signature_delta","signature":"SECRET-SIG-abc123"}}`,
		`{"type":"message_stop"}`,
	))
	if strings.Contains(body, "SECRET-SIG") || strings.Contains(body, "signature") {
		t.Errorf("signature leaked into stream: %q", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]") {
		t.Errorf("signature-only stream must still complete: %q", body)
	}
}

// Ordering: thinking/text/thinking each form their own frames in input order.
func TestReasoningStreamMixedOrder(t *testing.T) {
	body := streamBody(t, sse(
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"T1"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"X1"}}`,
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"T2"}}`,
		`{"type":"message_stop"}`,
	))
	iT1 := strings.Index(body, `"reasoning_content": "T1"`)
	iX1 := strings.Index(body, `"content": "X1"`)
	iT2 := strings.Index(body, `"reasoning_content": "T2"`)
	if !(iT1 >= 0 && iT1 < iX1 && iX1 < iT2) {
		t.Errorf("frame order broken: T1@%d X1@%d T2@%d body=%q", iT1, iX1, iT2, body)
	}
}

// Empty thinking must not emit a blank frame (mirror of the text=="" rule).
func TestReasoningStreamEmptyThinking(t *testing.T) {
	body := streamBody(t, sse(
		`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":""}}`,
		`{"type":"message_stop"}`,
	))
	if strings.Contains(body, "reasoning_content") {
		t.Errorf("empty thinking produced a frame: %q", body)
	}
}

// ---- non-streaming (§3.3) ---------------------------------------------------

func chatNonStream(t *testing.T, msg string) string {
	t.Helper()
	acct := []models.Account{accountJSON(t, "a-12345678", "zai", "jwt", "active")}
	client := &fakeClient{res: jsonResponse(200, "application/json", msg)}
	g := newTestGateway(t, client, acct, "")
	w := do(t, g, http.MethodPost, "/v1/chat/completions",
		`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestReasoningNonStreamJoin(t *testing.T) {
	body := chatNonStream(t, `{"type":"message","role":"assistant",
		"content":[{"type":"thinking","thinking":"t1","signature":"s1"},
			{"type":"thinking","thinking":"t2"},
			{"type":"text","text":"answer"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	if !strings.Contains(body, `"reasoning_content":"t1t2"`) {
		t.Errorf("thinking blocks not joined into reasoning_content: %q", body)
	}
	if !strings.Contains(body, `"content":"answer"`) {
		t.Errorf("text content broken: %q", body)
	}
	// signature field must not ride along anywhere
	if strings.Contains(body, "s1") {
		t.Errorf("signature leaked: %q", body)
	}
}

// Byte-level regression: thinking-free upstream responses serialize exactly
// as before (omitempty - no new null/empty member).
func TestReasoningNonStreamAbsent(t *testing.T) {
	body := chatNonStream(t, `{"type":"message","role":"assistant",
		"content":[{"type":"text","text":"plain"}],
		"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	if strings.Contains(body, "reasoning_content") {
		t.Errorf("reasoning_content appeared without thinking blocks: %q", body)
	}
	if !strings.Contains(body, `"content":"plain"`) {
		t.Errorf("plain response regressed: %q", body)
	}
}
