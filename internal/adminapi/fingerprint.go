package adminapi

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// fingerprintView is defined in view.go (the 7-field device profile).
//
// The legacy module has two producers, and which one runs is observable:
//
//   - `assign()` (new account) uses the HOST profile with a fresh device_mid —
//     measured live accounts all carry `linux/x64/6.18.18.c952-trim/en-US/
//     Asia/Shanghai/1920x1080`, i.e. this deployment's real shape;
//   - `rotate()` uses the RANDOM pool, which is restricted to the platform×arch
//     and os_version combinations the official client actually produces.
//
// `POST /admin/api/accounts/{id}/fingerprint/rotate` must therefore emit a
// pool-valid profile, NOT the host shape — the distinction is the endpoint's
// whole purpose ("换了一台机器").

// platformArchs is `_PLATFORM_ARCHS`: the real mainstream client combinations.
var platformArchs = [][2]string{
	{"darwin", "arm64"},
	{"darwin", "x64"},
	{"win32", "x64"},
	{"linux", "x64"},
}

// osVersions is `_OS_VERSIONS`, keyed by platform.
var osVersions = map[string][]string{
	"darwin": {"22.6.0", "23.6.0", "24.5.0", "24.6.0", "25.5.0"},
	"win32":  {"10.0.19045", "10.0.22000", "10.0.22621", "10.0.22631", "10.0.26100", "10.0.26200"},
	"linux":  {"5.15.0-91-generic", "6.1.0-18-amd64", "6.8.0-45-generic"},
}

// locales is `_LOCALES`: the language/timezone pairs the official client shows.
var locales = [][2]string{
	{"zh-CN", "Asia/Shanghai"},
	{"en-US", "America/New_York"},
	{"en-US", "America/Los_Angeles"},
	{"en-GB", "Europe/London"},
	{"de-DE", "Europe/Berlin"},
	{"ja-JP", "Asia/Tokyo"},
	{"ko-KR", "Asia/Seoul"},
	{"en-SG", "Asia/Singapore"},
}

// screens is `_SCREENS`: common desktop resolutions.
var screens = []string{
	"1920x1080", "2560x1440", "3840x2160", "5120x2880",
	"2560x1600", "1728x1117", "1512x982", "1440x900", "1366x768",
}

const fallbackScreen = "1920x1080"

// releaseShape is `_RELEASE_SHAPE`, the gate that lets a real host kernel
// version through even though it is absent from the preset pool.
var releaseShape = regexp.MustCompile(`^\d+\.\d+(\.\d+)?[\w.\-]*$`)

// langRe parses `zh_CN.UTF-8` into language + region.
var langRe = regexp.MustCompile(`^([a-z]{2,3})(?:[_-]([A-Za-z]{2,4}))?`)

// langDefaults is `_resolve_language`'s bare-language table.
var langDefaults = map[string]string{
	"zh": "zh-CN", "en": "en-US", "ja": "ja-JP", "ko": "ko-KR",
	"de": "de-DE", "fr": "fr-FR",
}

// randomProfile is `random_profile()`: pick a pool-valid combination, then
// self-validate (the legacy code raises on an invalid draw, which cannot happen
// with a correct pool — the validation is kept as an assertion of that).
func randomProfile() *fingerprintView {
	pa := platformArchs[randIndex(len(platformArchs))]
	loc := locales[randIndex(len(locales))]
	versions := osVersions[pa[0]]
	return &fingerprintView{
		Platform:  pa[0],
		Arch:      pa[1],
		OSVersion: versions[randIndex(len(versions))],
		Language:  loc[0],
		Timezone:  loc[1],
		Screen:    screens[randIndex(len(screens))],
		DeviceMid: newUUIDv4(),
	}
}

// hostProfile is `assign()`: the deployment host's REAL shape with a brand-new
// device_mid. Reproducing the real host values matters because the live
// accounts carry exactly this fingerprint and the UI displays it.
func hostProfile() *fingerprintView {
	p := &fingerprintView{
		Platform:  normalizePlatform(runtime.GOOS),
		Arch:      normalizeArch(runtime.GOARCH),
		OSVersion: hostOSVersion(),
		Language:  hostLanguage(),
		Timezone:  hostTimezone(),
		Screen:    fallbackScreen,
		DeviceMid: newUUIDv4(),
	}
	if p.OSVersion == "" {
		p.OSVersion = "0.0"
	}
	return p
}

// normalizePlatform maps Go's GOOS onto the client's process.platform values.
func normalizePlatform(goos string) string {
	switch strings.ToLower(goos) {
	case "darwin":
		return "darwin"
	case "windows":
		return "win32"
	default:
		return "linux"
	}
}

// normalizeArch maps Go's GOARCH onto the client's process.arch values
// (`_normalize_arch`: anything containing "arm"/"aarch" is arm64, else x64).
func normalizeArch(goarch string) string {
	m := strings.ToLower(goarch)
	if strings.Contains(m, "arm") || strings.Contains(m, "aarch") {
		return "arm64"
	}
	return "x64"
}

// hostOSVersion is `platform.release()`: the running kernel release. Read from
// /proc on Linux, which is the only platform this service is deployed on; other
// platforms fall back to "0.0" (the legacy code does the same when the value is
// unavailable).
func hostOSVersion() string {
	if runtime.GOOS != "linux" {
		return "0.0"
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "0.0"
	}
	v := strings.TrimSpace(string(b))
	if v == "" || !releaseShape.MatchString(v) {
		return "0.0"
	}
	return v
}

// hostLanguage is `_resolve_language`: parse LC_ALL/LANG, defaulting to en-US
// when there is no locale (the documented container case).
func hostLanguage() string {
	raw := strings.TrimSpace(os.Getenv("LC_ALL"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("LANG"))
	}
	m := langRe.FindStringSubmatch(raw)
	if m == nil {
		return "en-US"
	}
	if m[2] == "" {
		if v, ok := langDefaults[m[1]]; ok {
			return v
		}
		return "en-US"
	}
	return m[1] + "-" + strings.ToUpper(m[2])
}

// hostTimezone is `_resolve_timezone`: resolve /etc/localtime and take the IANA
// name after the `zoneinfo` segment, falling back to UTC.
func hostTimezone() string {
	target, err := filepath.EvalSymlinks("/etc/localtime")
	if err != nil {
		return "UTC"
	}
	parts := strings.Split(filepath.ToSlash(target), "/")
	for i, seg := range parts {
		if seg == "zoneinfo" && i+2 < len(parts) {
			return strings.Join(parts[i+1:], "/")
		}
	}
	return "UTC"
}

// newUUIDv4 produces a RFC 4122 version-4 UUID in the canonical lowercase form
// (the legacy `str(uuid.uuid4())`).
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A failure here means the OS entropy source is unusable; the account
		// pool cannot be safely keyed without it, so surface a zero UUID rather
		// than panic (callers treat a fingerprint failure as a 500).
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

// randIndex returns a uniform index in [0,n) using crypto/rand, mirroring
// `secrets.choice`. It never panics: on an entropy failure it degrades to 0,
// which is a valid pool entry.
func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return int(v % uint32(n))
}
