#!/bin/bash
# zcode2api 巡逻 v2：弹性频率分层 + 抖动 + 邮件告警（2026-09-14）
#
# 分层（crontab 三行共用本脚本，脚内按时段自判本班是否干活，防行漂移重复跑）：
#   周末 09:00-17:00  3 分钟/班   （周末包固定周六09:00上架秒杀）
#   工作日 07:00-23:00 15 分钟/班 （周一白天发包实证，2h班错过是本次事故）
#   夜间 23:00-07:00  30 分钟/班  （夜包存活久+凌晨405风控敏感期，降存在感）
#   全班次 sleep 抖动 ±随机秒，反机械规律
# 告警：探到新波 → 立即 claim → 邮件报告（复用 Kuma SMTP，密码不落盘）
# 状态：data/last_plans.txt 记录已领波次 plan_id；同波重见静默，下架清空
# 根目录：优先环境变量，否则按脚本自身位置推导（scripts/ 的上一级）
ROOT="${Z2A_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
API_BASE="${Z2A_API:-http://127.0.0.1:9120}"
ADMIN_KEY=$(grep "^ZCODE_ADMIN_KEY" "$ROOT/.env" | cut -d= -f2)
LOG="${Z2A_LOG:-/tmp/z2a_patrol.log}"
STATE="${Z2A_STATE:-$ROOT/data/last_plans.txt}"
ALERT_PY="$ROOT/scripts/z2a_alert_mail.py"

# ── 抖动（0~89 秒，错开 cron 整点机械指纹）─────────────────────────────────
sleep $((RANDOM % 90))

# ── 本班资格判定：四条 cron 行共用本脚本，非本班时段直接静默退出 ────────────
# 【2026-09-25 认知纠正·用户告知】发包时间不是「固定周六09:00」——周五就有可能发，
#   历史规律（09-12/09-14 事故）也证明是弹性投放。故新增周五 09-23 点 */5 热点行
#   （# Z2A-FRI-HOT），与周末 */3 同量级；本脚本判定资格同步覆盖周五全天 7-23。
DOW=$(date +%u)   # 1=周一 ... 7=周日
H=$(date +%H)
IS_WEEKEND_HOT=0; IS_WORKDAY=0; IS_NIGHT=0; IS_WEEKEND_EDGE=0
if [ "$DOW" = "6" ] || [ "$DOW" = "7" ]; then
  { [ "$H" -ge 9 ] && [ "$H" -lt 17 ]; } && IS_WEEKEND_HOT=1
  # 2026-09-25 盲区修复：周末 07-09 与 17-23 原被判为「边缘时段」静默退出，
  # cron 命中却什么都不做 → 每天 8 小时零覆盖；而「周末下午补货/晚间突发波」
  # 正是历史规律（09-12 布防明确写过周末 13-17 补货）。补为边界班次。
  { [ "$H" -ge 7 ] && [ "$H" -lt 23 ]; } && [ "$IS_WEEKEND_HOT" = "0" ] && IS_WEEKEND_EDGE=1
fi
if [ "$DOW" -ge 1 ] && [ "$DOW" -le 5 ]; then
  { [ "$H" -ge 7 ] && [ "$H" -lt 23 ]; } && IS_WORKDAY=1
fi
if [ "$H" -ge 23 ] || [ "$H" -lt 7 ]; then IS_NIGHT=1; fi
if [ "$IS_WEEKEND_HOT" = "0" ] && [ "$IS_WORKDAY" = "0" ] && [ "$IS_NIGHT" = "0" ] && [ "$IS_WEEKEND_EDGE" = "0" ]; then
  exit 0   # 真正的边缘时段才静默
fi

# ── 取数：区分「网关不可达」与「确实无包」（2026-09-25 假阴性修复）──────
# 旧写法 curl ... 2>/dev/null 把连接失败静默成空 RES → 日志一路写「无可领套餐」，
# 网关死掉时与真无包完全同形，发包窗口会零告警错过（本次巡检实测踩到）。
HTTP_CODE=$(curl -sS -m 120 -o /tmp/z2a_preview_body.json -w '%{http_code}' \
  $API_BASE/admin/api/claim/preview -H "Authorization: Bearer $ADMIN_KEY" 2>/dev/null)
CURL_RC=$?
if [ "$CURL_RC" -ne 0 ] || [ -z "$HTTP_CODE" ] || [ "$HTTP_CODE" = "000" ]; then
  echo "$(date '+%m-%d %H:%M') ‼️ 网关不可达（curl rc=$CURL_RC）$API_BASE — 本条不是「无包」，是链路故障，错过发包窗口！" >> $LOG
  # 尝试自愈：拉起网关（幂等，健康时静默）
  bash "${Z2A_GUARD:-/opt/scripts/z2a_gateway_guard.sh}" >/dev/null 2>&1
  exit 1
fi
RES=$(cat /tmp/z2a_preview_body.json 2>/dev/null)

# 汇总可领包清单（plan_id 拼接，用于 diff 检测新波）
PLAN_IDS=$(echo "$RES" | python3 -c "
import json,sys
try:
    d = json.load(sys.stdin)
    ids = []
    for a in d.get('preview', []):
        for p in a.get('plans') or []:
            ids.append(p.get('plan_id',''))
    print('|'.join(ids))
except Exception:
    print('')" 2>/dev/null)
PLAN_COUNT=$(echo "$PLAN_IDS" | awk -F'|' '{print NF}' 2>/dev/null)
[ -z "$PLAN_IDS" ] && PLAN_COUNT=0

TS=$(date '+%m-%d %H:%M')
LAST=$(cat "$STATE" 2>/dev/null || echo "")

if [ "$PLAN_COUNT" -gt 0 ] 2>/dev/null; then
  if [ "$PLAN_IDS" != "$LAST" ]; then
    echo "$TS 🚨 新套餐波出现! plans=$PLAN_COUNT [$PLAN_IDS] → 立即全池领取" >> $LOG
    CLAIM_OUT=$(curl -sS -m 180 -X POST $API_BASE/admin/api/claim       -H "Authorization: Bearer $ADMIN_KEY" -H "Content-Type: application/json" -d '{}' 2>&1)
    echo "$CLAIM_OUT" >> $LOG
    echo "" >> $LOG
    echo "$PLAN_IDS" > "$STATE"
    # ── 邮件告警（fire-and-forget：失败只进日志，不影响主流程）────────────
    # 摘要解析走独立脚本（内联 python 的 f-string 与 bash 双引号打架，2026-09-14 修复）
    SUMMARY=$(echo "$CLAIM_OUT" | python3 "$ROOT/scripts/z2a_claim_summary.py" 2>/dev/null)
    {
      echo "[$(date '+%m-%d %H:%M')] ZCode 新包已探到并自动领取"
      echo "波次: $PLAN_IDS"
      echo "$SUMMARY"
      echo "后台: $API_BASE/admin （管理页）"
    } > /tmp/z2a_alert_body.txt
    OK_COUNT=$(echo "$SUMMARY" | grep -c '✓')
    python3 "$ALERT_PY" "[ZCode] 🚨 新包已探到（成功领取 ${OK_COUNT} 个号）" "$(cat /tmp/z2a_alert_body.txt)" >> $LOG 2>&1
    ALERT_RC=$?
    echo "$TS 邮件告警已发 (exit=$ALERT_RC)" >> $LOG
    rm -f /tmp/z2a_alert_body.txt
  else
    echo "$TS 已知套餐仍在(plans=$PLAN_COUNT)，领取过则静默" >> $LOG
  fi
else
  # ── 资格自检（2026-09-25）：preview 只遍历 provider='zai' 且 mode='jwt' 的账号，
  #   池里若没有 JWT 号，返回恒为空 → 与「确实无包」不可区分。此处显式区分，
  #   避免「以为在蹲包、其实一个号都没资格」的静默失守。
  JWT_N=$(curl -sS -m 30 $API_BASE/admin/api/accounts -H "Authorization: Bearer $ADMIN_KEY" 2>/dev/null | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    print(sum(1 for a in d.get('accounts',[]) if a.get('provider')=='zai' and a.get('mode')=='jwt'))
except Exception:
    print(-1)" 2>/dev/null)
  if [ "$JWT_N" = "0" ]; then
    echo "$TS ‼️ 无可领套餐——但池内 0 个 JWT 号，preview 恒空，本轮探包无意义！需补 JWT 号" >> $LOG
  elif [ "$JWT_N" = "-1" ]; then
    echo "$TS ⚠️ 无可领套餐（资格自检失败：accounts 接口异常）" >> $LOG
  else
    echo "$TS 巡逻: 无可领套餐（JWT号=$JWT_N）" >> $LOG
  fi
  [ -n "$LAST" ] && { echo "$TS 上一波已下架，清空状态" >> $LOG; > "$STATE"; }
fi
exit 0
