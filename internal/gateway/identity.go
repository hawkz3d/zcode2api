package gateway

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

// zcodeSystemJSON is the official ZCode client system identity block array.
//
// PROVENANCE: upstream PROTOCOL DATA, not third-party implementation code.
// The block text is the content-review passphrase the Zhipu ZCode server
// itself demands; it has no alternative implementation - every client must
// send the same text or the request is rejected. Functionally it is a
// protocol constant, like a User-Agent string.
//
// Measured single-variable experiments on the zai-jwt plan channel:
//
//	original body, NO identity block -> 405 / 3012
//	identity block added             -> 200 OK
//	only metadata.user_id            -> 405
//	only cache_control               -> 405
//	any header variation             -> 405
//
// so the identity block is the ONLY switch: without it the zai-jwt /
// start-plan channel is entirely unusable.
//
// ADR-007 (revised by the user, 2026-10-15) therefore rules this asset
// IN SCOPE and KEEPS it: "is it someone else's implementation, or is it the
// upstream's protocol data?" - only the former is tainted by provenance.
// Byte-identical to zcode2api/app/zcode_system.json, recorded here for
// traceability rather than because the copy is suspect:
//   sha256 80884b190ab0db0e13a859b17d9c23413f276614ca74793a56fa0e9a5cbff721
//
//go:embed zcode_system.json
var zcodeSystemJSON []byte

// identityBlocks are the system blocks with their cache_control markers,
// parsed once at process start.
var identityBlocks = func() []map[string]any {
	var blocks []map[string]any
	if err := json.Unmarshal(zcodeSystemJSON, &blocks); err != nil {
		panic(fmt.Sprintf("gateway: bad embedded zcode_system.json: %v", err))
	}
	return blocks
}()

// mergeIdentitySystem builds the Anthropic "system" array for the zcode-plan
// channel: identity blocks FIRST, caller-supplied system texts AFTER
// (black-box contract: "system 消息映射为 Anthropic system 数组的末位元素
// （官方身份块之后）"). clientSystem may be nil, a string or an array of
// blocks; every existing member is preserved.
func mergeIdentitySystem(clientSystem any) []map[string]any {
	out := make([]map[string]any, 0, len(identityBlocks)+4)
	out = append(out, identityBlocks...)
	switch s := clientSystem.(type) {
	case nil:
	case string:
		if s != "" {
			out = append(out, map[string]any{"type": "text", "text": s})
		}
	case []any:
		for _, item := range s {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// decodeJSONBody unmarshals a JSON object body. Strict duplicate-key and
// trailing-garbage handling matches encoding/json defaults, which is fine:
// the contract only pins "not valid JSON" -> 400 (endpoints.md A-02/A-03).
func decodeJSONObject(body []byte) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	// Trailing content after the first JSON value still counts as invalid
	// for our purposes (curl-sent bodies never have it).
	if dec.More() {
		return nil, fmt.Errorf("trailing JSON content")
	}
	if m == nil {
		return nil, fmt.Errorf("body is not an object")
	}
	return m, nil
}
