#!/bin/bash
# ZCode 套餐守望器 v2：全天候巡检 + 热点窗口（cron 分频控制）
# 低频 30min 平时巡逻（任何不定时突发波都能在30分钟内发现）
# 高频 5min 热点窗口（周六9-12 开抢 / 周末下午补货 / 每日22-02 夜间波）
ROOT="${Z2A_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
API_BASE="${Z2A_API:-http://127.0.0.1:9120}"
ADMIN_KEY=$(grep "^ZCODE_ADMIN_KEY" "$ROOT/.env" | cut -d= -f2)
LOG="${Z2A_WEEKEND_LOG:-/tmp/z2a_weekend_watch.log}"
STATE="${Z2A_STATE:-$ROOT/data/last_plans.txt}"

RES=$(curl -sS -m 120 $API_BASE/admin/api/claim/preview -H "Authorization: Bearer $ADMIN_KEY" 2>/dev/null)

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
    curl -sS -m 180 -X POST $API_BASE/admin/api/claim \
      -H "Authorization: Bearer $ADMIN_KEY" -H "Content-Type: application/json" -d '{}' >> $LOG 2>&1
    echo "" >> $LOG
    echo "$PLAN_IDS" > "$STATE"
  else
    echo "$TS 已知套餐仍在(plans=$PLAN_COUNT)，领取过则静默" >> $LOG
  fi
else
  echo "$TS 巡逻: 无可领套餐" >> $LOG
  [ -n "$LAST" ] && { echo "$TS 上一波已下架，清空状态" >> $LOG; > "$STATE"; }
exit 0
fi
