# 切换演练（Dry Run）报告 —— 阶段 C 全流程隔离复刻

- **任务**: t15（verification / 切换演练 Dry Run）
- **执行者**: Hilkiah（架构师 · 契约冻结与拓扑）
- **日期**: 2026-10-15
- **性质**: 隔离演练。**全程未触碰生产**：老服务 9120 持续 active，真 systemd unit 未动，生产库未被写。
- **产物**: 本报告 + 辅助脚本 `docs/taskqueue/dryrun-cutover.sh`（可执行，六子命令）

---

## 一、演练环境与隔离方式

### 1.1 为什么需要隔离手段的选型

`cutover.sh` 直接操作生产（真 unit 名、端口 9120、`/etc/systemd/system/`）。演练要复刻**同一流程**却不能碰生产，因此首要问题是「用什么承载假 unit」。

候选与实测结论：

| 手段 | 落盘位置 | 是否可用 | 结论 |
|---|---|---|---|
| `cp` 假 unit 到 `/etc/systemd/system/` | 生产 unit 目录 | ❌ | **超出范围**（scope 明令禁止） |
| `systemd-analyze verify` | 不落盘 | ✅ 仅静态语法 | 能验语法，不能验行为 |
| **`systemd-run` 瞬态 unit** | **`/run/systemd/transient/`** | ✅ | **选定**：完整 systemd 语义，不落盘生产目录 |

实测确认瞬态 unit 不污染生产目录：

```
$ sudo systemd-run --unit=dryrun-probe --property=Restart=no /bin/true
Running as unit: dryrun-probe.service
$ ls /etc/systemd/system/dryrun-probe.service
ls: cannot access '/etc/systemd/system/dryrun-probe.service': No such file or directory   ← 未落盘
$ ls /run/systemd/transient/ | head
docker-010ce435….scope
```

> **这是本次演练最重要的方法论收获**：`systemd-run` 提供了「真 systemd 语义 + 零生产目录写入」的组合，使「在真 systemd 下演练」与「不碰生产」不再互斥。此前若用 `cp` 假 unit，演练本身就会污染生产 unit 目录，失去隔离意义。

### 1.2 三重隔离护栏

辅助脚本 `dryrun-cutover.sh` 在 `assert_isolated()` 中硬编码三条护栏，任何子命令执行前先校验：

1. **端口**：一律 191xx，**显式拒绝 9120**（`if [ "$PORT_NEW" = "9120" ]`）
2. **unit 名**：必须含 `dryrun`/`probe` 前缀（`case "$u" in *dryrun*|*probe*)`）
3. **数据目录**：必须位于 `/tmp/` 下（`case "$D" in /tmp/*)`）

> 护栏实测有效：首版因 `U_MIS=zhipucode2api-misconfig` 不含 `dryrun` 而被自己拦下（见 §四 F-16），证明护栏不是装饰。

### 1.3 演练沙箱构成

```
/tmp/dryrun/
├── old/data/accounts.db    ← 生产库快照（VACUUM INTO，6 账号，53248 B）
├── old/.env                ← 假 .env（键名真实 / 值全假，绝不含真密钥）
├── new/bin/zhipucode2api   ← 新二进制（16516258 B）
├── new/scripts/*.env       ← 演练 unit 的环境文件（端口 19130 + 绝对数据目录）
├── work/                   ← 备份、快照、stand-in
├── logs/                   ← 各 unit 日志
└── evidence/
```

**演练库快照手法**：`VACUUM INTO`（生产库以 `mode=ro` 打开）。这是 WAL 库唯一正确的快照方式——直接 `cp` 主文件会丢 WAL 帧（记忆库有专门事故条目：`SQLite运行中数据库快照禁止只cp主文件`）。

```
$ python3 -c "sqlite3.connect('file:…/data/accounts.db?mode=ro',uri=True).execute(\"VACUUM INTO '/tmp/dryrun/old/data/accounts.db'\")"
✔ 演练库就绪（6 账号，53248 B）
   integrity_check=ok  rows=6
```

**假 .env 的构造原则**：键名与真 `.env` 一致（使代码路径完全相同），**值全部伪造**——包括 `ZCODE_ADMIN_KEY`。这样演练输出可直接引用而不泄露生产密钥。

```
ZCODE_PORT=19130
ZCODE_HOST=127.0.0.1
ZCODE_DATA_DIR=/tmp/dryrun/old/data
ZCODE_ADMIN_KEY=dryrun-fake-key-not-real-000000000000   ← 假值
```

### 1.4 老服务的代表方式（一个必须说明的替代）

真老服务是 `external/zcode2api/.venv/bin/python cli.py serve`，而 `external/zcode2api/` 在 scope 外（禁止运行）。因此回滚环节的老服务由 **stand-in** 代表：一个仅 20 行的 Python HTTP 服务，只实现两个端点：

- `GET /v1/models` → 200
- `GET /admin/api/accounts` → **只读**查演练库，返回真实账号列表

> **这个替代的边界必须写明**：stand-in 验证的是「**回滚动作链**（停新→起老→数据可读）」成立，**不**验证「真 cli.py 能被 systemd 拉起」。后者的验证属真切换，不在演练范围。本报告所有涉及老服务的结论都限于前者。

---

## 二、逐步记录（命令 + 输出 + 耗时）

全部步骤由 `dryrun-cutover.sh` 复现；下列输出为实测原文（`✘`/`⚠` 为脚本输出色彩，此处保留文字）。

### 2.0 基线（演练前）

```
$ systemctl is-active zcode2api && curl -s -o /dev/null -w '%{http_code}' -m 5 http://127.0.0.1:9120/v1/models
active
200
$ sudo -n systemctl --version | head -1
systemd 252 (252.39-1~deb12u1)
```

### 2.1 阶段 C-前置：备份 + D4 快照（`switch` ①）

```
▶ ① 备份（D4 快照，本脚本补的步骤）
✔ 备份完成（53248 B）
```

D4 快照内容（6 账号逐条，`id|provider|status|enabled`）：

```
laohao-bigmodel-2420b3c2     bigmodel  active enabled=1
oauth-apikey-8808fbac        zai       active enabled=1
oauth-apikey-c5c641a1        zai       active enabled=1
oauth-login-8523aba4         zai       active enabled=1
oauth-login-ea92aa60         zai       active enabled=1
xinhao-2000w-02f71f86        bigmodel  active enabled=1
快照 sha256=170c705a0f777d9eed45c0b172be6608
```

### 2.2 阶段 C-3：安装新 unit（`switch` ②）

```
▶ ② 装（瞬态 unit，不落盘 /etc/systemd/system/）
✔ 瞬态 unit 已加载
```

对应真流程中 `sudo cp "$NEW_DIR/scripts/$NEW_UNIT.service" /etc/systemd/system/` + `daemon-reload`。演练用 `systemd-run` 一步完成，耗时 **0.035 s**。

### 2.3 阶段 C-4：启动 + 就绪等待（`switch` ③）

```
▶ ③ 起 + 等待就绪
✔ 监听 127.0.0.1:19130（active，0.16s）
```

**耗时（运维关心的核心指标）**：

| 指标 | 实测值 |
|---|---|
| 装 unit（`systemd-run`） | 0.035 s |
| 起服务 → 端口就绪 | **0.16 s** |
| **切换总耗时（备份→就绪）** | **0.161 s** |

> 首版脚本此值为 1.055 s，虚高来自 1 s 粒度轮询；改 0.1 s 粒度后回落到 0.16 s（见 §四 F-16）。

### 2.4 阶段 C-5：验证（`switch` ④）

```
▶ ④ 验证
    /v1/models        → 200
    /admin/api/accounts → 404  （t13 未完成时应为 404）
    日志: 2026/10/15 23:41:52 zhipucode2api listening on 127.0.0.1:19130 (db=/tmp/dryrun/old/data/accounts.db)
```

**C-1 / C-2 验证（`systemctl show` 口径）**：

```
$ systemctl show -p MainPID --value zhipucode2api-dryrun
1053726
$ sudo cat /proc/1053726/environ | tr '\0' '\n' | grep -E 'ZCODE_DATA_DIR|ZCODE_PORT'
ZCODE_PORT=19130
ZCODE_DATA_DIR=/tmp/dryrun/old/data        ← 绝对路径，C-1 成立
```

**C-3 两条判据的实际效力（关键）**：

| 判据 | 检查表口径 | 演练实测 |
|---|---|---|
| ① 日志断言 `pool: 6 accounts` | 「推荐，切换前可判」 | 🔴 **失效**：`grep -c 'pool: .* accounts'` = **0**，日志仅一行 `listening on …` → **t14 尚未加该日志** |
| ② `/proc/<MainPID>/environ` 查 `ZCODE_DATA_DIR` | 「或」的备选 | ✅ **有效**：实测输出如上 |

**C-3 的结论是严酷的**：唯一「切换前可判」的判据①当前不存在；剩下能用的判据②只验证「**环境变量配对了**」，**不验证「账号真的读到了」**——因为 `ZCODE_DATA_DIR` 正确却指向空库时，判据②照样通过。而「账号真的读到了」在 t13 完成前**没有任何可观测出口**（`/admin/api/accounts` 返回 404）。这正是 F-1 之所以致命的原因。

### 2.5 冒烟测试（`scripts/smoke.sh`）

```
$ bash scripts/smoke.sh 19130
── 1. 可达性 ──   ✔ GET /v1/models 无鉴权应 200 (HTTP 200)   ✔ 响应含 object:list 结构
── 2. 网关端点 ── ✔ 未知路径应 404 (HTTP 404)               ✔ POST /v1/chat/completions 空 body → 400
── 3. 管理 API ── ✘ GET /admin/api/status (带 key) 期望 200 实际 404
                  ✘ GET /admin/api/status (无 key) 期望 401 实际 404
── 4. 页面 ──     ✔ /admin/login → 200   ✔ /admin → 307   ✔ / → 307
通过 7 / 失败 2   ❌ 冒烟未通过        耗时 0.082s
```

2 项失败**归因明确**：t13（admin API）未完成。**注意**：`smoke.sh` 第 7 行 `OLD_ENV` 硬编码老仓 `.env`——演练环境用的是假 key，即使 t13 完成，第 3 组在**演练环境**下仍会 401 失败。该脚本设计上就是「对生产跑」的工具，用于演练需加环境变量覆盖入口（记为建议 F-12）。

### 2.6 回滚演练（`rollback`）

从「新服务运行中」回到「老服务运行中」：

```
▶ 演练回滚：停新 → 起老
✔ 新服务已停（inactive）
✔ 老服务 stand-in 已起（PID 1073720）
    回滚停机窗口: 0.166s

▶ 回滚后数据完整性（与切换前快照逐条比对）
    integrity_check=ok
    账号数: 前=6 后=6
    ⇒ 数据完整性: ✅ 逐条一致
```

| 指标 | 实测值 |
|---|---|
| 停新服务 | 0.046 s |
| 起老服务 → 端口就绪 | 0.12 s |
| **回滚停机窗口** | **0.166 s** |
| **数据完整性** | ✅ 逐条一致（sha256 前后均 `170c705a…`） |

**回滚验证的独立交叉**：不只比「前后 sha256」，还与**备份文件**三方比对：

```
切换前快照 sha256=170c705a0f777d9eed45c0b172be6608
回滚后实测 sha256=170c705a0f777d9eed45c0b172be6608
账号数: 前=6 后=6   ⇒ 数据完整性: ✅ 逐条一致
备份文件比对: ✅ 与备份一致
integrity_check=ok
```

### 2.7 决定性实验：C-1 误配（`misconfig`）

**这是本次演练最重要的单点实验。** 场景：`ZCODE_DATA_DIR` 指向新仓自带的**空 `data/`**（ADR-005 的 A2 情形）——即切换时最常见的配置错误。

```
▶ 演练 C-1 误配：ZCODE_DATA_DIR 指向新仓空 data/（ADR-005 A2）
    误配目标: /tmp/dryrun/new/data（空目录，0 项）

⚠ 第 1 次启动：守卫 FATAL
    unit=activating  端口=0
    zhipucode2api: database /tmp/dryrun/new/data/accounts.db was just created and has no usable
    accounts table (store: list accounts: SQL logic error: no such table: accounts (1))
    - ZCODE_DATA_DIR is probably wrong (ADR-005 A2)

⚠ 等待 Restart=always 自动重启（RestartSec=5）
    unit=active  端口=1
✘ 🔴 CR-39 复现：守卫拦截后第 2 次启动【静默放行】，服务跑在 0 账号库上
      /v1/models → 200  ← 200 会让人以为切换成功
      库内容: accounts=no-table tables=[]
```

**完整日志（真 systemd 环境下 4 行，这是 CR-39 的第一手证据）**：

```
1  zhipucode2api: database …/new/data/accounts.db was just created and has no usable accounts
   table (…) - ZCODE_DATA_DIR is probably wrong (ADR-005 A2)          ← 第 1 次：守卫 FATAL
2  2026/10/15 23:42:03 zhipucode2api listening on 127.0.0.1:19130 (db=…/new/data/accounts.db)
                                                                      ← 第 2 次：静默放行
3  2026/10/15 23:42:07 warning: gateway: read gateway_key: store: get setting "gateway_key":
   SQL logic error: no such table: meta (1)                           ← 空库导致的持续告警
4  2026/10/15 23:42:07 zhipucode2api shutting down
```

**CR-39 在此前（t5b）的形态是「手动连起两次」；本次演练把它推进到了「真 systemd + `Restart=always` 下的自动复现」** —— 不需要任何人工干预，systemd 自己就会完成「拦截→重启→放行」三步，且最终状态是 `active`。

### 2.8 隔离确认（每次演练后固定执行）

```
$ ls /etc/systemd/system/ | grep -c dryrun
0                                    ← 真 unit 目录零写入
$ systemctl list-units --all | grep -cE 'dryrun|envtest|reltest|misconfig'
0                                    ← 瞬态 unit 零残留
$ systemctl is-active zcode2api && curl -s -o /dev/null -w '%{http_code}' -m 5 http://127.0.0.1:9120/v1/models
active
200                                  ← 生产全程存活
$ stat -c '%y' …/external/zcode2api/data/accounts.db
2026-10-15 22:16:15.410625505 +0800  ← 生产库 mtime 未变（早于演练开始 23:28）
```

---

## 三、发现清单（按严重度）

> 每条含 **现象 / 证据 / 建议**。凡涉及 `cutover.sh` 的条目均**未修改该脚本**（scope 明令禁止）——本报告只记录缺陷与建议。

### F-1 🔴🔴 **CR-39 在真 systemd 下自动复现：`Restart=always` 使空库守卫第 2 次静默放行**

- **现象**：`ZCODE_DATA_DIR` 误配为空目录时，守卫第 1 次 FATAL（正确行为），但 systemd `Restart=always` + `RestartSec=5` 在 5 秒后自动重启，**第 2 次静默放行**，服务进入 `active`、端口监听、`/v1/models` 返回 **200**，日志无任何告警。
- **证据**：§2.7 完整 4 行日志（`/tmp/dryrun/new/data/accounts.db` 从「无表」变「4096 B 空库」后第二次即通过）；`systemctl is-active` = `active`；`curl /v1/models` = 200；库内 `accounts=no-table tables=[]`。此形态与 t5b 的 `guardFreshDatabase` 代码分析（`internal/../cmd/zhipucode2api/main.go` 的 `if !freshFile { return nil }`）**完全吻合**：守卫有效性绑定在「本进程是否创建了该文件」，而 `store.Open` 创建文件却不建表 → 守卫自己造出的 4096 B 产物让它从下一次启动起永久失效。
- **建议**：**切换前必修**。三条修复路径（t5b 已提交 captain，按推荐度排序）：① 判据与 `freshFile` 解耦——文件缺失→LOUD FATAL 且不创建；无表→FATAL；0 行→FATAL；确有合法空池需求时用显式 opt-in（如 `ZCODE_ALLOW_EMPTY_POOL=1`）；② 拦截时删除自己刚创建的空产物（严格限定「本进程创建 + 0 字节/无表」），使拦截幂等；③ 增加启动日志 `pool: %d accounts`（可观测性，**不能替代守卫**）。
- **与 C-3 的关系**：C-3 判据①依赖的正是建议③。当前 t14 未实现（见 F-2），故 C-3 在切换时**无法判真**。

### F-2 🔴 **`cutover.sh` 对检查表阶段 C/D 的 8 个关键项零覆盖**

- **现象**：检查表的 `C0`、`C-1`、`C-2`、`C-3`、`D1`～`D4` 在 `cutover.sh` 中**出现次数均为 0**。
- **证据**：
  ```
  $ for item in C0 C-1 C-2 C-3 D1 D2 D3 D4; do grep -c "$item" scripts/cutover.sh; done
  0 0 0 0 0 0 0 0
  $ grep -cE 'cp |backup|snapshot|快照|备份' scripts/cutover.sh
  1        ← 唯一命中在注释/无关处，switch() 内无任何备份动作
  ```
  `do_switch()` 全文仅 7 步：`preflight` → `cp` unit → `daemon-reload` → `stop+disable` 老 → `enable --now` 新 → `sleep 3` → `status` → `curl /v1/models`。**无备份、无快照、无 C-1/C-2 配置校验、无 C-3 账号数断言、无 C0 guard 让位**。
- **建议**：切换**不得**只依赖 `cutover.sh`。至少在 switch 前后各插入一次「账号数断言」（t14 落地后用 `pool: N accounts` 日志；在那之前用只读 SQLite 计数）；C0 必须在停老 unit **之前**单独执行。
- **补充证据（C0 已实测坐实）**：`workspace/scripts/z2a_gateway_guard.sh:25` 只认 `zcode2api.service`，且 cron 每 2 分钟跑一次。该脚本**未**被 `cutover.sh` 触及。

### F-3 🔴 **`switch` 失败无自动回滚：老服务已 stop + disable，故障窗口无兜底**

- **现象**：`do_switch()` 先 `stop` 且 `disable` 老服务，再 `enable --now` 新服务；若新服务起不来（如 F-1 类故障），**老服务已停且已 disable**，9120 无监听，而脚本不调用 `rollback`。
- **证据**：`do_switch()` 无 `trap`、无失败检测分支、无 `rollback()` 调用；`set -e`（L11）只会在未捕获错误时中止，**中止不等于回滚**。
- **建议**：加 `trap 'rollback' ERR` 或在 `enable --now` 后显式检测 `is-active` + 端口，失败即调用 `rollback()`；或改为「先 enable 新 unit 但不 stop 老 unit，确认新服务可起（非 9120 端口预验证，即检查表 C1）后再切换端口归属」。

### F-4 🟠 **preflight 项 7「消费方 2/2」零判别力：两个子条件实为同一谓词**

- **现象**：项 7 的两条子条件 (a) 「`.env` 可读 且 `grep -cE '^ZCODE_ADMIN_KEY='` ≥1」、(b) 「`.env` 文件存在」——**(a) 蕴含 (b)**。故 `consumers_ok` 必然为 2，`>= 2` 必然成立。该检查项**在任何输入下都通过**。
- **证据**：`cutover.sh` 项 7 代码；实测 `preflight` 全 8 项 PASS（见 F-5 同一输出）。
- **建议**：把项 7 改为检查**真正的消费方**：cron 行是否可达（`z2a_patrol.sh` 能取到 key）、`dsh-zcode-plan` 的 `accountsDb` 是否仍有效、guard 脚本是否能识别新 unit——即把 §F-10/F-11 的漏项纳入。

### F-5 🟠 **preflight 项 8「cron 引用盘点」无失败路径：两分支均 `c_ok`**

- **现象**：项 8 无论 `cron_refs` 为多少都走 `c_ok` 分支，**从不** `fail=$((fail+1))`。该项永远 PASS。
- **证据**：`cutover.sh` 项 8 代码（`if …; then c_ok … else c_ok …` 两分支同结论）；全文 `fail=$((fail+1))` 只出现在 L35/38/50/54/65，均不在项 8。
- **建议**：项 8 应在发现 `cron_refs > 0` 时给出**待办清单**（而非 PASS）；或明确降级为「信息展示项」，不参与 `fail` 计数——但需在脚本注释中写明，避免读者误以为它已把关。

### F-6 🟠 **preflight 项 2 检查「仓库副本」而非「生效 unit」，且不校验语法**

- **现象**：项 2 判据是 `[ -f "$NEW_DIR/scripts/$NEW_UNIT.service" ]`——即**仓库里有没有那个文件**，而不是 `/etc/systemd/system/` 下**真正生效的 unit** 是否就位、是否合法。
- **证据**：`cutover.sh` 项 2 代码。实测当前副本与生效 unit **恰好一致**（`diff` 零差异），但这**是巧合而非保证**——脚本无法发现「生效 unit 是旧的」或「unit 语法错误」。
- **建议**：项 2 增加 `systemd-analyze verify /etc/systemd/system/$NEW_UNIT.service` 与 `diff` 副本/生效版；或把「安装」与「校验」合并为「装后校验生效 unit 指纹」。

### F-7 🟠 **preflight 项 5 只证明「有人」在监听 9120，不证明是老服务**

- **现象**：项 5 用 `ss -tln | grep ":$PORT "` 或 `curl -s -o /dev/null http://127.0.0.1:$PORT/` 判定端口健康。二者都只说明**某进程**占着 9120，不区分是老服务、新服务、还是无关进程。
- **证据**：`cutover.sh` 项 5 代码（含 L41-42 关于 `pipefail`/SIGPIPE 的注释——该注释本身是正确的工程细节，但不解决身份问题）。演练中我实测到 19120 被**他人**的 `./bin/zhipucode2api serve`（PID 907547，22:16:58 启动）占用，说明「端口有人在听」与「是我们的服务」是两件事。
- **建议**：项 5 增加身份判定：`systemctl show -p MainPID` + `ss -tlnp` 交叉，确认监听者 PID 属预期 unit。

### F-8 🟠 **D4「切换前快照」缺失 → 「切换成功」与「空库灾难」不可区分**

- **现象**：检查表 D4 要求记录切换前账号快照，但 `cutover.sh` 不产出快照。切换后若库被换成空库，**没有任何基线可比对**。
- **证据**：§F-2 的 `grep` 结果（备份/快照关键词在 switch 流程内零命中）；§2.7 实证「空库服务的 `/v1/models` 返回 200」，而 `cutover.sh` 的 switch 尾步正是 `curl /v1/models` → 故**弱冒烟 + 无快照 = 空库灾难静默通过**。我在演练中手工补了快照（`work/snapshot.json`），才得以完成 §2.6 的完整性比对。
- **建议**：`switch` 前置步骤必须包含「只读快照（账号数 + 各账号 id/status）+ 切换后同一口径复测 + 差异即报警」。

### F-9 🟠 **C0 的真实条件与检查表描述不符：guard 的第一判据是「端口已在听」**

- **现象**：检查表 C0 称「guard 若仍认老 unit，会在老 unit 停后 2 分钟内**重新抢回** 9120」。实测复刻显示：guard 的**第一条判据是 `ss -tln | grep "127.0.0.1:$PORT "`，命中即 `exit 0`**——端口已被新服务占住时，guard **直接静默让位**，根本不进入 unit 判定。**只有在「端口空窗」时**（新服务崩溃且 systemd 尚未重启的窗口）guard 才会落到 `nohup` 兜底分支并**拉起老服务**。
- **证据**：隔离复刻 `guard-replica.sh`（仅替换 unit 名与拉起命令，其余逐字保留）：
  - 状态 A（端口在听）：`guard exit=0`，`guard.log` **0 行**（静默让位）
  - 状态 B（端口空窗）：`guard.log` 出现 `网关 19132 未监听 → 拉起` → 落入兜底
  另：原脚本首判据代码为 `if ss -tln | grep -q "127.0.0.1:${PORT} "; then exit 0; fi`。
- **建议**：C0 的描述应精确化为「**端口空窗期**（崩溃/重启窗口）guard 会拉起老服务」——风险依然存在（尤其配合 F-1：新服务在 5 秒重启窗口内端口确实为空），但**触发条件比检查表所述窄**。修复动作不变：让 guard 同时识别 `zhipucode2api.service`，且在停老 unit **之前**完成。

### F-10 🟠 **外部依赖清单遗漏 `dsh-zcode-plan`：硬编码老仓 `accounts.db`**

- **现象**：检查表 `cutover-external-deps.md` 只列 6 项依赖，且判定 `dsh-zcode-*` 插件「不需改」。但 `dsh-zcode-plan` **硬编码老仓数据库路径**。
- **证据**：
  ```
  $ grep -n 'accountsDb' workspace/dsh-zcode-plan/lib/index.js
  109:    accountsDb: String(userConfig.accountsDb ?? "/opt/zcode2api/data/accounts.db"),
  148:    const currentJwt = async () => cfg.jwt || (await jwtOf(cfg.accountsDb, cfg.accountName));
  $ grep -rn 'accountsDb' /opt/appdata/.dsh/settings.yaml
  （无输出 → 无配置层覆盖，走硬编码默认值）
  ```
- **建议**：**老仓目录必须保留**（与 E4「归档老仓」冲突，见 F-13）；或在 `settings.yaml` 为 `dsh-zcode-plan` 显式配置 `accountsDb` 指向新路径；或该插件改为从环境/配置解析。检查表依赖清单应补入此条并标明「需配置覆盖」。

### F-11 🟠 **外部依赖清单遗漏 `z2a-patches/entitlements.py`：对老仓 Python 源码打补丁**

- **现象**：`workspace/scripts/z2a-patches/` 是对老仓 Python 源码的**本地补丁集**，切换后必然失效。
- **证据**：
  ```
  $ grep -n 'TARGET' workspace/scripts/z2a-patches/entitlements.py
  19:TARGET = "/opt/zcode2api/app/quota.py"
  $ head -3 workspace/scripts/z2a-patches/README.md
  # zcode2api 本地补丁集
  上游：dengyie/zcode2api（AGPL），部署于 `external/zcode2api`。升级上游后需重跑本目录补丁。
  ```
- **建议**：Go 版必须**在源码层原生实现**该补丁语义（`plans[].entitlements[]` 解析），否则切换即功能回退。检查表依赖清单应补入此条并标明「语义需移植」。

### F-12 🟡 **`smoke.sh` 硬编码老仓 `.env`，无环境变量覆盖入口**

- **现象**：`smoke.sh:7` `OLD_ENV=/opt/zcode2api/.env` 硬编码。演练环境（假 key）下第 3 组必然 401 失败，无法用于预验证。
- **证据**：`smoke.sh` L5-7；§2.5 实测 2 项失败。
- **建议**：改为 `OLD_ENV="${Z2A_ENV:-…}"` 可覆盖形式，使该脚本能在非 9120 端口/非生产 key 的环境下做预验证（检查表 C1 正需要这个能力）。

### F-13 🟡 **D2（老仓可立即启动）与 E4（归档改名老仓）顺序约束未显式写明**

- **现象**：D2 要求回滚时老仓「仍在原地可立即启动」；E4 要求归档改名老仓。二者互斥，但检查表未把「E4 必须在 D 段全部完成且观察期结束后」写成硬约束。
- **证据**：实测「归档后老仓脚本仍可用」的反例——把 `z2a_patrol.sh` 放到归档目录后：
  ```
  $ grep: /tmp/dryrun/archive/.env: No such file or directory
  ```
  patrol 的 `ROOT="${Z2A_ROOT:-$(cd "$(dirname …)/.." && pwd)}"` 会解析到归档目录，而归档目录**没有 `.env`** → 取不到 `ZCODE_ADMIN_KEY`。（同理 F-10 的 `dsh-zcode-plan` 也依赖老仓路径。）
- **建议**：检查表增加显式顺序约束「**E4 不得早于观察期结束 + 回滚窗口关闭**」，并列出「归档即失效」的依赖清单（patrol、dsh-zcode-plan、z2a-patches）。

### F-14 🟡 **`rollback()` 不校验老 unit 文件与老 `.env` 是否仍存在**

- **现象**：`rollback()` 仅 `systemctl stop 新` + `disable 新` + `enable --now 老` + `sleep 3` + `status`。若老 unit 文件已被误删/覆盖，或老 `.env` 不可读，回滚会失败而**错误信息不明显**。
- **证据**：`cutover.sh` `rollback()` 全文 5 行，无任何前置存在性校验。
- **建议**：`rollback()` 开头加 `[ -f /etc/systemd/system/$OLD_UNIT.service ] && [ -r "$OLD_DIR/.env" ]` 断言，失败时 LOUD 报错并给出人工恢复指引。

### F-15 🟡 **`status()` 端口匹配无尾空格，可能误匹配 `91200`**

- **现象**：`preflight` 项 5 用 `grep ":$PORT "`（带尾空格，安全）；`status()` 用 `grep ":$PORT"`（**无**尾空格）。后者会把 `127.0.0.1:91200` 误判为 9120。
- **证据**：
  ```
  127.0.0.1:9120    → 带尾空格匹配: 1
  127.0.0.1:91200   → 带尾空格匹配: 0   ← 安全
  127.0.0.1:9120    → 无尾空格匹配: 1
  ```
- **建议**：统一为带尾空格（或改用 `ss -tlnp` 的结构化解析），消除两处口径不一致。

### F-16 🟢 **演练脚本自身缺陷（诚实记录）：stand-in 未在 `setup` 生成致回滚计时虚高**

- **现象**：首版 `dryrun-cutover.sh` 的 `rollback` 报出停机窗口 **15.258 s**（异常）。查明为**我自己的脚本缺陷**：`setup()` 会 `rm -rf $D`，而 `old-standin.py` 留到 `rollback()` 才生成 → 文件不存在 → 就绪轮询 15 次全 miss → 循环跑满 15 s。
- **证据**：`python3: can't open file '/tmp/dryrun/work/old-standin.py': [Errno 2] No such file or directory`；修正后同一命令测得 **0.166 s**。
- **建议/已修**：把 stand-in 生成移入 `setup()`，并加 `[ -f … ] || { c_no "stand-in 缺失"; exit 1; }` 前置断言；就绪轮询由 `sleep 1`×15 改为 `sleep 0.1`×100，使计时不受轮询粒度污染。**此条同时是「演练报告必须自证测量口径」的例证**——若不追查，15.258 s 会被当成「真实回滚很慢」的错误结论写进报告。
- **另**：隔离护栏首版因 `U_MIS=zhipucode2api-misconfig` 不含 `dryrun` 而拒绝执行 → 改为 `zhipucode2api-dryrun-misconfig`。护栏拦住了我，属**正确**行为，记录以示护栏有效。

### F-17 🟢 **新检测手段候选：空库服务的持续告警可作「库不对」的旁证**

- **现象**：空库服务运行中反复输出 `warning: gateway: read gateway_key: store: get setting "gateway_key": SQL logic error: no such table: meta (1)`。
- **证据**：§2.7 日志第 3 行；`grep -c 'no such table' mis.log` = 2。
- **建议**：即使采纳 F-1 的建议③（`pool: N accounts` 日志），也可保留「`meta` 表缺失告警」作为第二道旁证——它证明「服务虽在跑，但读的是无 schema 的库」。注意它**不是守卫**（服务已 `active` 才打这条），仅作事后识别。

---

## 四、演练结论

### 4.1 演练目标达成情况

| 目标 | 结论 |
|---|---|
| 隔离环境完成阶段 C 全流程 | ✅ 备份→装→起→验证→回滚全链跑通，`/tmp/dryrun/`，假 unit + 假端口 |
| 不碰生产 9120 / 真 systemd unit | ✅ 全程 `is-active` = active、9120 = 200；`/etc/systemd/system/` dryrun 文件数 = 0；生产库 mtime 未变 |
| 可观测证据（命令+输出+耗时） | ✅ 见 §二（含切换 0.161 s / 回滚 0.166 s） |
| 问题清单（现象/证据/建议） | ✅ 17 条（F-1…F-17） |
| 回滚演练通过（回新服务→回老服务 + 6 账号完整） | ✅ 停机窗口 0.166 s，数据 sha256 逐条一致 |
| 不修改 `cutover.sh` | ✅ `git diff --stat scripts/cutover.sh` = 0 行 |

### 4.2 三条硬结论

**结论一：当前 `cutover.sh` 的 preflight 是「假绿」。** 它在 t13 未完成、CR-39 未修复的状态下报出 `exit=0`「preflight 全通过 —— 可以切换」。8 项判据中，项 7（零判别力）、项 8（无失败路径）**结构性失效**，项 2/5（查副本、只验端口）**只验代理指标**。**不能把 `cutover.sh preflight` 通过当作切换放行条件。**

**结论二：CR-39 必须在切换前修复，且 `Restart=always` 放大了它的后果。** 本演练在真 systemd 下复现了完整因果链：误配 → 第 1 次 FATAL（守卫正确）→ `RestartSec=5` 自动重启 → 第 2 次静默放行 → `active` + 端口监听 + `/v1/models` 200。**该故障对 `cutover.sh` 的全部检查手段不可见**（无快照、弱冒烟），且 **C-3 的「切换前可判」判据（`pool: N accounts`）当前不存在**（t14 未实现）。

**结论三：切换放行还需三项前置条件，均不在 `cutover.sh` 中。**
1. **t13 完成** —— 否则 `/admin/api/accounts` 恒 404，「账号真的读到了」无出口（C-3 判据②只验环境变量，不验读数）
2. **C0 guard 改造** —— 让 `z2a_gateway_guard.sh` 识别 `zhipucode2api.service`，且必须在停老 unit **之前**执行；否则端口空窗期（含 F-1 的 5 秒重启窗口）guard 会拉起老服务
3. **外部依赖处置** —— `dsh-zcode-plan` 的 `accountsDb` 需配置覆盖（F-10）；`z2a-patches/entitlements.py` 的语义需在 Go 侧原生实现（F-11）；**老仓目录在观察期结束前不得归档**（F-13）

### 4.3 待办（交接项）

| # | 待办 | 归属 | 阻断关系 |
|---|---|---|---|
| T-1 | 修复 CR-39（三选一，需 captain 裁决） | 待派 | **阻断切换** |
| T-2 | t14 增加启动日志 `pool: %d accounts` | 待派 | 阻断 C-3 判据① |
| T-3 | `z2a_gateway_guard.sh` 双 unit 识别（C0） | 待派 | 阻断切换顺序 |
| T-4 | `cutover.sh` 补：D4 快照 / C-1·C-2·C-3 断言 / switch 失败自动回滚 | 待派 | 影响切换可靠性 |
| T-5 | `smoke.sh` 加环境变量覆盖入口（供 C1 非 9120 预验证） | 待派 | 影响预验证能力 |
| T-6 | `dsh-zcode-plan` 的 `accountsDb` 配置覆盖 | 待派 | 阻断 E4 归档 |
| T-7 | `z2a-patches/entitlements.py` 语义在 Go 侧移植 | 待派 | 功能等价缺口 |
| T-8 | 检查表补：E4 顺序约束 / 外部依赖两条漏项 / C0 条件精确化 | 待派 | 文档准确性 |

### 4.4 演练可复现性

```bash
cd /opt/zhipucode2api
bash docs/taskqueue/dryrun-cutover.sh setup      # 建沙箱（生产库 VACUUM INTO 快照）
bash docs/taskqueue/dryrun-cutover.sh preflight  # 8 项判据 + 可靠性标注
bash docs/taskqueue/dryrun-cutover.sh switch     # 备份→装→起→验证（计时）
bash docs/taskqueue/dryrun-cutover.sh rollback   # 回滚 + 数据完整性比对
bash docs/taskqueue/dryrun-cutover.sh misconfig  # CR-39 复现
bash docs/taskqueue/dryrun-cutover.sh cleanup    # 清沙箱 + 归档证据 + 生产确认
```

脚本内置三重隔离护栏，六子命令均已在本次演练中实跑通过。证据归档于 `workspace/temp/t15-dryrun/`（8 文件，含 `mis.log` 即 CR-39 第一手日志）。

---

## 附：证据文件索引

| 文件 | 内容 |
|---|---|
| `workspace/temp/t15-dryrun/zhipucode2api-dryrun-misconfig.log` | **CR-39 第一手证据**（4 行，含 FATAL→静默放行→meta 告警） |
| `workspace/temp/t15-dryrun/zhipucode2api-dryrun.log` | 正确配置下的启动日志（仅 `listening on`，无 `pool: N accounts`） |
| `workspace/temp/t15-dryrun/guard.log` | guard 复刻实验输出（状态 A 静默让位 / 状态 B 落入兜底） |
| `workspace/temp/t15-dryrun/guard-replica.sh` | guard 隔离复刻（仅替换 unit 名与拉起命令） |
| `workspace/temp/t15-dryrun/snapshot.json` | D4 切换前快照（6 账号，供回滚完整性比对） |

> **本报告不含任何生产密钥**：演练 `.env` 全为伪造值；生产 `.env` 仅被 `grep` 计数（不输出值）；`admin_key` 未在任何命令输出中出现。
