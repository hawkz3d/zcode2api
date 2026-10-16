#!/usr/bin/env bash
# 危险端点裸调守卫（Catherine 2026-10-16 凭据泄露事故后落地）
#
# 背景：/admin/api/export 是【凭据导出端点】，响应体按契约回显全部账号的明文
# secret（4 条 JWT + 2 条 apiKey）。一次「验证路径陷阱」的取证里，作者用
# `curl -s -H "Authorization: Bearer $KEY" .../admin/api/export` 打印响应体
# 看状态码 —— 明文凭据因此进入会话上下文。
#
# 判据（可证伪）：扫描本目录下所有脚本/配置，若出现该端点且【没有】以下任一
# 形态，则报错退出：
#   - `-o /dev/null`        （丢弃响应体）
#   - `-w '%{http_code}'`   （只取状态码）
#   - `keys(` / `list(`     （只取字段名，不下钻 secret）
#   - 注释行（# 开头）       （说明性引用，不执行）
#
# 这是「让工具在决策点拦住人」——比文档提醒有效（工具的输出具有强制力）。

set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
bad=0
checked=0

# 只扫描会【被执行】的文件：.sh / .py / .json(cases) / Makefile
while IFS= read -r -d '' f; do
  case "$f" in
    *latest/*) continue ;;                        # 对拍产物，非可执行源
  esac
  # 逐行扫描，跳过注释行
  while IFS= read -r line; do
    case "$line" in \#*) continue ;; esac
    case "$line" in
      *admin/api/export*)
        # 只在【真的发请求】的行上判定。声明式引用（cases.json 的 "path": …）
        # 不产生输出：harness 的 Sentinel 会在响应体进入 L2/L3 前做字段名+形态
        # 双道指纹化，明文零落盘 —— 这是另一套（已实现的）防线，不是裸调。
        case "$line" in
          *curl*|*httpx*|*requests.*get*|*urllib*|*wget*) ;;
          *) continue ;;
        esac
        checked=$((checked+1))
        case "$line" in
          *"-o /dev/null"*|*"-o/dev/null"*|*"%{http_code}"*|*"keys("*|*"list("*|*"[0]"*) ;;
          *) printf '❌ %s\n   → %s\n' "$f" "$(echo "$line" | sed 's/^[[:space:]]*//' | cut -c1-120)"
             printf '      该行【发请求】到凭据导出端点，但响应体未被丢弃 —— 明文 secret 会进入输出。\n'
             printf '      修法：加 -o /dev/null ，或只取字段名（body["providers"].keys()）。\n'
             bad=$((bad+1)) ;;
        esac ;;
    esac
  done < "$f"
# 扫描面：整个仓库（脚本与配置才是"会被执行"的载体）。
# 注意【不要把本文件排除在外】—— 排除自身会让"往本文件里加裸调"不被发现，
# 使守卫退化为零判别力（实测：早期版本 maxdepth 2 + continue 自身 → 造裸调仍绿）。
done < <(find "$ROOT" -type f \
           \( -name '*.sh' -o -name '*.py' -o -name 'cases.json' \) \
           -not -path '*/.git/*' -not -path '*/.worktrees/*' -not -path '*/latest/*' \
           -print0 2>/dev/null)

echo "── 危险端点守卫：扫描 $checked 处 export 引用 ──"
if [ "$bad" -eq 0 ]; then
  echo "✅ 无裸调（全部显式丢弃响应体或仅取字段名）"
  exit 0
fi
echo "❌ $bad 处裸调 —— 拒绝提交"
exit 1
