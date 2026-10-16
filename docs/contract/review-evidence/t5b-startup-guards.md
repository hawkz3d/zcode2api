# t5b · 启动守卫独立审查（候选 ③）

- 审查人：Hilkiah（架构师 · 契约冻结与拓扑）
- 审查对象：t6（Catherine）在 `cmd/zhipucode2api/main.go` 实现的两条启动守卫
- 审查方式：**独立复现**（不读实现自述即下结论；先读代码建立假设，再构造实验证伪）
- 日期：2026-10-15
- 结论：**verdict = needs_revision**（守卫 1 通过；**守卫 2 有阻断性缺陷**，见 F-1）

---

## A. 审查范围与判据

| # | 判据 | 来源 |
|---|---|---|
| A1 | 相对路径 `ZCODE_DATA_DIR` → **拒绝启动** | ADR-005 §裁定 + `adr-005` 修法③ |
| A2 | 库文件**本次新建**且 `accounts` 0 行 → **FATAL** | ADR-005 §附「A1/A2 精确化」 |
| A3 | 守卫的**触发与不触发**均须独立复现（不能只读代码） | captain 审查要求 |
| A4 | **A2 边界**：Catherine 自述首版 `listErr == nil && len==0` 会让全新库的 `no such table` 静默放行 → **她修了没有？现在真能拦 A2 吗？** | captain 审查要求 |
| A5 | 守卫**交互**（两者同时触发的顺序） | captain 审查要求 |
| A6 | **误报风险**：正常启动不得被拦 | captain 审查要求 |

---

## B. 实现结构（读代码建立假设）

`cmd/zhipucode2api/main.go` 的 `serve()` 顺序：

```
config.Load()
  ↓
[守卫1] if !filepath.IsAbs(cfg.DataDir) → return error      ← 在 Open 之前
  ↓
os.Stat(dbPath); freshFile := os.IsNotExist(statErr)         ← 先记身份
  ↓
store.Open(ctx, dbPath)                                      ← 会创建文件
  ↓
guardFreshDatabase(ctx, dbPath, freshFile, st)               ← [守卫2]
  ↓
srv.ListenAndServe()
```

**关键前置事实（我独立核实）**：`EnsureSchema` 在**生产代码里零调用点**
（`grep -rn EnsureSchema --include='*.go'` 只命中 `store.go:158` 定义 + 测试）
⇒ `store.Open` **只创建文件、不建表**。这正是 ADR-005 §附描述的 A2 形态的成因。

---

## C. 实测矩阵（全部为本机实跑，非推断）

构建：`go build -o /tmp/guard-probe/z2a ./cmd/zhipucode2api`（`BUILD_OK`）。

| # | 场景 | `ZCODE_DATA_DIR` | 预期 | **实测** | 判定 |
|---|---|---|---|---|---|
| 1 | 相对路径 | `data`（cwd 下） | 拒绝 | 🛡 **守卫1 拦截** exit=1 | ✅ |
| 2 | 相对路径变体 | `./data` | 拒绝 | 🛡 守卫1 拦截 exit=1 | ✅ |
| 3 | 相对路径变体 | `../x/data` | 拒绝 | 🛡 守卫1 拦截 exit=1 | ✅ |
| 4 | 目录**不存在**（A1） | `/tmp/m2`（不存在） | LOUD 失败 | 🛡 `unable to open database file (14)` exit=1 | ✅ |
| 5 | 目录存在且空 + **首次**（A2） | `/tmp/m3` | FATAL | 🛡 **守卫2 拦截** exit=1 | ✅ |
| 6 | **同一目录第 2 次** | `/tmp/m3`（已有 4096B 空库） | FATAL | 🔴 **放行 + 监听** exit=124 | ❌ **F-1** |
| 7 | 真库（6 账号） | `/tmp/m4` | 放行 | 🔴 放行 + 监听 exit=124 | ✅（无误报） |
| 8 | **有 `accounts` 表但 0 行**（已存在） | `/tmp/m5` | FATAL | 🔴 **放行 + 监听** exit=124 | ❌ **F-1** |

> exit=124 是 `timeout` 到点杀进程的码，**说明服务确实在监听**（不是退出）。

### A4 专项：Catherine 首版缺陷修了吗？

**修了，但只修了「首次」这一半。** 现实现（`main.go` `guardFreshDatabase`）是**两分支**：

```go
if !freshFile { return nil }          // ← 🔴 已存在的库：直接跳过，什么都不查
accts, listErr := st.ListAccounts(...)
switch {
case listErr != nil:  // 分支①：无表（no such table）→ FATAL
case len(accts)==0:    // 分支②：有表但 0 行 → FATAL
}
```

对照 Catherine 自述的首版「`listErr == nil && len==0`」——她**确实修好了**：
分支① 让「全新库 `no such table`」**不再静默放行**（实测场景 5 拦截 ✅）。
**但 `if !freshFile { return nil }` 这行把守卫的全部效力绑死在「本次进程是否创建了文件」上** —— 见 F-1。

---

## D. 🔴 F-1（blocker）：守卫 2 **自败**——拦截时留下了让自身永久失效的产物

### 现象

**场景 5→6 是同一条命令、同一个目录、连续两次**：

```
第 1 次:  🛡 守卫2 拦截 (exit=1)   ← 拦截「成功」
          ↓ 但目录里留下了 accounts.db（4096 B，无表）
第 2 次:  🔴 放行 + 监听           ← freshFile 已是 false
第 3 次:  🔴 放行 + 监听
```

**strace 坐实文件确由该进程创建**（`/tmp/g7.strace`）：

```
newfstatat(AT_FDCWD, "/tmp/g7/accounts.db", ...) = -1 ENOENT   ← 启动前不存在
open("/tmp/g7/accounts.db", O_RDWR|O_CREAT|O_LARGEFILE|O_NOFOLLOW|O_CLOEXEC, 0644) = 4
stat("/tmp/g7/accounts.db", {st_mode=S_IFREG|0600, st_size=0, ...}) = 0
```

启动前目录 0 项 → 进程退出后 `accounts.db` 4096 B 存在（`stat` 实测）。

### 后果链（与生产 unit 组合后**必然触发**）

```
空目录 / 目录不存在
  ↓ 第 1 次启动：守卫2 拦截（exit=1）—— 但留下 4096B 无表空库
  ↓ systemd Restart=always + RestartSec=5（生产 unit 实测配置）
  ↓ 第 2 次启动：freshFile=false → 守卫跳过 → **服务监听在空库上**
  ↓ /v1/models 返回 200（我实测：224B，正常模型列表）
  ↓ 用户请求走空账号池 → 无账号可用，但**启动日志零告警**
```

**这恰好是 ADR-005 要防的 A2「静默灾难」**，只是被**推迟了一次重启**。
而**新 unit**（`/etc/systemd/system/zhipucode2api.service:28-29`，我核实）正是
`Restart=always` + `RestartSec=5` → **重启必然发生**，所以这不是理论风险。

**实测放行后的行为**（`/tmp/g8.log` + `/tmp/g8.resp`）：
```
2026/10/15 22:21:03 zhipucode2api listening on 127.0.0.1:9142 (db=/tmp/g8/accounts.db)
/v1/models → 200 (224B)   ← 看起来完全健康
```
启动日志**只有一行 listening**，没有任何「0 账号」告警 → 与 ADR-005
「宁可起不来（立刻可见），也不要静默跑在空库上」的裁定**直接冲突**。

### 为什么这是 blocker 而非 medium

1. **守卫存在的唯一目的就是防这个形态**，而它只防住「第一次」；
2. **触发条件是生产配置本身**（`Restart=always`），非罕见路径；
3. **失败形态是静默的**（服务监听、`/v1/models` 200、日志无异常）——正是 ADR-005 全文强调的最危险形态；
4. **切换日判据 C-3**（`/admin/api/accounts` 返回 6 个而非 0）会在切换**之后**才发现，
   而 systemd 已经把服务拉起来了，中间窗口用户可见。

### 建议修法（择一，推荐 ①）

1. **守卫判据与「谁创建了文件」解耦** —— 改为无条件检查**账号数**（+表存在性），
   只在「库文件不存在」这个**明确安全**的初始态放行：
   ```
   if !fileExists(dbPath)  → LOUD FATAL（目录/文件配置错误，不要创建）
   open → 若 listErr != nil（无表）→ FATAL
        → 若 len(accts)==0 → FATAL
   ```
   注意：这**违反** ADR-005 §附「永远不要 MkdirAll」的反面——但方向一致：
   **绝不因"看起来是首次"就放过一个 0 账号的库**。
   若担心「合法的新部署确实要从 0 账号起步」，则应引入**显式 opt-in**
   （如 `ZCODE_ALLOW_EMPTY_POOL=1`）而非靠 `freshFile` 猜测意图。
2. **守卫拦截时清理自己创建的产物**（`os.Remove(dbPath)`）——
   让「被拦」成为**幂等**的：下次启动仍会拦。
   代价：若库里有真实数据则绝不能删（故须严格限定为「本进程刚创建且 0 字节/无表」）。
3. **叠加兜底**：`ListenAndServe` 之前把账号数写进启动日志
   （`log.Printf("pool: %d accounts", n)`），让 0 账号**在日志里可见**——
   这不是守卫替代品，但能让 A2 从「静默」变「可观测」。

---

## E. 其余判据结论

| 判据 | 结论 | 证据 |
|---|---|---|
| **A1** 相对路径守卫 | ✅ **通过**（3/3 形态拦截） | 场景 1-3 |
| **A1 副作用** | ✅ **正确**：拦截发生在 `store.Open` **之前** → **不创建文件**（`/tmp/g4` 拦截后目录 0 项） | 实测 |
| **A2 首次** | ✅ 通过（场景 5） | 守卫2 分支①（`no such table`）确实修好了 |
| **A2 非首次** | ❌ **F-1** | 场景 6、8 |
| **A5 守卫交互** | ✅ **顺序正确**：守卫1 在 Open 前 → 拦截时无副作用；守卫2 在 Open 后 → 需 Open 才能查表（**这正是 F-1 的成因**） | 读码 + 场景 1/5 对照 |
| **A6 误报** | ✅ **无误报**：6 账号真库正常启动（场景 7） | 实测 |
| **A6 边界** | ⚠️ 场景 8（有表 0 行、已存在）**放行** —— 同一 F-1 根因 | 实测 |

---

## F. 未自曝观察（非 F-1，供参考）

| # | 观察 | 等级 |
|---|---|---|
| B-1 | **`main.go` 不解析 `.env`**：`config.Load()` 只读**进程环境**。生产 unit 用 `EnvironmentFile=` 注入，故 systemd 下没问题；但**手工直接跑二进制**（如调试、cron）会静默落回 `DefaultAdminKey = "change-me"` 与 `DataDir=data`（相对路径 → 反而被守卫1 拦住，属**幸运**）。建议 README 明确「本二进制不自带 .env 解析」。 | 🟡 |
| B-2 | **守卫2 的 `freshFile` 判定用 `os.Stat` + `os.IsNotExist`**：若 `dbPath` 是一个**目录**或权限不足，`statErr` 非 `IsNotExist` → `freshFile=false` → 守卫静默跳过。**实测**：`accounts.db` 为目录 → `unable to open database file (14)`（LOUD，被 `store.Open` 拦住，属幸运）；权限不足（`chmod 500`）→ 同样 LOUD。⇒ **实际影响有限**（`store.Open` 的 `SELECT 1` 探针兜住了），但**判据本身不严谨**：它把「stat 失败」一律当成「文件已存在」。建议区分 errno。 | 🟡 |
| B-3 | **`store.Open` 以 READ-WRITE 打开**（代码注释自述「the full pool task will need to write cooling/exhausted state」）。当前网关请求路径不写，但**rw 打开本身**会在 WAL 库上创建 `-wal`/`-shm` 并可能触发 checkpoint 写（t5 已实测：纯 Python `mode=ro` 读也会推进 `-shm` mtime，故 `-shm` 不算污染）。**切换期与老服务同时运行**会破坏 ADR-005 §1「只有一方在运行」的前提 → 建议切换检查表加一条「确认老服务已 stop」。 | 🟠 |
| B-4 | 守卫2 拦截时的错误文本**含驱动内部错误**（`store: list accounts: SQL logic error: no such table: accounts (1)`）——信息充分，**但暴露了实现细节**。非缺陷，仅记录。 | 🟢 |

---

## G. 复现命令（供 captain / Catherine 复核）

```bash
# 构建
cd /opt/zhipucode2api
go build -o /tmp/guard-probe/z2a ./cmd/zhipucode2api

# F-1 核心复现（空目录连续两次启动）
rm -rf /tmp/m3 && mkdir -p /tmp/m3
ZCODE_DATA_DIR=/tmp/m3 ZCODE_ADMIN_KEY=x timeout 3 /tmp/guard-probe/z2a serve   # 第1次 → 🛡 拦截
ls -la /tmp/m3/                                                                  # → accounts.db 4096B 出现
ZCODE_DATA_DIR=/tmp/m3 ZCODE_ADMIN_KEY=x timeout 3 /tmp/guard-probe/z2a serve   # 第2次 → 🔴 放行+监听

# 场景 8（有表 0 行、已存在）
python3 -c "
import sqlite3
c=sqlite3.connect('/tmp/m5/accounts.db')
c.execute('CREATE TABLE accounts (id TEXT PRIMARY KEY, provider TEXT NOT NULL, name TEXT, mode TEXT, status TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at REAL, data TEXT NOT NULL)')
c.execute('CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)');c.commit();c.close()"
ZCODE_DATA_DIR=/tmp/m5 ZCODE_ADMIN_KEY=x timeout 3 /tmp/guard-probe/z2a serve   # → 🔴 放行
```

---

## H. 对下游的影响与建议

- **不阻断 t13**（admin API）——守卫属 `cmd/`，与 admin API 实现无耦合。
- **阻断切换**：`cutover-checklist.md` 的 C-3 判据（切换后 accounts 必须 = 6）
  在 F-1 存在时**不可靠**——因为服务能在 0 账号库上正常启动并通过 C-1/C-2 的文本检查。
  建议 C-3 升级为**启动日志断言**（见 D 建议 3）或把 F-1 修掉。
- **建议回退路径**：这是 `cmd/zhipucode2api/main.go` 的单点修改，修复面小、风险低。

---

## 附：环境事实

- 构建工具链：`/usr/local/bin/go`（Go 1.26.4），`BUILD_OK`
- 生产 unit（老服务）：`/etc/systemd/system/zcode2api.service`，`Restart=always`、`RestartSec=5`（第 27-28 行）
- **新 unit（切换目标）**：`/etc/systemd/system/zhipucode2api.service`，**`Restart=always`、`RestartSec=5`（第 28-29 行）** ← F-1 论证的直接依据
- 生产库：`/opt/zcode2api/data/accounts.db`（6 账号）
  —— 本次审查**全程只读**（`mode=ro` + `VACUUM INTO` 副本），**未写生产库**
- 证据归档：`workspace/temp/t5b-startup-guards/`（`g5.strace`、`g7.strace`、`g8.log`、`g8.resp`）
- 测试端口 9130-9159 全部已释放；临时库 `/tmp/g*`、`/tmp/m*` 已清

### 零污染验证（按 t5 三层判据）

| 层 | 方法 | 结果 |
|---|---|---|
| 逻辑指纹 | accounts + meta 全表 sha256，间隔 30s 两次采样 | **A == B 逐字一致** ✅ |
| mtime 前后比对 | `accounts.db` mtime | **22:16:15** —— **早于**我的审查起点（22:16:51 首次探针）⇒ 审查期间未变 ✅ |
| 路径隔离 | 所有探针一律 `ZCODE_DATA_DIR=/tmp/...`；对生产库仅 `mode=ro` + `VACUUM INTO` | 从不指向生产库 ✅ |

**诚实说明（指纹相对 t5 基线的变化已归因，非本次审查所致）**：
本次重算的 `accounts_sha256` 与 `meta_sha256` **与 t5 报告基线不同**。归因证据：
- `oauth-login-8523aba4` 的 `last_used_at = 1792070901.7504337` → **2026-10-15 21:28:21**
  —— **早于**本次审查的全部操作（22:16:51 起）⇒ 是**老服务自身的 quota 刷新**写入，不是我的探针。
- `meta` 变化来自 `admin_key` **轮换**（t5 期间已发生两次：`69c47757880b` → `12a9cfaa020b`）。
- 老服务 `MainPID=618152`，启动于 `20:15:04`，`systemctl is-active` = **active** ⇒ 它**持续在写**，
  任何跨小时的指纹比对都会看到它的写入（这正是 t5 报告 B-5 记录的「生产库噪声可精确归因」）。
- `accounts_rows=6` / `meta_rows=3` / `integrity_check=ok` **均与基线一致** ✅

> **教训**：跨会话比对生产库指纹时，**必须先把「服务自身写入」排除**，
> 否则会把服务的正常 quota 刷新误判成自己的污染。判据 = 比对 `last_used_at`/`last_checked_at`
> 与自身操作的时间窗（t5 已建立此哨兵，本次沿用）。
