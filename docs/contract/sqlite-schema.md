# sqlite-schema.md — zhipucode2api SQLite 契约（冻结版）

> 本文是 Go 版 `zhipucode2api` **读写 `data/accounts.db` 的唯一规格**。目标：Go 进程能与老 Python 进程
> **读同一份库、写同一份库**，字段级等价（对拍验收项「DB 字段级读等价」）。
>
> **规格先于实现声明**：本文全部事实来自对 `zcode2api/data/accounts.db` 的**只读探测**（`sqlite3` / Python `sqlite3` 模块）
> 与 `zcode2api/docs/**`。**未纳入本轮观测** `zcode2api/app/**/*.py`、`cli.py`、`captcha_node/`、`frontend/`。
>
> **实测环境**：`/opt/zcode2api/data/accounts.db`，实测 2026-10-15。
> 探测方式：`sqlite3.connect('file:data/accounts.db?mode=ro',uri=True)` —— **全程只读**，未写入任何字节。
>
> **脱敏**：`meta.admin_key` 的值、`data` JSON 内的 `jwt_token`/`api_key` 值一律不回显；
> 本文只记录其**长度、形态与加密状态结论**。真实凭证绝不落盘到文档。

---

## 1. 文件层契约

### 1.1 文件与路径

| 项 | 值（实测） |
|----|-----------|
| 主库路径 | `$ZCODE_DATA_DIR/accounts.db`（生产 `ZCODE_DATA_DIR=data` → `data/accounts.db`） |
| 主库大小 | 53248 字节 |
| 伴随文件 | `accounts.db-wal`（0 字节）、`accounts.db-shm`（32768 字节） |
| 属主 | `appuser:appuser`，权限 `rwxrwxrwx` |

**同目录其它文件（Go 版必须识别但**不得**当作数据库处理）**：

| 文件 | 大小 | 实测内容 | 语义 |
|------|:---:|------|------|
| `device_mid` | 36 字节 | UUIDv4 形态（正则 `[0-9a-f]{8}-...-4...` 匹配成功） | 设备指纹兜底值（无换行） |
| `release_version.json` | 47 字节 | `{"version": "3.15.3", "ts": 1792013576.6665423}` | 上游客户端版本快照，**与 `/meta` 的 `2.5.3` 是两个不同版本号** |
| `last_plans.txt` | 0 字节 | 空 | 空态标记 |
| `accounts.db.bak-*` (8 组) | 40960–49152 | 历史备份（含 `-shm`/`-wal`） | **考古文物，禁止读写** |
| `crontab.bak-20260912-152915` | 291 字节 | 3 条 cron（周末抢包巡逻） | 考古文物 |

> ⚠️ `data/` 下混放数据库、指纹、备份与 cron 备份。Go 版**只许打开 `accounts.db`**，
> 任何「扫描 data 目录并处理所有 .db」的实现都会误伤备份文件。

### 1.2 数据库级 PRAGMA（逐条实测）

| PRAGMA | 实测值 | 对 Go 的含义 |
|--------|--------|--------------|
| `journal_mode` | **`wal`** | 🔴 **必须启用 WAL**。Go 若以 `delete` 模式打开，老进程与新进程并发会互相阻塞/损坏 |
| `page_size` | `4096` | 建库时需一致（已存在的库无需改） |
| `page_count` | `13` | 52 KB / 4096 = 13 页 |
| `encoding` | `UTF-8` | 中文 `name` 字段依赖此编码 |
| `user_version` | `0` | 未使用 |
| `foreign_keys` | `0` | **无外键约束**，Go 不得假设有 |
| `auto_vacuum` | `0` | 不自动回收 |
| `schema_version` | **`4`** | 经 4 次迁移；**Go 不得自行递增**（老进程按版本判断） |
| `application_id` | `0` | 未使用 |
| `cache_size` | `-2000` | 2 MB |
| `synchronous` | `2` | FULL |
| `locking_mode` | `normal` | |
| `wal_autocheckpoint` | `1000` | |
| `mmap_size` | `0` | |

**WAL 判据（Go 侧硬要求）**：
```
实测 journal_mode = wal
→ Go 打开必须同样走 WAL：DSN 带 _pragma=journal_mode(WAL)（modernc.org/sqlite）
→ 并且必须容忍 -wal/-shm 伴随文件存在；不得在启动时删除它们
```
`-wal` 当前 0 字节属正常（已 checkpoint）。见 `compat-risks.md` CR-13。

### 1.3 对象清单（实测 `sqlite_master` 全量）

| 类型 | 名称 | 说明 |
|------|------|------|
| table | `accounts` | 账号池主表 |
| table | `meta` | 键值配置表 |
| index | `idx_acc_provider` | `accounts(provider)`，非唯一 |
| index | `idx_acc_status` | `accounts(status)`，非唯一 |
| index | `sqlite_autoindex_accounts_1` | `accounts` PK 自动索引（`origin=pk`, `unique=1`） |
| index | `sqlite_autoindex_meta_1` | `meta` PK 自动索引（`origin=pk`, `unique=1`） |

**无视图、无触发器、无额外表**（实测 `sqlite_master` 中 `sql` 非空者恰为上述 6 项）。

**行数**：`accounts` = **6 行**；`meta` = **3 行**。

---

## 2. 表 `accounts`

### 2.1 建表 SQL（`sqlite_master` 原文，逐字）

```sql
CREATE TABLE accounts (
                    id          TEXT PRIMARY KEY,
                    provider    TEXT NOT NULL,
                    name        TEXT,
                    mode        TEXT,
                    status      TEXT,
                    enabled     INTEGER NOT NULL DEFAULT 1,
                    created_at  REAL,
                    data        TEXT NOT NULL
                )
```

> 注：原文含缩进与换行（表体缩进 20 空格）。Go 若重建该表，**逐字复用此 SQL** 可保证 `sqlite_master`
> 字节一致；但**老库已存在，正常路径不应重建**。

### 2.2 `PRAGMA table_info(accounts)` 原文（实测逐字）

```
(0, 'id',         'TEXT',    0, None,  1)
(1, 'provider',   'TEXT',    1, None,  0)
(2, 'name',       'TEXT',    0, None,  0)
(3, 'mode',       'TEXT',    0, None,  0)
(4, 'status',     'TEXT',    0, None,  0)
(5, 'enabled',    'INTEGER', 1, '1',   0)
(6, 'created_at', 'REAL',    0, None,  0)
(7, 'data',       'TEXT',    1, None,  0)
```

字段位序：`cid, name, type, notnull, dflt_value, pk`。

### 2.3 字段语义表

| # | 字段 | 声明类型 | NOT NULL | 默认值 | PK | 实测取值 | 语义 |
|:--:|------|:---:|:---:|:---:|:---:|------|------|
| 0 | `id` | TEXT | 否（0） | — | **是（1）** | 6 行全非空，形如 `<prefix>-<8hex>` | 账号主键；**由 name/来源派生**，见 §2.5 |
| 1 | `provider` | TEXT | **是（1）** | — | 否 | `zai`(4) / `bigmodel`(2) | 上游供应商 |
| 2 | `name` | TEXT | 否 | — | 否 | 中文，如 `老号·抢包(JWT)` | 显示名 |
| 3 | `mode` | TEXT | 否 | — | 否 | `jwt`(2) / `apiKey`(4) | 凭证类型（**驼峰 K**） |
| 4 | `status` | TEXT | 否 | — | 否 | 6 行全 `active` | 状态机值（**全小写**） |
| 5 | `enabled` | INTEGER | **是（1）** | **`1`** | 否 | 6 行全 `1`（`typeof=integer`） | 启用标志（**0/1 整数，非布尔**） |
| 6 | `created_at` | REAL | 否 | — | 否 | 6 行全非空（`typeof=real`），unix 秒 | 创建时刻 |
| 7 | `data` | TEXT | **是（1）** | — | 否 | 6 行全非空 JSON | **账号全量负载**，见 §3 |

> 🔴 **`id` 的 `notnull=0` 是陷阱**：SQLite 的 `TEXT PRIMARY KEY` 历史上允许 NULL（非 INTEGER PK 不隐式 NOT NULL）。
> 实测 6 行全非空，但 Go 侧**不得依赖 NOT NULL 约束**做去重；应以应用层保证非空。

### 2.4 索引

| 索引 | 定义 | 列 | 唯一 |
|------|------|----|:---:|
| `idx_acc_provider` | `CREATE INDEX idx_acc_provider ON accounts (provider)` | `provider` (seq 0) | 否 |
| `idx_acc_status` | `CREATE INDEX idx_acc_status   ON accounts (status)`（注意源码里有多余空格） | `status` (seq 0) | 否 |
| `sqlite_autoindex_accounts_1` | 自动（PK） | `id` | 是 |

`PRAGMA index_list(accounts)` 实测：`(0,'idx_acc_status',0,'c',0)`、`(1,'idx_acc_provider',0,'c',0)`、`(2,'sqlite_autoindex_accounts_1',1,'pk',0)`。

> ⚠️ `index_list` 的**返回顺序**是 `idx_acc_status` 在前，而 `sqlite_master` 的创建顺序是 `idx_acc_provider` 在前
> —— 顺序不保证，Go 侧**禁止**依赖索引枚举顺序做逻辑判断。

### 2.5 `id` 派生规律（实测）

| 来源 | 实测 id |
|------|---------|
| `name=老号·抢包(JWT)`（OAuth 登录） | `oauth-login-ea92aa60` |
| `name=xin号·抢包(JWT)`（OAuth 登录） | `oauth-login-8523aba4` |
| `name=老号·智谱Key`（API Key 导入） | `oauth-apikey-c5c641a1` |
| `name=xin号·智谱Key`（API Key 导入） | `oauth-apikey-8808fbac` |
| `name=老号·BigModelKey` | `laohao-bigmodel-2420b3c2` |
| `name=xin号·BigModelKey` | `xinhao-2000w-02f71f86` |
| `POST /admin/api/accounts {"provider":"zai","tokens":[...],"name":"contract-probe"}` | `contract-probe-bf31b757` |
| `POST /admin/api/import {"name":"import-probe"}` | `import-probe-a63213aa` |
| `POST /admin/api/accounts {"provider":"bigmodel","name":"probe-toggle"}` | `probe-toggle-8e870470` |

**实测规律**：`<name 或来源标识>` + `-` + `8 位十六进制小写`。
🔴 `zcode2api/docs/development/03-data-formats.md` 写的是 `zai-<sha256[:12]>` —— **与实测不符**。
以实测为准。**后缀 8 位 hex 的生成算法未实测**（可能是随机或哈希截断），Go 侧只需保证
「唯一 + 形如 `^[A-Za-z0-9._·-]+-[0-9a-f]{8}$`」即可满足对拍的可观测部分。见 CR-11。

### 2.6 表列 ↔ `data` JSON 一致性（实测逐行比对）

| id | `provider` 列=JSON | `status` 列=JSON | `mode` 列=JSON | `name` 列=JSON | `created_at` 列≈JSON | `enabled` 列 | `data.enabled` |
|----|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `laohao-bigmodel-2420b3c2` | ✅ | ✅ | ✅ | ✅ | ✅ | `1` (int) | **`"True"` (str)** 🔴 |
| `oauth-apikey-8808fbac` | ✅ | ✅ | ✅ | ✅ | ✅ | `1` (int) | `true` (bool) |
| `oauth-apikey-c5c641a1` | ✅ | ✅ | ✅ | ✅ | ✅ | `1` (int) | `true` (bool) |
| `oauth-login-8523aba4` | ✅ | ✅ | ✅ | ✅ | ✅ | `1` (int) | `true` (bool) |
| `oauth-login-ea92aa60` | ✅ | ✅ | ✅ | ✅ | ✅ | `1` (int) | `true` (bool) |
| `xinhao-2000w-02f71f86` | ✅ | ✅ | ✅ | ✅ | ✅ | `1` (int) | `true` (bool) |

**结论（Go 必须遵守的取值优先级）**：

| 字段 | 唯一真相源 | 证据 |
|------|-----------|------|
| `provider` / `status` / `mode` / `name` / `created_at` | 表列与 JSON **一致**（实测 6/6） | 可任取；**但为与老服务一致，API 输出应走 JSON** |
| `enabled` | **`data` JSON**（不是表列） | 表列 6 行全 `1`，而 API 输出有一行 `"True"` —— 该值只可能来自 JSON |

> 🔴 **Go 侧硬规则**：读账号时以 `data` JSON 为准；写账号时**两处都写**（表列写 0/1 整数、JSON 写布尔）。
> 若只写表列而 JSON 缺失该键，老服务的 `GET /admin/api/accounts` 会输出 `null`/缺键 → 对拍差异。

---

## 3. 表 `meta`

### 3.1 建表 SQL 与 `PRAGMA` 原文

```sql
CREATE TABLE meta (
                    key   TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                )
```

```
(0, 'key',   'TEXT', 0, None, 1)
(1, 'value', 'TEXT', 1, None, 0)
```

索引：仅 `sqlite_autoindex_meta_1`（PK）。**无业务索引**。

### 3.2 实测行（3 行，键名全列出；值按脱敏规则处理）

| `key` | `value` 长度 | 形态 | 语义 | 是否可回显值 |
|-------|:---:|------|------|:---:|
| `admin_key` | 32 | 非数字字符串 | 后台鉴权密钥 | ❌ **禁止回显** |
| `gateway_key` | **0** | **空串** | 网关 API Key；**空 = 不校验**（实测 `gateway_key_set:false`） | ✅ 可（空串） |
| `quota_refresh_interval` | 3 | 纯数字 `"600"` | 额度轮询间隔（秒），**存为字符串** | ✅ 可（`600`） |

> 🔴 **`value` 是 TEXT，数字也存字符串**（实测 `quota_refresh_interval` 的 `typeof` 是 `text`，内容 `"600"`）。
> Go 侧读时必须 `strconv.Atoi` 转换，**不得**用列类型判断为整数。

### 3.3 `meta` 语义与 Go 读写规则

| key | 读取优先级（实测 + 文档） | Go 写入时机 |
|-----|--------------------------|------------|
| `admin_key` | **DB 优先于 env** —— `.env.example` 原文注释：「后台管理密码（首次启动写入 meta 表，**之后以数据库为准**）」 | 仅首次建库时从 `ZCODE_ADMIN_KEY` 写入；后续 `PUT /settings` 更新 |
| `gateway_key` | DB 优先；空串 = 不校验 | `PUT /settings` 更新 |
| `quota_refresh_interval` | DB 优先，实测 `600` | `PUT /settings` 更新 |

**实测佐证「DB 优先」**：`.env` 中 `ZCODE_ADMIN_KEY` 与 `meta.admin_key` **长度同为 32**，且用
`?app_key=<.env 值>` 与 `Authorization: Bearer <.env 值>` **均返回 200** → 二者当前值一致（无法区分优先级）。
但 `.env.example` 的注释与 `03-data-formats.md` §53-54（`ZCODE_ADMIN_KEY` 默认 `zcode`，
注「**之后以 DB meta 为准**」）**两处独立佐证 DB 优先**。见 `env-contract.md` §2。

> 🔴 **Go 若实现为「env 覆盖 DB」，老服务的改密行为会失效**：`PUT /settings` 改的 admin_key 会被 env 覆盖。
> 必须实现为 **DB 优先，env 仅作首次播种**。

### 3.4 缺失 `meta` 行的行为

实测仅有 3 行；`admin_key`/`gateway_key`/`quota_refresh_interval` 均有行。
若某 key 缺行，Go 侧的默认值应与 `env-contract.md` §3 的默认值表一致（`gateway_key`→`""`、
`quota_refresh_interval`→`60`、`admin_key`→`ZCODE_ADMIN_KEY`）。

---

## 4. `accounts.data` 的 JSON 结构契约

### 4.1 顶层键序（实测逐字，**6 个账号完全一致**）

```
id, name, provider, mode, jwt_token, api_key, enabled, status, quota, plan, plans, usage,
subscription, use_count, fail_count, risk_strikes, recent_results, last_used_at,
last_checked_at, cooling_until, last_error, created_at, fingerprint
```

**共 23 个顶层键，6/6 账号键集合完全相同**（实测 `COMMON` = 全部 23 键，`EXTRA` = 空集）。

> 🔴 **键序是稳定的且与 API 输出顺序不同**：`data` JSON 里 `id, name, provider, mode, jwt_token, api_key,
> enabled, status, ...`，而 API `AccountView` 是 `id, name, provider, mode, token_masked, enabled, status, ...`
> （用 `token_masked` **替换**了 `jwt_token`/`api_key` 两个键的位置）。
> Go 侧若复用同一 struct 序列化，顺序会错 → 见 CR-14。

### 4.2 顶层字段类型与语义（实测）

| 字段 | 实测类型（全 6 行） | 语义 | 备注 |
|------|-------------------|------|------|
| `id` | string | 同表列 `id` | |
| `name` | string | 同表列 `name` | |
| `provider` | string | `zai` / `bigmodel` | |
| `mode` | string | `jwt` / `apiKey` | |
| `jwt_token` | **string \| null** | JWT 凭证 | 🔴 **明文**（见 §4.4）；apiKey 账号为 `null` |
| `api_key` | **string \| null** | API Key 凭证 | 🔴 **明文**；jwt 账号为 `null` |
| `enabled` | **bool \| string** | 启用标志 | 🔴 5 行为 `true`(bool)，1 行为 `"True"`(str) |
| `status` | string | 状态机值（全小写） | |
| `quota` | object | 模型额度表，可 `{}` | 见 §4.3 |
| `plan` | object | 套餐，可 `{}` | 实测全 6 行均 `{}` |
| `plans` | array | 套餐列表，可 `[]` | 实测全 6 行均 `[]` |
| `usage` | object | 用量，可 `{}` | 实测全 6 行均 `{}`；**API 输出中不出现** |
| `subscription` | object | 订阅信息，可 `{}` | 见 §4.3 |
| `use_count` | integer | 使用次数 | 实测样例 `1315` |
| `fail_count` | integer | 失败次数 | 实测样例 `1` |
| `risk_strikes` | integer | 风控命中次数 | 实测全 6 行 `0` |
| `recent_results` | array | 最近结果，最多 20 条 | 见 §4.3 |
| `last_used_at` | **number \| null** | 最近使用时刻（unix 秒 float） | |
| `last_checked_at` | **number \| null** | 最近检查时刻 | |
| `cooling_until` | **null** | 冷却截止时刻 | 实测全 6 行 `null` |
| `last_error` | **null** | 最近错误文本 | 实测全 6 行 `null` |
| `created_at` | number | 创建时刻（unix 秒 float） | 与表列一致 |
| `fingerprint` | **object \| null** | 设备指纹 | 见 §4.3 |

### 4.3 嵌套结构（实测）

**`quota`**：`object<model_name, QuotaItem>`，apiKey 账号实测 `{}`，jwt 账号实测 1 键：
```json
{"GLM-5.3-Flash":{"total":300000000,"used":0,"remaining":300000000,
 "expires_at":1791766800,"starts_at":1791633922,"effective_at":1791644400,
 "plan_name":"ZCode Weekend Build","source":"plan"}}
```
`QuotaItem` 字段：`total`/`used`/`remaining` (int)、`expires_at`/`starts_at`/`effective_at` (num)、
`plan_name` (str)、`source` (str, 实测 `plan`)。

**`subscription`**：apiKey 账号实测 5 键，jwt 账号实测 `{}`：
```json
{"level":null,"windows":[],"token_windows":[],"checked_at":1791645312.128439,"no_plan":true}
```
`level`(null)、`windows`(array)、`token_windows`(array)、`checked_at`(num)、`no_plan`(bool)。

**`plan`** / **`plans`** / **`usage`**：实测 6/6 全为空容器（`{}` / `[]` / `{}`）。
其**非空时的结构未实测** —— Go 侧应实现为**通用 JSON 透传**（`json.RawMessage`），不得假设固定 schema。

**`fingerprint`**（7 字段，实测顺序）：
```json
{"platform":"linux","arch":"x64","os_version":"6.18.18.c952-trim","language":"en-US",
 "timezone":"Asia/Shanghai","screen":"1920x1080","device_mid":"<uuid-v4>"}
```
实测 `os_version` 取自宿主内核串；`device_mid` 每账号互异（UUIDv4）。

**`recent_results`**：实测 6/6 长度均为 **20**（上限）。元素 3 字段：
```json
{"ok":true,"at":1791978426.7117856,"detail":"HTTP 200 · glm-4.7-flash · 0.8s"}
{"ok":false,"at":1791978315.0038407,"detail":"HTTP 529 重试 3 次耗尽，冷却"}
```
`ok`(bool)、`at`(num)、`detail`(str)。`detail` 含 **U+00B7 中点** `·`。
黑盒测试断言 `recent_results[-1]` 的 shape 为 `{ok, at, detail}` —— **取末位（最旧）**。

### 4.4 🔴 凭证加密状态（与文档冲突，重大结论）

**实测结论：`data` JSON 内的凭证是 `enc:v1:` 前缀的**明文**，未加密。**

| 字段 | 账号类型 | 实测长度 | `enc:` 前缀 | 结论 |
|------|---------|:---:|:---:|------|
| `api_key` | 4 个 apiKey 账号 | **49** | **否** | 明文（形态 `32hex.16char`） |
| `jwt_token` | 2 个 jwt 账号 | **255** | **否** | 明文 JWT |

`03-data-formats.md` §46 声明「凭证字段在 `data` JSON 内**加密存储**（AES-256-GCM，主密钥来自
`ZCODE_MASTER_SECRET` env）」；`07-roadmap.md` §25 把「凭证落库加密（`ZCODE_MASTER_SECRET`）」列为
**未勾选的待办 `[ ]`** —— 两处文档自相矛盾，**实测支持 roadmap 侧：加密未实现**。

> 🔴🔴 **Go 侧硬要求**：
> 1. 读旧库时必须能读**明文**凭证（不能假定有 `enc:` 前缀）。
> 2. 写库时**必须保持明文**（若 Go 引入加密，老 Python 进程将读不出凭证 → 双进程互操作破裂，
>    对拍与「SQLite 数据兼容」验收直接失败）。
> 3. 除非用户明确要求并**同时**改造老进程，否则**不得**实现 `ZCODE_MASTER_SECRET` 加密。
> 见 `compat-risks.md` CR-15。

**凭证形态（用于校验，非用于回显）**：
- `api_key`：`[a-f0-9]{32}` + `.` + 16 字符（实测 `len=49`）—— 与 `/admin/api/export` 的 `secret` 同源。
- `jwt_token`：三段式 JWT（`len=255`）。
- `token_masked`（API 输出）：前 8 字符 + `…` + 后 6 字符（脱敏形态）。

---

## 5. Go 读写兼容点（逐条，可执行）

| # | 兼容点 | 要求 | 违反后果 |
|:--:|--------|------|----------|
| G-1 | **WAL 模式** | DSN 必须启用 `journal_mode(WAL)`；容忍 `-wal`/`-shm` 存在 | 与老进程并发读写冲突/损坏 |
| G-2 | **`enabled` 双写双读** | 表列写 INTEGER `0/1`；JSON 写 bool；**读 API 输出时取 JSON** | API 输出类型/值与老服务不一致 |
| G-3 | **`enabled` 类型透传** | JSON 里是 `"True"`(str) 就必须原样透传为 `"True"` | `laohao-bigmodel-2420b3c2` 一行对拍差异 |
| G-4 | **`data` JSON 键序** | 序列化顺序必须为 §4.1 的 23 键顺序 | 字节级对拍差异（DB 内容 diff） |
| G-5 | **凭证明文** | 读明文、写明文，**不得**引入加密 | 老进程读不出凭证 |
| G-6 | **`id` 格式** | `^[A-Za-z0-9._·-]+-[0-9a-f]{8}$`，唯一 | 老服务 404 或主键冲突 |

> **G-6 落位分工（t3 定案，避免 t5 误判）**：该约束**只约束「新生成 id」**（未来 CREATE 端点），
> **不是 store 层的写入门禁**。DDL 为 `id TEXT PRIMARY KEY`，**历史行必须可原样写回**（含
> 不符合上述正则的遗留 id），否则老库回写被拒 → 违反 G-5 同级的数据兼容目标。
> 故实现为 `models.ValidAccountID()` 供调用方**主动校验**，store 层**不强制拦截**；
> 若后续要求 store 也拦，属**契约变更**，须另行裁决，不得由实现方自行加严。
| G-7 | **`meta` 优先 DB** | `admin_key`/`gateway_key`/`quota_refresh_interval` 以 DB 为准，env 仅首次播种 | `PUT /settings` 改密失效 |
| G-8 | **`meta.value` 是 TEXT** | 数字也按字符串读写（`"600"`） | 写入 `600`(int) 后老进程 `int(value)` 报错 |
| G-9 | **`created_at` 是 REAL** | unix 秒 float，保留小数位 | 时间精度丢失 |
| G-10 | **无外键** | 不得创建 FK 约束 | 老进程写入被拒 |
| G-11 | **不递增 `schema_version`** | 保持 `4` | 老进程按版本判断迁移，可能拒绝启动 |
| G-12 | **只打开 `accounts.db`** | 忽略 `data/` 下 `*.bak-*`、`device_mid`、`release_version.json` | 误伤备份/误判配置 |
| G-13 | **`plan`/`plans`/`usage` 透传** | 用 `json.RawMessage`，不假设 schema | 非空时解析失败 |
| G-14 | **`recent_results` 上限 20** | 新结果🔴 **后插（`[-1]` 最新）**，超出截头（丢最旧） | **t5 审查期更正，原「前插」错误**；见 CR-16 |
| G-15 | **`quota`/`subscription` 可空容器** | `{}` 与缺失语义不同（实测老服务输出 `{}`） | API 输出 `null` vs `{}` 差异 |
| G-16 | 🔴 **`accounts` 列表排序键** | `(provider 分组序, 组内 `created_at` 升序)` —— **不是** rowid | Go 侧 `ORDER BY rowid` **错**；且对拍 `sort_by:id` 折叠顺序 → **对拍抓不到**。见 CR-35 |

**「超出截头（丢最旧）」的证据等级说明（t5 补验）**：该结论**在更正当时并未被实测**（只验了方向，未验截断端）
—— 属「推断冒充实测」，见 CR-36。**本轮补验后成立**：6/6 账号 `n` **全部恰好 20**，
且 `[-1].at` 与 `last_used_at` 同刻（容差内）→ 保留最新 ⇒ 丢最旧。
> ⚠️ 两者**不精确相等**（Δ ≈ 7e-6~2e-5 秒，系两次独立 `time.time()` 取值）→ 判"同一次调用"须用容差，**不可 `==`**。

**`recent_results` 方向判据（🔴 t5 审查期更正）**：6/6 账号 `at` 序列**严格升序**
（`[0]` = 10-14 11:47 最旧，`[-1]` = 10-14 17:08 最新）→ **新结果后插，`[-1]` 最新**。
因果证据：t5 审查期对 `/v1/messages` 的新探测条目落在 `[-1]`。
⚠️ **t1 原判据写反了**（原文称「`[0]` 的 `at`=1791978426.71，`[19]` 更旧 → `[0]` 最新」——
该值恰是**最旧**的一条）。黑盒测试用 `recent_results[-1]` 断言 shape，
只验形状不验方向，**不构成反证**。完整证据与复现命令见 CR-16。

---

## 6. 可复现探测脚本（供下游复核）

```bash
cd /opt/zcode2api
python3 - <<'EOF'
import sqlite3
c = sqlite3.connect('file:data/accounts.db?mode=ro', uri=True).cursor()
for t in ('accounts', 'meta'):
    print(f'-- PRAGMA table_info({t})')
    for r in c.execute(f'PRAGMA table_info({t})'): print('  ', r)
print('-- objects')
for n, t, s in c.execute("select name,type,sql from sqlite_master where sql is not null order by name"):
    print(f'  [{t}] {n}: {s}')
print('-- pragmas')
for p in ('journal_mode','page_size','encoding','user_version','foreign_keys','auto_vacuum','schema_version'):
    print(f'  {p} =', c.execute(f'PRAGMA {p}').fetchone()[0])
print('-- counts:', c.execute('select count(*) from accounts').fetchone()[0],
                    c.execute('select count(*) from meta').fetchone()[0])
EOF
```

**只读保证**：`mode=ro` URI 打开 —— 本次探测**未写入任何字节**，`-wal` 保持 0 字节。

---

## 7. 未实测并显式标注的项

- `plan` / `plans` / `usage` 的**非空**结构（实测 6/6 为空容器）。
- `subscription.level` 的非 `null` 取值、`windows`/`token_windows` 的非空元素结构。
- `id` 后缀 8 位 hex 的**生成算法**（只确认形态，未确认算法）。
- `cooling_until` / `last_error` 的**非 null** 形态（实测 6/6 为 `null`）。
- `status` 取非 `active` 值时的 DB 形态（实测 6/6 为 `active`；黑盒测试覆盖了内存态 `cooling`）。
- 建库/迁移路径（`schema_version=4` 的 4 次迁移内容）——**未实测**，因需要新建库才能观察，
  而任务要求不得破坏生产状态。
- `enabled` 为 `0` 的账号的 API 输出（实测 6/6 为启用态）。
