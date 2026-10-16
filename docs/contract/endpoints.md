# endpoints.md — zhipucode2api 端点契约（冻结版）

> 本文是 Go 版 `zhipucode2api` 的**唯一端点规格**。下游 Go 实现（Lamias）与对拍验证（Liche）逐字依赖本文。
>
> **规格先于实现声明**：本文全部事实来自 ①`zcode2api/docs/**` 协议文档 ②`zcode2api/tests/integration/**` 黑盒测试
> ③对运行中老服务的 HTTP 实测 ④SQLite 只读探测。**未纳入本轮观测** `zcode2api/app/**/*.py`、`cli.py`、`captcha_node/`、`frontend/`。
>
> **实测环境**：老服务 `http://127.0.0.1:9120`（uvicorn），实测日期 2026-10-15，版本 `2.5.3`（来自 `GET /meta`）。
> 枚举手段：`GET /openapi.json`（FastAPI 自动生成的路由表，属设计意图层，实测 200，19913 字节）。
>
> **敏感值处理**：本文所有示例中的密钥/JWT/token 一律写作 `<redacted>`、`<jwt_token>`、`<api_key>`、
> `<ZCODE_ADMIN_KEY>` 占位符。`token_masked` 字段的示例值亦为伪造（该字段虽已脱敏，仍不落真实片段）。

---

## 0. 计数口径（33 条）

| 组 | 条数 | 范围 |
|----|:---:|------|
| A 网关（对外） | 3 | `/v1/models`、`/v1/messages`、`/v1/chat/completions` |
| B 管理 API（`/admin/api`） | 22 | 见 §2（与 `openapi.json` 的 `/admin/api/*` 路由数**精确吻合**） |
| C 页面 | 6 | `/`、`/admin`、`/admin/login`、`/admin/accounts`、`/admin/monitoring`、`/admin/settings` |
| D 元数据与工具 | 2 | `/meta`、`/openapi.json` |
| **合计** | **33** | |

**口径说明（必读）**：`/docs`、`/redoc`、`/docs/oauth2-redirect`、`/static/**` 是 FastAPI 与 StaticFiles 的
**内置附属路由**（实测均 200），不属业务端点，故不计入 33，列于附录 §5。

**`/admin/api/*` 实测恰为 22 条**，与任务书「管理 API 22 条」一致 —— 这是强证据，说明 22 这个数字
来自老服务真实路由表，而非文档估计。

---

## 1. 通用契约

### 1.1 鉴权

| 范围 | 头/参数 | 规则 | 实测 |
|------|---------|------|------|
| 管理 API `/admin/api/*` | `Authorization: Bearer <ZCODE_ADMIN_KEY>` **或** `?app_key=<ZCODE_ADMIN_KEY>` | 二选一，等价。缺失 → 401 `缺少鉴权凭证`；不匹配 → 401 `鉴权凭证无效` | ✅ 见 §1.3 |
| 网关 `/v1/*` | `Authorization: Bearer <gateway_key>` **或** `x-api-key: <gateway_key>` | **仅当 meta 表 `gateway_key` 非空才校验**；为空则完全放行（实测当前为空 → 无凭证 200） | ✅ `GET /v1/models` 无凭证 200；带错误 key 亦 200 |
| 页面 `/admin/*`、`/` | **无服务端鉴权** | 页面本体对匿名请求返回 200；鉴权在**客户端 JS** 做（`localStorage` 存 admin key） | ✅ `GET /admin/accounts` 无凭证 200 |

> ⚠️ 页面层无服务端鉴权是**对外可观测行为**，Go 版必须一致复现，否则对拍在状态码层即差异。

### 1.2 错误响应形态（两套，不可混用）

**形态 A —— FastAPI HTTPException**（全部 `/admin/api/*` 与未知路径）：

```json
{"detail":"<message>"}
```

| 状态码 | `detail` 取值（实测原文，逐字） | 触发 |
|:---:|------|------|
| 400 | `缺少 account_id` | `/claim/manual` 未给 account_id |
| 400 | `请输入至少一个 Token / API Key` | `/accounts` 空 tokens |
| 401 | `缺少鉴权凭证` | 管理接口无凭证 |
| 401 | `鉴权凭证无效` | 管理接口凭证不匹配 |
| 404 | `账号不存在` | 账号级操作目标 id 不存在 |
| 404 | `JWT 账号不存在` | `/claim/manual` 目标非 JWT 账号或不存在 |
| 404 | `Not Found` | 未知路径（含 `/nope`、`/admin/api/nope`） |
| 405 | `Method Not Allowed` | 路径存在但方法不支持 |

**形态 B —— 网关自有错误**（`/v1/*`）：

```json
{"error":{"message":"<人读信息>","type":"<机器可读类型>"}}
```

> 🔴 **键序陷阱**：实测键序是 **`message` 在前、`type` 在后**。
> 而 `zcode2api/docs/development/04-api-spec.md` §1.6 写的是 `type` 在前 —— **文档与实测不一致，以实测为准**。
> 字节级对拍必须复现实测键序。详见 `compat-risks.md` CR-02。

| 状态码 | 实测 `type` | 触发 | 实测证据 |
|:---:|------|------|------|
| 400 | `invalid_request`（**无 `_error` 后缀**） | `/v1/messages` 请求体非法 JSON | ✅ 实测 `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request"}}` |
| 400 | `invalid_request_error`（**带 `_error` 后缀**） | `/v1/chat/completions` 请求体非法 JSON | ✅ 实测 `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request_error"}}` |

> 🔴 同一语义、两个不同 `type` 值（`invalid_request` vs `invalid_request_error`），**取决于端点**。
> 这是老服务的真实不一致，Go 版若统一为一个值，对拍必挂。详见 `compat-risks.md` CR-01。

`04-api-spec.md` §1.6 还声明了这些 type（**本次未实测**，因会消耗真实上游额度，标注为「文档声明、未实测」）：
`authentication_error`(401/403)、`start_plan_jwt_invalid`(401)、`request_too_large`(413)、
`captcha_error`(500)、`upstream_unreachable`(502)、`translation_failed`(502)、
`credential_unavailable`/`no_available_account`(503)。

### 1.3 鉴权实测记录

| 请求 | 状态码 | 响应体 |
|------|:---:|------|
| `GET /admin/api/accounts` 无凭证 | **401** | `{"detail":"缺少鉴权凭证"}` |
| `GET /admin/api/accounts` `Authorization: Basic ...` | **401** | `{"detail":"缺少鉴权凭证"}`（非 Bearer 形态一律视为缺失） |
| `GET /admin/api/accounts` `Authorization: Bearer wrong-key-0000` | **401** | `{"detail":"鉴权凭证无效"}` |
| `GET /admin/api/accounts?app_key=nope` | **401** | `{"detail":"鉴权凭证无效"}` |
| `GET /admin/api/accounts?app_key=$ZCODE_ADMIN_KEY` | **200** | 与 Bearer 形态响应一致 |
| `GET /admin/api/verify` 无凭证 | **401** | `{"detail":"缺少鉴权凭证"}` |
| `POST /admin/api/monitoring/clear` 无凭证 | **401** | `{"detail":"缺少鉴权凭证"}` |

### 1.4 响应头契约

实测老服务由 uvicorn 直出，无 CORS 头（`OPTIONS /v1/messages` 实测 **405**，`allow: POST`）。

| 头 | 值 | 出现范围 |
|----|----|----------|
| `server` | `uvicorn` | 全部响应 |
| `content-type` | `application/json`（无 charset） | 全部 JSON 响应 |
| `content-type` | `application/json; charset=utf-8` | **上游透传响应**（如 `/v1/messages` 成功/上游错误体） |
| `content-type` | `text/html; charset=utf-8` | 页面 |
| `content-type` | `text/css; charset=utf-8` / `text/javascript; charset=utf-8` | 静态资源 |
| `cache-control: no-store` | — | **仅 `/admin/login` 页面**（实测；其余页面无此头） |

> Go 版 `net/http` 默认不写 `server` 头。若对拍严格到响应头层，需显式设置 `Server: uvicorn`。见 `compat-risks.md` CR-09。

### 1.5 尾斜杠重定向（FastAPI 默认行为）

实测：`GET /admin/api/accounts/refresh/` → **307** → `Location: /admin/api/accounts/refresh`（去尾斜杠）。
`GET /admin/` → **307** → `/admin`；`GET /static` → **307** → `/static/`。

Go 版若不实现尾斜杠规范化，对拍会在 307 与 200 之间产生结构性差异。见 `compat-risks.md` CR-10。

---

## 2. B 组 · 管理 API（22 条，前缀 `/admin/api`）

**全部 22 条统一要求**：
- 鉴权：`Authorization: Bearer <ZCODE_ADMIN_KEY>` 或 `?app_key=`
- 请求头：带 JSON body 的方法需 `Content-Type: application/json`
- 错误：形态 A（§1.2），401 两种 detail 见 §1.1

`openapi.json` 为每条端点声明了两个可选参数：`app_key`(query, string|null) 与 `authorization`(header, string|null) —— 与实测一致。

### B-01 `GET /admin/api/verify`

| 项 | 内容 |
|----|------|
| 请求结构 | 无 body、无参数 |
| 成功响应 | `{"status":"ok"}` — `status`:string(非空) |
| 错误码 | 401 形态 A |
| **实测** | **200**，body `{"status":"ok"}`（15 字节） |

用途：前端 `auth.js` 的 `verifyKey()` 用它探测密钥是否有效（`fetch(...).ok`）。

### B-02 `GET /admin/api/accounts`

| 项 | 内容 |
|----|------|
| 请求结构 | 无 body |
| 成功响应 | `{"accounts":[<AccountView>...],"stats":{...},"providers":[...],"ts":<float>}` |
| 错误码 | 401 形态 A |
| **实测** | **200**，14424 字节 / 6 账号 |

**顶层字段（逐字，实测）**：

| 字段 | 类型 | 可空 | 实测值/说明 |
|------|------|:---:|------|
| `accounts` | array\<AccountView\> | 否 | 见下 |
| `stats` | object | 否 | `{"total":6,"active":6,"exhausted":0,"cooling":0,"invalid":0,"disabled":0,"calls":2724,"fail":4}` |
| `providers` | array\<string\> | 否 | `["zai","bigmodel"]`（**顺序敏感**） |
| `ts` | number(float, unix秒) | 否 | 响应生成时刻，如 `1792063649.872453` |

**`stats` 子字段**：`total`/`active`/`exhausted`/`cooling`/`invalid`/`disabled`/`calls`/`fail` 全为 integer(非空)。

#### 🔴 `accounts` 数组顺序契约（t5 审查期新增，`[实测]` 三方交叉验证）

**排序键 = `(provider 分组序, 组内 created_at 升序)`** —— **不是** rowid 序，**不是** id 序。

| 证据 | 内容 |
|---|---|
| **① 组内反证** | `zai` 组内 rowid = `[13211, 12899, 13212, 12900]`（**乱序**），而 created_at = `[1790328959.0, 1790328961.4, 1790329509.2, 1790329520.1]`（**严格升序**）→ 排除 rowid |
| **② 候选穷举** | rowid 升/降序、id 升序、name codepoint 升/降、`(provider,name)`、`(provider,rowid)`、`(provider,data.created)`、单列 created_at —— **全部 ❌**；唯一 ✅ = `(provider分组序, created_at升序)` |
| **③ 端点交叉** | `/admin/api/export` 的 `providers.zai`/`providers.bigmodel` 数组顺序与 `/admin/api/accounts` 的组内顺序**逐项一致**（`zai` ✅、`bigmodel` ✅）→ 两端点同一排序逻辑 |
| **④ 稳定性** | 连查 5 次 `/admin/api/accounts` 顺序**逐次一致** ✅ |
| **⑤ 组序来源** | `provider` 组序 = `providers` 字段序 `["zai","bigmodel"]`（与 B-05 实测一致） |
| **⑥ created_at 列/data 一致** | 6/6 账号 列 `created_at` 与 `data.created_at` 数值一致（Δ<0.001）→ 用哪个作键等价 |

> ⚠️ **Go 侧现状（t3）**：`internal/store/store.go:214` `ListAccounts` 用 `ORDER BY rowid`
> → 实测产出 `[xinhao-2000w…, oauth-apikey-c5c641a1, oauth-apikey-8808fbac, laohao-bigmodel…, oauth-login-ea92aa60, oauth-login-8523aba4]`
> → **与老服务顺序完全不同**。须改为 `ORDER BY provider, created_at`（组序按 providers 表序）。

> 🔴 **对拍盲区（与 CR-16b 同型）**：`scripts/conformance/normalize.rules.json` 首条规则
> `{"path":"$.accounts","sort_by":"id", ...}` **把账号数组按 id 重排** →
> **顺序差异在对拍中完全不可见**。该规则的理由（"位置顺序是实现查询游标，不是契约"）**不成立**：
> 顺序稳定、跨端点一致、可被 UI 观察到 → 它是**持久契约**。
> 必须由**单侧断言**（契约测试/单元测试）覆盖，不能依赖对拍。详见 `compat-risks.md` CR-35。

**`AccountView` 字段（逐字，实测键序）**：

| 序 | 字段 | 类型 | 可空 | 实测样例 / 语义 |
|:--:|------|------|:---:|------|
| 1 | `id` | string | 否 | `oauth-login-ea92aa60` |
| 2 | `name` | string | 否 | `老号·抢包(JWT)`（UTF-8 中文原样） |
| 3 | `provider` | string | 否 | `zai` / `bigmodel` |
| 4 | `mode` | string | 否 | `jwt` / `apiKey`（**驼峰 K**） |
| 5 | `token_masked` | string | 否 | 脱敏凭证，形如 `xxxxxxx…xxxxxx`（前 8 + `…` + 后 6） |
| 6 | `enabled` | **bool \| string** | 否 | 🔴 **类型不统一**：5 账号为 `true`(bool)，`laohao-bigmodel-2420b3c2` 为 `"True"`(string)。详见 §2.1 |
| 7 | `status` | string | 否 | `active` / `exhausted` / `cooling` / `invalid` / `disabled`（**全小写**） |
| 8 | `quota` | object\<string,QuotaItem\> | 否（可空对象 `{}`） | 键=模型名；见下 |
| 9 | `subscription` | object | 否（可为 `{}`） | 见下 |
| 10 | `plan` | object | 否（可为 `{}`） | 见下 |
| 11 | `plans` | array | 否（可为 `[]`） | 见下 |
| 12 | `use_count` | integer | 否 | `1315` |
| 13 | `fail_count` | integer | 否 | `1` |
| 14 | `risk_strikes` | integer | 否 | `0` |
| 15 | `recent_results` | array\<RecentResult\> | 否（可 `[]`） | 最多 20 条，🔴 **正序（`[0]` 最旧、`[-1]` 最新）** |
| 16 | `last_used_at` | number\|null | **是** | unix 秒 float |
| 17 | `last_checked_at` | number\|null | **是** | unix 秒 float |
| 18 | `cooling_until` | number\|null | **是** | unix 秒；当前 6 账号全 `null` |
| 19 | `last_error` | string\|null | **是** | 当前 6 账号全 `null` |
| 20 | `created_at` | number | 否 | unix 秒 float |
| 21 | `fingerprint` | object\|null | **是** | 见下 |

> ⚠️ `usage` 存在于 `data` JSON（DB 内）但**不出现在 `AccountView`** 中 —— 实测确认。见 `account-model.md`。

**`quota` 子结构**：`{"<model>":{"total":int,"used":int,"remaining":int,"expires_at":num,"starts_at":num,"effective_at":num,"plan_name":str,"source":str}}`
实测样例（jwt 账号）：
```json
{"GLM-5.3-Flash":{"total":300000000,"used":0,"remaining":300000000,
 "expires_at":1791766800,"starts_at":1791633922,"effective_at":1791644400,
 "plan_name":"ZCode Weekend Build","source":"plan"}}
```
`source` 实测取值：`plan`。apiKey 账号的 `quota` 为 `{}`。

**`subscription` 子结构**（仅 apiKey 账号有值）：`{"level":null,"windows":[],"token_windows":[],"checked_at":<float>,"no_plan":true}`
jwt 账号的 `subscription` 为 `{}`。

**`fingerprint` 子结构**（7 字段，逐字，顺序实测）：
```json
{"platform":"linux","arch":"x64","os_version":"6.18.18.c952-trim","language":"en-US",
 "timezone":"Asia/Shanghai","screen":"1920x1080","device_mid":"<uuid-v4>"}
```
`platform`/`arch`/`os_version`/`language`/`timezone`/`screen`/`device_mid` 全为 string(非空)。
`device_mid` 为 UUIDv4 形态（实测 6 个账号互异）。

**`recent_results` 子结构**：`{"ok":bool,"at":float,"detail":string}` —— 实测 `detail` 样例
`"HTTP 200 · GLM-5.3-Flash · 3.2s"`、`"HTTP 529 重试 3 次耗尽，冷却"`（`·` 为 U+00B7 中点）。

### 2.1 🔴 `enabled` 类型泄漏（重大契约事实）

| 账号 | DB 表列 `enabled` | DB `data` JSON `enabled` | API 输出 `enabled` |
|------|:---:|:---:|:---:|
| `oauth-login-ea92aa60` | `1` (integer) | `true` (bool) | `true` (bool) |
| `oauth-apikey-c5c641a1` | `1` (integer) | `true` (bool) | `true` (bool) |
| `oauth-login-8523aba4` | `1` (integer) | `true` (bool) | `true` (bool) |
| `oauth-apikey-8808fbac` | `1` (integer) | `true` (bool) | `true` (bool) |
| **`laohao-bigmodel-2420b3c2`** | `1` (integer) | **`"True"` (string)** | **`"True"` (string)** |
| `xinhao-2000w-02f71f86` | `1` (integer) | `true` (bool) | `true` (bool) |

**结论（硬契约，三条）**：

1. `enabled` 在**两个地方各存一份**：表列 `accounts.enabled`（INTEGER）与 `data` JSON 的 `enabled` 键。
   二者**可以不一致**（表列 6 行全 `1`，data JSON 却有一个 `"True"`）。
2. **API 输出的 `enabled` 取自 `data` JSON，不是表列** —— 证据：`laohao-bigmodel-2420b3c2` 表列是
   integer `1`，而 API 输出 `"True"`，该值只可能来自 data JSON。Go 版必须照此取值顺序，否则对拍必挂。
3. 这是**库里的脏数据 + 原值透传**，不是设计分支。触发条件**不是** provider / mode / status / name：
   全表唯一区分 `laohao-bigmodel-2420b3c2` 的差异就是它自己 `data.enabled` 的值类型。**无法用其他列预测**，
   因此 Go 版唯一正确的复现方式是「原样透传 data JSON 的值」。

裁决建议见 `compat-risks.md` CR-03。

### B-03 `POST /admin/api/accounts`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `{"provider":string,"tokens":array\<string\>,"name"?:string}` —— `openapi.json` 声明为 `additionalProperties:true`，实测接受 `tokens` 数组形态 |
| 成功响应 | `{"count":integer,"ids":array\<string\>}` |
| 错误码 | 400 `请输入至少一个 Token / API Key`；401 形态 A |
| **实测** | 空 body `{}` → **400** `{"detail":"请输入至少一个 Token / API Key"}`；`{"provider":"zai"}` → **400** 同文；`{"provider":"zai","tokens":["<fake>"],"name":"contract-probe"}` → **200** `{"count":1,"ids":["contract-probe-bf31b757"]}` |

**实测 id 派生规律**：id 前缀取自 `name`（`contract-probe-bf31b757`），后缀 8 位十六进制。
`zcode2api/docs/development/03-data-formats.md` 写的是 `zai-<sha256[:12]>` —— **与实测不符**，以实测为准（见 CR-11）。

**副作用（实测 + 黑盒测试佐证）**：新增 JWT 账号会**调度自动领取**（`test_add_account_schedules_auto_claim`）；
重复凭证去重后**不再触发**。

### B-04 `DELETE /admin/api/accounts`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `["<account_id>", ...]` —— **JSON 数组**（`openapi.json` 声明 `{"type":"array","items":{"type":"string"}}`） |
| 成功响应 | `{"deleted":integer}` |
| 错误码 | 401 形态 A |
| **实测** | `[]` → **200** `{"deleted":0}`；`["__no_such_id__"]` → **200** `{"deleted":0}`；`["<真实id>"]` → **200** `{"deleted":1}` |

> 🔴 **注意路径**：删除是**集合级** `DELETE /admin/api/accounts` + body 数组，
> **不是** `DELETE /admin/api/accounts/{id}`（实测单项 DELETE → **405**）。见 CR-04。

### B-05 `GET /admin/api/status`

| 项 | 内容 |
|----|------|
| 成功响应 | `{"providers":array\<string\>,"gateway_key_set":bool,"quota_pool":object\<string,int\>}` |
| **实测** | **200** `{"providers":["zai","bigmodel"],"gateway_key_set":false,"quota_pool":{"zai":4,"bigmodel":2}}`（92 字节） |

字段：`providers`(array,非空)、`gateway_key_set`(bool,非空)、`quota_pool`(object，键=provider，值=该 provider 账号数)。

### B-06 `PUT /admin/api/accounts/{account_id}`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `additionalProperties:true`；实测接受 `{"name":"probe-renamed"}` |
| 成功响应 | `{"ok":true}` |
| 错误码 | 404 `账号不存在`；401 形态 A |
| **实测** | 真实 id + `{}` → **200** `{"ok":true}`；假账号 + `{"name":"probe-renamed"}` → **200** `{"ok":true}`；`__no_such_id__` → **404** `{"detail":"账号不存在"}` |

> ✅ **安全实测**：对真实账号 `oauth-login-ea92aa60` 发 `PUT {}` 后，重新 `GET /accounts` 核验：
> `name`/`provider`/`mode`/`status`/`token_masked`/`fingerprint.device_mid` **全部未变**（仅 `use_count` 因期间请求 +1）。
> 即**空 body 不产生破坏性覆盖**。

### B-07 `POST /admin/api/accounts/{account_id}/enabled`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `{"enabled":bool}` |
| 成功响应 | `{"ok":true}` |
| 错误码 | 404 `账号不存在`；401 形态 A |
| **实测** | 假账号 `{"enabled":false}` → **200** `{"ok":true}`；`{"enabled":true}` → **200** `{"ok":true}`；`__no_such_id__` → **404** `{"detail":"账号不存在"}` |

**🔴 双写实测（2026-10-15，captain 指定问题）**：

用**自建探针账号** `probe-toggle-live-06cabd6e`（假凭证，绝不可能是真凭证）做端到端实测，
每步**只读**回读 DB 与 API，事后**完整复原并删除**：

| 阶段 | 表列 `accounts.enabled` | `data` JSON `enabled` | API 输出 `enabled` |
|------|:---:|:---:|:---:|
| 初始（新建后） | `1` (int) | `true` (bool) | `true` (bool) |
| `POST {"enabled":false}` → 200 `{"ok":true}` | **`0` (int) 【变】** | **`false` (bool) 【变】** | **`false` 【变】** |
| `POST {"enabled":true}` → 200 `{"ok":true}`（复原） | `1` (int) | `true` (bool) | `true` (bool) |
| `DELETE ["probe-toggle-live-06cabd6e"]` → 200 `{"deleted":1}` | — | — | — |

**结论（硬契约）**：

1. ✅ **`SetEnabled` 双写** —— 表列与 `data` JSON 的 `enabled` **同时变更**，无遗漏。
2. ✅ **`data` JSON 键集合不变**（23 → 23），即写回是**全量序列化**，不丢字段。
3. ✅ **API 输出跟随 data JSON**（`false` ↔ `true` 与 JSON 值一致），再次佐证 §2.1 的「输出来自 data JSON」。
4. ✅ **`{"ok":true}` 与实际落库一致**（不是只回 200 而不写）。

> **对 Go 版（ADR-002）的含义**：`SetEnabled` **必须双写**（列写 INTEGER 0/1 + `data.enabled` 写 bool）。
> 只写列会让老服务 API 输出停留在旧值 → 对拍 L3 值级必挂。见 `account-model.md` §4 与 CR-24。
>
> **复原与清理证据**：探针账号删除后账号总数回到 **6**，`probe-toggle-live%` 残留 **0**，
> 全部 id 与探测前逐字一致（`laohao-bigmodel-2420b3c2`、`oauth-apikey-8808fbac`、`oauth-apikey-c5c641a1`、
> `oauth-login-8523aba4`、`oauth-login-ea92aa60`、`xinhao-2000w-02f71f86`）。**生产数据零残留**。

### B-08 `POST /admin/api/accounts/{account_id}/fingerprint/rotate`

| 项 | 内容 |
|----|------|
| 请求结构 | **无 body** |
| 成功响应 | `{"ok":true,"fingerprint":<Fingerprint\>}` |
| 错误码 | 404 `账号不存在`；401 形态 A |
| **实测** | 假账号 → **200** `{"ok":true,"fingerprint":{"platform":"linux","arch":"x64","os_version":"6.8.0-45-generic","language":"ja-JP","timezone":"Asia/Tokyo","screen":"2560x1600","device_mid":"<uuid-v4>"}}`；`__no_such_id__` → **404** |

> 黑盒测试 `test_rotate_fingerprint_endpoint` 佐证：新 `device_mid` ≠ 旧值，且**落库**（重新 GET 仍是新值）。
> 注意响应**同时含 `ok` 与 `fingerprint`**（实测；而 B-06/B-07 只有 `ok`）。

### B-09 `POST /admin/api/accounts/refresh`

| 项 | 内容 |
|----|------|
| 请求体 | `additionalProperties:true`；实测空 `{}` 可用 |
| 成功响应 | `{"summary":{"ok":int,"fail":int},"count":integer,"skipped_cooling":integer}` |
| **实测** | `{}` → **200** `{"summary":{"ok":0,"fail":0},"count":0,"skipped_cooling":0}` |

`count` = 参与刷新的账号数；`skipped_cooling` = 因冷却被跳过的账号数。

### B-10 `POST /admin/api/accounts/{account_id}/refresh`

| 项 | 内容 |
|----|------|
| 请求结构 | **无 body** |
| 成功响应 | `{"ok":bool,"result":object,"account":<AccountView\>}` |
| 错误码 | 404 `账号不存在`；401 形态 A |
| **实测** | 假账号 → **200** `{"ok":false,"result":{"error":"无法获取额度数据"},"account":{...}}`（该 account 的 `status` 被置为 `invalid`）；`__no_such_id__` → **404** |

> ⚠️ 注意：**失败也返回 200**，失败信息在 body 的 `ok:false` 与 `result.error` 里。
> `result.error` 实测原文 `无法获取额度数据`。`ok:true` 时的 `result` 结构**未实测**。

### B-11 `POST /admin/api/login/start`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `{"label"?:string}`（`label` 作账号名入池）；空 body 亦可 |
| 成功响应 | `{"flow_id":string,"authorize_url":string,"expires_in":integer}` |
| 错误码 | 401 形态 A |
| **实测** | `{"label":"contract-probe-2"}` → **200** `{"flow_id":"6004548dfccfcf563b90ed703790a32f","authorize_url":"https://chat.z.ai/api/oauth/authorize?client_id=client_P8X5CMWmlaRO9gyO-KSqtg&redirect_uri=https://zcode.z.ai/api/v1/oauth/cli/callback/zai&state=<state>&response_type=code","expires_in":300}`；空 `{}` → **200** 同结构 |

`expires_in` 实测恒为 **300**（与文档 §2「expires_in:300」一致）。
`flow_id` 为 32 位十六进制小写（实测两次样例：`6178c3ed65bad8ca5dbefbdda5701201`、`0827b8de58befbb5aacb1f0eaba534c0`）。
`authorize_url` 是**完整绝对 URL**，含 `client_id`/`redirect_uri`/`state`/`response_type=code` 查询参数。

### B-12 `GET /admin/api/login/poll/{flow_id}`

| 项 | 内容 |
|----|------|
| 请求结构 | 路径参数 `flow_id`:string |
| 成功响应 | `{"status":string}`，`status` ∈ `pending`/`ready`/`failed`/`expired`；`failed` 附 `message`；`ready` 附 `account` |
| 错误码 | 401 形态 A |
| **实测** | 未知 flow `nope-xyz` → **200** `{"status":"expired"}`；未知 flow `unknown-flow-xyz` → **200** `{"status":"expired"}` |

> 🔴 **关键语义**：**未知/超时/已消费的 flow_id 一律返回 `expired`，且 HTTP 状态码是 200（不是 404）**。
> 文档 §2 明确此设计意图：「会话一次性，防重入兑换链」。Go 版若对未知 flow 返回 404，对拍即差异。
> `ready` 与 `failed` 的完整形态**未实测**（需真实 OAuth 完成）。

### B-13 `GET /admin/api/claim/preview`

| 项 | 内容 |
|----|------|
| 参数 | `account_id`(query, string, 可选) —— 限定单账号 |
| 成功响应 | `{"preview":array\<PreviewItem\>}` |
| 错误码 | 401 形态 A |
| **实测** | 无参数 → **200** `{"preview":[{"account_id":"oauth-login-ea92aa60","account_name":"老号·抢包(JWT)","plans":[],"error":null,"activated":true,"activation_error":null},{"account_id":"oauth-login-8523aba4","account_name":"xin号·抢包(JWT)","plans":[],"error":null,"activated":true,"activation_error":null}]}`（293 字节，仅 2 个 JWT 账号）；`?account_id=oauth-login-ea92aa60` → **200** 单元素 `preview` |

**`PreviewItem` 字段（逐字）**：`account_id`:string、`account_name`:string、`plans`:array（可 `[]`）、
`error`:string\|null、`activated`:bool、`activation_error`:string\|null。

> ✅ **重要**：`preview` **只包含 `mode=jwt` 账号**（实测 6 账号 → 2 条）。apiKey 账号被过滤。
> 与 `test_claim_skips_api_key_accounts` 断言一致。

### B-14 `POST /admin/api/claim`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `{"account_ids"?:array\<string\>,"plan_id"?:string}` —— 省略即全部 JWT 账号 |
| 成功响应 | `{"outcomes":array\<ClaimOutcome\>,"summary":{"ok":int,"fail":int}}` |
| 错误码 | 401 形态 A |
| **实测** | `{"account_ids":["__no_such_id__"]}` → **200** `{"outcomes":[],"summary":{"ok":0,"fail":0}}`；`{"account_ids":[<4 个 apiKey id>]}` → **200** `{"outcomes":[],"summary":{"ok":0,"fail":0}}` |

**`ClaimOutcome` 字段（来自黑盒测试断言，逐字）**：`ok`:bool、`account_name`:string、`message`:string。
`test_claim` 断言 `outcome["ok"] is False` 且 `"上游网络错误" in outcome["message"]`。

> ✅ 实测佐证：`claim` 对 apiKey 账号**静默过滤**（返回空 outcomes 而非报错）。
> 真实 JWT 领取**未实测**（会消耗真实活动名额）。

### B-15 `GET /admin/api/claim/captcha-config`

| 项 | 内容 |
|----|------|
| 成功响应 | `{"enabled":bool,"scene_id":string,"region":string,"prefix":string}` |
| **实测** | **200** `{"enabled":true,"scene_id":"11xygtvd","region":"cn","prefix":"no8xfe"}`（70 字节） |

黑盒测试 `test_captcha_config_endpoint` 断言同 4 字段（mock 环境值不同）。
`region` 实测 `cn`；`02-porting-map.md` 写默认 `sgp` —— 生产实测为 `cn`（动态拉取上游 configs 的结果）。

### B-16 `POST /admin/api/claim/manual`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `{"account_id":string,"captcha_verify_param":string,"captcha_region"?:string,"plan_id"?:string}` |
| 成功响应 | `{"outcomes":array\<ClaimOutcome\>,"summary":{"ok":int,"fail":int}}`（与 B-14 同形） |
| 错误码 | 400 `缺少 account_id`；404 `JWT 账号不存在`；401 形态 A |
| **实测** | `{}` → **400** `{"detail":"缺少 account_id"}`；`{"account_id":"<apiKey id>","captcha_verify_param":"probe"}` → **404** `{"detail":"JWT 账号不存在"}` |

> ✅ 实测佐证「只接受 JWT 账号」：传 apiKey 账号 → **404**（而非静默过滤）。
> 黑盒测试 `test_manual_claim_success_forwards_browser_param` 断言：
> `captcha_verify_param` 被**原样转发**到上游头 `x-aliyun-captcha-verify-param`，`captcha_region` → `x-aliyun-captcha-verify-region`；
> 且**服务端求解器零调用**（浏览器滑块参数由用户提供）。

### B-17 `GET /admin/api/settings`

| 项 | 内容 |
|----|------|
| 成功响应 | `{"admin_key":string,"gateway_key":string,"quota_refresh_interval":integer}` |
| **实测** | **200** `{"admin_key":"<redacted>","gateway_key":"","quota_refresh_interval":600}`（94 字节） |

> 🔴🔴 **安全契约事实**：`GET /admin/api/settings` **返回 `admin_key` 的明文原值**（实测字段值长度 32，
> 与 `meta` 表 `admin_key` 一致）。这是老服务的真实行为。`gateway_key` 当前为空串 `""`。
> Go 版必须**照此返回**才能对拍通过，但这意味着后台密钥会经网络明文回传 —— 见 `compat-risks.md` CR-05。

### B-18 `PUT /admin/api/settings`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `additionalProperties:true`；可含 `admin_key`/`gateway_key`/`quota_refresh_interval` |
| 成功响应 | `{"ok":true}` |
| 错误码 | 401 形态 A |
| **实测** | `{}` → **200** `{"ok":true}`；`{}` 后重新 `GET /settings` 三字段**未变**（`admin_key` 长度仍 32、`gateway_key` 仍 `""`、`quota_refresh_interval` 仍 `600`） |

> ✅ **安全实测**：空 body 不产生破坏性覆盖。

### B-19 `GET /admin/api/export`

| 项 | 内容 |
|----|------|
| 成功响应 | `{"version":integer,"exported_at":number,"providers":object\<string,array\<ExportItem\>\>}` |
| 错误码 | 401 形态 A |
| **实测** | **200**，1131 字节，`{"version":1,"exported_at":1792063653.4000368,"providers":{"zai":[{"name":"老号·抢包(JWT)","mode":"jwt","secret":"<redacted>"},{"name":"老号·智谱Key","mode":"apiKey","secret":"<redacted>"},{"name":"xin号·抢包(JWT)","mode":"jwt","secret":"<redacted>"},{"name":"xin号·智谱Key","mode":"apiKey","secret":"<redacted>"}],"bigmodel":[{"name":"老号·BigModelKey","mode":"apiKey","secret":"<redacted>"},{"name":"xin号·BigModelKey","mode":"apiKey","secret":"<redacted>"}]}}` |

**`ExportItem` 字段（逐字）**：`name`:string、`mode`:string、`secret`:string（**明文凭证**）。
`version` 实测恒为 `1`；`exported_at` 为 unix 秒 float。
`providers` 键与 B-02 的 `providers` 一致（`zai`、`bigmodel`），值为数组。

> 🔴🔴 **最危险端点**：`secret` 是**未脱敏的完整凭证**（实测输出 6 个账号的全部凭证）。
> 契约必须保留此行为（对拍要求），但**任何日志/落盘/文档都不得记录其真实值**。见 `compat-risks.md` CR-06。

### B-20 `POST /admin/api/import`

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json` |
| 请求体 | `{"providers":object\<string,array\<ExportItem\>\>}`（与 B-19 输出**同构**，可直接回灌） |
| 成功响应 | `{"count":integer}` |
| 错误码 | 401 形态 A |
| **实测** | `{}` → **200** `{"count":0}`；`{"providers":{"zai":[{"name":"import-probe","secret":"<fake>"}]}}` → **200** `{"count":1}` |

> 实测证实 **import 是 export 的逆**（`providers` → `{name,mode,secret}` 同构）。
> 黑盒测试 `test_import_schedules_auto_claim` 佐证：导入的 JWT 账号会调度自动领取。

### B-21 `GET /admin/api/monitoring`

| 项 | 内容 |
|----|------|
| 成功响应 | `{"entries":array\<MonitorEntry\>,"keep":integer}` |
| 错误码 | 401 形态 A |
| **实测** | 空态 → **200** `{"entries":[],"keep":500}`（25 字节）；有数据 → **200** 4 条 entries，`keep=500` |

**`MonitorEntry` 字段（逐字，15 字段，实测键序）**：

| 序 | 字段 | 类型 | 可空 | 实测样例 |
|:--:|------|------|:---:|------|
| 1 | `req_id` | string | 否 | `3542a2f262cf1cb9`（16 位十六进制） |
| 2 | `ts` | number | 否 | `1792063783.004661` |
| 3 | `endpoint` | string | 否 | `messages` / `chat`（**注意：`/v1/chat/completions` 记为 `chat`**） |
| 4 | `model` | string | 否 | `GLM-5.3`；未识别时 `"-"` |
| 5 | `stream` | bool | 否 | `false` |
| 6 | `preview` | string | 否（可 `""`） | 请求正文预览 |
| 7 | `account` | string | 否 | 账号 `name`（如 `xin号·抢包(JWT)`） |
| 8 | `mode` | string | 否 | `jwt` |
| 9 | `ok` | bool\|null | **是** | 完成前为 `null` |
| 10 | `status` | integer\|null | **是** | 上游 HTTP 码 |
| 11 | `error` | string | 否（可 `""`） | 错误文本 |
| 12 | `t_first` | number\|null | **是** | 首字节耗时 |
| 13 | `t_total` | number\|null | **是** | 总耗时 |
| 14 | `input_tokens` | integer\|null | **是** | 未知记 `null`（**不是 0**） |
| 15 | `output_tokens` | integer\|null | **是** | 同上 |

`keep` 实测恒为 `500`（环形缓冲容量）。

> 🔴 黑盒测试锁定的语义：`ok:null`+`status:null`+空 `error` 是**在途请求**的形态（实测抓到一条）；
> 流式成功路径 `ok:true`、`stream:true`，`input_tokens:null`（透传不解析 SSE，**未知 ≠ 0**）；
> `/v1/chat/completions` 流式能拿到 `output_tokens`（转换器带 usage）。

### B-22 `POST /admin/api/monitoring/clear`

| 项 | 内容 |
|----|------|
| 请求结构 | **无 body** |
| 成功响应 | `{"ok":true}` |
| 错误码 | 401 形态 A；405 形态 A |
| **实测** | 带凭证 → **200** `{"ok":true}`（11 字节）；无凭证 → **401** `{"detail":"缺少鉴权凭证"}`；用 `GET` → **405** `{"detail":"Method Not Allowed"}` |

实测 `clear` 后 `GET /monitoring` 的 `entries` 变 `[]`（黑盒测试 `test_clear_endpoint` 佐证）。

---

## 3. A 组 · 网关端点（3 条）

**鉴权**：`Authorization: Bearer <gateway_key>` 或 `x-api-key: <gateway_key>`；
**当前 meta `gateway_key` 为空 → 完全放行**（实测：无凭证 200、错误 key 亦 200）。

### A-01 `GET /v1/models`

| 项 | 内容 |
|----|------|
| 请求头 | `authorization`(可选)、`x-api-key`(可选) |
| 成功响应 | `{"object":"list","data":array\<ModelItem\>}` |
| **实测** | 无凭证 → **200**，224 字节；带错误 key → **200**（同样内容） |

**`ModelItem` 字段（逐字，4 字段）**：`id`:string、`type`:string、`display_name`:string、`created_at`:string(ISO8601)。

**实测完整响应**（当前生产，仅 2 个模型）：
```json
{"object":"list","data":[
 {"id":"GLM-5.3-Flash","type":"model","display_name":"GLM-5.3-Flash","created_at":"2025-01-01T00:00:00Z"},
 {"id":"GLM-5.3","type":"model","display_name":"GLM-5.3","created_at":"2025-01-01T00:00:00Z"}]}
```

> 🔴 **文档与实测冲突**：`04-api-spec.md` §1.2 声明清单有 11 个模型
> （`glm-4.5-air, glm-4.6, glm-4.6v, glm-4.7, glm-5, glm-5-turbo, glm-5v-turbo, glm-5.1, glm-5.2, glm-5.3, glm-5.3-flash`），
> 且示例里 `type:"model"` 无 `created_at`。**实测只有 2 个模型且多了 `created_at`**。
> `created_at` 恒为 `"2025-01-01T00:00:00Z"`（占位常量，非真实创建时间）。见 `compat-risks.md` CR-07。

**鉴权错误形态（黑盒测试锁定，本次未实测）**：
`test_gateway_auth` 断言：key 已配置且无凭证 → **401**；错凭证 → **403**；
`Authorization: Bearer` 与 `x-api-key` 双通道均放行。**形态 B 的错误体内容未实测**。

### A-02 `POST /v1/messages`（Anthropic Messages）

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json`；`authorization`/`x-api-key`(可选)；客户端透传头另计 |
| 请求体 | 标准 Anthropic Messages（`model`/`messages`/`max_tokens`/`stream`/`system`/`tools` …） |
| 成功响应 | 标准 Anthropic Messages 响应；`stream:true` 时 SSE 透传 |
| **实测（错误路径）** | 分四档，**两条互不相同的代码路径**（400 本地 vs 500 崩溃），见下方「A-02 错误路径分档表」 |
| **实测（上游业务错误）** | **HTTP 200 原样透传**上游业务错误体（**不翻译**） |
| **未实测** | **成功路径（真实业务 200 / SSE 内容）本次未实测** —— 理由：会消耗真实账号额度，且任务书要求避免破坏生产状态 |

#### A-02 错误路径分档表（`[实测]` t5 审查期 Hilkiah 逐档复现，2026-10-15）

> 🔴 **本表的核心结论：`/v1/messages` 的「非对象」不是被校验拒绝，而是把网关打崩成 500。**
> 这与 A-03 的同名场景（返回优雅的本地 400）**行为完全相反**，是极易实现错的一档。

| 请求体 | HTTP | `content-type` | body | 说明 |
|------|:---:|------|------|------|
| 畸形 JSON（`{oops`）/ **空 body** | **400** | `application/json` | `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request"}}`（75 B） | 网关**本地** JSON 合法性校验，形态 B |
| **合法但非对象**：`[]`、`"str"`、`123`、`null` | **500** | `text/plain; charset=utf-8` | `Internal Server Error`（**21 B**） | 🔴 **未捕获异常**（非校验）——网关只在"JSON 合法"后才尝试取对象成员，非对象直接崩 |
| 合法空对象 `{}` | **400** | `application/json` | `{"code":3001,"msg":"parameter error","logid":"<redacted>"}`（82 B） | **转发上游**，上游判定参数错误（形态 C，上游原文） |
| `{"model":"GLM-5.3-Flash"}`（缺 `messages`） | **200** | `application/json; charset=utf-8` | `{"code":1005,"msg":"exceed quota limit","logid":"<redacted>"}` | **转发上游**并回传上游业务错误（形态 C） |
| `{"model":"no-such-model-xyz",...}` | **400** | `application/json` | `{"code":3006,"msg":"model not allowed","logid":"<redacted>"}` | 上游拒绝（形态 C），**非**本地白名单 |

**上游业务错误路径的完整响应头**（`[实测]`）：`HTTP 200` + `cache-control: no-cache` +
`content-type: application/json; charset=utf-8` + `transfer-encoding: chunked` + `server: uvicorn`。

> 🔴 **三条实现禁令**：
> 1. **禁止把「合法非对象」实现成 400。** 老服务在这一档是 **500 + `text/plain` + 21 字节**。
>    Go 版若"好心"返回 `{"detail":...}` 或 `{"error":...}`，对拍在这一档**必挂**（状态码、content-type、body 三者全不同）。
> 2. **禁止把「畸形 JSON」与「合法非对象」合成一条路径。** 前者是 400/JSON/形态 B，后者是 500/纯文本/21 字节。
> 3. **禁止对 Anthropic 请求体做结构校验**（`model`/`messages` 必填等）——缺 `messages` 走的是上游 200，
>    与 A-03 的本地必填校验**相反**。网关只校验「JSON 合法性」与「能否取到对象成员」这两件事。

**实测到的上游透传响应（重要）**：以下两条是**真实上游响应原样透传**，状态码 200 但 body 是上游业务错误体：

| 请求 | HTTP | body（脱敏后原文） |
|------|:---:|------|
| `{"model":"GLM-5.3-Flash"}`（**缺 `messages`**） | **200** | `{"code":1005,"msg":"exceed quota limit","logid":"<redacted>"}` |
| `{"model":"no-such-model-xyz",...}`（**未知模型**） | **400** | `{"code":3006,"msg":"model not allowed","logid":"<redacted>"}` |

> 🔴🔴 **两条极重要的契约事实**：
> 1. **缺 `messages` 不返回 400，而是转发到上游并回传上游业务错误（HTTP 200 + `code:1005`）**。
>    Go 版若在网关层做 `messages` 必填校验并返回 400，对拍必挂。**网关不做 Anthropic 请求体结构校验**（除 JSON 合法性）。
> 2. **未知模型由上游拒绝**（HTTP 400 + `code:3006 "model not allowed"`），**不是网关本地白名单拒绝**。
>    错误体是上游原文（`code`/`msg`/`logid` 三字段），**不是**形态 B 的 `{"error":{...}}`。
> 3. `logid` 字段值每次都不同（上游签发），**不可用于对拍断言**。

`04-api-spec.md` §1.1 声明的模型名规范化映射（小写化后 `glm-5.2→GLM-5.2` 等）与上游转发目标（JWT→
`zcode.z.ai/...`、apiKey→`api.z.ai/...`）属**上游侧契约**，见 `zcode2api/docs/development/05-upstream-protocols.md`。

**故障转移/错误分类语义**（文档 + 黑盒测试，非本次实测）：
`MAX_CAPTCHA_RETRIES=3`、`MAX_ACCOUNT_ATTEMPTS=5`（`zcode2api-architecture.md` §4）；
402/quota 关键字 → `exhausted` 换号；429 → `cooling` 换号；401/403（非验证码）→ `invalid` 换号；
403 验证码挑战 → 原账号重试；耗尽后回传上游 status + content-type + 原文。

#### 🔴 captcha 调用时机契约（2026-10-16 冻结 · t23/t22，见 `review-evidence/i3-captcha-lazy-contract.md`）

> 上面那半句「403 验证码挑战 → 原账号重试」**只说了一半** —— 它覆盖的是**重试**，
> **没有说明首轮请求该不该带 token**。后者是 I-3 惰性化的本体，此前契约零描述。

**对话网关链路（`/v1/messages`、`/v1/chat/completions`）—— 条件式调用（惰性化）**：

| 场景 | 正确行为 |
|---|---|
| 首轮请求 | **不带** `x-aliyun-captcha-verify-param`（`captcha_retries = 0` 在循环前初始化 ⇒ 首轮门恒假） |
| 上游返回成功 | 直接透传，**求解次数 = 0** |
| 收到 403 验证码挑战 | **才取** token → `invalidate()` 旧池 → **原账号**重试（`MAX_CAPTCHA_RETRIES = 3` 次上限） |
| 挑战自愈路径 | **完整保留**，不被惰性化削掉 |
| `unusual_activity` / 3012 | 不受影响（→ `disabled`，`risk_strikes+1`） |

**领取链路（`POST /admin/api/claim`）—— 反向：每次必取**（黑盒锚 `solve_count == 1`；
3007 被拒 → 换码重试一次，总计 2）。**惰性化不适用于 claim** —— 若把条件式调用搬过来，领取会直接失败。
`/admin/api/claim/manual` 转发调用方 token，solver **恒 0 次**。

**重试常量分属两层，不是同一参数**（CR-21 已由此判定关闭）：
`MAX_CAPTCHA_RETRIES = 3` 是 **gateway 挑战重试上限**；
`ZCODE_CAPTCHA_RETRIES = 4` 是 **solver 单次求解的尝试次数**（Go：`internal/captcha.DefaultAttempts`）。

> ⚠️ **时效性**：「不带 token 也放行」是 2026-09-30 的实测结论（软校验），
> **不是可依赖的设计前提** —— 上游可能收紧。契约要求**挑战分支自动兜住**，
> 故即使变成硬校验，行为仍正确（仅延迟回退）。

### A-03 `POST /v1/chat/completions`（OpenAI Chat）

| 项 | 内容 |
|----|------|
| 请求头 | `Content-Type: application/json`；`authorization`/`x-api-key`(可选) |
| 请求体 | 标准 OpenAI Chat（`model`/`messages`/`stream`/`max_tokens`…） |
| 成功响应 | 标准 OpenAI Chat 响应；`stream:true` 时逐块翻译，**末尾必须是 `data: [DONE]`** |
| **实测（错误路径）** | 非法 JSON → **400** `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request_error"}}` |
| **实测（本地校验链）** | 有**三档**本地校验，见下方「A-03 本地校验链」 |
| **实测（上游业务错误）** | 🔴 **翻译成 502**（**不是** 200 透传，与 A-02 相反），见「A-03 上游错误翻译」 |
| **未实测** | 真实业务成功路径（200 / SSE 内容）本次未实测，理由同 A-02 |

#### A-03 本地校验链（`[实测]` t5 审查期 Hilkiah 复现，2026-10-15）

**与 A-02 截然不同**：chat 在网关层**做完整的请求体结构校验**（A-02 完全不做）。

| 请求体 | HTTP | body |
|------|:---:|------|
| 畸形 JSON / 空 body | **400** | `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request_error"}}` |
| **合法但非对象**（`[]`/`"str"`/`123`/`null`） | **400** | `{"error":{"message":"请求体必须是 JSON 对象","type":"invalid_request_error"}}` |
| 合法对象、缺 `model` | **400** | `{"error":{"message":"必须提供 model 参数","type":"invalid_request_error"}}` |
| 合法对象、缺 `messages` 或 `messages` 非数组 | **400** | `{"error":{"message":"必须提供 messages 数组","type":"invalid_request_error"}}` |
| 合法对象、`model`+`messages` 齐备 | — | **转发上游**（上游业务错误见下） |

🔴 **与 A-02 的同场景对照**（这是两端点最易实现错的地方）：
- 请求体 `[]`：A-02 → **500 `text/plain` 21 字节**；A-03 → **400 JSON `invalid_request_error`**。
- 缺 `messages`：A-02 → **200 上游透传**；A-03 → **400 本地拒绝**。

#### A-03 上游错误翻译（`[实测]`，🔴 与 A-02 语义相反）

上游业务错误（`code=1005 exceed quota limit`）在 chat 被**翻译成 502**，body 是网关构造的**形态 B**：

| 项 | 值 |
|----|----|
| HTTP | 🔴 **502 Bad Gateway**（**非** 200 / 400 / 500） |
| `content-type` | `application/json`（**无** charset） |
| `content-length` | 78 |
| `cache-control` | **无**（对比 A-02 的 `no-cache`） |
| body | `{"error":{"message":"exceed quota limit","type":"upstream_error","code":1005}}` |

> 🔴 **同一上游错误，两端点两种形态 + 两种状态码**：
> A-02 = HTTP **200** + 上游原文（形态 C `{"code":..,"msg":..,"logid":..}`）；
> A-03 = HTTP **502** + 网关翻译（形态 B `{"error":{"message":..,"type":"upstream_error","code":..}}`）。
> Go 版若共用一条「上游错误 → 响应」路径，**至少一端必挂**。

##### 🔴 范围闭合：**分叉只发生在「上游 HTTP 200 + 体内错误码」这一档**

**2026-10-15 由 t8 核验发现、captain 独立复现**——上文表述若理解为「chat 把所有上游错误都翻译成 502」**则过宽**：

| 上游响应形态 | A-03 `chat/completions` | A-02 `messages` | 是否分叉 |
|---|---|:---:|:---:|
| **真 HTTP 4xx**（如 `3006 model not allowed`） | **400** 透传形态 C：`{"code":3006,"msg":"model not allowed","logid":"…"}` | **400** 同左 | ❌ **不分叉，两端点一致** |
| **HTTP 200 + 体内错误码**（如 `1005 exceed quota limit`） | **502** 翻译形态 B | **200** 透传形态 C | ✅ **分叉** |

**实现警告**：给 chat 配「所有上游错误统一翻译」会导致 **3006/3001 等 HTTP-4xx 档对拍必挂**。
正确做法是**按上游 HTTP 状态分档**：4xx 透传、200-with-error-body 翻译。

**复现命令**（零额度消耗）：
```bash
curl -s -D - -X POST http://127.0.0.1:9120/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"no-such-model-xyz","messages":[{"role":"user","content":"hi"}]}' | head -c 200
# → HTTP 400 + {"code":3006,...}（与 messages 一致，非 502）
```

#### A-03 流式（SSE）流内错误（`[实测]`）

`stream:true` 时**首帧即真实上游 chunk 格式**（不是占位帧），且**流内错误也是 SSE 帧**而非 HTTP 状态码：

```
data: {"id": "chatcmpl-<redacted>", "object": "chat.completion.chunk", "created": <int>, "model": "GLM-5.3-Flash", "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": null}]}

data: {"error": {"message": "上游业务错误 code=1005: exceed quota limit", "type": "upstream_error", "code": 1005}}
```

响应头：`200` + `content-type: text/event-stream; charset=utf-8` + `cache-control: no-cache` + `transfer-encoding: chunked`。

**黑盒测试锁定的成功响应契约**（`test_gateway_openai` / `test_gateway_monitoring`，逐字）：
- `data["object"] == "chat.completion"`
- `data["model"] == "GLM-5.3-Flash"`（**模型名被规范化为大写官方名**，即使请求传 `glm-5.3-flash`）
- `data["choices"][0]["message"]["content"]`（上游文本原样）
- `data["choices"][0]["finish_reason"] == "stop"`
- `data["usage"]["prompt_tokens"] == 10`
- 流式：`res.text.rstrip().endswith("data: [DONE]")`
- `system` 消息映射为 Anthropic `system` 数组的**末位**元素（官方身份块之后）：`payload["system"][-1]["text"]`
- `max_tokens` **钳制到 131072**（含字符串形态 `"999999"`），防上游 400 code 1210

---

## 4. C 组 · 页面（6 条）

**全部页面无服务端鉴权**（实测匿名 200）。鉴权由客户端 JS（`/static/js/auth.js`，读 `localStorage`）完成。

| # | 方法 | 路径 | 状态码 | 响应 | 实测证据 |
|:--:|------|------|:---:|------|------|
| C-01 | GET | `/` | **307** | 空 body，`Location: /admin` | ✅ 实测 `307`，`redirect_url=http://127.0.0.1:9120/admin` |
| C-02 | GET | `/admin` | **307** | 空 body，`Location: /admin/login` | ✅ 实测 `307`，`redirect_url=.../admin/login` |
| C-03 | GET | `/admin/login` | **200** | `text/html; charset=utf-8`，1897 字节，**带 `cache-control: no-store`** | ✅ 实测 |
| C-04 | GET | `/admin/accounts` | **200** | `text/html; charset=utf-8` | ✅ 实测 |
| C-05 | GET | `/admin/monitoring` | **200** | `text/html; charset=utf-8` | ✅ 实测 |
| C-06 | GET | `/admin/settings` | **200** | `text/html; charset=utf-8` | ✅ 实测 |

**页面共同特征**（实测）：
- 每页均引用 `/static/css/app.css?v=2.5.1`、`/static/js/auth.js?v=2.5.1`、`/static/js/toast.js?v=2.5.1`
- `/admin/accounts`、`/admin/monitoring`、`/admin/settings` 额外引用 `/static/js/header.js?v=2.5.1`
- 所有页面均引用外部 CDN `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- 版本查询串 `?v=2.5.1` 与 `/meta` 的 `2.5.3` **不一致**（缓存键版本滞后，属真实不一致）

**错误码**：未知页面路径 → **404** 形态 A（`{"detail":"Not Found"}`），实测 `/nope`、`/admin/claim`、`/admin/export`。

> 🔴 **重定向语义**：`/` 无条件 307 到 `/admin`；`/admin` **无条件** 307 到 `/admin/login`
> （实测匿名请求即重定向，且因鉴权在客户端，服务端无法区分登录态）。
> Go 版需用 307（**临时重定向，非 302**）。

> ⚠️ **修正（2026-10-15，由 t9 实测发现、captain 复核）**：
> **Location 的形态是不对称的** —— 原表述「Location 为绝对 URL」**是错的**（系把 httpx 跟随链终点误记为 header 值）：
>
> | 请求 | `location` 实测值 | 形态 |
> |---|---|---|
> | `GET /` | `/admin` | **相对** |
> | `GET /admin` | `/admin/login` | **相对** |
> | `GET /static` | `http://127.0.0.1:9120/static/` | **绝对** |
>
> **captain 独立复核**：
> ```bash
> curl -s -D - -o /dev/null http://127.0.0.1:9120/        | grep -i ^location   # → /admin
> curl -s -D - -o /dev/null http://127.0.0.1:9120/admin    | grep -i ^location   # → /admin/login
> curl -s -D - -o /dev/null http://127.0.0.1:9120/static   | grep -i ^location   # → http://…/static/
> ```
> **实现要求**：复刻这个**不对称**（页面重定向用相对、`/static` 用绝对）。
> `/static` 的绝对形态源于 Starlette `Mount` 的行为差异。

---

## 5. D 组 · 元数据与工具（2 条）

### D-01 `GET /meta`

| 项 | 内容 |
|----|------|
| 鉴权 | **无** |
| 成功响应 | `{"version":string}` |
| **实测** | 无凭证 → **200** `{"version":"2.5.3"}`（19 字节） |

用途：`header.js` 用它渲染页头版本号 `v<version>`。**注意服务版本是 `2.5.3`，与静态资源 `?v=2.5.1` 不同。**

### D-02 `GET /openapi.json`

| 项 | 内容 |
|----|------|
| 鉴权 | **无** |
| 成功响应 | FastAPI 自动生成的 OpenAPI 3.1.0 文档 |
| **实测** | 无凭证 → **200**，19913 字节，`{"openapi":"3.1.0","info":{"title":"zcode-hub","version":"2.5.3"},"paths":{...}}` |

`info.title` = `zcode-hub`（**不是** `zcode2api`），`info.version` = `2.5.3`。
`paths` 实测 25 条（22 管理 + 3 网关），与 §0 计数吻合。

> 该端点对 Go 版有双重意义：①它是对拍端点之一；②它是**枚举老服务路由表的权威手段**（本次契约提取即依赖它）。
> Go 版若不提供 `/openapi.json`，对拍差异需用户裁决（见 CR-08）。

---

## 6. 附录 · 未计入 33 的内置路由（实测均存在）

| 方法 | 路径 | 实测状态码 | 说明 |
|------|------|:---:|------|
| GET | `/docs` | **200** | FastAPI Swagger UI |
| GET | `/redoc` | **200** | FastAPI ReDoc |
| GET | `/docs/oauth2-redirect` | **200** | FastAPI OAuth2 重定向页 |
| GET | `/static/css/app.css` | **200** | `text/css; charset=utf-8`，23788 字节 |
| GET | `/static/js/auth.js` | **200** | `text/javascript; charset=utf-8`，2701 字节 |
| GET | `/static/js/header.js` | **200** | `text/javascript; charset=utf-8`，1274 字节 |
| GET | `/static/js/toast.js` | **200** | `text/javascript; charset=utf-8`，1368 字节 |
| GET | `/static/nonexist.js` | **404** | 形态 A |
| GET | `/static` | **307** | → `/static/`（StaticFiles mount 规范化） |

**已实测确认不存在**（404/405）：`/health`、`/healthz`、`/api/health`、`/v1/health`、`/livez`、`/readyz`、
`/v1/responses`、`/v1/messages/count_tokens`、`/v1/embeddings`、`/v1/complete`、`/favicon.ico`、
`/admin/api/pool`、`/admin/api/quota`、`/admin/api/claim/history`、`/admin/api/bundle/export`、
`/admin/api/bundle/import`、`/admin/api/reqlog`、`/admin/api/logs`、`/admin/api/version`、
`/admin/api/monitoring/stream`、`/admin/api/events`、`/admin/logout`、`/login`、`/logout`。

> ⚠️ 其中 `/admin/api/pool`、`/admin/api/quota`、`/admin/api/claim/history`、`/admin/api/bundle/export`、
> `/admin/api/bundle/import` 出现在 `04-api-spec.md` §2 与 `02-porting-map.md` §7 中，
> 但**实测 404 —— 这些端点在本版本老服务中不存在**。见 `compat-risks.md` CR-12。
> `/health` 同属此列（`04-api-spec.md` §1.5 声明，实测 **404**）。

---

## 7. 实测记录索引

所有实测均为 2026-10-15 对 `http://127.0.0.1:9120` 的真实 HTTP 请求（`curl` / Python `urllib`）。
原始响应在落盘前**已在内存中脱敏**；脱敏后的样本存放于
`workspace/temp/t1-contract/raw2/`（每端点一份 JSON，含 `status`、`content_type`、`request_body`、`response`）。

| 端点 | 方法 | 实测状态码 | 计数 |
|------|------|:---:|:---:|
| `/v1/models` | GET | 200（无凭证）、200（错误 key）、**405（POST，`allow: GET`）**、**404（`/V1/models` 大小写敏感）** | 4 |
| `/v1/messages` | POST | 400（非法 JSON/空 body）、**500（合法非对象，`text/plain` 21 字节）**、**400（空对象 `{}` → 上游 `code:3001`）**、200（缺 messages 透传）、400（未知模型）、**307（尾斜杠）** | 6 |
| `/v1/chat/completions` | POST | 400（非法 JSON）、**400（合法非对象）**、**400（缺 model）**、**400（缺/非数组 messages）**、**502（上游业务错误翻译）**、**200（SSE 流，流内错误为 SSE 帧）**、**307（尾斜杠）** | 7 |
| `/admin/api/verify` | GET | 200、401×2 | 3 |
| `/admin/api/accounts` | GET | 200×8 | 8 |
| `/admin/api/accounts` | POST | 400×2、200×3 | 5 |
| `/admin/api/accounts` | DELETE | 200×4、401×1 | 5 |
| `/admin/api/status` | GET | 200 | 1 |
| `/admin/api/accounts/{id}` | PUT | 200×2、404×1、405×1 | 4 |
| `/admin/api/accounts/{id}/enabled` | POST | 200×2、404×1 | 3 |
| `/admin/api/accounts/{id}/fingerprint/rotate` | POST | 200×1、404×2、405×1 | 4 |
| `/admin/api/accounts/refresh` | POST | 200 | 1 |
| `/admin/api/accounts/{id}/refresh` | POST | 200、404 | 2 |
| `/admin/api/login/start` | POST | 200×2 | 2 |
| `/admin/api/login/poll/{flow_id}` | GET | 200×2 | 2 |
| `/admin/api/claim/preview` | GET | 200×3 | 3 |
| `/admin/api/claim` | POST | 200×3、401×1、405×1 | 5 |
| `/admin/api/claim/captcha-config` | GET | 200 | 1 |
| `/admin/api/claim/manual` | POST | 400、404 | 2 |
| `/admin/api/settings` | GET | 200×3 | 3 |
| `/admin/api/settings` | PUT | 200 | 1 |
| `/admin/api/export` | GET | 200 | 1 |
| `/admin/api/import` | POST | 200×2 | 2 |
| `/admin/api/monitoring` | GET | 200×4 | 4 |
| `/admin/api/monitoring/clear` | POST | 200、401、405 | 3 |
| `/` `/admin` | GET | 307×2 | 2 |
| `/admin/login` `/admin/accounts` `/admin/monitoring` `/admin/settings` | GET | 200×4 | 4 |
| `/meta` | GET | 200×2 | 2 |
| `/openapi.json` | GET | 200 | 1 |
| `/docs` `/redoc` `/docs/oauth2-redirect` | GET | 200×3 | 3 |
| `/static/*` | GET | 200×4、404×1、307×1 | 6 |
| 不存在路径（负向） | 多种 | 404×20+ | 20+ |

**未实测并已显式标注的项**（下游不得当作已确证事实）：
`/v1/messages` 与 `/v1/chat/completions` 的**真实业务 200 成功路径 / SSE 成功内容**
（避免消耗真实额度；**错误路径已全部实测**，见 A-02/A-03 各分档表）；
`/admin/api/login/poll` 的 `pending`/`ready`/`failed` 形态（需完成真实 OAuth）；
`/admin/api/claim` 的**真实** JWT 领取（避免消耗活动名额）；
`/admin/api/accounts/{id}/refresh` 的 `ok:true` 时的 `result` 结构；
网关 key 已配置时的 401/403 错误体内容。

> **t5 审查期补充实测（2026-10-15，Hilkiah）**：A-02/A-03 的**全部错误路径分档**、
> SSE 流内错误帧、`POST /v1/models` 405、`/V1/models` 404、三条尾斜杠 307
> 均已由本次实测确证，不再属于未实测项。原始样本见 `workspace/temp/t6-gateway/`
> （正常路径 3 份）与本文各分档表内的命令复现（错误路径，无需凭证）。
