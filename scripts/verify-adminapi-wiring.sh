#!/usr/bin/env bash
# verify-adminapi-wiring.sh - t13/t14 post-wiring check (Lamias, t17 plan §五).
# Compares candidate (Go) against baseline (legacy) on the ROUTING SURFACE:
# adminapi attribution, the /admin/api exact-block, trailing-slash semantics,
# and pages/gateway/static isolation. Content equivalence of the 22 endpoints
# is the conformance harness's job, not this script's.
#
# usage: verify-adminapi-wiring.sh [CAND_URL] [BASE_URL]
#   defaults: CAND=http://127.0.0.1:19120  BASE=http://127.0.0.1:9120
# never echoes secrets; anonymous shapes only.
set -u
CAND="${1:-http://127.0.0.1:19120}"
BASE="${2:-http://127.0.0.1:9120}"
pass=0 fail=0

# st URL -> "code ct locTail body40" one line (locTail = Location minus scheme://host)
st() {
	local url="$1" body hdr code loc ct
	body=$(mktemp)
	code=$(curl -s -o "$body" -w '%{http_code}' -D /tmp/vaw.hdr "$url" --max-time 10)
	ct=$(awk 'BEGIN{IGNORECASE=1}/^content-type:/{gsub(/\r/,"");print $2;exit}' /tmp/vaw.hdr)
	loc=$(awk 'BEGIN{IGNORECASE=1}/^location:/{gsub(/\r/,"");print $2;exit}' /tmp/vaw.hdr)
	loc=$(sed -E 's#^https?://[^/]+##' <<<"$loc")
	printf '%s ct=%s loc=%s body=%s\n' "$code" "$ct" "$loc" "$(head -c 40 "$body" | tr -d '\n')"
	rm -f "$body"
}
same() { # desc: assert the two st() outputs are identical (loc already host-stripped)
	local desc="$1" a="$2" b="$3"
	if [ "$a" = "$b" ]; then pass=$((pass+1)); printf 'PASS  %-44s %s\n' "$desc" "$a"
	else fail=$((fail+1)); printf 'FAIL  %-44s\n  cand: %s\n  base: %s\n' "$desc" "$a" "$b"; fi
}
want() { # desc cand_out expect_substr
	local desc="$1" out="$2" exp="$3"
	if grep -qF -- "$exp" <<<"$out"; then pass=$((pass+1)); printf 'PASS  %-44s %s\n' "$desc" "$out"
	else fail=$((fail+1)); printf 'FAIL  %-44s got %s want substr %q\n' "$desc" "$out" "$exp"; fi
}
cmpbody() { # desc path : byte cmp candidate vs baseline body
	local desc="$1" p="$2"
	if cmp -s <(curl -s --max-time 10 "$CAND$p") <(curl -s --max-time 10 "$BASE$p"); then
		pass=$((pass+1)); printf 'PASS  %-44s body IDENTICAL\n' "$desc"
	else fail=$((fail+1)); printf 'FAIL  %-44s body DIFFERS (%s)\n' "$desc" "$p"; fi
}

printf '== adminapi wiring verification ==\ncand=%s base=%s\n\n[1] pages survive the mount (200+no-store, exact both sides)\n' "$CAND" "$BASE"
for p in login accounts monitoring settings; do
	same "GET /admin/$p equivalence" "$(st "$CAND/admin/$p")" "$(st "$BASE/admin/$p")"
done
cc=$(curl -s -D - -o /dev/null "$CAND/admin/login" --max-time 10 | tr -d '\r' | grep -ci '^cache-control: no-store')
want "candidate pages carry no-store" "hits=$cc" "hits=1"

printf '\n[2] static isolation (byte-identical vs legacy)\n'
for f in css/app.css js/auth.js js/header.js js/toast.js; do cmpbody "GET /static/$f" "/static/$f"; done

printf '\n[3] gateway isolation\n'
cmpbody "GET /v1/models" "/v1/models"
same "GET /health (404 shape A both sides)" "$(st "$CAND/health")" "$(st "$BASE/health")"

printf '\n[4] adminapi attribution + the exact-block (THE wiring assertions)\n'
# status endpoint: same status both sides (401 anon today; 200 if keys configured) -
# the point is it is NOT a page-404 => wired to adminapi, and both sides agree.
same "GET /admin/api/status equivalence" "$(st "$CAND/admin/api/status")" "$(st "$BASE/admin/api/status")"
# boundary quartet: legacy says 404 shape A for all four; a missing block shows 307.
same "GET /admin/api (exact-block! not 307)" "$(st "$CAND/admin/api")" "$(st "$BASE/admin/api")"
same "GET /admin/api/" "$(st "$CAND/admin/api/")" "$(st "$BASE/admin/api/")"
same "GET /admin/api/nonexistent" "$(st "$CAND/admin/api/nonexistent")" "$(st "$BASE/admin/api/nonexistent")"
same "GET /admin/api/nonexistent/" "$(st "$CAND/admin/api/nonexistent/")" "$(st "$BASE/admin/api/nonexistent/")"
# trailing slash on an EXISTING route: 307 to host-stripped /admin/api/status both sides.
same "GET /admin/api/status/ (307, host-normalized)" "$(st "$CAND/admin/api/status/")" "$(st "$BASE/admin/api/status/")"

printf '\n[5] redirect surface unchanged by the mount\n'
for p in / /admin /admin/ /admin/login/ /static; do
	same "GET $p equivalence" "$(st "$CAND$p")" "$(st "$BASE$p")"
done

printf '\n== summary: pass=%d fail=%d ==\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
