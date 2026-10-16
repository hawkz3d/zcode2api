#!/bin/bash
# 点名领取调度器：注册一个「某时刻去领智谱鸡蛋」的一次性任务
#
# 用法:
#   z2a_schedule_claim.sh "2026-09-13 09:00"          指定时刻领取+邮件报告
#   z2a_schedule_claim.sh "09:00"                     今天该时刻（已过则顺延到明天）
#   z2a_schedule_claim.sh --list                      列出待执行任务
#   z2a_schedule_claim.sh --cancel <ID>               取消任务
#
# 实现：往 crontab 追加一条带唯一标记的一次性条目；任务执行后自我注销，
# 不会重复触发。不用 at（atd 未启用）、不用 systemd-run（无用户 bus）。
set -u

DIR="${Z2A_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
RUNNER="$DIR/scripts/z2a_claim_job.sh"
MARK="Z2A-CLAIM-JOB"

usage() { sed -n '2,12p' "$0"; exit 1; }

[ $# -ge 1 ] || usage

# ── 列出待执行 ──
if [ "$1" = "--list" ]; then
  echo "待执行的点名领取任务："
  crontab -l 2>/dev/null | grep "$MARK" | while read -r line; do
    echo "  $line"
  done
  crontab -l 2>/dev/null | grep -q "$MARK" || echo "  （无）"
  exit 0
fi

# ── 取消任务 ──
if [ "$1" = "--cancel" ]; then
  [ $# -ge 2 ] || { echo "用法: $0 --cancel <ID>"; exit 1; }
  ID="$2"
  NEW=$(crontab -l 2>/dev/null | grep -v "$MARK.*$ID")
  echo "$NEW" | crontab -
  echo "已取消任务 $ID"
  exit 0
fi

# ── 解析时刻 ──
SPEC="$1"
if [[ "$SPEC" =~ ^([0-9]{4})-([0-9]{2})-([0-9]{2})[[:space:]]+([0-9]{1,2}):([0-9]{2})$ ]]; then
  M="${BASH_REMATCH[5]}"; H="${BASH_REMATCH[4]}"
  D="${BASH_REMATCH[3]}"; MO="${BASH_REMATCH[2]}"
elif [[ "$SPEC" =~ ^([0-9]{1,2}):([0-9]{2})$ ]]; then
  M="${BASH_REMATCH[2]}"; H="${BASH_REMATCH[1]}"
  D="*"; MO="*"
  # 今天该时刻已过 → 顺延到明天
  if [ "$(date +%s)" -ge "$(date -d "today $H:$M" +%s 2>/dev/null || echo 0)" ]; then
    D=$(date -d tomorrow +%d); MO=$(date -d tomorrow +%m)
  else
    D=$(date +%d); MO=$(date +%m)
  fi
else
  echo "无法识别的时刻: $SPEC"; usage
fi

ID=$(date +%Y%m%d%H%M%S)
CRON_LINE="$M $H $D $MO * $RUNNER $ID  # $MARK $ID"

{ crontab -l 2>/dev/null; echo "$CRON_LINE"; } | crontab -

echo "✔ 已注册点名领取任务"
echo "    ID   : $ID"
echo "    时刻 : $(printf '%s-%s-%s %s:%s' "$(date +%Y)" "$MO" "$D" "$H" "$M")"
echo "    动作 : 两号同时上号检查可领额度 → 领取 → 邮件报告"
echo "    取消 : $0 --cancel $ID"
