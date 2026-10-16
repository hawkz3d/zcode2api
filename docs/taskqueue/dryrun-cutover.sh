#!/usr/bin/env bash
# 切换演练（Dry Run）辅助脚本 —— 在隔离环境复刻 cutover.sh 的阶段 C 全流程
#
# 与 scripts/cutover.sh 的区别（这是本脚本存在的理由）：
#   cutover.sh 操作【生产】：真实 unit 名 + 端口 9120 + /etc/systemd/system/
#   本脚本只操作【演练沙箱】：假 unit 名 + 假端口 + 瞬态 unit（不落盘 /etc/systemd/system/）
#
# 用法:
#   ./dryrun-cutover.sh setup      建演练沙箱（生产库 VACUUM INTO 快照 + 假 .env）
#   ./dryrun-cutover.sh preflight  复刻 cutover.sh preflight 的 8 项判据并标注可靠性
#   ./dryrun-cutover.sh switch     演练切换：备份 → 装 → 起 → 验证
#   ./dryrun-cutover.sh rollback   演练回滚：停新 → 起老 → 验证数据完整
#   ./dryrun-cutover.sh misconfig  演练 C-1 误配（ZCODE_DATA_DIR 指向空目录）
#   ./dryrun-cutover.sh cleanup    清理沙箱与全部瞬态 unit
#
# 隔离保证（三条，缺一不可）：
#   ① 端口一律 191xx（绝不 9120）
#   ② unit 一律 *-dryrun / *-probe 前缀，经 systemd-run 瞬态加载
#   ③ 数据目录一律 /tmp/dryrun/...（绝不指向 external/zcode2api/data）
set -uo pipefail

D=${DRYRUN_DIR:-/tmp/dryrun}
PORT_NEW=${DRYRUN_PORT_NEW:-19130}
PORT_OLD=${DRYRUN_PORT_OLD:-19131}
U_NEW=zhipucode2api-dryrun
U_MIS=zhipucode2api-dryrun-misconfig
PROD_DB=/opt/zcode2api/data/accounts.db
NEW_BIN=/opt/zhipucode2api/bin/zhipucode2api
FAKE_KEY=dryrun-fake-key-not-real-000000000000

c_ok(){ printf '\033[32m✔ %s\033[0m\n' "$*"; }
c_no(){ printf '\033[31m✘ %s\033[0m\n' "$*"; }
c_in(){ printf '\033[36m▶ %s\033[0m\n' "$*"; }
c_wn(){ printf '\033[33m⚠ %s\033[0m\n' "$*"; }

# ── 隔离护栏：任何操作前先确认没碰生产 ──────────────────────────────────
assert_isolated(){
  case "$D" in /tmp/*) ;; *) c_no "沙箱目录必须在 /tmp 下，实际=$D"; exit 1;; esac
  if [ "$PORT_NEW" = "9120" ] || [ "$PORT_OLD" = "9120" ]; then
    c_no "禁止使用生产端口 9120"; exit 1
  fi
  for u in "$U_NEW" "$U_MIS"; do
    case "$u" in *dryrun*|*probe*) ;; *) c_no "unit 名必须含 dryrun/probe 前缀: $u"; exit 1;; esac
  done
}

setup(){
  c_in "建演练沙箱 $D"
  rm -rf "$D"; mkdir -p "$D"/{old/data,new/bin,new/scripts,work,logs,evidence}

  # 演练库 = 生产库安全快照。VACUUM INTO 是 WAL 库唯一正确的快照手法
  # （禁止只 cp 主文件 —— 见 memory: SQLite运行中数据库快照禁止只cp主文件）
  python3 -c "
import sqlite3
c=sqlite3.connect('file:$PROD_DB?mode=ro',uri=True)
c.execute(\"VACUUM INTO '$D/old/data/accounts.db'\")
c.close()" || { c_no "快照失败"; exit 1; }
  local n; n=$(python3 -c "
import sqlite3;c=sqlite3.connect('file:$D/old/data/accounts.db?mode=ro',uri=True)
print(c.execute('SELECT count(*) FROM accounts').fetchone()[0]);c.close()")
  c_ok "演练库就绪（$n 账号，$(stat -c%s "$D/old/data/accounts.db") B）"

  cat > "$D/old/.env" <<EOF
ZCODE_PORT=$PORT_OLD
ZCODE_HOST=127.0.0.1
ZCODE_DATA_DIR=data
ZCODE_ADMIN_KEY=$FAKE_KEY
EOF
  c_ok "假 .env 就绪（键值全假，绝不含真密钥）"

  cat > "$D/new/scripts/$U_NEW.env" <<EOF
ZCODE_PORT=$PORT_NEW
ZCODE_HOST=127.0.0.1
ZCODE_DATA_DIR=$D/old/data
ZCODE_ADMIN_KEY=$FAKE_KEY
EOF
  cp "$NEW_BIN" "$D/new/bin/" && c_ok "新二进制就位（$(stat -c%s "$D/new/bin/zhipucode2api") B）"

  # 老服务 stand-in：external/zcode2api/ 在 scope 外，禁止真跑 cli.py，
  # 故用一个只读账号表的极小 HTTP 服务代表「老服务」。
  # ⚠️ 必须在 setup 里生成 —— setup 会 rm -rf $D，若留到 rollback 才建则文件不存在
  #    （这正是本脚本首版 rollback 停机窗口虚高到 15.2s 的原因：grep 永不命中，循环跑满）
  cat > "$D/work/old-standin.py" <<'PY'
import http.server, json, sqlite3, sys
DB, PORT = sys.argv[1], int(sys.argv[2])
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/v1/models":
            b = json.dumps({"object":"list","data":[{"id":"GLM-5.3"}]}).encode()
            self.send_response(200); self.send_header("content-type","application/json")
            self.send_header("content-length",str(len(b))); self.end_headers(); self.wfile.write(b)
        elif self.path == "/admin/api/accounts":
            c = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
            rows = c.execute("SELECT id,provider,name FROM accounts ORDER BY provider, created_at").fetchall()
            c.close()
            b = json.dumps({"accounts":[{"id":r[0],"provider":r[1],"name":r[2]} for r in rows]}, ensure_ascii=False).encode()
            self.send_response(200); self.send_header("content-type","application/json")
            self.send_header("content-length",str(len(b))); self.end_headers(); self.wfile.write(b)
        else:
            self.send_response(404); self.end_headers()
    def log_message(self,*a): pass
http.server.HTTPServer(("127.0.0.1",PORT),H).serve_forever()
PY
  c_ok "老服务 stand-in 已生成"

  # 切换前快照（检查表 D4）—— cutover.sh 缺这一步，本脚本显式补上
  python3 -c "
import sqlite3,json
c=sqlite3.connect('file:$D/old/data/accounts.db?mode=ro',uri=True)
rows=[list(r) for r in c.execute('SELECT id,provider,name,status,enabled FROM accounts ORDER BY id')]
json.dump(rows,open('$D/work/snapshot.json','w'),ensure_ascii=False); c.close()"
  c_ok "D4 切换前快照已产出（$D/work/snapshot.json）"
}

preflight_replica(){
  c_in "复刻 cutover.sh preflight 的 8 项判据（标注每项的可靠性）"
  echo
  printf '  %-4s %-26s %s\n' "项" "判据" "可靠性"
  printf '  %-4s %-26s %s\n' "----" "--------------------------" "--------"
  printf '  %-4s %-26s %s\n' "1"  "新服务二进制 -x"          "✅ 直接"
  printf '  %-4s %-26s %s\n' "2"  "仓库内 unit 副本存在"       "🟡 不查生效 unit / 不校验语法"
  printf '  %-4s %-26s %s\n' "3"  ".env 可读"                "✅ 直接"
  printf '  %-4s %-26s %s\n' "4"  "老服务 active"            "✅ 直接"
  printf '  %-4s %-26s %s\n' "5"  "端口有监听"               "🔴 只证明'有人'在听，不证明是老服务"
  printf '  %-4s %-26s %s\n' "6"  "日志目录可写"              "✅ 直接"
  printf '  %-4s %-26s %s\n' "7"  "消费方 2/2"               "🔴 两条判据同一条件 → 恒定 PASS，零判别力"
  printf '  %-4s %-26s %s\n' "8"  "cron 引用盘点"             "🟡 两分支皆 c_ok，永不失败"
  echo
  c_wn "关键：本项检查的是【仓库副本】，不检查 /etc/systemd/system/ 下真正生效的 unit"
  echo "  实测副本与生效 unit: $(diff -q /etc/systemd/system/zhipucode2api.service \
      /opt/zhipucode2api/scripts/zhipucode2api.service >/dev/null 2>&1 \
      && echo '一致（当前恰好一致，但脚本不保证）' || echo '🔴 不一致')"
  echo
  c_wn "关键：8 项全部无法检出 CR-39（A2 空库守卫自败）——见 misconfig 子命令"
}

start_new(){
  local u=$1 envf=$2
  sudo systemd-run --unit="$u" --description="DRYRUN rehearsal" \
    -p Type=simple -p User=appuser -p Group=appuser \
    -p WorkingDirectory="$D/new" -p EnvironmentFile="$envf" \
    -p Restart=always -p RestartSec=5 \
    -p StandardOutput=append:"$D/logs/$u.log" \
    -p StandardError=append:"$D/logs/$u.log" \
    "$D/new/bin/zhipucode2api" serve >/dev/null 2>&1
}

do_switch(){
  c_in "演练切换（假 unit=$U_NEW，假端口=$PORT_NEW）"
  local t0 t1
  t0=$(date +%s.%N)

  c_in "① 备份（D4 快照，本脚本补的步骤）"
  cp -a "$D/old/data/accounts.db" "$D/work/backup-accounts.db"
  c_ok "备份完成（$(stat -c%s "$D/work/backup-accounts.db") B）"

  c_in "② 装（瞬态 unit，不落盘 /etc/systemd/system/）"
  start_new "$U_NEW" "$D/new/scripts/$U_NEW.env"
  c_ok "瞬态 unit 已加载"

  c_in "③ 起 + 等待就绪"
  # 0.1s 粒度轮询，最多 10s。用 0.1s 而非 1s 是为了让「切换耗时」这个
  # 运维关心指标不被轮询粒度污染（首版用 sleep 1 会引入最多 1s 虚高）。
  local i
  for i in $(seq 1 100); do
    ss -tln 2>/dev/null | grep -q "127.0.0.1:$PORT_NEW " && break
    sleep 0.1
  done
  t1=$(date +%s.%N)
  if ss -tln 2>/dev/null | grep -q "127.0.0.1:$PORT_NEW "; then
    c_ok "监听 127.0.0.1:$PORT_NEW（$(systemctl is-active "$U_NEW")，$(python3 -c "print(f'{$t1-$t0:.2f}s')")）"
  else
    c_no "🔴 10s 内未监听 —— 切换失败（真实切换中此处应触发回滚）"
  fi

  c_in "④ 验证"
  echo "    /v1/models        → $(curl -s -o /dev/null -w '%{http_code}' -m 5 http://127.0.0.1:$PORT_NEW/v1/models)"
  echo "    /admin/api/accounts → $(curl -s -o /dev/null -w '%{http_code}' -m 5 http://127.0.0.1:$PORT_NEW/admin/api/accounts)  （t13 未完成时应为 404）"
  echo "    日志: $(head -1 "$D/logs/$U_NEW.log" 2>/dev/null)"
  python3 -c "print(f'    切换耗时: {$t1-$t0:.3f}s')"
}

do_rollback(){
  c_in "演练回滚：停新 → 起老"
  local t0 t1
  t0=$(date +%s.%N)
  sudo systemctl stop "$U_NEW" 2>/dev/null; sudo systemctl reset-failed "$U_NEW" 2>/dev/null
  c_ok "新服务已停（$(systemctl is-active "$U_NEW")）"

  # 老服务用 stand-in 代表（external/zcode2api/ 在 scope 外，禁止真跑 cli.py）
  [ -f "$D/work/old-standin.py" ] || { c_no "stand-in 缺失（应先在 setup 生成）"; exit 1; }
  nohup python3 "$D/work/old-standin.py" "$D/old/data/accounts.db" "$PORT_OLD" \
    > "$D/logs/old-standin.log" 2>&1 &
  echo $! > "$D/work/old.pid"
  local i
  for i in $(seq 1 100); do
    ss -tln 2>/dev/null | grep -q "127.0.0.1:$PORT_OLD " && break
    sleep 0.1
  done
  t1=$(date +%s.%N)
  if ss -tln 2>/dev/null | grep -q "127.0.0.1:$PORT_OLD "; then
    c_ok "老服务 stand-in 已起（PID $(cat "$D/work/old.pid")）"
    python3 -c "print(f'    回滚停机窗口: {$t1-$t0:.3f}s')"
  else
    c_no "🔴 老服务 10s 内未监听 —— 回滚失败（真实场景：检查老 unit 文件与 .env 是否仍在）"
  fi

  c_in "回滚后数据完整性（与切换前快照逐条比对）"
  python3 -c "
import sqlite3,json,hashlib
c=sqlite3.connect('file:$D/old/data/accounts.db?mode=ro',uri=True)
after=[list(r) for r in c.execute('SELECT id,provider,name,status,enabled FROM accounts ORDER BY id')]
print('    integrity_check='+c.execute('PRAGMA integrity_check').fetchone()[0]); c.close()
before=json.load(open('$D/work/snapshot.json'))
print('    账号数: 前=%d 后=%d'%(len(before),len(after)))
print('    ⇒ 数据完整性: '+('✅ 逐条一致' if before==after else '🔴 不一致'))"
}

do_misconfig(){
  c_in "演练 C-1 误配：ZCODE_DATA_DIR 指向新仓空 data/（ADR-005 A2）"
  mkdir -p "$D/new/data"
  cat > "$D/new/scripts/misconfig.env" <<EOF
ZCODE_PORT=$PORT_NEW
ZCODE_HOST=127.0.0.1
ZCODE_DATA_DIR=$D/new/data
ZCODE_ADMIN_KEY=$FAKE_KEY
EOF
  echo "    误配目标: $D/new/data（空目录，$(ls -A "$D/new/data" | wc -l) 项）"

  start_new "$U_MIS" "$D/new/scripts/misconfig.env"
  c_wn "第 1 次启动：守卫 FATAL"
  sleep 2
  echo "    unit=$(systemctl is-active "$U_MIS")  端口=$(ss -tln 2>/dev/null | grep -c "127.0.0.1:$PORT_NEW ")"
  head -1 "$D/logs/$U_MIS.log" 2>/dev/null | sed 's/^/    /'

  c_wn "等待 Restart=always 自动重启（RestartSec=5）"
  sleep 8
  local st; st=$(systemctl is-active "$U_MIS")
  echo "    unit=$st  端口=$(ss -tln 2>/dev/null | grep -c "127.0.0.1:$PORT_NEW ")"
  if [ "$st" = "active" ]; then
    c_no "🔴 CR-39 复现：守卫拦截后第 2 次启动【静默放行】，服务跑在 0 账号库上"
    echo "      /v1/models → $(curl -s -o /dev/null -w '%{http_code}' -m 5 http://127.0.0.1:$PORT_NEW/v1/models)  ← 200 会让人以为切换成功"
    echo "      库内容: $(python3 -c "
import sqlite3
c=sqlite3.connect('file:$D/new/data/accounts.db?mode=ro',uri=True)
print('accounts=%s tables=%s'%(c.execute('SELECT count(*) FROM accounts').fetchone()[0] if c.execute(\"SELECT count(*) FROM sqlite_master WHERE name='accounts'\").fetchone()[0] else 'no-table',
      [r[0] for r in c.execute(\"SELECT name FROM sqlite_master WHERE type='table'\")]));c.close()" 2>&1)"
  fi
  sudo systemctl stop "$U_MIS" 2>/dev/null; sudo systemctl reset-failed "$U_MIS" 2>/dev/null
}

do_cleanup(){
  c_in "清理演练沙箱"
  for u in "$U_NEW" "$U_MIS" dryrun-probe envtest1 envtest2 envtest3 envtest4 reltest1 reltest2; do
    sudo systemctl stop "$u" 2>/dev/null; sudo systemctl reset-failed "$u" 2>/dev/null
  done
  [ -f "$D/work/old.pid" ] && kill "$(cat "$D/work/old.pid")" 2>/dev/null
  local ev=/opt/temp/t15-dryrun
  mkdir -p "$ev"; cp "$D"/logs/*.log "$ev/" 2>/dev/null; cp "$D/work/snapshot.json" "$ev/" 2>/dev/null
  c_ok "证据已归档 $ev"
  rm -rf "$D"
  c_ok "沙箱已删"
  echo "  瞬态 unit 残留: $(systemctl list-units --all 2>/dev/null | grep -cE 'dryrun|envtest|reltest|misconfig')"
  echo "  /etc/systemd/system/ dryrun 残留: $(ls /etc/systemd/system/ | grep -c dryrun)"
  echo "  生产: $(systemctl is-active zcode2api) 9120 → $(curl -s -o /dev/null -w '%{http_code}' -m 5 http://127.0.0.1:9120/v1/models)"
}

assert_isolated
case "${1:-setup}" in
  setup)      setup ;;
  preflight)  preflight_replica ;;
  switch)     do_switch ;;
  rollback)   do_rollback ;;
  misconfig)  do_misconfig ;;
  cleanup)    do_cleanup ;;
  *) echo "用法: $0 {setup|preflight|switch|rollback|misconfig|cleanup}"; exit 2 ;;
esac
