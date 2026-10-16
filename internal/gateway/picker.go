package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"zhipucode2api/internal/models"
)

// errNoAccount surfaces as the 503 no_available_account shape.
var errNoAccount = errors.New("gateway: no selectable account")

// Channel enumerates the three upstream transports measured in
// 05-upstream-protocols.md + env-contract.md §3.
type Channel int

const (
	// ChannelPlan: zai JWT on the zcode.z.ai plan endpoint - Bearer auth,
	// full ZCode identity header set, official system blocks required
	// (the 405/3012 root cause - see zcode-system.json provenance note).
	ChannelPlan Channel = iota
	// ChannelZaiKey: zai apiKey on api.z.ai - x-api-key, no captcha, no
	// identity injection.
	ChannelZaiKey
	// ChannelBigModel: bigmodel on open.bigmodel.cn - x-api-key, plain
	// Anthropic-compatible endpoint.
	ChannelBigModel
)

// accountSel is a resolved account: channel, credential and request pieces.
type accountSel struct {
	Account   models.Account
	Channel   Channel
	URL       string
	AuthHdr   [2]string // header name/value pair (Authorization vs x-api-key)
	DeviceMid string    // from fingerprint when present
	// Mode is the monitoring `mode` value: the CREDENTIAL kind ("jwt" /
	// "apiKey"), measured on the live ring - NOT the channel name. The legacy
	// reqlog stores `mode` from the picked account's data, so it is read there
	// rather than re-derived from Channel.
	Mode string
	// Fingerprint extras for the plan identity headers (empty when absent).
	Platform, Arch, Language, Timezone, OSVersion string
}

// headers assembles the upstream header set per channel
// (05-upstream-protocols.md §3/§8 - the identity set the ZCode client
// mimics; "API Key 通道差异: x-api-key 替代 Bearer, 无验证码头").
// headers assembles the upstream header set. `verifyParam` is the Aliyun
// captcha token: EMPTY ON THE FIRST ATTEMPT by design (I-3 laziness) - it is
// populated only after a challenge, and only for the retry. The legacy
// spelling is lowercase (`x-aliyun-captcha-verify-param`), matching the
// B-16 rule that this header is forwarded verbatim and must not be rewritten.
func (s *accountSel) headers(opt Options) http.Header {
	return s.headersWithCaptcha(opt, "")
}

func (s *accountSel) headersWithCaptcha(opt Options, verifyParam string) http.Header {
	h := http.Header{}
	v := opt.AppVersion
	h.Set("content-type", "application/json")
	h.Set("anthropic-version", "2023-06-01")
	h.Set("user-agent", "ZCode/"+v)
	h.Set("x-api-key", "") // clear-then-set ordering below
	h.Del("x-api-key")
	h.Set(s.AuthHdr[0], s.AuthHdr[1])
	switch s.Channel {
	case ChannelPlan, ChannelZaiKey:
		// zcode-branded identity headers (05 §8; api.z.ai shares the client
		// identity set per §3 "API Key 通道差异" - only auth + captcha differ).
		h.Set("x-zcode-app-version", v)
		h.Set("x-zcode-agent", "glm")
		h.Set("x-title", "Z Code@zcode2api") // sourceTitle unverified - fixed tag
		h.Set("http-referer", "https://zcode.z.ai/")
		if s.DeviceMid != "" {
			h.Set("x-device-mid", s.DeviceMid)
		}
		if s.Platform != "" && s.Arch != "" {
			h.Set("x-platform", s.Platform+"-"+s.Arch)
			h.Set("x-os-category", s.Platform)
		}
		h.Set("x-release-channel", "production")
		if s.Language != "" {
			h.Set("x-client-language", s.Language)
		}
		if s.Timezone != "" {
			h.Set("x-client-timezone", s.Timezone)
		}
	case ChannelBigModel:
		// Standard Anthropic-compatible endpoint: auth + version suffice
		// (precedent: the bigmodel bridge ran on the bare template).
	}
	// I-3: present ONLY when solved. An empty value is never sent - the legacy
	// gate leaves the header off entirely on the first attempt, and "header
	// present but empty" is a different request shape upstream.
	if strings.TrimSpace(verifyParam) != "" {
		h.Set("x-aliyun-captcha-verify-param", verifyParam)
	}
	return h
}

// chanKind is the REQUEST-SIDE ledger criterion (I-9, t18 §六确证一).
// The legacy chansplit patch routes by the provider the request declares:
// wanted_mode = "jwt" if provider=="zai" else "apiKey", with the bigmodel
// search spanning ("zai","bigmodel") and include_exhausted for that leg.
// The provider comes from the x-provider REQUEST HEADER (memory
// 《ZCode双通道拆分落地实践》step ③: _detect_provider reads the header; DSH
// splits channels purely by settings headers - the measured subscription
// traffic used a bare "glm-4.5-flash" model name, so model naming is NOT
// the criterion). x-provider appears in no contract header table in/out;
// outbound headers are freshly built by (*accountSel).headers, so the
// routing header is consumed locally and never leaks upstream.
type chanKind int

const (
	// chanPlan is the DEFAULT (no x-provider header => zai per legacy
	// _detect_provider): JWT-credential accounts only. exhausted is NOT
	// exempt here - on this ledger exhausted really means no quota and
	// retrying wastes turns (legacy design note, memory step ④).
	chanPlan chanKind = iota
	// chanSub: reachable via x-provider: bigmodel header OR the "bigmodel/"
	// MODEL PREFIX (prefix wins detection, see detectChannel): ANY provider's
	// apiKey accounts (defect b - the OAuth-minted zai keys are usable here;
	// restricting by provider field would halve the pool) and exhausted
	// accounts pass (defect c - exhausted is decided by PAID-model balance
	// 1113; the free pool glm-4.5-flash measured 200 on exhausted keys, and
	// success flips the account back to active). invalid/disabled stay
	// excluded.
	chanSub
)

// ledgerPrefixBigmodel is the request-side subscription marker the legacy
// gateway strips before forwarding (captain probe 2026-10-16: upstream echo
// carried the bare name).
const ledgerPrefixBigmodel = "bigmodel/"

// detectChannel applies the ledger criterion with TWO request-side signals,
// prefix taking precedence:
//  1. model name starts with "bigmodel/" -> chanSub. Captain's live probe
//     2026-10-16 against the legacy service: "bigmodel/nonexistent-zzz"
//     reached the upstream (1211 Unknown Model = prefix stripped, forwarded),
//     bare "nonexistent-zzz" was locally rejected 3006 on the plan ledger,
//     and "bigmodel/glm-4.5-flash" answered 200 echoing "glm-4.5-flash" -
//     prefix consumed by the gateway.
//  2. x-provider header == bigmodel -> chanSub (the DSH settings-side
//     channel marker, memory step ③).
//
// Why the PREFIX WINS on contradiction (e.g. x-provider: zcode + a
// bigmodel/ model): the prefix is a two-part carrier - it simultaneously
// decides the ledger AND the strip - and honoring it makes the request
// SUCCEED on the subscription pool; honoring the header instead would pin
// the request to the plan ledger carrying a bigmodel-prefixed name that the
// upstream rejects anyway (3006), i.e. a guaranteed failure. Preferring the
// routable reading is closer to legacy intent. Contradiction combos were
// never black-box measured.
// 实现决定〔对拍未锚〕: precedence + case handling (exact lowercase prefix;
// case-insensitive header match, trim) are annotated choices.
func detectChannel(r *http.Request, model string) chanKind {
	if strings.HasPrefix(model, ledgerPrefixBigmodel) {
		return chanSub
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("x-provider")), "bigmodel") {
		return chanSub
	}
	return chanPlan
}

// pickAccount selects the next account FOR THIS REQUEST'S CHANNEL with a
// failover loop (t24 / I-9, superseding the t6 contract-minimal single-pick
// that returned 503 on the first resolve failure). model is the RAW request
// model string (prefix detection runs before any normalization/strip).
//
// Rotation: per-channel cursor (legacy shares one cursor per provider-tuple;
// our two fixed channels get two cursors). Candidate order = ListAccounts
// order, which is the frozen (provider, created_at) sequence - CR-35.
//
// 实现决定〔对拍未锚〕: the failover bound here is "one full pass over the
// channel's candidates". The legacy MAX_ACCOUNT_ATTEMPTS cap and the
// per-error-class retry policy (429 Retry-After, 1113, 3012 backoff) are
// upstream-side behavior the black-box never anchored - error-class-driven
// failover stays with the pool state-writes task (t6 SCOPE NOTE).
func (g *Gateway) pickAccount(r *http.Request, model string) (*accountSel, error) {
	accs, err := g.accounts.ListAccounts(r.Context())
	if err != nil {
		return nil, err
	}
	ch := detectChannel(r, model)
	now := float64(time.Now().Unix())
	var cands []models.Account
	for _, a := range accs {
		if candidateFor(a, now, ch) {
			cands = append(cands, a)
		}
	}
	n := len(cands)
	if n == 0 {
		return nil, errNoAccount
	}
	cur := &g.planCursor
	if ch == chanSub {
		cur = &g.subCursor
	}
	start := *cur % uint64(n)
	for i := 0; i < n; i++ {
		idx := (start + uint64(i)) % uint64(n)
		sel, rerr := g.resolve(&cands[idx])
		if rerr == nil {
			*cur = start + uint64(i) + 1 // next request resumes after the winner
			return sel, nil
		}
		// resolve failure = the candidate lacks a transport this credential
		// can serve (unknown provider passthrough A-1, mode casing A-2):
		// SKIP and keep trying (defect a). errNoAccount only after the full
		// pass, matching the legacy "exhausted candidates -> 503" shape.
	}
	return nil, errNoAccount
}

// candidateFor replaces the t6 candidateOf with channel-aware admission
// (account-model.md §3.1/§3.3 + the chansplit patch semantics):
// enabled gate, status rules, then the credential filter for the channel.
// The credential filter is deliberately lax about provider: transport
// mapping stays resolve's authority (A-1 unknown providers pass storage,
// get filtered by the failover loop above).
func candidateFor(a models.Account, now float64, ch chanKind) bool {
	if !a.EffectiveEnabled() {
		return false
	}
	d, err := a.ParseData()
	if err != nil {
		return false
	}
	status, _ := d.String(models.DataKeyStatus)
	switch status {
	case models.StatusActive:
	case models.StatusCooling:
		until, ok := d.Float(models.DataKeyCoolingUntil)
		if !ok || until >= now {
			return false // cooling countdown (is_cooling semantics: not yet)
		}
	case models.StatusExhausted:
		if ch != chanSub {
			return false // plan ledger: exhausted = truly no quota, don't burn turns
		}
	default: // invalid/disabled/unknown: never selectable, both ledgers
		return false
	}
	jwt, _ := d.String(models.DataKeyJWTToken)
	apiKey, _ := d.String(models.DataKeyAPIKey)
	if ch == chanSub {
		return apiKey != "" // defect b: any provider, apiKey credential
	}
	return jwt != ""
}

// resolve maps an account to its channel + credential (A-2: mode is
// case-sensitive "apiKey"; unknown provider values pass through per A-1 but
// have no defined transport - they are skipped with a 503 rather than
// guessed).
func (g *Gateway) resolve(a *models.Account) (*accountSel, error) {
	d, err := a.ParseData()
	if err != nil {
		return nil, err
	}
	jwt, _ := d.String(models.DataKeyJWTToken)
	apiKey, _ := d.String(models.DataKeyAPIKey)
	mode, _ := d.String(models.DataKeyMode)
	sel := &accountSel{Account: *a, Mode: mode}
	fp, _ := d.Object(models.DataKeyFingerprint)
	get := func(k string) string {
		if raw, ok := fp[k]; ok {
			var s string
			if jsonUnquote(raw, &s) {
				return s
			}
		}
		return ""
	}
	sel.DeviceMid = get("device_mid")
	sel.Platform = get("platform")
	sel.Arch = get("arch")
	sel.Language = get("language")
	sel.Timezone = get("timezone")
	sel.OSVersion = get("os_version")

	switch {
	case a.Provider == models.ProviderZai && mode == models.ModeJWT && jwt != "":
		sel.Channel, sel.URL = ChannelPlan, g.opt.ZAIPlanURL
		sel.AuthHdr = [2]string{"authorization", "Bearer " + jwt}
	case a.Provider == models.ProviderZai && mode == models.ModeAPIKey && apiKey != "":
		sel.Channel, sel.URL = ChannelZaiKey, g.opt.ZAIKeyURL
		sel.AuthHdr = [2]string{"x-api-key", apiKey}
	case a.Provider == models.ProviderBigmodel && apiKey != "":
		sel.Channel, sel.URL = ChannelBigModel, g.opt.BigModelURL
		sel.AuthHdr = [2]string{"x-api-key", apiKey}
	default:
		return nil, errNoAccount
	}
	return sel, nil
}

func jsonUnquote(raw []byte, out *string) bool {
	return json.Unmarshal(raw, out) == nil
}

// sortedKeys helper kept for deterministic debug output in tests.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
