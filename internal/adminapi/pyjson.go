package adminapi

// This file reproduces CPython's json.JSONDecodeError for a request body.
//
// The legacy service decodes bodies with FastAPI, i.e. CPython's json.loads —
// and CPython ships the C accelerator (_json), whose reporting differs from the
// pure-Python decoder in ways that matter for a byte-exact 对拍:
//
//   - `Invalid \escape`, `Invalid \uXXXX escape` and `Invalid control
//     character at` carry NO offending character in the accelerator, while the
//     pure-Python decoder appends `: 'q'`.
//   - JSONDecodeError.pos is a 0-based CODE POINT offset into the body — not a
//     byte offset, and not Go's SyntaxError.Offset (1-based, and a different
//     place for the same input: measured 5/29 agreement).
//
// So the scanner walks the body as RUNES and mirrors the accelerator's state
// machine. pyNormalizeJSON additionally rewrites the three bare constants
// Python's json.loads accepts but encoding/json rejects (NaN, Infinity,
// -Infinity) to `null`, so the Go decoder accepts exactly the language Python
// accepts.
//
// Fidelity is verified against json.loads in the SAME interpreter that serves
// the baseline: see pyjson_test.go + testdata/pyjson_cases.json.
//
// KNOWN residual divergences (disclosed, not exercised by any conformance
// case): a body that is valid UTF-8 is required (Go maps invalid bytes to
// U+FFFD, shifting code point offsets), and re-serialising a NaN-bearing value
// into a 422 `input` field cannot match, since FastAPI renders it with
// allow_nan=False while Go emits `null`.

const (
	pyMsgExpectingValue = "Expecting value"
	pyMsgExpectingProp  = "Expecting property name enclosed in double quotes"
	pyMsgExpectingColon = "Expecting ':' delimiter"
	pyMsgExpectingComma = "Expecting ',' delimiter"
	pyMsgExtraData      = "Extra data"
	pyMsgUnterminated   = "Unterminated string starting at"
	pyMsgInvalidEscape  = "Invalid \\escape"
	pyMsgInvalidUXXXX   = "Invalid \\uXXXX escape"
	pyMsgInvalidControl = "Invalid control character at"
)

// pyScanError is a JSONDecodeError: the 0-based code point offset and the
// accelerator's message.
type pyScanError struct {
	pos int
	msg string
}

// pyConst records a bare NaN/Infinity token so pyNormalizeJSON can rewrite it.
type pyConst struct {
	start int
	size  int
}

type pyScanner struct {
	src    []rune
	consts []pyConst
}

// pyNormalizeJSON validates raw as CPython's json.loads would and returns the
// body encoding/json can also decode (bare NaN/Infinity/-Infinity -> null).
//
// On failure it reports JSONDecodeError.pos and .msg verbatim.
func pyNormalizeJSON(raw []byte) (out []byte, pos int, msg string, ok bool) {
	sc := &pyScanner{src: []rune(string(raw))}
	if e := sc.parse(); e != nil {
		return nil, e.pos, e.msg, false
	}
	if len(sc.consts) == 0 {
		return raw, 0, "", true
	}
	repl := make(map[int]int, len(sc.consts))
	for _, c := range sc.consts {
		repl[c.start] = c.size
	}
	buf := make([]rune, 0, len(sc.src))
	for i := 0; i < len(sc.src); {
		if size, hit := repl[i]; hit {
			buf = append(buf, 'n', 'u', 'l', 'l')
			i += size
			continue
		}
		buf = append(buf, sc.src[i])
		i++
	}
	return []byte(string(buf)), 0, "", true
}

// parse is JSONDecoder.raw_decode plus the top-level `Extra data` check.
func (p *pyScanner) parse() *pyScanError {
	end, err := p.scanOnce(p.skipWS(0))
	if err != nil {
		return err
	}
	if end = p.skipWS(end); end != len(p.src) {
		return &pyScanError{pos: end, msg: pyMsgExtraData}
	}
	return nil
}

// skipWS is WHITESPACE.match(s, i).end(); it never moves past len(s).
func (p *pyScanner) skipWS(i int) int {
	n := len(p.src)
	if i > n {
		i = n
	}
	for i < n {
		if !isPyWS(p.src[i]) {
			break
		}
		i++
	}
	return i
}

// runeAt is s[i:i+1] — the empty slice outside the string, which Python's
// substring tests treat as a non-match for every character class here.
func (p *pyScanner) runeAt(i int) rune {
	if i < 0 || i >= len(p.src) {
		return 0
	}
	return p.src[i]
}

// lit reports whether s[i:] starts with the literal word.
func (p *pyScanner) lit(i int, word string) bool {
	if i+len(word) > len(p.src) {
		return false
	}
	for k, r := range []rune(word) {
		if p.src[i+k] != r {
			return false
		}
	}
	return true
}

func (p *pyScanner) err(pos int, msg string) *pyScanError {
	return &pyScanError{pos: pos, msg: msg}
}

// scanOnce mirrors the accelerator's _scan_once: the dispatch ORDER matters,
// because it decides whether a failing input is a value error or a literal
// that fell through to StopIteration.
func (p *pyScanner) scanOnce(idx int) (int, *pyScanError) {
	if idx >= len(p.src) {
		return 0, p.err(idx, pyMsgExpectingValue)
	}
	switch p.src[idx] {
	case '"':
		return p.scanString(idx + 1)
	case '{':
		return p.scanObject(idx + 1)
	case '[':
		return p.scanArray(idx + 1)
	}
	if p.lit(idx, "null") {
		return idx + 4, nil
	}
	if p.lit(idx, "true") {
		return idx + 4, nil
	}
	if p.lit(idx, "false") {
		return idx + 5, nil
	}
	if end := p.matchNumber(idx); end > idx {
		return end, nil
	}
	if p.lit(idx, "NaN") {
		p.consts = append(p.consts, pyConst{idx, 3})
		return idx + 3, nil
	}
	if p.lit(idx, "Infinity") {
		p.consts = append(p.consts, pyConst{idx, 8})
		return idx + 8, nil
	}
	if p.lit(idx, "-Infinity") {
		p.consts = append(p.consts, pyConst{idx, 9})
		return idx + 9, nil
	}
	return 0, p.err(idx, pyMsgExpectingValue)
}

// matchNumber is NUMBER_RE = (-?(?:0|[1-9]\d*))(\.\d+)?([eE][-+]?\d+)?.
// It returns the end of the longest match, or -1 when nothing matches; a
// partial match (e.g. `01` -> `0`) is what makes Python report `Extra data`.
func (p *pyScanner) matchNumber(idx int) int {
	n := len(p.src)
	i := idx
	if i < n && p.src[i] == '-' {
		i++
	}
	switch {
	case i < n && p.src[i] == '0':
		i++
	case i < n && p.src[i] >= '1' && p.src[i] <= '9':
		for i < n && isDigitRune(p.src[i]) {
			i++
		}
	default:
		return -1
	}
	if i+1 < n && p.src[i] == '.' && isDigitRune(p.src[i+1]) {
		i++
		for i < n && isDigitRune(p.src[i]) {
			i++
		}
	}
	if i < n && (p.src[i] == 'e' || p.src[i] == 'E') {
		j := i + 1
		if j < n && (p.src[j] == '+' || p.src[j] == '-') {
			j++
		}
		if j < n && isDigitRune(p.src[j]) {
			for j < n && isDigitRune(p.src[j]) {
				j++
			}
			i = j
		}
	}
	return i
}

// scanString is py_scanstring. `end` is the index just past the opening quote;
// the reported position for an unterminated string is that quote.
func (p *pyScanner) scanString(end int) (int, *pyScanError) {
	n := len(p.src)
	begin := end - 1
	i := end
	for i < n {
		c := p.src[i]
		switch {
		case c == '"':
			return i + 1, nil
		case c == '\\':
			// Measured against the live baseline, and reproduced by json.loads
			// in the same interpreter: the accelerator reports a bad simple
			// escape at the BACKSLASH but a bad \uXXXX escape at the `u`.
			// Compare {"a":"\q"} -> 6 with {"a":"\u12"} -> 7.
			e := i + 1
			if e >= n {
				return 0, p.err(begin, pyMsgUnterminated)
			}
			if p.src[e] == 'u' {
				uni, next, err := p.scanUXXXX(e)
				if err != nil {
					return 0, err
				}
				i = next
				// A high surrogate combines with an immediately following \u
				// escape. CPython tests `s[end:end+2] == '\\u'` on a SLICE, so
				// a truncated lookahead simply fails to match and the escape
				// is then handled by the normal path below — same position and
				// message, which is why no length guard is needed here.
				if uni >= 0xd800 && uni <= 0xdbff && p.runeAt(i) == '\\' && p.runeAt(i+1) == 'u' {
					uni2, _, err := p.scanUXXXX(i + 1)
					if err != nil {
						return 0, err
					}
					if uni2 >= 0xdc00 && uni2 <= 0xdfff {
						i += 6
					}
				}
				continue
			}
			if !isSimpleEscape(p.src[e]) {
				return 0, p.err(i, pyMsgInvalidEscape)
			}
			i = e + 1
		case c <= 0x1f:
			return 0, p.err(i, pyMsgInvalidControl)
		default:
			i++
		}
	}
	return 0, p.err(begin, pyMsgUnterminated)
}

// scanUXXXX validates the four hex digits that follow the `u` at index u. A
// SHORT escape is not "unterminated" in the accelerator: measured `"\u` (no
// digits at all) reports Invalid \uXXXX escape at the `u`, exactly like
// `"\uZZZZ"`. Both cases report the `u`, which is what the live baseline
// returns for `{"a":"\u12"}` (pos 7).
func (p *pyScanner) scanUXXXX(u int) (int, int, *pyScanError) {
	n := len(p.src)
	uni := 0
	for k := u + 1; k <= u+4; k++ {
		if k >= n {
			return 0, 0, p.err(u, pyMsgInvalidUXXXX)
		}
		d := hexVal(p.src[k])
		if d < 0 {
			return 0, 0, p.err(u, pyMsgInvalidUXXXX)
		}
		uni = uni<<4 | d
	}
	return uni, u + 5, nil
}

// scanObject is JSONObject. `end` is the index just past the `{`.
func (p *pyScanner) scanObject(end int) (int, *pyScanError) {
	if p.runeAt(end) != '"' {
		if isPyWS(p.runeAt(end)) {
			end = p.skipWS(end)
		}
		if p.runeAt(end) == '}' {
			return end + 1, nil
		}
		if p.runeAt(end) != '"' {
			return 0, p.err(end, pyMsgExpectingProp)
		}
	}
	end++
	for {
		key, err := p.scanString(end)
		if err != nil {
			return 0, err
		}
		end = key
		if p.runeAt(end) != ':' {
			end = p.skipWS(end)
			if p.runeAt(end) != ':' {
				return 0, p.err(end, pyMsgExpectingColon)
			}
		}
		end++
		// Python skips at most two whitespace characters before the value.
		if isPyWS(p.runeAt(end)) {
			end++
			if isPyWS(p.runeAt(end)) {
				end = p.skipWS(end + 1)
			}
		}
		val, err := p.scanOnce(end)
		if err != nil {
			return 0, err
		}
		end = val
		nc := p.runeAt(end)
		if isPyWS(nc) {
			end = p.skipWS(end + 1)
			nc = p.runeAt(end)
		}
		end++
		if nc == '}' {
			return end, nil
		}
		if nc != ',' {
			return 0, p.err(end-1, pyMsgExpectingComma)
		}
		end = p.skipWS(end)
		nc = p.runeAt(end)
		end++
		if nc != '"' {
			return 0, p.err(end-1, pyMsgExpectingProp)
		}
	}
}

// scanArray is JSONArray. `end` is the index just past the `[`.
func (p *pyScanner) scanArray(end int) (int, *pyScanError) {
	if isPyWS(p.runeAt(end)) {
		end = p.skipWS(end + 1)
	}
	if p.runeAt(end) == ']' {
		return end + 1, nil
	}
	for {
		val, err := p.scanOnce(end)
		if err != nil {
			return 0, err
		}
		end = val
		nc := p.runeAt(end)
		if isPyWS(nc) {
			end = p.skipWS(end + 1)
			nc = p.runeAt(end)
		}
		end++
		if nc == ']' {
			return end, nil
		}
		if nc != ',' {
			return 0, p.err(end-1, pyMsgExpectingComma)
		}
		if isPyWS(p.runeAt(end)) {
			end++
			if isPyWS(p.runeAt(end)) {
				end = p.skipWS(end + 1)
			}
		}
	}
}

func isPyWS(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func isDigitRune(r rune) bool { return r >= '0' && r <= '9' }

func isSimpleEscape(r rune) bool {
	switch r {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	}
	return false
}

func hexVal(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return -1
}
