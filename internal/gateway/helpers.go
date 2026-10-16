package gateway

import (
	"bytes"
	"encoding/json"
)

// jsonMarshalCompact marshals like the legacy Python json.dumps does with
// ensure_ascii=False: compact separators and NO HTML escaping of < > &
// (Go's json.Marshal escapes them by default, which would mutate user
// prompt text on the way upstream).
func jsonMarshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
