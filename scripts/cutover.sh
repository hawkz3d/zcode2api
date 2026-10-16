#!/usr/bin/env bash
# zhipucode2api 切换 / 回滚脚本
#
# 用法:
#   ./cutover.sh preflight      只做体检，不改任何东西（默认）
#   ./cutover.sh switch         停老服务、起新服务（需先通过 preflight）
#   ./cutover.sh rollback       回滚到老服务
#   ./cutover.sh status         看两边状态
#
# 铁律：switch 会在 preflight 未通过时拒绝执行。
set -euo pipefail

OLD_UNIT=zcode2api
NEW_UNIT=zhipucode2api
PORT=9120
OLD_DIR=/opt/zcode2api
NEW_DIR=/opt/zhipucode2api
LOG=/opt/logs
WS=/opt/workspace
# preflight 用于验证新服务的端口（非生产 9120）
PREFLIGHT_NEW_PORT="${PREFLIGHT_NEW_PORT:-19120}"

c_ok(){ printf '\033[32m✔ %s\033[0m\n' "$*"; }
c_no(){ printf '\033[31m✘ %s\033[0m\n' "$*"; }
c_in(){ printf '\033[36m▶ %s\033[0m\n' "$*"; }

preflight(){
  local fail=0
  c_in "1/9 检查新服务二进制"
  if [ -x "$NEW_DIR/bin/zhipucode2api" ]; then c_ok "二进制存在且可执行"; else c_no "缺 $NEW_DIR/bin/zhipucode2api"; fail=$((fail+1)); fi

  c_in "2/9 检查 systemd 单元（仓库副本 + 生效副本）"
  # t15 演练发现：原项只查仓库副本，不查真生效的 unit —— 检查了错的对象。
  if [ -f "$NEW_DIR/scripts/$NEW_UNIT.service" ]; then
    c_ok "仓库副本存在"
  else c_no "缺 $NEW_DIR/scripts/$NEW_UNIT.service"; fail=$((fail+1)); fi
  # 生效 unit：若已安装，必须与仓库副本内容一致（否则切换用的是旧版）
  if [ -f "/etc/systemd/system/$NEW_UNIT.service" ]; then
    if diff -q "$NEW_DIR/scripts/$NEW_UNIT.service" "/etc/systemd/system/$NEW_UNIT.service" >/dev/null 2>&1; then
      c_ok "生效 unit 与仓库副本一致"
    else
      c_no "/etc/systemd/system/$NEW_UNIT.service 与仓库副本不一致（需重新安装）"; fail=$((fail+1))
    fi
  else
    c_ok "生效 unit 尚未安装（switch 阶段会装）"
  fi

  c_in "3/9 检查 .env 可读（键名，不打印值）"
  if [ -r "$OLD_DIR/.env" ]; then
    local nkeys; nkeys=$(grep -cE '^[A-Z_]+=' "$OLD_DIR/.env" 2>/dev/null || true); nkeys=${nkeys:-0}
    c_ok ".env 可读，键数=$nkeys"
  else c_no ".env 不可读"; fail=$((fail+1)); fi

  c_in "4/9 检查老服务当前健康（回滚基线）"
  if systemctl is-active --quiet "$OLD_UNIT"; then c_ok "老服务 active"; else c_no "老服务未运行（切换后无法回滚对比）"; fail=$((fail+1)); fi

  c_in "5/9 检查端口占用情况（须辨明持有者是老服务）"
  # t15 演练发现：原项只证「有人」在听，不辨身份 —— 若换成别的进程占端口也会「通过」。
  # 注意：不能用 `ss ... | grep -q` —— set -o pipefail 下 grep 命中即退出会给 ss 发 SIGPIPE，
  # 整条管道被判失败，产生必然误报。改为先落变量再匹配。
  local port_line owner_pid
  port_line=$(ss -tlnp 2>/dev/null | grep ":$PORT " || true)
  owner_pid=$(systemctl show "$OLD_UNIT" -p MainPID --value 2>/dev/null || echo "")
  if [ -n "$port_line" ]; then
    if [ -n "$owner_pid" ] && [ "$owner_pid" != "0" ] && echo "$port_line" | grep -q "pid=$owner_pid"; then
      c_ok "端口 $PORT 由 $OLD_UNIT (pid=$owner_pid) 持有"
    else
      c_no "端口 $PORT 被监听，但持有者不是 $OLD_UNIT (MainPID=$owner_pid) —— 切换前须查清"; fail=$((fail+1))
    fi
  else
    c_no "端口 $PORT 无监听（老服务异常？）"; fail=$((fail+1))
  fi

  c_in "6/9 检查日志目录可写（非 sticky，避免 fs.protected_regular=2 拦截）"
  if [ -d "$LOG" ] && [ -w "$LOG" ]; then c_ok "$LOG 可写"; else c_no "$LOG 不可写"; fail=$((fail+1)); fi

  c_in "7/9 检查消费方依赖（cron + DSH 插件，逐项独立）"
  # t15 演练发现：原项两子条件都查同一个谓词（$OLD_DIR/.env 是否存在）→ 必然 2/2，零判别力。
  # 改为三个**互相独立**的消费方，各自有失败路径。
  local consumers_bad=0
  # 7a. cron 脚本需要 .env 里有 ADMIN_KEY
  local n_key
  n_key=$(grep -cE '^ZCODE_ADMIN_KEY=' "$OLD_DIR/.env" 2>/dev/null || true); n_key=${n_key:-0}
  if [ "$n_key" -ge 1 ]; then c_ok "7a cron 消费方：.env 含 ZCODE_ADMIN_KEY"
  else c_no "7a cron 消费方：.env 缺 ZCODE_ADMIN_KEY"; consumers_bad=$((consumers_bad+1)); fi
  # 7b. crontab 里引用老仓的脚本**必须存在**（否则巡逻/发包静默停摆）
  local cron_missing=0
  while IFS= read -r line; do
    case "$line" in
      *"$OLD_DIR"*) for tok in $line; do
          case "$tok" in /*.sh|/*.py) [ -e "$tok" ] || cron_missing=$((cron_missing+1));; esac
        done;;
    esac
  done < <(crontab -l 2>/dev/null || true)
  if [ "$cron_missing" -eq 0 ]; then c_ok "7b crontab 引用的脚本文件均存在"
  else c_no "7b crontab 有 $cron_missing 个引用指向不存在的文件"; consumers_bad=$((consumers_bad+1)); fi
  # 7c. DSH 插件 dsh-zcode-plan 的 accountsDb 可达性。
  #
  # 🔴 第三次修正（captain，2026-10-16）：前两版都把「指向老仓」当成缺陷，
  #    这【判据本身是错的】。retire-plan.md 第 6/7 项明确：老仓的 `data/` 与
  #    `.env` 在归档后【仍必须保留】（新 unit 的 EnvironmentFile 正是老 .env，
  #    新服务的库也指向老 data/）。所以插件读老仓路径是【设计正确的】。
  #    真正的风险不是「路径含 zcode2api」，而是【该路径在归档后不可达】——
  #    归档用 mv，若被移走/删除，插件立刻断。判据改为断言可达性（可证伪：
  #    把目录移走 → 红；恢复 → 绿）。
  local plan_js="$WS/dsh-zcode-plan/lib/index.js"
  if [ ! -r "$plan_js" ]; then
    c_no "7c 找不到 $plan_js（消费方 dsh-zcode-plan 缺失？）"; consumers_bad=$((consumers_bad+1))
  else
    # 取出插件实际配置的 accountsDb（硬编码默认或用户配置），断言该文件可读。
    local db_path
    db_path=$(grep -oE '/[^"'"'"' ]*accounts\.db' "$plan_js" 2>/dev/null | head -1 || true)
    if [ -z "$db_path" ]; then
      # 插件支持用户配置覆盖；无硬编码则说明走配置，不阻塞（无法静态断言）。
      c_ok "7c dsh-zcode-plan 无硬编码 accountsDb（走用户配置，静态不可判）"
    elif [ -r "$db_path" ]; then
      c_ok "7c dsh-zcode-plan 的 accountsDb 可达（$db_path）"
    else
      c_no "7c dsh-zcode-plan 的 accountsDb 不可达（$db_path）—— 插件会拿不到 JWT"
      consumers_bad=$((consumers_bad+1))
    fi
  fi

  c_in "8/9 检查切换后需改的外部引用（静态可证伪）"
  # 🔴 t15 第二轮反向复核（B-1）修正：上一版判据查「检查表里已勾选的 cron 待办」，
  #    但 C7（验证 cron 兼容）与 E5（归档后更新路径）都是**切换后**的待办，
  #    而 preflight 在**切换前**跑 ⇒ 判据恒红 = 死锁；
  #    更危险的是它诱导人**提前勾掉 C7**（= 伪造一条未做的验证记录）。
  #    改为**静态可证伪**：crontab 引用老仓的那一行，其脚本路径必须
  #    ①当前存在 ②已登记在 cutover-external-deps.md（删掉登记行 → 红，可证伪）。
  local cron_refs cron_unregistered=0 cron_missing_scripts=0
  cron_refs=$(crontab -l 2>/dev/null | grep -c "$OLD_DIR" || true); cron_refs=${cron_refs:-0}
  if [ "$cron_refs" -gt 0 ]; then
    local deps_doc="$NEW_DIR/docs/contract/cutover-external-deps.md"
    while IFS= read -r line; do
      case "$line" in
        *"$OLD_DIR"*) for tok in $line; do
            case "$tok" in
              /*.sh|/*.py)
                [ -e "$tok" ] || cron_missing_scripts=$((cron_missing_scripts+1))
                # 登记检查：脚本 basename 必须出现在依赖分析文档里
                if [ -r "$deps_doc" ] && ! grep -qF "$(basename "$tok")" "$deps_doc" 2>/dev/null; then
                  cron_unregistered=$((cron_unregistered+1))
                fi
                ;;
            esac
          done;;
      esac
    done < <(crontab -l 2>/dev/null || true)

    if [ "$cron_missing_scripts" -gt 0 ]; then
      c_no "8 crontab 有 $cron_missing_scripts 个引用指向不存在的脚本"; fail=$((fail+1))
    elif [ "$cron_unregistered" -gt 0 ]; then
      c_no "8 crontab 有 $cron_unregistered 个引用未登记在 cutover-external-deps.md —— 切换后会断"
      fail=$((fail+1))
    else
      c_ok "8 crontab 的 $cron_refs 处老仓引用：脚本均存在且已登记"
    fi
  else
    c_ok "8 crontab 无指向老仓的硬编码路径"
  fi

  c_in "9/9 检查切换前置条件（t15 演练新增：防假绿）"
  # t15 演练发现：原 preflight 在 t13 未完成、CR-39 未修复时仍报「可以切换」。
  # 这两项是**硬前置**，必须显式断言。

  # ── 9) 切换前置条件（t15 双向复核后重排：先辨明持有者身份，再共用判定）
  # 🔴 顺序很重要：newpid 必须先解析，9a 与 9b 都要用它（原序 9a 在前导致变量未定义 → 误报）
  local newport_line newpid newcmd newcwd newpid_ok=0
  # 🔴 取 pid 必须用 sudo ss：非 root 的 `ss -tlnp` 对【他人 socket】不显示 users:()
  #    字段（实测：普通用户跑 pid= 取空 → newpid="" → 误判"幽灵进程"；
  #    sudo ss 同一端口输出 users:(("zhipucode2api",pid=1516554,fd=7))）。
  #    先试 sudo，失败回退普通 ss（保持无 sudo 环境可用）。
  newport_line=$(sudo ss -tlnp 2>/dev/null | grep ':'"$PREFLIGHT_NEW_PORT"' ' || true)
  [ -z "$newport_line" ] && newport_line=$(ss -tln 2>/dev/null | grep ':'"$PREFLIGHT_NEW_PORT"' ' || true)
  if [ -n "$newport_line" ]; then
    newpid=$(echo "$newport_line" | grep -oE 'pid=[0-9]+' | head -1 | cut -d= -f2 || true)
    # cmdline 非 sudo 可读；cwd/exe 需 sudo（本机 procfs 硬化，实测：
    # `/proc/$pid/cwd` 普通用户 readlink 失败而 cmdline 成功）。
    # ⇒ cwd 取 sudo 优先、回退空；**判据不依赖 cwd**（见下方归属断言改用
    #   systemd 单元的 ExecStart，比 cwd 更强：cwd 只能说明"在哪运行"，
    #   ExecStart 直接锁定"跑的是哪个二进制"）。
    newcmd=$(tr '\0' ' ' < "/proc/${newpid:-0}/cmdline" 2>/dev/null || true)
    newcwd=$(sudo readlink "/proc/${newpid:-0}/cwd" 2>/dev/null || true)
    # 🔴 t15 第二轮反向复核（M-2）：仅凭 cmdline/cwd 会**放行幽灵进程**
    #    （实测 pid 907547：PPID=1 孤儿，systemctl is-active=false，MainPID=0，
    #      但 cmdline 含 zhipucode2api 且 cwd==NEW_DIR ⇒ 旧判据放行并会向它发生产 key）。
    #
    # 🔴🔴 第三次修正（captain，2026-10-16 02:20，实测死锁）：
    #    上一版要求 `systemctl show $NEW_UNIT -p MainPID == newpid`。
    #    但 $NEW_UNIT（zhipucode2api.service）的 EnvironmentFile 把端口定为 9120
    #    （= 与老服务冲突的生产端口），**它在 preflight 阶段不可能占用 19120** ——
    #    ⇒ 该判据【无法被满足】，preflight 永久红，等于用红锁死切换（Hilkiah 反复
    #    定罪的那类"零判别力判据"，只是方向相反）。实测：用 systemd-run 起的预检实例
    #    （单元名 z2a-preflight）被 judging 为 MainPID=0 → 误报"幽灵进程"。
    #
    #    正确判据（既挡幽灵又可达）：**归属断言改为"它是由 systemd 托管的、指向本仓
    #    二进制的进程"** —— 即存在一个 active 的 systemd 单元，其 MainPID == newpid
    #    且其 ExecStart 指向 $NEW_DIR/bin/zhipucode2api。systemd 托管的进程必然是
    #    服务（非孤儿），而 PathEx 校验确保它确实是我们这个二进制；孤儿进程的
    #    MainPID 不会出现在任何 active 单元的 MainPID 上。
    local owner_unit owner_exe
    owner_unit=$(systemctl list-units --type=service --state=active --no-legend --plain 2>/dev/null \
                 | awk '{print $1}' | while read -r u; do
                     mp=$(systemctl show "$u" -p MainPID --value 2>/dev/null || echo 0)
                     [ "${mp:-0}" = "$newpid" ] && { echo "$u"; break; }
                   done)
    owner_exe=""
    if [ -n "$owner_unit" ]; then
      owner_exe=$(systemctl show "$owner_unit" -p ExecStart --value 2>/dev/null \
                  | grep -oE '/[^ ;"]*zhipucode2api' | head -1 || true)
    fi
    # 归属断言（三重，全部可非 sudo 取得或已用 sudo 取到）：
    #   ① cmdline 含 zhipucode2api（进程自我声明）
    #   ② 存在 active 单元认领该 pid（⇒ 是服务，不是孤儿）
    #   ③ 该单元的 ExecStart 精确指向 $NEW_DIR/bin/zhipucode2api（⇒ 是我们这个二进制）
    # cwd 不参与判定：它需 sudo 才可读，且信息量弱于 ExecStart（只答"在哪跑"）。
    if [ -n "$newpid" ] && echo "$newcmd" | grep -q zhipucode2api \
       && [ -n "$owner_unit" ] && [ "$owner_exe" = "$NEW_DIR/bin/zhipucode2api" ]; then
      newpid_ok=1
      c_ok "9 新服务持有者已辨明（pid=$newpid 由 systemd 单元 $owner_unit 托管，ExecStart=$owner_exe）"
    elif [ -n "$newpid" ] && echo "$newcmd" | grep -q zhipucode2api \
         && [ -z "$owner_unit" ]; then
      c_no "9 端口 $PREFLIGHT_NEW_PORT 持有者 pid=$newpid 是【孤儿进程】（无 active 单元认领，MainPID 不匹配）—— 幽灵进程？拒发凭据"
      fail=$((fail+1))
    else
      c_no "9 端口 $PREFLIGHT_NEW_PORT 持有者非本项目二进制（pid=${newpid:-?} unit=${owner_unit:-无} exe=${owner_exe:-?} cmd='${newcmd:0:40}'）—— 拒发凭据"
      fail=$((fail+1))
    fi
  else
    c_no "9 端口 $PREFLIGHT_NEW_PORT 无监听（新服务未起？）"; fail=$((fail+1))
  fi

  # 9a. adminapi 端点可达性（端点存在性检查，非文件计数）
  if [ "$newpid_ok" = "1" ]; then
    local admin_probe
    admin_probe=$(curl -s -o /dev/null -w '%{http_code}' -m 5 \
      -H "Authorization: Bearer $(grep -m1 '^ZCODE_ADMIN_KEY=' "$OLD_DIR/.env" 2>/dev/null | cut -d= -f2- || true)" \
      "http://127.0.0.1:$PREFLIGHT_NEW_PORT/admin/api/status" 2>/dev/null || true)
    if [ "$admin_probe" = "200" ]; then
      c_ok "9a adminapi 端点可达（/admin/api/status → 200）"
    else
      c_no "9a adminapi 端点不可达（→ ${admin_probe:-无响应}）—— t13 未完成或鉴权失败"; fail=$((fail+1))
    fi

    # 9b. 账号数（A2 空库的最终防线）—— 身份已辨明才发凭据（H-2）
    local k acc
    k=$(grep -m1 '^ZCODE_ADMIN_KEY=' "$OLD_DIR/.env" 2>/dev/null | cut -d= -f2- || true)
    acc=$(curl -s -m 5 -H "Authorization: Bearer $k" \
      "http://127.0.0.1:$PREFLIGHT_NEW_PORT/admin/api/accounts" 2>/dev/null | grep -o '"id"' | wc -l || true)
    unset k
    if [ "${acc:-0}" -ge 1 ]; then
      c_ok "9b 新服务能列出 $acc 个账号"
    else
      c_no "9b 新服务列出 0 个账号 —— 空库（A2）或 t13 端点未实现（需人工区分）"; fail=$((fail+1))
    fi
  else
    c_no "9a/9b 跳过：持有者身份未辨明（不发凭据）"; fail=$((fail+1))
  fi


  echo
  if [ "$fail" -eq 0 ]; then c_ok "preflight 全通过（9 项）—— 可以切换"; return 0
  else c_no "preflight 未通过（$fail 项失败）—— 拒绝切换"; return 1; fi
}

do_switch(){
  preflight || { c_no "拒绝执行 switch"; exit 1; }

  # 🔴 t15 反向复核修正（H-3）：原实现无备份/无失败检测/无自动回滚 ——
  #    stop+disable 老服务与 enable 新服务之间零失败出口，新服务起不来时「老已停新未起」= 静默灾难。
  local BK="$LOG/cutover-backup-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$BK"
  c_in "0) 备份现状（老 unit 状态 + 库快照）"
  systemctl is-enabled "$OLD_UNIT" > "$BK/old-unit-enabled.txt" 2>&1 || true
  systemctl is-active  "$OLD_UNIT" > "$BK/old-unit-active.txt"  2>&1 || true
  # 🔴 t15 第二轮反向复核（M-1）：本机无 sqlite3 CLI，原写法永远失败 →
  #    改用 python3 stdlib（可移植），且备份失败**不得继续切换**（无备份无法回滚）。
  if python3 -c "
import sqlite3, sys
src = 'file:$OLD_DIR/data/accounts.db?mode=ro'
dst = '$BK/accounts-snapshot.db'
c = sqlite3.connect(src, uri=True)
c.execute(\"VACUUM INTO '%s'\" % dst)
c.close()
" 2>/dev/null; then
    c_ok "库快照已备份（$BK）"
  else
    c_no "库快照备份失败 —— 无备份无法回滚，拒绝切换"
    exit 1
  fi

  # 失败自动回滚：任何一步失败即恢复老服务
  set +e
  rollback_on_fail(){
    c_no "🔴 切换过程失败 —— 自动回滚"
    sudo systemctl stop "$NEW_UNIT" 2>/dev/null || true
    sudo systemctl disable "$NEW_UNIT" 2>/dev/null || true
    if systemctl is-enabled --quiet "$OLD_UNIT" 2>/dev/null || [ "$(cat "$BK/old-unit-enabled.txt" 2>/dev/null)" = "enabled" ]; then
      sudo systemctl enable --now "$OLD_UNIT" 2>/dev/null || true
    fi
    sleep 2
    if curl -s -o /dev/null -m 5 "http://127.0.0.1:$PORT/v1/models" 2>/dev/null; then
      c_ok "回滚成功：老服务已恢复"
    else
      c_no "🔴🔴 回滚后老服务仍未响应 —— 需人工介入（备份在 $BK）"
    fi
    exit 1
  }
  trap 'rollback_on_fail' ERR

  c_in "1) 安装新单元"
  sudo cp "$NEW_DIR/scripts/$NEW_UNIT.service" /etc/systemd/system/ || { c_no "装单元失败"; rollback_on_fail; }
  sudo systemctl daemon-reload || { c_no "daemon-reload 失败"; rollback_on_fail; }

  c_in "2) 停用老服务"
  sudo systemctl stop "$OLD_UNIT"    || { c_no "停老服务失败"; rollback_on_fail; }
  sudo systemctl disable "$OLD_UNIT" || { c_no "disable 老服务失败"; rollback_on_fail; }

  c_in "3) 启用新服务"
  sudo systemctl enable --now "$NEW_UNIT" || { c_no "启新服务失败"; rollback_on_fail; }
  sleep 3

  c_in "4) 验证新服务（失败即回滚）"
  if ! systemctl is-active --quiet "$NEW_UNIT"; then c_no "新服务未 active"; rollback_on_fail; fi
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "http://127.0.0.1:$PORT/v1/models" 2>/dev/null || true)
  if [ "$code" != "200" ]; then c_no "新服务 /v1/models → ${code:-无响应}"; rollback_on_fail; fi
  # 账号数是 A2 空库的最终防线
  local acc
  acc=$(curl -s -m 5 -H "Authorization: Bearer $(grep -m1 '^ZCODE_ADMIN_KEY=' "$OLD_DIR/.env" 2>/dev/null | cut -d= -f2- || true)" \
    "http://127.0.0.1:$PORT/admin/api/accounts" 2>/dev/null | grep -o '"id"' | wc -l || true)
  if [ "${acc:-0}" -lt 1 ]; then c_no "新服务列出 0 账号（A2 空库）"; rollback_on_fail; fi

  trap - ERR
  set -e
  c_ok "✅ 切换成功（新服务 active，$acc 个账号，备份在 $BK）"
  status
}

rollback(){
  c_in "回滚：停新服务、恢复老服务"
  sudo systemctl stop "$NEW_UNIT" 2>/dev/null || true
  sudo systemctl disable "$NEW_UNIT" 2>/dev/null || true
  sudo systemctl enable --now "$OLD_UNIT"
  sleep 3
  status
}

status(){
  echo "--- 老服务 $OLD_UNIT ---"
  systemctl is-active "$OLD_UNIT" 2>/dev/null || true
  echo "--- 新服务 $NEW_UNIT ---"
  systemctl is-active "$NEW_UNIT" 2>/dev/null || true
  echo "--- 端口 $PORT ---"
  ss -tlnp 2>/dev/null | grep ":$PORT " || echo "（无监听）"
}

case "${1:-preflight}" in
  preflight) preflight ;;
  switch)    do_switch ;;
  rollback)  rollback ;;
  status)    status ;;
  *) echo "用法: $0 {preflight|switch|rollback|status}"; exit 2 ;;
esac
