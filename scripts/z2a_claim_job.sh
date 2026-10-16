#!/bin/bash
# 点名领取任务执行器（由 z2a_schedule_claim.sh 注册的一次性 cron 调用）
# 流程：两号并发上号检查可领额度 → 领取 → 邮件报告结果 → 自我注销（不重复触发）
set -u

DIR="${Z2A_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
LOG="${Z2A_CLAIM_LOG:-/tmp/z2a_claim_job.log}"
MARK="Z2A-CLAIM-JOB"
ID="${1:-manual}"

TS=$(date '+%Y-%m-%d %H:%M:%S')
echo "===== $TS 点名领取任务启动 (ID=$ID) =====" >> "$LOG"

# 并发检查 + 领取 + 邮件报告
"$DIR/.venv/bin/python" "$DIR/scripts/z2a_claim_report.py" --claim >> "$LOG" 2>&1
RC=$?
echo "----- 退出码 $RC -----" >> "$LOG"

# 自我注销：移除本 ID 的 cron 条目，避免下个周期重复触发
if [ "$ID" != "manual" ]; then
  crontab -l 2>/dev/null | grep -v "$MARK $ID" | crontab -
  echo "$TS 已自我注销 cron 条目 $ID" >> "$LOG"
fi

exit 0
