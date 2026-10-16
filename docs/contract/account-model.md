# account-model.md — 账号模型契约（冻结版）

> 本文是 Go 版 `zhipucode2api` **账号领域模型的唯一规格**：provider 枚举、`mode` 枚举、
> 状态机、`enabled` 双份存储、`AccountView` 序列化契约。
>
> 🔴 **本文遵循 `adr-001-source-of-truth.md`**：事实来源优先级 = ①运行中服务真实 HTTP 响应
> ②生产库真实 schema/数据 ③黑盒测试 ④描述现状的文档 ⑤`docs/development/03-data-formats.md`
> **仅作未来设计参考、不得作为实现依据** ⑥"设计要点/扩展"章节**默认未实现**。
>
> **规格先于实现声明**：证据来自 ①`http://127.0.0.1:9120` 的**真实 HTTP 探测** ②`data/accounts.db` **只读探测**
> ③`tests/integration/**` 黑盒测试源码 ④`zcode2api/docs/**`（按 ADR-001 分级采信）。
> **未纳入本轮观测** `zcode2api/app/**/*.py`、`cli.py`、`captcha_node/`、`frontend/`。
>
> **证据标记**：`[实测]` 本次直接执行所得（附命令/输出）；`[黑盒]` 来自 `tests/integration/**`；
> `[adr]` captain 裁定；`[docs]` 文档断言但未实测；`unverified` 无证据。

---

## 1. Provider 枚举

| provider | 实测账号数 | 语义 | 上游端点（`.env.example` 默认，**未实测**） |
|----------|:---:|------|------|
| `zai` | **4** | 智谱 Z.AI（ZCode 通道） | `https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages`（回退 `https://api.z.ai/api/anthropic/v1/messages`） |
| `bigmodel` | **2** | 智谱 BigModel 开放平台 | `https://open.bigmodel.cn/api/anthropic/v1/messages` |

`[实测]` 证据：`GET /admin/api/status` → **200**
`{"providers":["zai","bigmodel"],"gateway_key_set":false,"quota_pool":{"zai":4,"bigmodel":2}}`。

**枚举封闭性**：实测 DB 中 `provider` 只有这两个值；`docs/development/03-data-formats.md` §11
亦写 `'zai' | 'bigmodel'` —— **两处一致**，可作为封闭枚举。

> ⚠️ Go 侧**不得**硬编码「只接受这两个值」而拒绝未知 provider：DB 中若出现新值，
> 老服务行为未知（`unverified`）。建议 Go 侧**透传**未知 provider，仅在已知 provider 上启用专属逻辑。

---

## 2. `mode` 枚举（凭证类型）

| mode | 实测账号数 | 凭证字段 | 凭证长度（实测） | 语义 |
|------|:---:|------|:---:|------|
| `jwt` | **2** | `data.jwt_token` | **255** | OAuth 登录所得 JWT（抢包通道） |
| `apiKey` | **4** | `data.api_key` | **49** | API Key（形态 `32hex.16char`） |

🔴 **大小写敏感**：`apiKey` 是**驼峰 K**（不是 `apikey`/`API_KEY`）。`[实测]` DB 6 行逐字。
`docs/03` §12 亦写 `'jwt' | 'apiKey'` —— 一致。

**两字段互斥性（实测）**：`jwt` 账号的 `api_key` 为 `null`，`apiKey` 账号的 `jwt_token` 为 `null`。
**两字段始终同时存在（键存在、值可 null）** —— 23 键集合 6/6 相同，故 Go 侧 struct 需两个可空字段，
**不是** union。

---

## 3. 状态机

### 3.1 状态枚举

| 状态 | 实测出现 | 参与轮询 | 触发条件 | 恢复条件 |
|------|:---:|:---:|------|------|
| `active` | **6/6 行** | ✅ | 默认 / 新增 / 导入 | — |
| `cooling` | 0 行（`[黑盒]` 覆盖） | ⏳ 冷却到期后 | **5xx 重试耗尽**、**连接级失败** | `cooling_until` 到点 |
| `exhausted` | 0 行 | ❌ | 额度=0 / 上游 402 / quota 关键字 | 额度刷新检测到剩余 > 0 |
| `invalid` | 0 行 | ❌ | 401/403 **非验证码**鉴权失败 | 改凭证 / 重新启用 |
| `disabled` | 0 行 | ❌ | **手动禁用**、**3012/405 真风控封禁** | **仅手动启用** |

🔴 **状态值全小写**（`active`，不是 `ACTIVE`）。`[实测]` DB 6 行 `status='active'`。

> **文档冲突（ADR-001 适用）**：`docs/reference/zcode2api-architecture.md` §5 的状态机把
> `429 限流` 画成 `active → cooling`；但 `[黑盒]` `test_gateway_risk_cooldown.py` §3-4 明确
> **「429 频控：账号不冷却，原地等待重试」**，并 §112-116 断言 429 耗尽后
> `status == ACTIVE`、`cooling_until is None`、`risk_strikes == 0`。
> 🔴 **以黑盒测试为准**：**429 不导致 `cooling`**。架构文档的状态图在此处**过时**。

### 3.2 触发条件详解（`[黑盒]` `test_gateway_risk_cooldown.py`）

| 上游情形 | 重试策略 | 账号状态变化 | 证据（测试行） |
|----------|---------|------------|------|
| **429 频控** | 原地重试（默认 5 次；`Retry-After` 优先，封顶 `RETRY_429_WAIT_MAX`）；耗尽后**换号** | **不变**（保持 `active`），不计 `risk_strikes` | §92-116（`test_429_retries_then_fails_over_account_stays_active`） |
| **5xx 等一般错误** | 重试（默认 3 次），耗尽后**冷却** | → `cooling`，`cooling_until = now + COOLING_SECONDS`，`last_error` 含 `"重试 2 次耗尽"`，`risk_strikes` **不计** | §160-179 |
| **3012 / 405「unusual activity」真风控** | **不重试、不退避** | → **`disabled`**，`enabled` 保持 `true`（便于一键恢复），`risk_strikes` **+1**，`cooling_until = None`，`last_error` 含 `"风控封禁"` | §37-56 |
| **连接级失败**（`httpx.HTTPError`） | 换下一个账号 | → `cooling` | `test_gateway_connect_fail.py` §35-37（`assert accounts["a-fail"].status == "cooling"`） |
| **成功（200）** | — | `risk_strikes` **清零**、`status` → `active`、`last_error` → `None`、`cooling_until` → `None`、`use_count` **+1** | §213-230 |

**关键不变量（`[黑盒]` §252-269）**：**冷却期内零上游流量** ——
QuotaMonitor 跳过冷却账号、后台手动 refresh 跳过（`{"count":0,"skipped_cooling":1}`）、
单账号 refresh 返回 `{"ok":false,"message":"...冷却..."}`、claim 跳过（`outcome.ok=false`，`"冷却"` 在 message）、
**验证码池关门**（`mgr._gate_open() is False`，全冷却/封禁时）。

> 🔴 **`disabled` 与 `enabled` 是两回事**：风控封禁设 `status=disabled`，**但 `enabled` 保持 `true`**
> （§51 `assert risk.enabled is True`）—— 「封禁」不是「用户停用」。Go 侧不得把二者混为一谈。

### 3.3 `is_selectable()` / `is_cooling()` 语义（`[黑盒]`）

| 方法 | 语义 | 实测断言 |
|------|------|------|
| `is_selectable()` | 是否可参与轮询 | `disabled` → `False`；`cooling`（未到期）→ `False`；`cooling`（`cooling_until < now`）→ **`True`**；`active` → `True` |
| `is_cooling()` | 是否处于冷却**倒计时**中 | `cooling` + 未到期 → `True`；已到期 → `False`；**`disabled` → `False`**（§56 注释：「封禁不是冷却，不计恢复倒计时」） |

> 🔴 **`is_cooling()` 与 `status==cooling` 不等价**：`disabled` 账号 `is_cooling()` 返回 `False`。
> Go 侧若用 `status == cooling` 替代 `is_cooling()`，验证码池门控（§264-269）会误开。见 CR-25。

### 3.4 轮询算法

`[docs]` `zcode2api-architecture.md` §125-126：`select(provider, skip=已试)` → **round-robin 游标**，
`MAX_ACCOUNT_ATTEMPTS = 5`、`MAX_CAPTCHA_RETRIES = 3`。

> ⚠️ **常量冲突（未实测）**：架构文档 §162 写 `MAX_CAPTCHA_RETRIES = 3`；
> `.env.example` 的 `ZCODE_CAPTCHA_RETRIES` 默认 `4`。**两个数字来源不同**（前者是代码常量，后者是 env 默认），
> **无法判定是否同一参数** —— 标 `unverified`，见 `env-contract.md` CR-21。
> `MAX_ACCOUNT_ATTEMPTS = 5` 与 `docs/03` §（`POOL_MAX_ATTEMPTS = 4`）**亦冲突**；
> 且 `POOL_*` 系列 env **实测不存在**（ADR-001 已裁定），故取架构文档的 `5`，标 `[docs]` 未实测。

**轮询游标持久性**：`[docs]` 架构文档 §95 称「内存账号表 + round-robin 游标」，
`[docs]` `docs/03` §「内存驻留 + 落库同步」。**游标是否落库未实测**（DB 无对应列/meta 键 → 推定**仅内存**）。

---

## 4. 🔴 `enabled` 双份存储（ADR-002 核心）

### 4.1 双份事实（`[实测]` + `[adr]`）

| 存储位置 | 类型 | 实测值（6 行） | 权威性 |
|----------|------|--------------|--------|
| **表列** `accounts.enabled` | `INTEGER NOT NULL DEFAULT 1` | **6 行全为 `1`**（`typeof=integer`） | 存储权威（写路径） |
| **`data` JSON** `enabled` | `bool` **或脏值 `string`** | 5 行 `true`(bool)；**`laohao-bigmodel-2420b3c2` = `"True"`(string)** | **API 输出来源** |

### 4.2 API 输出来源（`[实测]`，关键）

| 账号 | 表列 | `data.enabled` | API 输出 |
|------|:---:|:---:|:---:|
| `oauth-login-ea92aa60` | `1` (int) | `true` (bool) | `true` (bool) |
| `oauth-apikey-c5c641a1` | `1` (int) | `true` (bool) | `true` (bool) |
| `oauth-login-8523aba4` | `1` (int) | `true` (bool) | `true` (bool) |
| `oauth-apikey-8808fbac` | `1` (int) | `true` (bool) | `true` (bool) |
| **`laohao-bigmodel-2420b3c2`** | `1` (int) | **`"True"` (str)** | **`"True"` (str)** |
| `xinhao-2000w-02f71f86` | `1` (int) | `true` (bool) | `true` (bool) |

**判定依据**：表列 6 行**全为 integer `1`**，而 API 输出中该行是 **string `"True"`** ——
这个值**不可能**来自表列，只可能来自 `data` JSON。

> ✅ **老服务 API 输出的 `enabled` 取自 `data` JSON 的 `enabled` 键**（非表列）。

### 4.3 写路径：`SetEnabled` **双写**（`[实测]`，本次新增结论）

用自建探针账号 `probe-toggle-live-06cabd6e` 端到端实测（每步只读回读 DB + API，事后复原并删除）：

| 阶段 | 表列 | `data.enabled` | API 输出 | `data` 键数 |
|------|:---:|:---:|:---:|:---:|
| 新建后 | `1` (int) | `true` (bool) | `true` (bool) | 23 |
| `POST /enabled {"enabled":false}` → 200 `{"ok":true}` | **`0` 【变】** | **`false` 【变】** | **`false` 【变】** | 23（不变） |
| `POST /enabled {"enabled":true}`（复原） | `1` | `true` | `true` | 23 |
| `DELETE [id]` → 200 `{"deleted":1}` | — | — | — | — |

**结论**：`SetEnabled` **双写**（列 + `data` JSON），且**全量序列化回写**（键集合不变、不丢字段）。

> 🔴 **对 Go 版（Lamias 的 `SetEnabled` 需改）**：必须**双写**。
> 只写列 → 老服务 API 输出停留在旧值 → 对拍 L3 值级必挂。见 CR-24。

### 4.3.1 🔴 写死这一条，免得后人误解

> **老服务 `SetEnabled` 同时更新 `accounts.enabled` 列与 `data` JSON 内的 `enabled` 键；
> 全服务唯一 enabled 写入口就是它，且它写两处。**
>
> ⚠️ **反命题（错误）**：「只有列被更新，JSON 里的 `enabled` 是只读遗留脏值」——
> 这是**实测已证伪**的说法。若 Go 版按此实现只写列，`laohao-bigmodel-2420b3c2` 的
> `data.enabled` 会永久停留在 `"True"`，而老服务在任意一次 toggle 后它都会变成 bool。

### 4.3.2 🔴 服务**内存持有账号状态**（t1 新增实测，关键）

**实测方法**（自建探针账号 `probe-dirty-ae9c2738`，假凭证，事后删除、零残留）：

| 步骤 | 操作 | DB 内 `data.enabled` | API 输出 |
|:--:|------|:---:|:---:|
| 1 | 建探针号 | `true` (bool) | `true` (bool) |
| 2 | **用 sqlite3 直接**把 DB 内该键改成 `"True"` (string) | `"True"` (str) | — |
| 3 | 立刻 `GET /admin/api/accounts`（**不重启服务**） | `"True"` (str) | 🔴 **`true` (bool)** |
| 4 | `POST /enabled {false}` | `false` (bool) | `false` (bool) |
| 5 | `POST /enabled {true}` | `true` (bool) | `true` (bool) |

**两条硬结论**：

1. 🔴 **老服务不重读 DB**：第 3 步 DB 已是 `"True"`(str)，服务输出仍是 `true`(bool) ——
   证明老服务在**内存中持有账号对象**，API 输出取自内存，**不是**每次请求查库。
   （反证：若每次读库且做规范化，则 `laohao-bigmodel-2420b3c2` 不会输出未规范化的 `"True"`。）
2. 🔴 **脏值会被 toggle 洗掉**：第 4 步一次 toggle 后，DB 内 `data.enabled` 从
   `"True"`(str) 变为 `false`(bool) —— **脏值不永久驻留**。

> ⚠️ **对 captain ADR-003 §4「库内永久驻留」的精确化**（t1 实测补充）：
> `laohao-bigmodel-2420b3c2` 的 `"True"` 之所以在**当前**输出中可见，是因为它是
> **服务启动时从 DB 加载进内存**的脏值，且**至今未被 toggle 过**。
> 精确表述应为：**脏值驻留至该账号被 toggle 一次为止**（内存与 DB 同时被洗为 bool）。
>
> 🔴 **对拍影响（Liche 必读）**：豁免清单条目 `accounts[].enabled: "True"→true` 的
> **前提是「对拍期间不对该账号执行 toggle」**。若对拍用例对该账号跑 B-07，
> **老服务侧的值会变成 `true`(bool)**，该豁免条目随即**失效**（此时两侧本就相同，
> 差异消失，不是通过而是前提消失）。→ 对拍用例应**避免 toggle 该特定账号**，
> 或把该 case 拆成「先记录基线、再断言」两段。

### 4.4 ADR-002 处置（**规范化**，用户裁定）

| 层 | 老服务行为 | Go 版行为（ADR-002） |
|----|-----------|-------------------|
| **API 序列化** | 原值透传（含 `"True"` 脏值） | **一律输出 JSON `true`/`false`**（归一化：接受 `bool`/`"True"`/`"true"`/`"1"`/数值 `1`） |
| **存储往返** | 原样 | **保持保真**：`map[string]json.RawMessage`，**不得丢字段、不得改脏值**（除非本次写操作明确修改该字段） |
| **旧库脏值** | — | 读到 `"True"` **不得报错**，必须正确解析 |
| **写路径** | 双写（ADR-003 实测） | **双写**（列 INTEGER 0/1 + `data.enabled` bool） |

**对拍影响（必须走豁免清单，严禁静默归一化）**：
`laohao-bigmodel-2420b3c2` 的 `enabled` 老服务 `"True"`(string) → 新服务 `true`(bool)。
豁免条目须写明：字段路径 `accounts[].enabled`、差异形态 `string → bool`、
理由「ADR-002 规范化裁定」、批准人「用户」。见 CR-03。

> 🔴 **脏值驻留边界（t1 实测补充，对 Liche 关键）**：该豁免条目**只在「对拍期间不对该账号执行
> toggle」的前提下成立**。一旦对拍用例对该账号跑 B-07，老服务侧 `data.enabled` 会被洗为
> `true`(bool)，**差异消失**（此时两侧本就相同 —— 是**前提消失**，不是「通过」）。
> 详见 §4.3.2。**对拍用例必须避免 toggle 该特定账号**，或拆成「记录基线 → 再断言」两段。

### 4.5 🔴 冲突场景裁决：**data 优先，列兜底**（ADR-003 终裁，t1 确认）

ADR-002 的两句话分别描述**两个不同的层**，此前被读成冲突：

| ADR-002 原文 | 实际所指的层 | 含义 |
|-------------|------------|------|
| 「`accounts.enabled` **列**是权威来源」 | **写路径（权威）** | 写入口是 `SetEnabled`，它写列（并双写 JSON）。列是**持久化的权威存储**。 |
| 「`data` 值缺失/null → **回退列**」 | **读路径（输出）** | 输出时**先看 `data` JSON**，仅当它不可用时才回退列。 |

**终裁（ADR-003 §1）**：`EffectiveEnabled()` 的读法 = **`data-known → 列兜底`**
（即 Lamias 现状**正确，不需要翻**；ADR-003 §3 明确「保持现状」）。

**裁决依据（ADR-001 优先级 1：运行中服务的真实 HTTP 响应为最高权威）**：

1. `[实测]` 表列 6/6 全为 integer `1`，而 API 输出中 `laohao-bigmodel-2420b3c2` 是 string `"True"` ——
   该值**只能来自 `data` JSON**。若读法是「列覆盖 data」，该账号将输出 `true`(bool)，
   **与老服务实测直接矛盾** → 违反契约等价。
2. 故**列覆盖 data** 的读法被实测**证伪**，不可采用。
3. 「列是权威来源」仅在**写路径**成立（它是写入口落库的位置）。

**证据强度说明（诚实标注）**：
- 上述裁决在**生产 6/6 行上两种读法输出相同**（都 `true`），故**生产无法区分**。
- 区分只在**人为构造的冲突场景**（如 `data.enabled=false` 而列 `=1`）——
  **该场景生产不存在**，属**外推**，标 `unverified`。
- 但外推方向**由实测证据唯一确定**：既然输出恒取自 `data`，冲突时必以 `data` 为准。
- 另：`[实测]` §4.3.2 第 3 步证明**输出取自内存对象**（该内存对象在启动时由 DB 的
  `data` JSON 填充）—— 与「data 优先」结论一致，且**进一步排除**了「输出读表列」的可能。

**`data.enabled` 缺失/null 时的兜底（外推，`unverified`）**：
生产 6/6 行的 `data` 均含 `enabled` 键，**该分支从未被观测**。Go 版按 ADR-002 要求 1 的解析列表
处理：`bool` → 直接取；`"True"/"true"/"1"` → `true`；数值 `1` → `true`；
**无法解析或键缺失 → 回退表列**（列 `!= 0` 即 `true`）。

**统一实现（Go 侧照抄即可）**：
```go
// parseKnownEnabled 把 data["enabled"] 的原始值解析为规范化布尔值。
// 返回 (值, 是否为【已知形态】)。枚举外形态 → known=false，交由调用方兜底。
//
// 已知形态（ADR-002 §要求1 的解析列表）：
//   bool            → 直接取
//   "True"/"true"/"1"   → true
//   "False"/"false"/"0" → false
//   数值（JSON number 解码为 float64）→ != 0
// 枚举外形态（"banana"、"TRUE"、对象、数组等）→ known=false。
//   ⚠️ 不得把枚举外形态判成 false —— 那会把「无法解析」伪装成「已解析为禁用」，
//   与下方正文「无法解析 → 回退表列」矛盾。
func parseKnownEnabled(v any) (val bool, known bool) {
    switch t := v.(type) {
    case bool:
        return t, true
    case string:
        switch t {
        case "True", "true", "1":
            return true, true
        case "False", "false", "0":
            return false, true
        }
        return false, false // 枚举外字符串
    case float64:
        return t != 0, true
    }
    return false, false // 对象/数组/null 等
}

// EffectiveEnabled 返回对外输出的规范化布尔值。
// 读路径：data JSON 优先（老服务实测输出来源），列兜底。ADR-003 §1/§3 终裁。
func (a *Account) EffectiveEnabled() bool {
    if v, ok := a.Data["enabled"]; ok && v != nil {
        if val, known := parseKnownEnabled(v); known {
            return val
        }
    }
    return a.EnabledCol != 0   // 兜底：表列（未观测分支）
}
```

> **本文正文为准**：上文 §4.5「**无法解析或键缺失 → 回退表列**」是本节的**规范表述**；
> 代码块是该表述的实现。若两者冲突，**以正文为准并修代码块**（本条由 t3 交付时的
> 文档自审发现，Lamias 按正文实现，`models.Enabled()` accessor 即两态语义）。
> 生产 6/6 行不受影响（`laohao-bigmodel-2420b3c2` 的 `"True"` 两种读法同为 `true`），
> 该差异只落在**未观测的外推域**（`unverified`）。
**写路径（ADR-003 §2 强制）**：`SetEnabled(v)` → **双写**
`EnabledCol = boolToInt(v)` **且** `Data["enabled"] = v`（bool）。
写 data 时须**保真**：只改 `enabled` 这一个键，其余键与原字节不动（Lamias 的
`map[string]json.RawMessage` 设计已支持）。

---

## 5. `data` JSON 字段清单（`[实测]`）

**23 键，6/6 账号键集合完全相同，顺序一致**（详见 `sqlite-schema.md` §4）。

| 字段 | 类型（实测） | 语义 | 是否进 `AccountView` |
|------|------------|------|:---:|
| `id` | string | 主键 | ✅ |
| `name` | string | 显示名 | ✅ |
| `provider` | string | `zai`/`bigmodel` | ✅ |
| `mode` | string | `jwt`/`apiKey` | ✅ |
| `jwt_token` | string \| null | JWT 凭证（**明文**） | ❌ → 替换为 `token_masked` |
| `api_key` | string \| null | API Key 凭证（**明文**） | ❌ → 替换为 `token_masked` |
| `enabled` | bool \| string | 启用标志（**§4 双份**） | ✅ |
| `status` | string | 状态机值 | ✅ |
| `quota` | object | 模型额度表（可 `{}`） | ✅ |
| `plan` | object | 套餐（实测全 `{}`） | ✅ |
| `plans` | array | 套餐列表（实测全 `[]`） | ✅ |
| `usage` | object | 用量（实测全 `{}`） | ❌ **不出现在 AccountView** |
| `subscription` | object | 订阅（可 `{}`） | ✅ |
| `use_count` | integer | 使用次数 | ✅ |
| `fail_count` | integer | 失败次数 | ✅ |
| `risk_strikes` | integer | 风控命中次数 | ✅ |
| `recent_results` | array（≤20） | 最近结果，🔴 **`[-1]` 最新（后插）** | ✅ |
| `last_used_at` | number \| null | 最近使用时刻 | ✅（可空） |
| `last_checked_at` | number \| null | 最近检查时刻 | ✅（可空） |
| `cooling_until` | null（实测 6/6） | 冷却截止 | ✅（可空） |
| `last_error` | null（实测 6/6） | 最近错误文本 | ✅（可空） |
| `created_at` | number | 创建时刻（unix 秒） | ✅ |
| `fingerprint` | object \| null | 设备指纹（7 字段） | ✅（可空） |

### 5.1 `AccountView` 序列化契约（`[实测]`，21 字段，顺序即实测响应顺序）

```
id, name, provider, mode, token_masked, enabled, status, quota, subscription,
plan, plans, use_count, fail_count, risk_strikes, recent_results, last_used_at,
last_checked_at, cooling_until, last_error, created_at, fingerprint
```

**与 `data` JSON 的差异**：
- `jwt_token` + `api_key` **两个键合并为一个** `token_masked`（位置在 `mode` 之后、`enabled` 之前）。
- `usage` **完全省略**。
- 其余 20 键**顺序完全一致**。

**`token_masked` 形态（`[实测]`）**：`"eyJhbGci…Hbs6FU"` = 前 8 字符 + `…`(U+2026) + 后 6 字符。
与 `docs/03` §47 的 `{key[:8]}…{key[-6:]}` **一致**。

> 🔴 **`data` 的键序 ≠ `AccountView` 的键序**（前者 `jwt_token, api_key` 两键，后者一个 `token_masked`）。
> Go 侧若用同一 struct 序列化两处，键序必错 → 见 `sqlite-schema.md` CR-14。

### 5.2 `fingerprint` 7 字段（`[实测]`）

```
platform, arch, os_version, language, timezone, screen, device_mid
```
实测样例：`{"platform":"linux","arch":"x64","os_version":"6.18.18.c952-trim","language":"en-US",
"timezone":"Asia/Shanghai","screen":"1920x1080","device_mid":"<uuid-v4>"}`。
`rotate` 接口实测返回同 7 字段（`POST .../fingerprint/rotate` → `{"ok":true,"fingerprint":{...}}`）。

### 5.3 `quota` 与 `subscription`（`[实测]`，按 mode 分化）

| mode | `quota` | `subscription` |
|------|---------|---------------|
| `jwt` | **非空**（实测键 `GLM-5.3-Flash`，`{total,used,remaining,expires_at,starts_at,effective_at,plan_name,source}`） | `{}` |
| `apiKey` | `{}` | **非空**（`{level:null,windows:[],token_windows:[],checked_at,no_plan:true}`） |

> **分化规律**：`quota` 只对 `jwt` 账号填充，`subscription` 只对 `apiKey` 账号填充。
> **非空结构**（如 `subscription.level` 非 null）未实测 → 见 `sqlite-schema.md` §7。

### 5.4 `recent_results`（`[实测]`）

实测 6/6 长度 **20**（上限）。元素：`{ok:bool, at:number, detail:string}`。
`detail` 含 U+00B7 中点 `·`，样例：`"HTTP 200 · glm-4.7-flash · 0.8s"`、`"HTTP 529 重试 3 次耗尽，冷却"`。
`[黑盒]` 断言 `[-1]` 元素 shape 为 `{ok, at, detail}`。

**方向（`[实测]`，🔴 t5 审查期更正）**：6/6 账号 `at` 序列**严格升序** ——
`[0]` = 10-14 11:47（**最旧**），`[-1]` = 10-14 17:08（**最新**）
→ **新结果后插（append 到尾部），`[-1]` 最新，`[0]` 最旧**。

> ⚠️ **t1 原结论写反了**：原文称「`[0]` 的 `at` 大于 `[19]` → `[0]` 最新（前插）」。
> `1791978426.71` 换算即 10-14 11:47，恰是**最旧**的一条。
> **因果证据**：t5 审查期（13:01/13:02 UTC）对 `/v1/messages` 的新探测条目落在 `[-1]`。
> 三方证据（DB 原始字节 / API 输出 / 因果）一致。完整证据与复现命令见 CR-16。

---

## 6. 凭证明文事实（`[实测]`，与 `docs/03` 冲突）

**实测**：`data` JSON 内 `api_key`（len 49）与 `jwt_token`（len 255）**均无 `enc:v1:` 前缀 → 明文**。
`docs/03` §46 声称「AES-256-GCM 加密存储」；但 `docs/development/07-roadmap.md` §25 把
「凭证落库加密（`ZCODE_MASTER_SECRET`）」列为**未勾选待办 `[ ]`**。

🔴 **按 ADR-001**：`docs/03` 是**未来蓝图**，`ZCODE_MASTER_SECRET` env **实测不存在** → 加密**未实现**。
**Go 版必须读明文、写明文**，不得引入加密（否则老进程读不出凭证）。见 CR-15。

---

## 7. Go 实现清单（可直接落地）

| # | 规则 | 证据 |
|:--:|------|------|
| A-1 | `provider` 枚举 `zai`/`bigmodel`，未知值**透传**不拒绝 | `[实测]` status 响应 |
| A-2 | `mode` 枚举 `jwt`/`apiKey`（**驼峰 K**） | `[实测]` DB 6 行 |
| A-3 | `jwt_token` 与 `api_key` 是**两个独立可空字段**（非 union） | `[实测]` 23 键集合 |
| A-4 | 状态值**全小写**；`disabled` 与 `enabled=false` **是两回事** | `[实测]` + `[黑盒]` §51 |
| A-5 | **429 不导致 `cooling`**（架构文档状态图过时） | `[黑盒]` §92-116 |
| A-6 | 5xx 耗尽 → `cooling`；3012/405 → `disabled` + `risk_strikes+1` | `[黑盒]` §160-179 / §37-56 |
| A-7 | 成功 → `risk_strikes=0`、`status=active`、`last_error=nil`、`cooling_until=nil`、`use_count+1` | `[黑盒]` §213-230 |
| A-8 | `is_cooling()` **不等于** `status==cooling`（`disabled` 时为 false） | `[黑盒]` §56 |
| A-9 | 冷却期**零上游流量**（monitor/refresh/claim/验证码池全部跳过） | `[黑盒]` §252-269 |
| A-10 | **`SetEnabled` 双写**（列 INTEGER + `data.enabled` bool） | `[实测]` §4.3 |
| A-11 | API 序列化 `enabled` **归一为 bool**（ADR-002），存储**保真透传** | `[adr]` ADR-002 |
| A-12 | `AccountView` 21 字段**按实测顺序**输出；`token_masked` 替代两凭证键；省略 `usage` | `[实测]` §5.1 |
| A-13 | `token_masked` = 前 8 + `…` + 后 6 | `[实测]` + `[docs]` §47 一致 |
| A-14 | `recent_results` 上限 20，🔴 **后插（`[-1]` 最新，`[0]` 最旧）** —— **t5 审查期更正，原「前插」结论错误** | `[实测]` 三方证据，见 §5.4 |
| A-15 | 凭证**明文**读写，不加密 | `[实测]` + `[adr]` ADR-001 |
| A-16 | `quota`/`subscription`/`plan`/`plans`/`usage` 用 `json.RawMessage` 透传 | `[实测]` 全空容器 |

---

## 8. 未实测并显式标注的项

- `MAX_ACCOUNT_ATTEMPTS`（`5`）与 `POOL_MAX_ATTEMPTS`（`4`）的真实取值 —— `[docs]`，且 `POOL_*` env 实测不存在。
- ~~`MAX_CAPTCHA_RETRIES`（`3`）与 `ZCODE_CAPTCHA_RETRIES`（`4`）是否同一参数 —— `unverified`。~~
  **已关闭为「否，不是同一参数」**（t22 / Hilkiah，2026-10-16，判据实测）：二者**层级与对象都不同** ——
  - `MAX_CAPTCHA_RETRIES = 3`：**gateway 挑战重试上限**（`gateway.py` L441，单次对话请求内遇到验证码挑战后最多重试几次）
  - `ZCODE_CAPTCHA_RETRIES = 4`：**solver 单次求解的尝试次数**（`solver.js` 调用层，一次 Solve 内最多尝试几轮）

  实测判据：求解层 attempt 分母恒为 `{4}`（无 3 无 5）；attempt 分布闭合
  （3548 进入 → 3061+370+77 = 3508 成功）；`diff | grep -c MAX_CAPTCHA_RETRIES` = **0**
  ⇒ 重试次数**未被 I-3 惰性化改变**。
  Go 侧对应：`internal/captcha.DefaultAttempts = 4`（`runner`，语义已钉死为"每次 Solve 的总尝试数"，见 CR-21 注记）。
  **契约落地**：CR-21 的两个数字**本就不冲突** —— 它们描述不同层的不同对象。
  原"冲突"叙述保留在下方以免历史丢失，但**结论已由 unverified 转为已判定**。
- round-robin **游标是否持久化** —— DB 无对应列/键，推定仅内存，`unverified`。
- `cooling` / `exhausted` / `invalid` / `disabled` 在生产库中的**真实行**（实测 6/6 全 `active`）。
- `subscription.level` 非 `null`、`windows`/`token_windows` 非空的**结构**。
- `plan` / `plans` / `usage` **非空**结构（实测 6/6 为空容器）。
- `last_error` / `cooling_until` 的**非 null** 形态。
- `exhausted` 的**触发细节**（402 vs quota 关键字的具体判定）—— 仅 `[docs]` 架构文档，`unverified`。
- `disabled` 的**自动恢复**路径：`[黑盒]` §6-8 明确「不做自动退避，需人工手动启用」，
  但后台是否另有定时任务自动恢复 —— `unverified`。
