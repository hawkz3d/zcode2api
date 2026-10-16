#!/usr/bin/env bash
# 新服务冒烟测试：不依赖对拍器，纯 curl 验证基本可用性
# 用法: ./smoke.sh [port]   默认 19120（避免与老服务 9120 冲突）
set -uo pipefail
PORT="${1:-19120}"
BASE="http://127.0.0.1:$PORT"
OLD_ENV=/opt/zcode2api/.env
pass=0; fail=0

ok(){ printf '  \033[32m✔\033[0m %s\n' "$*"; pass=$((pass+1)); }
no(){ printf '  \033[31m✘\033[0m %s\n' "$*"; fail=$((fail+1)); }
chk(){ # chk <期望码> <实际码> <描述>
  if [ "$1" = "$2" ]; then ok "$3 (HTTP $2)"; else no "$3 期望 $1 实际 $2"; fi
}

echo "=== 新服务冒烟测试 @ $BASE ==="
echo
echo "── 1. 可达性 ──"
code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$BASE/v1/models" 2>/dev/null || true); code=${code:-000}
# 契约实测：/v1/models 无鉴权返回 200（模型列表非敏感），带错误 key 同样 200
chk 200 "$code" "GET /v1/models 无鉴权应 200（契约实测行为）"
body=$(curl -s -m 10 "$BASE/v1/models" 2>/dev/null || true)
if echo "$body" | grep -q '"object":"list"'; then ok "响应含 object:list 结构"; else no "响应缺少 object:list"; fi

echo
echo "── 2. 网关端点 ──"
code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$BASE/definitely/not/a/route" || true)
chk 404 "$code" "未知路径应 404"

code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 -X POST "$BASE/v1/chat/completions" \
  -H 'Content-Type: application/json' -d '{}' || true)
case "$code" in 400|401) ok "POST /v1/chat/completions 空 body → $code" ;;
  *) no "POST /v1/chat/completions 空 body 期望 400/401 实际 $code" ;; esac

echo
echo "── 3. 管理 API（需 admin key）──"
if [ -r "$OLD_ENV" ]; then
  K=$(grep -m1 '^ZCODE_ADMIN_KEY=' "$OLD_ENV" | cut -d= -f2- | tr -d '"'"'"'')
  code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 -H "Authorization: Bearer $K" "$BASE/admin/api/status" || true)
  chk 200 "$code" "GET /admin/api/status (带 key)"
  code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$BASE/admin/api/status" || true)
  chk 401 "$code" "GET /admin/api/status (无 key)"
  unset K
else
  no ".env 不可读，跳过管理 API 测试"
fi

echo
echo "── 4. 页面 ──"
for p in /admin/login /admin /; do
  code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$BASE$p" || true)
  case "$code" in 200|307|302) ok "GET $p → $code" ;; *) no "GET $p 期望 200/307 实际 $code" ;; esac
done

echo
echo "════════════════════════════"
printf '  通过 %d / 失败 %d\n' "$pass" "$fail"
[ "$fail" = "0" ] && { echo "  ✅ 冒烟通过"; exit 0; } || { echo "  ❌ 冒烟未通过"; exit 1; }
