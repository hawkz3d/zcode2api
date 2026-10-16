# t5 独立审查证据（Hilkiah）—— 不是复述实现者自述

> 审查对象：t3 `config` / `models` / `store`（提交 `6f33151`）
> 审查者：Hilkiah（t1 契约作者，未参与 t3 实现 —— 无自审）
> 时间：2026-10-15 · 方法：读代码 + 自建探针 + strace 归因 + 生产库前后比对

---

## A. 6 条 acceptance 的独立复核结论

| # | acceptance | 独立复核结论 | 我自己的证据 |
|:--:|---|:--:|---|
| 1 | `go build ./...` + `go vet ./...` 零错误 | ✅ **通过** | 在**真实工作树**跑 → `OK`；另在干净 `6f33151` 提取树跑 → `BUILD_OK`/`VET_OK` |
| 2 | `go test ./internal/...` 全通过，且 store 测试**确实从生产库只读快照读到数据**（断言 ≥1） | ✅ **通过** | 13/13 PASS **无 SKIP**（若有 skip 我会看到 `SKIP`）；`store_test.go:64` 断言 `>=1`；我另写探针独立 `Snapshot()` + `ListAccounts()` 读同一生产库 |
| 3 | 对生产库**无任何写操作** | ✅ **通过（三重独立证明）** | 见 §C —— strace 归因 + 无干扰窗口前后比对 + 逻辑指纹 |
| 4 | models 序列化往返：两次 JSON 语义等价且**未知字段不丢失** | ✅ **通过** | `go test ./internal/models/ -run RoundTrip...` 5/5 PASS；我另建探针 H1 用**逐键 diff** 验证 `data` 列 2587 B 字节不变 |
| 5 | config 覆盖 5 个环境变量默认值/覆盖，含单测 | ✅ **通过** | `config_test.go` 6/6 PASS；源码实证 `EnvPort/EnvHost/EnvDataDir/EnvAdminKey/EnvNodePath` 恰好 5 个 |
| 6 | 代码中无明文密钥/JWT（grep 验证） | ✅ **通过（强化形态集）** | 见 §D —— 我用了 **6 种**形态而非仅 `eyJ` |

---

## B. 实现者未自曝的观察（≥3 处）

### 🔴 B-1（**高**）`dsn()` 只转义 `%`，路径含 `?` / `#` 时**静默写到错的文件**

`internal/store/store.go` 的 `dsn()` 仅做 `strings.ReplaceAll(p, "%", "%25")`。
SQLite URI 语法中 `?` 起 query 段、`#` 起 fragment 段 —— 二者**未转义**。

**实测**（我的探针 `TestZZDsnEscaping`，7 个路径）：
```
PASS "plain.db"            PASS "with space.db"      PASS "with%percent.db"
FAIL "with?question.db"    FAIL "with#hash.db"
PASS "中文名.db"           PASS "plus+sign.db"
```
`with?question.db` 实际被创建为名为 **`with`** 的文件（28672 B），
而 `st.Path()` 仍返回原路径 —— **无任何报错**。
`Open` / `EnsureSchema` / `SetSetting` **全部返回成功**（静默数据错位）。

**触发条件**：`ZCODE_DATA_DIR` 或 `AccountsDB()` 路径含 `?`/`#`。
**影响**：数据写到非预期文件、重启后读不到、备份/巡检指向空库。
**已验证的修复**：在 `%` 之后追加 `%3F`/`%23`（顺序：先 `%` 避免二次转义）→ 我的 7/7 全 PASS。
**严重度**：高（数据落错位置且静默）→ 见下方 findings。

### 🟠 B-2（**中**）契约 `recent_results` 方向写反 —— **t1 我自己写错的**，t5 期发现并已更正

`recent_results` 契约称「`[0]` 最新（前插）」，实测**恰好相反**：6/6 账号 `at` **严格升序**，
`[0]`=10-14 11:47（最旧）、`[-1]`=10-14 17:08（最新）→ **后插**。
**因果证据**：我 13:01/13:02 UTC 对 `/v1/messages` 的探测条目落在 **`[-1]`**。
已提交更正（`d65a78e`，5 文件 7 处），并新增 **CR-16b**：对拍归一化把该字段整体折叠
→ **方向错误对拍抓不到**，必须单侧断言。**Go 侧尚未实现该逻辑，未污染代码。**

### 🟠 B-3（**中**）**t8 对抗性核验未覆盖该错误** —— 多轮审查漏网

t8（Liche 的契约对抗性核验，43 项实测，verdict=pass）与 t1 冻结、t5 前审查
**均未发现** `recent_results` 方向错误。原因：该断言看似有 `[黑盒]` 支撑
（`recent_results[-1]` 的 shape 断言），但**只验形状不验方向**，
形成"有证据外观、无方向覆盖"的盲区。

### 🟡 B-4（**低**）`fd 轮询`在判"零污染"上**方法失效**（已由 captain 复核并写成方法论）

老服务**不长期持有** `accounts.db` 的 fd（15 个 fd 中无它）。我最初用 `/proc` 扫描
得出"无进程持有"——该结论**正确但不代表安全**：它只说明"此刻没人开着"，
**不能**推出"没人写过"。对 40 秒 strace 该进程也未见它 open `accounts.db`（按请求开）。
→ 零污染判据**必须**用写类系统调用归因或内容指纹，**不可**用 fd 轮询。

### 🟡 B-5（**低**）生产库的"噪声"可被**精确归因**，不必放弃前后比对

我最初以为"生产库持续被写 → 前后比对无效"。实测可闭合归因：
mtime `1792071373` ↔ 两 JWT 账号 `last_checked_at` = `1792071373.20` / `.57`
**同一时刻** → 是**服务自身 quota 刷新**（captain 列的干扰源①）。
→ 判据升级：以 `last_checked_at` 为"是否发生业务写入"的哨兵。

---

## C. 验收项 3「生产库零污染」—— 我的三重独立证明

### 证明 ①：写类系统调用归因（最强，精确到 syscall）

```bash
strace -f -y -e trace=openat,pwrite64,ftruncate,unlinkat,renameat,fsync \
  -o /tmp/t5-final-strace.txt timeout 300 go test ./internal/... -count=1
```
结果（trace 7835 行，**写类调用 1417 条**）：

| 检查 | 结果 |
|---|---|
| 落在 `accounts.db` / `accounts.db-wal` 的 **pwrite64/ftruncate/unlink/rename** | **0 条** ✅ |
| 落在 `accounts.db-shm` 的写 | 81 条（SQLite 读模式亦 mmap 索引，**允许**） |
| 生产主库 open 模式 | `O_RDONLY\|O_LARGEFILE\|O_NOFOLLOW\|O_CLOEXEC` ✅ |
| 业务数据写落点 Top | `live-wal.db-wal`(208) `snapshot.db`(89) `fresh.db-wal`(53) …**全在 /tmp** |

**对照组**（排除"是 Go 特有问题"）：纯 Python `sqlite3 mode=ro` 读同一库，
`-shm` mtime 同样推进 —— 说明 `-shm` 写入是 **SQLite WAL 读模式的机制必然**，非实现缺陷。

### 证明 ②：无干扰窗口内的前后比对（captain 推荐的三层）

```bash
# 以 last_checked_at 为哨兵：quota 刷新会推进它，我的测试不应推进它
# BEFORE → go test ./internal/... → AFTER
```
结果：**6/6 账号 `last_checked_at` 与 `use_count` 逐字不变** ✅
（`1792071373.2040205` / `.572651` / `1791645311.667656` … 全部原值）

另：连发 **5 次** admin 读（`/accounts` ×3、`/settings`、`/stats`）+ **60 秒** mtime 监控
→ **mtime 零变化** ✅（独立复现 captain 的"读不写库"结论）

### 证明 ③：逻辑内容指纹（排除 WAL/mtime 噪声）

对 `accounts` 全表 + `meta` 做 sha256 逻辑指纹，测试前后比对：
```
accounts_sha256  一致（rows=6, enabled_cols=[1,1,1,1,1,1]）
meta_sha256      defcc07a623a7501fb67a1af7c0bfe393caaf950a4bea2b8e1bd177de76404e0  一致
integrity_check  ok → ok
```
⚠️ 诚实说明：期间指纹**变过一次**，经闭合归因确认是**服务自身 quota 刷新**
（`last_checked_at` 同刻推进），**非我的测试**。

---

## D. 验收项 6「无明文密钥」—— 强化形态集

任务书给的是 `! grep -rniE 'eyJ[A-Za-z0-9_-]{20,}'`。依 ADR-004 补正（形态集须覆盖
**被扫对象自身的产物形态**），我用了 6 种：

| 形态 | 命中 |
|---|---|
| `eyJ[A-Za-z0-9_-]{20,}`（JWT） | 0 |
| `[0-9a-f]{32}\.[A-Za-z0-9]{16}`（本项目 api_key 形态） | 0 |
| `sk-[A-Za-z0-9]{20,}` | 0 |
| `AKID[A-Za-z0-9]{16,}` | 0 |
| `ghp_[A-Za-z0-9]{30,}` | 0 |
| `-----BEGIN [A-Z ]*PRIVATE KEY-----` | 0 |

✅ 结论：**无明文密钥**（且形态集强于任务书要求）。

---

## E. ADR-002 / ADR-003 的代码层核实（读代码，非注释）

**ADR-003（双写）—— 真实存在于代码**：`internal/store/store.go:320` `SetEnabled`
```go
tx, _ := s.db.BeginTx(ctx, nil)
tx.QueryRowContext(ctx, `SELECT data FROM accounts WHERE id = ?`, id).Scan(&data)
d, _ := models.ParseAccountData(data)   // 保留未知字段/顺序
d.SetEnabled(enabled)
merged, _ := d.Serialize()
tx.ExecContext(ctx, `UPDATE accounts SET enabled = ?, data = ? WHERE id = ?`, col, merged, id)
```
**列 `enabled` 与 `data.enabled` 在同一条 UPDATE 中一起写** —— 非注释声称，是执行语句。

**ADR-002（归一）—— 真实存在于代码**：`models.go:138` `EffectiveEnabled()`
（**data 优先、列兜底**）；`Serialize()` 保留原始字节（未知键/顺序不丢）。

**我自己的行为层探针**（非引用他人测试）：
- H1：`SetEnabled` 后 `data` 列**逐键 diff** → `changed=[enabled]`，`dropped=[]`，`added=[]`，23 键序保留，其余 22 成员**字节不变**（2587 B）
- H2：`json.Marshal` 会**压缩** `{"api_key":"k1",...}`，而 `Serialize()` 保留 `", "`/`": "` → 证明实现者**没有**图省事用 `json.Marshal`
- H3：5 个写入口在只读 Store 上全部返回 `store.ErrReadOnly`，且 6 行仍可读

---

## F. 工作树级观察（非 t3 缺陷）

审查期间 `git status` 有 `?? internal/gateway/`、`?? internal/upstream/`、`?? cmd/`（t6/t7/t9 在飞）。
曾一度 `go build ./...` 失败（`handleMessages undefined` 等），**归因 t6 未完成**、非 t3。
现 t6 已补全，真实工作树 `build`+`vet`+`test` **全部通过** ✅。
**t3 三包与 `6f33151` 逐字等价**（`git diff --stat 6f33151 HEAD -- internal/{config,models,store}` 为空），
故 t3 结论不受后续提交影响。
