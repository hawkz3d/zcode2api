package adminapi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// pyjsonCase mirrors one entry of testdata/pyjson_cases.json. Bodies are stored
// as code point arrays so lone surrogates and raw control characters survive
// the fixture round trip; pos/msg are null when CPython's json.loads ACCEPTS
// the body.
type pyjsonCase struct {
	CP  []int   `json:"cp"`
	Pos *int    `json:"pos"`
	Msg *string `json:"msg"`
}

type pyjsonFixture struct {
	Cases []pyjsonCase `json:"cases"`
}

func loadPyjsonFixture(t *testing.T) pyjsonFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/pyjson_cases.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f pyjsonFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("fixture is empty")
	}
	return f
}

func cpString(cp []int) string {
	var b strings.Builder
	for _, c := range cp {
		b.WriteRune(rune(c))
	}
	return b.String()
}

// TestPyJSONErrorMatchesCPython is the fidelity gate for pyjson.go: every case
// was labelled by json.loads in the SAME interpreter that serves the baseline,
// so a mismatch is a real divergence from the frozen contract's error shape.
//
// A case where the oracle ACCEPTS must be accepted here too — that is what
// keeps bare NaN/Infinity (which encoding/json rejects) from turning into a
// spurious 422.
func TestPyJSONErrorMatchesCPython(t *testing.T) {
	f := loadPyjsonFixture(t)
	var errs, accepted int
	for _, c := range f.Cases {
		body := cpString(c.CP)
		_, pos, msg, ok := pyNormalizeJSON([]byte(body))
		if c.Pos == nil {
			accepted++
			if !ok {
				t.Errorf("body %q: oracle accepts, scanner rejected with pos=%d msg=%q", body, pos, msg)
			}
			continue
		}
		errs++
		if ok {
			t.Errorf("body %q: oracle errors (pos=%d msg=%q), scanner accepted", body, *c.Pos, *c.Msg)
			continue
		}
		if pos != *c.Pos || msg != *c.Msg {
			t.Errorf("body %q: got (pos=%d msg=%q), want (pos=%d msg=%q)", body, pos, msg, *c.Pos, *c.Msg)
		}
	}
	t.Logf("pyjson oracle: %d cases (%d errors, %d accepted) all matched", len(f.Cases), errs, accepted)
}

// TestPyNormalizeKeepsEncodingJSONHappy pins the second half of the contract:
// whatever the scanner accepts must also survive encoding/json, otherwise the
// handler would report a 422 for a body the legacy service processed.
func TestPyNormalizeKeepsEncodingJSONHappy(t *testing.T) {
	f := loadPyjsonFixture(t)
	for _, c := range f.Cases {
		if c.Pos != nil {
			continue
		}
		body := cpString(c.CP)
		out, _, msg, ok := pyNormalizeJSON([]byte(body))
		if !ok {
			t.Fatalf("body %q: scanner rejected (%s)", body, msg)
		}
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Errorf("body %q: normalized to %q which encoding/json still rejects: %v", body, out, err)
		}
	}
}

// TestPyNormalizeRewritesBareConstants covers the three tokens Python accepts
// and Go does not. Each must become a null so the decode path continues.
func TestPyNormalizeRewritesBareConstants(t *testing.T) {
	cases := []struct{ in, want string }{
		{`NaN`, `null`},
		{`Infinity`, `null`},
		{`-Infinity`, `null`},
		{`{"a":NaN}`, `{"a":null}`},
		{`{"a":-Infinity}`, `{"a":null}`},
		{`[1,Infinity,2]`, `[1,null,2]`},
		{`{"a":"NaN"}`, `{"a":"NaN"}`}, // inside a string: untouched
		{`{"a":1}`, `{"a":1}`},         // no constants: byte-identical
		{`{"NaNx":1}`, `{"NaNx":1}`},   // key text: untouched
	}
	for _, c := range cases {
		out, pos, msg, ok := pyNormalizeJSON([]byte(c.in))
		if !ok {
			t.Errorf("%q: rejected (pos=%d msg=%q)", c.in, pos, msg)
			continue
		}
		if string(out) != c.want {
			t.Errorf("%q: normalized to %q, want %q", c.in, out, c.want)
		}
	}

	// A bare constant is a TOKEN, so `Infinity` followed by junk is not one:
	// the scanner consumes the token, then the enclosing object rejects the
	// stray character. json.loads in the baseline venv agrees (pos 13).
	_, pos, msg, ok := pyNormalizeJSON([]byte(`{"a":InfinityX}`))
	if ok {
		t.Error(`{"a":InfinityX}: accepted, want rejected`)
	} else if pos != 13 || msg != pyMsgExpectingComma {
		t.Errorf(`{"a":InfinityX}: got (pos=%d msg=%q), want (pos=13 msg=%q)`, pos, msg, pyMsgExpectingComma)
	}
}

// TestPyJSONErrorMeasuredTable pins the exact pairs measured against the live
// baseline (127.0.0.1:9120, PUT /admin/api/settings). These are the values the
// 对拍 harness compares, so they must never drift.
func TestPyJSONErrorMeasuredTable(t *testing.T) {
	measured := []struct {
		body string
		pos  int
		msg  string
	}{
		{`{`, 1, "Expecting property name enclosed in double quotes"},
		{`[`, 1, "Expecting value"},
		{`not json`, 0, "Expecting value"},
		{`"unterminated`, 0, "Unterminated string starting at"},
		{`{,}`, 1, "Expecting property name enclosed in double quotes"},
		{`{"a"}`, 4, "Expecting ':' delimiter"},
		{`{"a":}`, 5, "Expecting value"},
		{`{"a":1,}`, 7, "Expecting property name enclosed in double quotes"},
		{`{"a":1`, 6, "Expecting ',' delimiter"},
		{`{"a" 1}`, 5, "Expecting ':' delimiter"},
		{`{"a":1} extra`, 8, "Extra data"},
		{`tru`, 0, "Expecting value"},
		{`nul`, 0, "Expecting value"},
		{`fals`, 0, "Expecting value"},
		{`falsee`, 5, "Extra data"},
		{`truefalse`, 4, "Extra data"},
		{`nullnull`, 4, "Extra data"},
		{`1.2.3`, 3, "Extra data"},
		{`1.`, 1, "Extra data"},
		{`01`, 1, "Extra data"},
		{`1e`, 1, "Extra data"},
		{`+1`, 0, "Expecting value"},
		{`.5`, 0, "Expecting value"},
		{`-`, 0, "Expecting value"},
		{`{"a":01}`, 6, "Expecting ',' delimiter"},
		{`{"a":"x" "b":1}`, 9, "Expecting ',' delimiter"},
		{`{}{}`, 2, "Extra data"},
		{`}`, 0, "Expecting value"},
		{`]`, 0, "Expecting value"},
		{"   {", 4, "Expecting property name enclosed in double quotes"},
		{"\n{", 2, "Expecting property name enclosed in double quotes"},
		{"   ", 3, "Expecting value"},
		{"\n\t ", 3, "Expecting value"},
		{`{"a":"\q"}`, 6, "Invalid \\escape"},
		{`{"a":"\x"}`, 6, "Invalid \\escape"},
		{`{"a":"\u12"}`, 7, "Invalid \\uXXXX escape"},
		{`{"a":"\uZZZZ"}`, 7, "Invalid \\uXXXX escape"},
		{`{"a":"x`, 5, "Unterminated string starting at"},
		{"{\"a\":\"x\ny\"}", 7, "Invalid control character at"},
		{"{\"a\":\"x\ty\"}", 7, "Invalid control character at"},
		{`{,`, 1, "Expecting property name enclosed in double quotes"},
		{`{"a":1,,}`, 7, "Expecting property name enclosed in double quotes"},
		{`[1,]`, 3, "Expecting value"},
		{`[1 2]`, 3, "Expecting ',' delimiter"},
		{`[1`, 2, "Expecting ',' delimiter"},
		{`[,]`, 1, "Expecting value"},
	}
	for _, c := range measured {
		_, pos, msg, ok := pyNormalizeJSON([]byte(c.body))
		if ok {
			t.Errorf("body %q: accepted, want pos=%d msg=%q", c.body, c.pos, c.msg)
			continue
		}
		if pos != c.pos || msg != c.msg {
			t.Errorf("body %q: got (pos=%d msg=%q), want (pos=%d msg=%q)", c.body, pos, msg, c.pos, c.msg)
		}
	}
}

// TestPyNormalizeOffsetsAreCodePoints guards the offset UNIT: Python counts
// code points, so a multi-byte character before the error must not shift the
// reported position the way a byte offset would.
func TestPyNormalizeOffsetsAreCodePoints(t *testing.T) {
	// {"中":"值" 1} — the object expects ':' or ',' after the value; in code
	// points the reported position is 9, where a byte offset would say 15.
	body := `{"中":"值" 1}`
	_, pos, msg, ok := pyNormalizeJSON([]byte(body))
	if ok {
		t.Fatalf("body %q: accepted, want an error", body)
	}
	want := 9 // code points, not the 15 bytes Go's SyntaxError.Offset reports
	if pos != want || msg != pyMsgExpectingComma {
		t.Errorf("body %q: got (pos=%d msg=%q), want (pos=%d msg=%q)", body, pos, msg, want, pyMsgExpectingComma)
	}
}
