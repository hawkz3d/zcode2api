# compat-risks.md — zhipucode2api 兼容风险清单（冻结版）

> 本文列出 Go 版 `zhipucode2api` 重写 `zcode2api` 时**可验证的兼容性风险**，每条给出
> 现象、证据、后果与**可执行的验证判据**。
>
> 🔴 **本文遵循 `adr-001-source-of-truth.md`**：事实来源优先级 = ①运行中服务真实 HTTP 响应
> ②生产库真实 schema/数据 ③黑盒测试 ④描述现状的文档 ⑤`docs/development/03-data-formats.md`
> **仅作未来设计参考** ⑥"设计要点/扩展"章节**默认未实现**。
> 并遵循 `adr-002-enabled-normalization.md`（`enabled` 规范化裁定）。
>
> **规格先于实现声明**：证据来自真实 HTTP 探测、DB 只读探测、`tests/integration/**`、
> `zcode2api/docs/**`、systemd unit 原文。**未纳入本轮观测** `zcode2api/app/**/*.py`、`cli.py`。
>
> **风险等级**：🔴 blocker（阻断对拍/切换）· 🟠 high（产生差异）· 🟡 medium（需注意）· 🟢 low（记录）

---

## 0. 风险总览

| ID | 风险 | 等级 | 类别 |
|:--:|------|:---:|------|
| CR-01 | 端点总数 33 的口径未闭合（`/v1/responses`、`/health` 实测 404） | 🔴 | 契约完整性 |
| CR-02 | 三种错误体形态混用，Go 单一 error handler 必错 | 🔴 | 错误语义 |
| CR-03 | `enabled` 脏值 `"True"` —— ADR-002 规范化产生**已知对拍差异** | 🟠 | 类型漂移 |
| CR-04 | 删除是**集合级** `DELETE` + body 数组，非 `DELETE /{id}` | 🟠 | 路由形态 |
| CR-05 | `GET /admin/api/settings` **回显 admin_key 明文** | 🔴 | 安全 |
| CR-06 | `/admin/api/export` 导出**明文凭证** | 🔴 | 安全 |
| CR-07 | 网关 key 为空 → **匿名可用**（生产实测完全放行） | 🔴 | 安全 |
| CR-08 | FastAPI 307 尾斜杠重定向 + 绝对 Location | 🟡 | 路由形态 |
| CR-09 | `405` 的 `{"detail":"Method Not Allowed"}` 与 `allow` 头 | 🟡 | 错误语义 |
| CR-10 | `server: uvicorn` 响应头 + `content-type` 无 charset | 🟡 | 头部差异 |
| CR-11 | `id` 派生规律与 `docs/03` 不符（`zai-<sha256[:12]>` 是错的） | 🟠 | 数据兼容 |
| CR-12 | `data` JSON 键序是**可观测契约**（DB 内容 diff） | 🟠 | 数据兼容 |
| CR-13 | WAL 模式必须一致，否则双进程互操作破裂 | 🔴 | 数据兼容 |
| CR-14 | `data` 键序 ≠ `AccountView` 键序 | 🟠 | 序列化 |
| CR-15 | 凭证明文 vs `docs/03` 声称加密 —— **不得引入加密** | 🔴 | 数据兼容 |
| CR-16 | `recent_results` 方向（🔴 **`[-1]` 最新，后插** —— t5 审查期更正） | 🟡 | 数据兼容 |
| CR-17 | `ZCODE_PORT` 默认 3000 但生产 9120；3000 本机被占用 | 🟠 | 配置 |
| CR-18 | `ZCODE_HOST` 默认 `0.0.0.0`，生产 `127.0.0.1` | 🟠 | 安全/配置 |
| CR-19 | 相对路径必须按 `WorkingDirectory` 解析 | 🟠 | 配置 |
| CR-20 | `ZCODE_NODE_PATH` 绝对路径 + `CAPTCHA_POOL_MIN=3`（Node 求解器依赖） | 🟠 | 能力缺口 |
| CR-21 | `ZCODE_CAPTCHA_RETRIES` 4 vs `MAX_CAPTCHA_RETRIES` 3 | 🟡 | 配置 |
| CR-22 | `NO_PROXY` 是上游直连的生命线（systemd 必须保留） | 🔴 | 部署 |
| CR-23 | `.env` 行尾 `\r` 未剥离 → 鉴权静默 401 | 🔴 | 配置解析 |
| CR-24 | `SetEnabled` 必须**双写**（列 + data JSON） | 🔴 | 写路径 |
| CR-25 | `is_cooling()` ≠ `status==cooling` | 🟠 | 状态语义 |
| CR-26 | 生产进程环境**无任何 `ZCODE_*`** → `.env` 由应用自读 | 🟠 | 配置 |
| CR-27 | `meta.value` 是 TEXT，数字存字符串 `"600"` | 🟡 | 数据兼容 |
| CR-28 | 页面静态资源 `?v=2.5.1` 与 `/meta` 的 `2.5.3` 不一致 | 🟢 | 版本 |
| CR-29 | `zcode2api/docs/development/03-data-formats.md` **整体不可信** | 🔴 | 文档 |
| CR-30 | `account-model` 中 429 语义：架构文档状态图**过时** | 🟠 | 状态语义 |
| CR-31 | 老服务**内存持有账号状态**，DB 外部改动不即时可见 | 🟠 | 数据兼容 |
| CR-32 | 🔴 **同场景两端点错误语义相反**：`/v1/messages` 非对象体 **500 崩溃** vs `/v1/chat/completions` 本地 **400 校验** | 🔴 | 错误语义 |
| CR-33 | 🔴 上游错误的处理**按「上游 HTTP 状态」分档**：HTTP 4xx 时两端点**一致透传**；HTTP 200 体内嵌错误码时**分叉**（chat→502 翻译 / messages→200 透传） | 🔴 | 错误语义 |
| CR-34 | 🟠 同类型参数互换是编译器抓不到的缺陷（`writeShapeB(w,401,typ,msg)` 实参对调） | 🟠 | 实现注意 |
| CR-35 | 🔴 **账号列表顺序是持久契约**（`provider 分组序 + 组内 created_at 升序`），Go 侧 `ORDER BY rowid` **错**；且**对拍器 `sort_by:id` 把它整体折叠 → 顺序差异对拍看不见** | 🔴 | 数据兼容/对拍盲区 |
| CR-36 | 🟠 **契约自身证据等级缺陷**：`G-14` 的「超出截头（丢最旧）」在 t5 期被标为结论但**当时无实测证据**（事后被 n=20 分布证实）；同类「推断冒充实测」须逐条体检 | 🟠 | 方法论 |
| CR-37 | 🟠 **`monitoring.entries` 方向契约被对拍折叠**（实测**严格降序，新在前**）——与 CR-16b/CR-35 同型：`replace_typed` 折叠后方向错误对拍看不见 | 🟠 | 对拍盲区 |
| CR-38 | 🟢 **归一化规则全量体检完备性结论**：22 条中容器级**恰好 3 条**，全部对应已定罪 CR-35/16b/37，**无第四处漏网**；并给出 `replace_typed` 维度账目（丢失=内容+长度+顺序，保留=类型+键存在性） | 🟢 | 方法论 |
| CR-39 | 🔴 **A2 空库守卫自败**：`if !freshFile { return nil }` 使守卫效力绑死在「本次是否创建文件」上，而 `store.Open` 创建文件但不建表 ⇒ **第 1 次拦截留下的 4096B 空库让第 2 次起永久放行**；配 `Restart=always` 必然触发，服务静默跑在空库上（实测 `/v1/models` 200、日志零告警） | 🔴 | 切换阻断 |

---

## 1. 契约完整性风险

### CR-01 🔴 端点总数 33 的口径未闭合

**现象**：任务书要求 33 端点契约等价，但**实测无法凑出 33 个「真实存在的 HTTP 端点」**。

**实测证据**：
- `GET /openapi.json` → 200，其中 `paths` 展开后恰为 **25 个 method+path 操作**（3 网关 + 22 管理）。
- 页面路由实测 **7 个**：`/`、`/admin`、`/admin/login`、`/admin/accounts`、`/admin/monitoring`、`/admin/settings`、`/meta`。
- 25 + 7 = **32**，距 33 差 1。
- 候选补位全部实测 **404**：`/v1/responses`、`/health`、`/healthz`、`/api/health`、`/v1/health`、`/livez`、`/readyz`、`/favicon.ico`。
- `/docs`、`/redoc`、`/static/**` 实测 200，但属 **FastAPI 内建**，不应计入业务契约。

**本文采用的口径**：33 = 3 网关 + 22 管理 + 6 页面 + 2（`/meta`、`/openapi.json`）。
（`/admin/login` 与 `/meta` 中取 6 页面时需注意：§`endpoints.md` §0 列出 C 类 6 个为
`/`、`/admin`、`/admin/login`、`/admin/accounts`、`/admin/monitoring`、`/admin/settings`，
`/meta` 归 D 类。）

**后果**：若 Go 版按 33 个「端点」实现，会**发明一个不存在的端点** → 对拍必然多出差异。

**验证判据**：
```bash
# 实测 openapi.json 操作数（应为 25）
curl -s http://127.0.0.1:9120/openapi.json | python3 -c "
import json,sys; d=json.load(sys.stdin)
ops=[(m,p) for p,v in d['paths'].items() for m in v if m in ('get','post','put','delete','patch','head','options')]
print('ops =',len(ops)); [print(' ',m.upper(),p) for m,p in sorted(ops)]"
```
**处置**：**不得发明端点**；33 的口径差异已在本清单显式记录，交由 captain 与用户确认。

### CR-29 🔴 `docs/development/03-data-formats.md` 整体不可信

**现象**：该文档与生产库**全面不符**（ADR-001 触发原因）。

| 项 | `docs/03` 描述 | 实测 |
|----|---------------|------|
| `accounts` 列 | 带 `label`、`cooling_until`，`mode`/`status` NOT NULL | **无** `label`/`cooling_until` 列；`mode`/`status` **notnull=0** |
| 额外表 | `claim_history` | **不存在**；只有 `accounts` + `meta` |
| `data` 存储 | AES-256-GCM 加密 | **明文 JSON** |
| 主密钥 | `ZCODE_MASTER_SECRET` | **该 env 不存在** |
| 索引 | 未提 | `idx_acc_provider`、`idx_acc_status` |
| `id` 派生 | `zai-<sha256[:12]>` | **不符**（见 CR-11） |
| `ZCODE_ADMIN_KEY` 默认 | `zcode` | 实现为 `os.getenv(..., "<弱默认>")`；README 写 `change-me` |
| `POOL_MAX_ATTEMPTS`/`POOL_COOLDOWN_SECONDS`/`POOL_EXHAUSTED_RETRY_SECONDS` | 列为运行时配置 | **env 实测均不存在** |

**该文档自述**："设计要点（**继承 z2a 并扩展**）"、"这是对 z2a 明文存储缺陷的**修复**"、
"`bundle.py`（替换明文导出）" 在 roadmap 中是**未勾选 `[ ]` 待办** → 证明它是**未来蓝图**。

**后果**：任何按 `docs/03` 实现 Go 版的行为都会**同时**破坏存储兼容与 API 兼容。

**验证判据**：
```bash
cd /opt/zcode2api
python3 -c "
import sqlite3; c=sqlite3.connect('file:data/accounts.db?mode=ro',uri=True).cursor()
print([r[0] for r in c.execute(\"select name from sqlite_master where type='table'\")])   # 期望 ['meta','accounts']
print([r[1] for r in c.execute('PRAGMA table_info(accounts)')])                          # 期望无 label/cooling_until
print(c.execute('select count(*) from accounts where data like \"%enc:v1:%\"').fetchone())  # 期望 0
"
```
**处置**：`docs/03` 降级为「未来设计参考」；契约以实测为准（ADR-001）。

---

## 2. 错误语义风险

### CR-02 🔴 三种错误体形态混用

**现象**：老服务的错误响应**不是单一 schema**，而是**三套并存**。

| 形态 | 结构 | 出现场景（实测） |
|------|------|----------------|
| **A** FastAPI | `{"detail":"<中文文本>"}` | 管理 API 鉴权/校验/404/405 |
| **B** 网关预检 | `{"error":{"message":...,"type":...}}` | 网关本地校验（如 `invalid_request_error`） |
| **C** 上游透传 | `{"code":N,"msg":...,"logid":...}` | 上游错误原样回传 |

**实测证据**：
- 形态 A：`GET /admin/api/accounts` 无凭证 → **401** `{"detail":"缺少鉴权凭证"}`；错 key → **401** `{"detail":"鉴权凭证无效"}`；未知路径 → **404** `{"detail":"Not Found"}`；`GET /admin/api/monitoring/clear` → **405** `{"detail":"Method Not Allowed"}`。
- 形态 B：`/v1/chat/completions` 非法 payload → `{"error":{"type":"invalid_request_error",...}}`（`[黑盒]` `test_gateway_openai.py` §127）。
- 形态 C：`/v1/messages` 缺 `messages` → **200**（HTTP 成功！）但 body 是上游 `{"code":1005,"msg":"exceed quota limit","logid":...}`；未知模型 → **400** `{"code":3006,"msg":"model not allowed","logid":...}`。

🔴 **两个 `type` 字符串不同**：`/v1/messages` 用 `invalid_request`，`/v1/chat/completions` 用 `invalid_request_error`。
**不得统一**（对拍会挂）。

**后果**：Go 版若用统一 `ErrorResponse` struct，三处全错。

**验证判据**：
```bash
K=$(grep -m1 '^ZCODE_ADMIN_KEY=' .env | cut -d= -f2-)
curl -s -o /dev/null -w '%{http_code} ' http://127.0.0.1:9120/admin/api/accounts; echo
curl -s -H "Authorization: Bearer $K" -X POST http://127.0.0.1:9120/admin/api/accounts \
  -H 'Content-Type: application/json' -d '{}'          # 期望 {"detail":"请输入至少一个 Token / API Key"}
```
**处置**：Go 侧必须实现**三套 error writer**，按路由分组选用。

> ⚠️ **形态 B 的 `type` 字符串还随端点不同**：`/v1/messages` 用 `invalid_request`，
> `/v1/chat/completions` 用 `invalid_request_error`（CR-02 正文已列）。
> 且**同一场景（合法但非对象的请求体）在两个端点走完全不同的代码路径** —— 见 CR-32。

### CR-32 🔴 同场景两端点错误语义相反：messages **500 崩溃** vs chat 本地 **400 校验**

**现象**（`[实测]` t5 审查期 Hilkiah 逐档复现，2026-10-15，命令见下）：请求体「**合法 JSON 但非对象**」
（`[]` / `"str"` / `123` / `null`）在两端点的处理**完全相反**：

| 端点 | 请求体 | HTTP | `content-type` | body |
|------|------|:---:|------|------|
| `/v1/messages` | `[]` 等合法非对象 | 🔴 **500** | `text/plain; charset=utf-8` | `Internal Server Error`（**21 字节**） |
| `/v1/chat/completions` | `[]` 等合法非对象 | **400** | `application/json` | `{"error":{"message":"请求体必须是 JSON 对象","type":"invalid_request_error"}}` |
| `/v1/messages` | **畸形 JSON** / 空 body | **400** | `application/json` | `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request"}}` |
| `/v1/chat/completions` | **畸形 JSON** / 空 body | **400** | `application/json` | `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request_error"}}` |

🔴 **关键**：`/v1/messages` 的「非对象」**不是被校验拒绝，而是把网关打崩成未捕获异常的 500**
（`text/plain` + 21 字节 = FastAPI 默认 `Internal Server Error`）。它只在"JSON 合法"之后才尝试取对象成员。

**后果**：
- Go 版若把两个端点合用一个请求体解码函数 → **`/v1/messages` 这一档必挂**
  （状态码 500↔400、`content-type` `text/plain`↔`application/json`、body 三处全不同）。
- 若"好心"把 messages 的 500 修成优雅 400 → 同样必挂。**这一档必须照抄"崩溃"行为**。

**验证判据**（无需凭证，零额度消耗）：
```bash
B=http://127.0.0.1:9120
curl -s -i -X POST $B/v1/messages          -H 'content-type: application/json' --data-binary '[]' | head -6
#   期望 HTTP/1.1 500 + content-type: text/plain; charset=utf-8 + body 恰 21 字节 "Internal Server Error"
curl -s -i -X POST $B/v1/chat/completions  -H 'content-type: application/json' --data-binary '[]' | head -6
#   期望 HTTP/1.1 400 + application/json + {"error":{"message":"请求体必须是 JSON 对象","type":"invalid_request_error"}}
```

### CR-33 🔴 chat 把上游业务错误**翻译成 502**，messages 则 **200 原样透传**

**现象**（`[实测]`，上游真实业务错误 `code=1005 exceed quota limit`，同一上游错误在两个端点被**反向处理**）：

| 端点 | HTTP | `content-type` | body | 语义 |
|------|:---:|------|------|------|
| `/v1/chat/completions` | 🔴 **502** | `application/json` | `{"error":{"message":"exceed quota limit","type":"upstream_error","code":1005}}`（78 B） | **翻译**成 OpenAI 错误形态 |
| `/v1/messages` | **200** | `application/json; charset=utf-8` | `{"code":1005,"msg":"exceed quota limit","logid":"<redacted>"}` | **原样透传**上游体 |

`chat` 的 502 响应头（`[实测]`）：`content-length: 78`、`content-type: application/json`、
**无** `cache-control`。**注意 502 而非 200/400/500** —— 这是最容易实现错的一档。

#### 🔴 范围闭合（2026-10-15 修正 · 由 t8 核验发现，captain 独立复现）

**上文「chat 翻译成 502」的范围过宽** —— 该翻译**只适用于「上游 HTTP 200 + 体内嵌错误码」这一档**。
上游返回**真 HTTP 4xx** 时，两端点行为**完全一致**（都透传）：

| 上游响应形态 | `/v1/chat/completions` | `/v1/messages` | 一致？ |
|---|---|:---:|:---:|
| **HTTP 4xx**（如 3006 `model not allowed`） | **400** 原样透传形态 C：`{"code":3006,"msg":"model not allowed","logid":"…"}` | **400** 同左 | ✅ **一致** |
| **HTTP 200 + 体内错误码**（如 1005 `exceed quota limit`） | 🔴 **502** 翻译：`{"error":{"message":"exceed quota limit","type":"upstream_error","code":1005}}` | **200** 原样透传 | ❌ **分叉** |

**captain 独立复现命令**（零额度消耗，参数层拒绝）：
```bash
# 上游 HTTP 4xx：两端点都是 400 透传
curl -s -D - -X POST http://127.0.0.1:9120/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"no-such-model-xyz","messages":[{"role":"user","content":"hi"}]}' | head -c 200
curl -s -D - -X POST http://127.0.0.1:9120/v1/messages -H 'Content-Type: application/json' \
  -d '{"model":"no-such-model-xyz","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}' | head -c 200
# → 均为 HTTP 400 + {"code":3006,...}
```

**⚠️ 实现警告**：若 t6 按原字面给 chat 配「所有上游错误都翻译成 502」，
则 **3006 / 3001 等 HTTP-4xx 档对拍必挂**。

**哨兵**：若未来实测发现 chat-3006 变成 502，说明上游行为迁移，需重新核验并更新本条。

> ✅ **双向证据闭环（t18 diff-only 取证 + 实弹，2026-10-16）**：
> - **代码侧**（`app/routes/gateway.py` 的 `.bak↔current` diff 实读）：上游 200 + 业务错误体 → **502**
>   `{"error":{"message":..,"type":"upstream_error","code":..}}` —— 与本节实测**完全一致**
> - **实弹侧**（captain 复核）：`chat + GLM-5.3-Flash`（配额尽）→ **HTTP 502** +
>   `{"error":{"message":"exceed quota limit","type":"upstream_error","code":1005}}`
> - **SSE 行内错误同形态**：流内错误行也是该结构（与 P-4 实弹撞到的 1005 流内错误**两侧证据闭环**）
>
> 结论：本条从「实测 + 悬置」升为「**代码与实测双向确证**」。

**流式（SSE）同语义**（`[实测]`，`/v1/chat/completions` `stream:true`）：首帧即真实上游 chunk 格式，
**流内错误也是 SSE 帧**而非 HTTP 状态码：
```
data: {"id": "chatcmpl-<redacted>", "object": "chat.completion.chunk", "created": <int>, "model": "GLM-5.3-Flash", "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": null}]}
data: {"error": {"message": "上游业务错误 code=1005: exceed quota limit", "type": "upstream_error", "code": 1005}}
```

**后果**：
- 两端点若共用一条「上游错误 → 响应」路径 → **至少一端必挂**。
- chat 的 502 是**网关自己构造**的形态 B（`{"error":{...}}`），而 messages 是**上游原文**（形态 C）——
  同一上游错误，**两种形态 + 两种状态码**。

**验证判据**：`endpoints.md` §3 A-02/A-03 的「错误路径分档表」。⚠️ 会消耗真实账号额度，**对拍时应使用
已 `exhausted` 的账号或假凭证**以避免消耗。

### CR-34 🟠 同类型参数互换是编译器抓不到的缺陷

**现象**（t6 并发写冲突期实测发现）：`gateway.go` 调用 Catherine 定义的
`writeShapeB(w, 401, "缺少鉴权凭证", "authentication_error")`，而真实签名是
`writeShapeB(w, status int, typ, message string)` —— **两个 `string` 实参对调**。

**后果**：**编译通过**（类型都是 `string`），但输出变成
`{"error":{"type":"缺少鉴权凭证","message":"authentication_error"}}` —— **语义完全相反**。
这是 CR-02（三套错误形态）类风险的**现实化案例**：三个 error writer 的签名若都是
`(w, int, string, string)`，任何一处实参对调都不会被编译器拦住。

**处置**（实现注意事项）：
1. 三个 error writer **参数名必须自解释**（`typ` / `message`），调用处**具名传参或加注释**；
2. **必须有测试逐字断言 body 的 `type` 与 `message` 两个字段**（只断言状态码抓不到此类缺陷）；
3. 若可行，用**具名 struct 传参**（`writeShapeB(w, errShape{Status:401, Type:"authentication_error", Message:"缺少鉴权凭证"})`）
   让字段名成为编译期约束。

---

## 3. 安全风险（对拍与切换前必须知情）

### CR-05 🔴 `GET /admin/api/settings` 回显 admin_key 明文

**实测**：`GET /admin/api/settings` → **200**
`{"admin_key":"<redacted:32chars>","gateway_key":"","quota_refresh_interval":600}`。

**后果**：
- Go 版若「顺手修掉」（不返回 admin_key），**对拍必挂**（字段缺失）。
- 若照抄，**风险延续**：任何持有管理凭证者可读回密钥本身。

**处置**：**本次等价重写必须照抄该行为**（契约等价优先），但**在 `docs/` 与部署说明中标注为已知安全问题**，
作为**独立增强需求**另行处理（ADR-001 §37：增强需求不属于本次范围）。

**验证判据**：
```bash
K=$(grep -m1 '^ZCODE_ADMIN_KEY=' .env | cut -d= -f2-)
curl -s -H "Authorization: Bearer $K" http://127.0.0.1:9120/admin/api/settings \
  | python3 -c "import json,sys;d=json.load(sys.stdin);print(sorted(d.keys()), len(d['admin_key']))"
# 期望 ['admin_key','gateway_key','quota_refresh_interval'] 32
```

### CR-06 🔴 `/admin/api/export` 导出明文凭证

**实测**：`GET /admin/api/export` → **200**
`{"version":1,"exported_at":<float>,"providers":{"zai":[{"name","mode","secret"}],...}}`，`secret` 为**明文**。

> ⚠️ **路径陷阱**：`/admin/api/accounts/export` 实测 **405**（不存在）—— 真实路径是
> `/admin/api/export`（**无 `accounts` 段**）。同理导入是 `/admin/api/import`（POST）。
> 这两个 405 是早期探测的方法不匹配产物，**不得**写进实现契约。

**证据强度**：🔴 **强** —— 探测时该响应曾被落盘，captain 已**归档隔离**
（`workspace/temp/z2a-credential-quarantine-20261015-192747/export.body`，700 目录 / 600 文件）。
本清单**不回显任何明文**。

**后果**：Go 版若「改为加密导出」→ 与老 `.zsb`/`export` 格式不兼容（`import` 是老服务，无法读新格式）。
`docs/03` §183 描述的 `.zsb` 口令封包是**未实现蓝图**（roadmap `[ ]`）。

**处置**：等价重写**必须保持明文导出**；加密导出作为独立增强。

**验证判据**（**只看结构，不落盘**）：
```bash
K=$(grep -m1 '^ZCODE_ADMIN_KEY=' .env | cut -d= -f2-)
curl -s -H "Authorization: Bearer $K" http://127.0.0.1:9120/admin/api/export \
  | python3 -c "import json,sys;d=json.load(sys.stdin);print(sorted(d.keys()), {k:len(v) for k,v in d['providers'].items()})"
# 期望 ['exported_at','providers','version'] {'zai':4,'bigmodel':2} —— 严禁打印 secret 值
```

### CR-07 🔴 网关 key 为空 → 匿名完全放行

**实测**：
- `.env` 中 `ZCODE_GATEWAY_KEY` **被注释**；DB `meta.gateway_key` **长度 0（空串）**。
- `GET /admin/api/status` → `"gateway_key_set":false`。
- `/v1/models` **无凭证 200**；**带错误 key 也 200**。

**`[黑盒]` 语义**（`test_gateway_auth.py` 首行）：`key 未配置 → 放行（空 key fallback）；已配置 → 无凭证 401 / 错凭证 403`。

> ⚠️ **401/403 的具体语义仅来自黑盒测试**（生产 key 为空，无法实测）。

**后果**：Go 版若「默认强制鉴权」→ 生产行为不一致；若「照抄空 key 放行」→ 风险延续。
且 `gateway_key_set` 字段的值直接影响 `GET /admin/api/status` 对拍。

**处置**：实现空 key fallback（放行），并在部署文档标注必须设 key。

**验证判据**：
```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9120/v1/models   # 期望 200（空 key 放行）
curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer wrong-key-0000' http://127.0.0.1:9120/v1/models  # 期望 200
```

### CR-18 🟠 `ZCODE_HOST` 默认 `0.0.0.0`，生产 `127.0.0.1`

**实测**：`.env.example` 默认 `0.0.0.0`；`.env` 实测 **`127.0.0.1`**。
`systemd` unit 注释亦写「监听 127.0.0.1:9120」（**注释准确**）。

**后果**：Go 默认值必须是 `0.0.0.0`（契约一致），但切换时**必须照抄 `ZCODE_HOST=127.0.0.1`**，
否则新服务**暴露到局域网**（叠加 CR-07 的空 key = 完全敞开）。

**验证判据**：
```bash
ss -tlnp | grep 9120    # 期望 127.0.0.1:9120，不是 0.0.0.0:9120
```

---

## 4. 路由与头部形态风险

### CR-04 🟠 删除是**集合级** `DELETE` + body 数组

**实测**：`DELETE /admin/api/accounts` + body `["<id>"]` → **200** `{"deleted":1}`。
`DELETE /admin/api/accounts/{id}` → **405**。
`openapi.json` 声明请求体为 `{"type":"array","items":{"type":"string"}}`。
`["__no_such_id__"]` → **200** `{"deleted":0}`（**不报 404**）。

**后果**：Go 版若实现为 RESTful `DELETE /{id}` → 路由不匹配 + 状态码差异。

**验证判据**：
```bash
K=$(grep -m1 '^ZCODE_ADMIN_KEY=' .env | cut -d= -f2-)
curl -s -X DELETE -H "Authorization: Bearer $K" -H 'Content-Type: application/json' \
  -d '["__no_such_id__"]' -w ' [%{http_code}]\n' http://127.0.0.1:9120/admin/api/accounts   # 期望 {"deleted":0} [200]
```

### CR-08 🟡 FastAPI 307 尾斜杠重定向 + 绝对 Location

**实测**：
- `/` → **307** → `/admin`
- `/admin` → **307** → `/admin/login`
- `/admin/api/accounts/refresh/` → **307** → `/admin/api/accounts/refresh`
- `/static` → **307** → `/static/`
- **Location 是绝对 URL**（含 scheme+host）

**后果**：Go `http.ServeMux` 默认**不做**尾斜杠重定向（且行为相反）。
对拍器若不跟随重定向，会记录 `307` vs `200` 差异。

**验证判据**：
```bash
for p in / /admin /admin/api/accounts/refresh/ /static; do
  printf '%-36s' "$p"; curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' "http://127.0.0.1:9120$p"
done
```

### CR-09 🟡 `405` 的 body 与 `allow` 头

**实测**：`GET /admin/api/monitoring/clear` → **405** `{"detail":"Method Not Allowed"}`（形态 A）；
`OPTIONS /v1/messages` → **405**，带 `allow: POST`（**无 CORS 头**）。

**后果**：Go 若返回 `405` 但 body 为空或 `Allow` 缺失/不同 → 差异。

### CR-10 🟡 `server: uvicorn` 与 `content-type` 无 charset

**实测**：
- **所有**响应带 `server: uvicorn`（Go 默认 `server: ` 为空或 `Go-http-server`）。
- JSON 响应 `content-type: application/json`（**无 charset**）；上游透传为 `application/json; charset=utf-8`。
- 页面 `text/html; charset=utf-8`；`/admin/login` **唯一**带 `cache-control: no-store`。

**后果**：若对拍比对响应头，`server` 与 `content-type` 必差。

**处置**：对拍器**应对 `server` 头做豁免**（Go 不应伪装 uvicorn）；`content-type` 需逐类对齐。

**验证判据**：
```bash
curl -sI http://127.0.0.1:9120/v1/models | grep -i 'server\|content-type'
curl -sI http://127.0.0.1:9120/admin/login | grep -i 'cache-control'
```

---

## 5. 数据兼容风险

### CR-13 🔴 WAL 模式必须一致

**实测**：`PRAGMA journal_mode = wal`；伴随文件 `accounts.db-wal`（0 B）、`accounts.db-shm`（32768 B）。

**后果**：Go 若以 `delete` 模式打开 → 与老进程并发读写**互相阻塞甚至损坏**。

**验证判据**：
```bash
cd /opt/zcode2api
python3 -c "import sqlite3;print(sqlite3.connect('file:data/accounts.db?mode=ro',uri=True).execute('PRAGMA journal_mode').fetchone()[0])"  # 期望 wal
ls -la data/accounts.db-wal data/accounts.db-shm   # 期望存在
```
**处置**：Go DSN 必须带 `_pragma=journal_mode(WAL)`（`modernc.org/sqlite`）。

### CR-15 🔴 凭证明文 —— **不得引入加密**

**实测**：`data.api_key`（len 49）与 `data.jwt_token`（len 255）**均无 `enc:v1:` 前缀** → 明文。
`ZCODE_MASTER_SECRET` env **实测不存在**；`docs/03` 的「AES-256-GCM 加密」是**蓝图**，
roadmap §25 仍为**未勾选 `[ ]`**。

**后果**：Go 若实现加密 → 老 Python 进程**读不出凭证** → 双进程互操作破裂、对拍与「SQLite 数据兼容」验收直接失败。

**验证判据**：
```bash
python3 -c "
import sqlite3,json
c=sqlite3.connect('file:data/accounts.db?mode=ro',uri=True).cursor()
n=0
for (d,) in c.execute('select data from accounts'):
    j=json.loads(d)
    for k in ('api_key','jwt_token'):
        v=j.get(k)
        if isinstance(v,str) and v.startswith('enc:'): n+=1
print('enc-prefixed creds =',n)"   # 期望 0
```

### CR-11 🟠 `id` 派生规律

**实测**：`<name/来源标识>-<8 位十六进制小写>`。
样例：`contract-probe-bf31b757`、`probe-toggle-live-06cabd6e`、`import-probe-a63213aa`、
`probe-toggle-8e870470`、`oauth-login-ea92aa60`、`laohao-bigmodel-2420b3c2`。
`docs/03` §10 写 `zai-<sha256[:12]>` —— **不符**。

**未实测**：后缀 8 位 hex 的**生成算法**（随机 or 哈希截断）。

**后果**：Go 若按 sha256 派生 → `id` 与老库/老引用（cron 脚本、DB 外键引用）不匹配。

**处置**：只需保证「唯一 + 形如 `^...-[0-9a-f]{8}$`」；**不得**假定算法。

### CR-12 🟠 `data` JSON 键序是可观测契约

**实测**：23 键顺序 6/6 账号**完全一致**：
```
id, name, provider, mode, jwt_token, api_key, enabled, status, quota, plan, plans, usage,
subscription, use_count, fail_count, risk_strikes, recent_results, last_used_at,
last_checked_at, cooling_until, last_error, created_at, fingerprint
```
（键集合 6/6 相同，`COMMON` == 全集，`EXTRA` == 空。）

**后果**：Go 若用 `map[string]any` 序列化 → Go 的 map 迭代序**随机** → DB 内容 diff 不稳定；
若 struct 字段顺序与实测不同 → **逐字节 diff 必挂**。

**处置**：Go 侧 `data` 用**有序**序列化（struct 或 ordered map），字段顺序**逐字对齐**。

**验证判据**：
```bash
python3 -c "
import sqlite3,json,collections
c=sqlite3.connect('file:data/accounts.db?mode=ro',uri=True).cursor()
sets=[tuple(json.loads(d,object_pairs_hook=lambda p:[k for k,_ in p]).keys()) for (d,) in c.execute('select data from accounts')]
print('键序唯一 =',len(set(sets))==1,'| 键数 =',len(sets[0]))"   # 期望 True 23
```

### CR-14 🟠 `data` 键序 ≠ `AccountView` 键序

**实测**：
- `data` JSON：`... mode, jwt_token, api_key, enabled, status ...`
- `AccountView`：`... mode, token_masked, enabled, status ...`

即 `AccountView` 用**一个** `token_masked` 替换了 `jwt_token` + `api_key` **两个**键的位置，
且**省略** `usage`。

**后果**：Go 若复用同一 struct 序列化两处 → 两处键序**至少一处错**。

**处置**：**两个独立类型**：`AccountData`（存储，23 键）与 `AccountView`（API，21 键）。

**验证判据**：
```bash
K=$(grep -m1 '^ZCODE_ADMIN_KEY=' .env | cut -d= -f2-)
curl -s -H "Authorization: Bearer $K" http://127.0.0.1:9120/admin/api/accounts \
 | python3 -c "
import json,sys;d=json.load(sys.stdin);a=d['accounts'][0]
print('AccountView 键序:',list(a.keys()))
print('字段数:',len(a))"   # 期望 21 键，mode 后紧跟 token_masked
```

### CR-16 🟡 `recent_results` 方向 —— 🔴 **t1 原结论错误，t5 审查期更正**

> ⚠️ **本条在 t1 冻结时结论写反了**，t5 审查期由 Hilkiah 用三方独立证据更正。
> 保留原文并显式标注，以免下游按旧结论实现。

**❌ 原（错误）结论**：`[0]` 最新（新结果**前插**）。
**✅ 更正结论**：**`[-1]` 最新（新结果后插 / append 到尾部，`[0]` 最旧）**。

**三方独立证据**（2026-10-15 t5 审查期，全部可复现）：

1. **DB 原始字节**：`laohao-bigmodel-2420b3c2` 的 `data` 切片显示
   `"recent_results": [{"ok": true, "at": 1791978426.7117856, ...}, {"ok": true, "at": 1791978531.6, ...}, ...]`
   —— `at` **递增**（1791978426.71 < 1791978531.62 < …）。
2. **API 输出**：`GET /admin/api/accounts` 6/6 账号 `at` 序列**严格升序**，
   `[0]` = 10-14 11:47、`[-1]` = 10-14 17:08。
3. **因果证据（最强）**：我在 13:01/13:02 UTC 对 `/v1/messages` 发的探测请求，
   其新条目出现在 `[-1]` 位置（`13:16`），而非 `[0]` —— 直接证明写入方向。

**原判据为何写错**：原文写「`[0]` 的 `at` = `1791978426.71`，`[19]` 更旧 → `[0]` 最新」。
`1791978426.71` 换算即 **10-14 11:47**，恰是**最旧**的一条 —— 把最旧值误读为最新。
`[黑盒]` 断言 `recent_results[-1]` 的 shape（只验形状不验方向），与此不矛盾，**不构成反证**。

**后果**：Go 若**前插**（`prepend`）→ 顺序反转。**UI 展示会反过来**（最新一条显示在最前），
且新写入的条目落在 `[0]`，与老进程交错写同一 DB 时会破坏顺序语义。

**复现命令**：
```bash
python3 - <<'PY'
import sqlite3, json, datetime
P="/opt/zcode2api/data/accounts.db"
con=sqlite3.connect(f"file:{P}?mode=ro",uri=True,timeout=15)
for i,d in con.execute("SELECT id,data FROM accounts ORDER BY id"):
    a=[r["at"] for r in json.loads(d)["recent_results"]]
    f=lambda v: datetime.datetime.utcfromtimestamp(v).strftime("%m-%d %H:%M")
    print(f"{i:32} [0]={f(a[0])} [-1]={f(a[-1])} 升序={all(a[k]<=a[k+1] for k in range(len(a)-1))}")
PY
```
期望：6/6 打印 `升序=True`，`[-1]` 晚于 `[0]`。

### CR-16b 🟠 **对拍器把该字段整体折叠，故方向错误不会被对拍抓到**

`scripts/conformance/normalize.rules.json` 的 `$.accounts[*].recent_results` 规则是
`replace_typed`（按类型折叠为空数组），理由记为「进程态非持久契约」。
该理由对**内容**成立，但**方向**是持久契约的一部分 —— 折叠后 L3 不再比对，
**前插/后插的实现错误在对拍中不可见**。
**必须**由单元测试/黑盒测试单侧断言方向（见上方复现命令），不得只依赖对拍。

### CR-35 🔴 **账号列表顺序是持久契约，但被对拍器折叠 —— 顺序差异对拍看不见**

**t5 审查期新增（Hilkiah）。与 CR-16b 完全同型：对拍能过的错误。**

#### 实测：真实排序键 = `(provider 分组序, 组内 created_at 升序)`

**证据 ① 组内反证（排除 rowid）** —— `zai` 组内：
```
rowid      = [13211, 12899, 13212, 12900]   ← 乱序
created_at = [1790328959.0, 1790328961.4, 1790329509.2, 1790329520.1]  ← 严格升序
```
若按 rowid 排序，组内应是 `[12899,12900,13211,13212]`，实测不是 → **排除 rowid**。

**证据 ② 候选穷举**（6 账号全量）：rowid 升/降序 ❌、id 升序 ❌、name codepoint 升/降 ❌、
`(provider,name)` ❌、`(provider,rowid)` ❌、`(provider,data.created)` ❌、单列 `created_at` ❌；
**唯一 ✅** = `(provider分组序, created_at升序)`。

**证据 ③ 端点交叉验证**：`/admin/api/export` 的 `providers.zai`/`providers.bigmodel` 数组顺序
与 `/admin/api/accounts` 的组内顺序**逐项一致** → 两端点共用同一排序逻辑（非巧合）。

**证据 ④ 稳定性**：连查 5 次顺序逐次一致 ✅（非随机、非游标残留）。

**证据 ⑤ 组序来源**：`provider` 组序 = `providers` 字段序 `["zai","bigmodel"]`。

**证据 ⑥**：列 `created_at` 与 `data.created_at` 6/6 数值一致（Δ<0.001）→ 键等价。

#### 🔴 Go 侧现状错误

`internal/store/store.go:214`：`SELECT ... FROM accounts ORDER BY rowid`
实测产出 `[xinhao-2000w…, oauth-apikey-c5c641a1, oauth-apikey-8808fbac, laohao-bigmodel…, oauth-login-ea92aa60, oauth-login-8523aba4]`
**与老服务顺序完全不同**（老服务 zai 组在前、组内按 created 升序）。
须改为 `ORDER BY provider, created_at`（组序按 providers 表序）。

#### 🔴 为什么这是最危险的一类风险：对拍抓不到

`scripts/conformance/normalize.rules.json` **首条规则**：
```json
{"path":"$.accounts","sort_by":"id",
 "why":"账号列表的【位置顺序】是实现查询游标，不是契约；…按 id 升序把两侧对齐到同账号同位置后再逐位置比对"}
```
该规则把两侧数组**按 id 重排后再比对** → **顺序差异被彻底抹除**。
其理由「位置顺序是**实现查询游标**，不是契约」**经实测证伪**：
顺序**稳定**（5/5）、**跨端点一致**（accounts/export）、**组序有明确来源**（providers 表）、
且**用户可见**（UI 列表顺序）→ 它是**持久契约**，不是"游标残留"。

**后果**：Go 侧 `ORDER BY rowid` 会通过全部对拍（因为被重排），但**前端列表顺序与老服务不同**，
用户可见差异。**对拍绿 ≠ 契约等价**。

#### 🔴 rowid 不稳定旁证（t5 审查期实测，两轮独立观测）

同一次观测窗口内，**两个账号的 rowid 发生了变化**（被删重建）：

| id | 我第一轮探针 | 第二轮（Liche 独立观测） | created_at |
|---|---|---|---|
| `oauth-login-ea92aa60` | 13211 | **13214** ⚠ | 1790328959.02（**不变**）|
| `oauth-login-8523aba4` | 13212 | **13213** ⚠ | 1790329509.18（**不变**）|
| `oauth-apikey-c5c641a1` | 12899 | 12899 | 1790328961.38 |
| `oauth-apikey-8808fbac` | 12900 | 12900 | 1790329520.14 |
| `laohao-bigmodel-2420b3c2` | 12901 | 12901 | 1789147671.78 |
| `xinhao-2000w-02f71f86` | 12898 | 12898 | 1789148266.10 |

**结论**：rowid 是**会漂移的实现细节**（`ON CONFLICT` 保 rowid，但 `INSERT OR REPLACE`/删除重建会改它）；
而 `created_at × provider` 顺序**跨重建保持稳定**（两账号 created_at 未变、位置未变）。
→ **再次独立排除「rowid 说」**：一个会漂移的键不可能是 UI 展示契约。

> **旁证的意义**：即便不穷举候选排序键，"rowid 会漂移而实测顺序不变"这一条**单独就足以证伪 rowid 说**。
> 该观测由两名成员在**不同时刻独立**获得（Hilkiah 第一轮 / Liche 第二轮），互为交叉验证。

#### 独立验证结论（t5 审查期，Hilkiah）

| 验证项 | 方法 | 结果 |
|---|---|---|
| Go 实现顺序正确性 | **我自写探针**（非跑实现者测试）直接对生产库快照跑 `ListAccounts`，与老服务 API 实测序列逐项比对 | ✅ **逐项一致** |
| 字典序陷阱真被规避 | 合成用例：`bigmodel` 的 created_at 最小（=1） | ✅ zai 组仍在前（**未**用字典序）|
| 未知 provider 排后 | 合成 `zzz-unknown`/`aaa-unknown` | ✅ 排在两已知组之后 |
| NULL created_at | 合成 `created_at=nil` | ✅ 排组内最前 |
| ORDER 哨兵非摆设 | **独立复现**：同内容不同序 → pre-sort 后 L3 逐字节相同（洗绿）而 ORDER **必红**；同序 → 绿（非永红） | ✅ 四项全对 |
| 缺证据不判过 | baseline 非数组 / 缺路径 | ✅ 均 **红**（未静默放行）|

> ⚠️ **必须分清的一层**：`harness.py --engine-selftest` 的 `AD-accounts ORDER:PASS` 是
> **candidate 缺省 = baseline**（`candidate = args.candidate or args.baseline`）→ 那是**老服务自比**
> （引擎自洽性验证），**不能**当作"Go 实现顺序正确"的证据。
> Go 侧顺序正确性**只能**由上面第一行的独立探针（或等 admin API 实现后的真跨服务对拍）证明。
> **这是又一处「证据外观 ≠ 覆盖」**：`ORDER:PASS` 字样在自测与真对拍两种语境下含义不同。

**处置**：
1. Go 侧改为 `ORDER BY provider, created_at`（**已由 t11 落地**：`ProviderGroupOrder` + `sort.SliceStable`）；
2. 对拍规则 `sort_by:id` **保留**（位置对齐仍有价值：避免字段差异错位到别的账号），
   但**必须另加单侧断言**覆盖顺序（契约测试/单元测试），不得依赖对拍；
3. 该模式应推广：**凡"归一化规则"声称某维度非契约，须逐条复核该声称是否成立** —— 见 CR-36。

### CR-39 🔴 **A2 空库守卫「自败」：拦截时留下产物，使自身在下次启动永久失效**

**t5b 审查期新增（Hilkiah，候选 ③ 独立审查）。** 证据文档 `review-evidence/t5b-startup-guards.md`。

**位置**：`cmd/zhipucode2api/main.go` `guardFreshDatabase()` —— ADR-005 A2 的代码化守卫。

#### 现象（同一命令、同一目录、连续两次）

```
第 1 次:  🛡 守卫拦截 (exit=1)        ← 但留下了 accounts.db（4096 B，无表）
第 2 次:  🔴 放行 + 监听              ← freshFile 已是 false
第 3 次:  🔴 放行 + 监听
```

守卫首行是 `if !freshFile { return nil }` —— **全部效力绑死在「本次进程是否创建了文件」**。
而 `store.Open` **创建文件但不建表**（`EnsureSchema` 在生产代码零调用点，我独立核实），
所以**拦截的那一刻，正是它亲手制造了让下次失效的产物**。

**strace 坐实文件由该进程创建**：启动前 `newfstatat(...)= -1 ENOENT` →
`open(..., O_RDWR|O_CREAT|..., 0644) = 4` → 退出后 `accounts.db` 4096 B 存在。

#### 后果链（与生产 unit 组合后必然触发）

```
空目录 / 目录不存在
  ↓ 第 1 次启动：守卫拦截（exit=1）—— 留下 4096B 无表空库
  ↓ 新 unit `Restart=always` + `RestartSec=5`（/etc/systemd/system/zhipucode2api.service:28-29）
  ↓ 第 2 次启动：freshFile=false → 守卫跳过 → **服务监听在空库上**
  ↓ /v1/models → 200（我实测 224B，正常模型列表）；启动日志只有一行 listening，零告警
```

**这正是 ADR-005 全文要防的 A2「静默灾难」，只是被推迟了一次重启**；
而重启由生产 unit 配置**保证发生** ⇒ **不是理论风险**。
与 ADR-005「宁可起不来（立刻可见），也不要静默跑在空库上」**直接冲突**。

#### 附：同类边界（同一根因）

**「库已存在、有 `accounts` 表、但 0 行」也放行**（实测）——
`freshFile=false` 让守卫在**任何**已存在库上都不检查账号数。

#### 建议修法（择一，推荐 ①）

1. **判据与「谁创建了文件」解耦**：改为无条件检查（文件不存在 → LOUD FATAL 不创建；
   无表 → FATAL；0 行 → FATAL）。若「合法新部署确实从 0 账号起步」是真实需求，
   应引入**显式 opt-in**（如 `ZCODE_ALLOW_EMPTY_POOL=1`），而非靠 `freshFile` 猜意图。
2. **拦截时清理自己创建的产物**（严格限定「本进程刚创建 + 0 字节/无表」才 `os.Remove`）→ 让拦截**幂等**。
3. **叠加兜底**：`ListenAndServe` 前把账号数写进启动日志（`pool: %d accounts`）——
   不是守卫替代品，但能让 A2 从「静默」变「可观测」。

#### 对切换检查表的影响

`cutover-checklist.md` 的 **C-3**（切换后 `/admin/api/accounts` 必须 = 6）在 F-1 存在时**不可靠**：
服务能在 0 账号库上正常启动、通过 C-1/C-2 的文本检查，C-3 只能在切换**之后**才发现问题。

### CR-38 🟢 **归一化规则全量体检：容器级规则恰好 3 条，且全部已定罪（完备性结论）**

**t5 审查期新增（Hilkiah）。** CR-16b/CR-35/CR-37 三处同型发现后，
对 `normalize.rules.json` **全部 22 条规则**做了穷举分类，回答「是否还有第四处漏网」。

#### 分类结果（可复核：`len(rules)==22`）

| 类别 | 条数 | 路径 |
|---|:---:|---|
| 🔴 **容器级**（折叠数组/对象 → 可能含顺序维度） | **3** | `$.accounts`(sort_by)、`$.accounts[*].recent_results`(replace_typed)、`$.entries`(replace_typed) |
| 🟢 标量级（折叠单值 → 不含顺序/长度维度） | **19** | `$.ts`、`$.id`、`$.created`、`$.stats.calls/fail`、`$.accounts[*].{use_count,fail_count,last_used_at,last_checked_at,cooling_until,last_error×2,token_masked}`、`$.admin_key`、`$.gateway_key`、`$.choices[*].message.content`、`$.__sse_frames__[*].{id,created,choices[*].delta.content}` |

**结论**：容器级规则**恰好 3 条**，**逐条对应已定罪的 CR-35 / CR-16b / CR-37** ——
**无第四处漏网**。标量级 19 条不含顺序维度，**无风险**（其折叠的值本就是进程态计数器/时间戳/密钥）。

#### `replace_typed` 的维度账目（实测，非推断）

对 `$.entries` 构造 5 组对照，逐维度判定折叠后是否仍可辨：

| 维度 | 折叠后 L3 | 原始 L2 diff | 判定 |
|---|:---:|:---:|---|
| 内容不同（长度 3 vs 1） | 相同 | 1 条 | ✅ 仍可辨（L2 长度）|
| **顺序不同（同内容）** | 相同 | **0 条** | ❌ **丢失** |
| 类型不同（list vs str） | 不同 | 1 条 | ✅ 仍可辨 |
| 类型不同（list vs null） | 不同 | 1 条 | ✅ 仍可辨 |
| 键缺失 | 不同 | 1 条 | ✅ 仍可辨 |

> **精确账目**：`replace_typed` 丢失 = **内容 + 长度 + 顺序**；保留 = **类型 + 键存在性**（由 L2 承担，
> 实现见 `harness.py:193` `replace_typed` 按 `type_name` 分派返回**对应类型**的空值 —— 设计正确）。
> → **唯一致命维度 = 顺序**。内容/长度本就是进程态（各服务流量史不同），折叠可辩护；
> **顺序不是**（它是持久契约且 UI 可见）。

#### 推广判据（建议纳入 `verification-methodology.md`）

> 凡归一化规则声称「某维度非契约」，须逐条回答**三问**：
> ① 该规则折叠的是**容器还是标量**？（容器才可能藏顺序维度）
> ② 折叠后**哪些维度仍可辨**？（逐维度构造对照实测，不靠读代码推断）
> ③ 被折叠的维度里，**有没有"持久契约"混在"进程态"里被一并扔掉**？
> **CR-16b/CR-35/CR-37 三次都是第③问被漏答。**

### CR-37 🟠 **`monitoring.entries` 方向契约被对拍折叠 —— 与 CR-16b/CR-35 同型**

**t5 审查期新增（Hilkiah）。**

#### 实测：`entries` **严格降序（新在前）**

一次观测（44 条）逐对验证：`ts` **严格递减**（无相等），
`最新 ts=1792070901.5955966`、`最旧 ts=1792068104.5739286`（Δ≈2800s）；
连查 2 次 `req_id` 序列**逐项一致**（`961a180d0e992850`、`907db18c68411535`、`90be0306d276815a`…）。

**UI 可见**：`/admin/monitoring` 页面用 `forEach` + `slice(0,…)` 渲染 `entries`（实测页面含这 3 个 token）
→ 列表**首行 = 最新请求**，用户直接可见 → **方向是持久契约**。

#### 🔴 对拍盲区

`normalize.rules.json` 的 `$.entries` 规则是 **`replace_typed`**（按类型折叠为空数组），
理由记为「请求监控实时日志流…这是【进程态】非持久契约」。

该理由对**内容**成立（各服务流量史不同，正式对拍必然不同），
但**方向**（新在前）与内容无关 —— 折叠后 L3 不再比对，
**若 Go 实现按升序（旧在前）输出，对拍完全看不见**，而 UI 列表顺序会整体反转。

**与 CR-16b 完全同型**：同一份 `normalize.rules.json` 里，
**第二处「折叠了持久契约的一个维度」**。这说明该模式**不是孤例**。

**处置**：
1. 给 `AD-monitoring` 补 `order_keys`（按 `ts` 降序断言）；
2. 复核 `normalize.rules.json` **其余全部 `replace_typed`/`replace` 规则**，
   逐条问「该规则声称非契约的那个维度，**除内容外是否还含方向/顺序/长度/边界**」——见 CR-38 的排查表。

> ⚠️ **顺带观察（未判为缺陷）**：`MUT-monitoring-clear` 用例执行后，
> `entries` 从 44 变 0 且**在后续观测中未自愈**（连续 4 次采样均 0，打 `/v1/models` 与无效 model 的
> `/v1/chat/completions` 也不恢复 —— 与"只记录 gateway 实际转发流量"的契约一致，
> 本地校验失败不入监控）。故**该清空是预期行为**，但意味着**对拍用例顺序会影响后续断言**：
> 若 ORDER 断言在 clear 之后跑，`entries` 为空 → 顺序断言**无样本可判**（应显式 fail 或 skip，不可静默 PASS）。

### CR-36 🟠 **契约自身的证据等级缺陷：「推断冒充实测」**

**t5 审查期新增（Hilkiah，自查发现）。**

**现象**：我在更正 `recent_results` 方向（CR-16）时，顺带在 `sqlite-schema.md` G-14 写了
「超出**截头（丢最旧）**」—— 但**当时我并没有验证"丢哪一端"**：CR-16 只验了方向（后插），
没验截断端。这是**同一个错误的复发**：把推断当结论写。

**事后补验（本轮）**：6/6 账号 `n` **全部恰好 20**，且 `[-1].at` 与 `last_used_at`
**同刻**（Δ ≈ 1e-5 秒，见下方精度说明）→ 保留的是**最新**那条 → 丢的必是**最旧** →
**「截头」成立**（事后被证实，但当时属未验证断言）。

> ⚠️ **精度陷阱（顺带实测）**：`[-1].at` 与 `last_used_at` **并不精确相等**，Δ ≈ 7e-6 ~ 2e-5 秒。
> 它们是**两次独立 `time.time()` 取值**（一条给 `last_used_at`，一条给 `recent_results[-1].at`），
> 故**不能用 `==` 判定"同一次调用"**，须用容差（如 `abs(Δ) < 1e-3`）。

**通用判据（建议纳入 `verification-methodology.md`）**：
> 契约文档里每个结论句，须能回答「**这句的 `[实测]`/`[黑盒]` 标记，对应哪一条命令与输出？**」
> 答不出 → 降级为 `[推断]` 或补测。
> **标记本身也是断言** —— 错误标记比没有标记更危险（它让下游审查者跳过验证）。

**本轮体检范围**：`docs/contract/` 全部 13 份文档中「顺序/方向/最新/最旧/截头/排序」类断言。
结果：除已更正的 CR-16/G-14 与新增 CR-35 外，其余（键序、AccountView 字段序、
providers 顺序敏感、`index_list` vs `sqlite_master` 顺序差异）**均有实测支撑** ✅。

### CR-24 🔴 `SetEnabled` 必须双写

**实测**（t1 独立复现 + captain `adr-003` 假账号实验，两轮独立证据一致；
t1 用自建探针账号 `probe-toggle-live-06cabd6e`，事后复原并删除）：

| 阶段 | 表列 | `data.enabled` | API 输出 |
|------|:---:|:---:|:---:|
| 新建后 | `1` (int) | `true` (bool) | `true` |
| `POST /enabled {"enabled":false}` → 200 | **`0` 【变】** | **`false` 【变】** | **`false` 【变】** |
| `POST /enabled {"enabled":true}`（复原） | `1` | `true` | `true` |
| `DELETE [id]` → 200 `{"deleted":1}` | — | — | — |

**结论**：老服务 `SetEnabled` **双写**（列 + `data` JSON），并**全量序列化回写**（键集合 23 不变）。

**后果**：Lamias 的 `SetEnabled` 目前**只写列** → 老服务 API 输出停留在旧值 → **对拍 L3 值级必挂**。

> ⚠️ **反命题已实测证伪**：「只有列被更新，`data.enabled` 是只读遗留脏值」——
> 若按此实现，`laohao-bigmodel-2420b3c2` 的 `"True"` 会永久驻留，而老服务任意一次 toggle 后都会变 bool。
> 即 **`data.enabled` 脏值不是永久驻留的**，不需要单独的一次性迁移。

**透出面（实测扫描，豁免清单范围）**：`data.enabled` 只经 **一个**端点透出 ——
**B-02 `GET /admin/api/accounts`**（`accounts[].enabled`）。实测确认以下端点**均不含** `enabled`：
`/admin/api/export`（条目仅 `{name,mode,secret}`）、`/admin/api/import`、`/admin/api/claim`
（条目 `{account_id,account_name,message,ok}`）、`/admin/api/claim/preview`
（`{account_id,account_name,activated,activation_error,error,plans}`）、`/admin/api/status`、
`/admin/api/settings`、`/admin/api/monitoring`。单账号 `GET /admin/api/accounts/{id}` **不存在**（405）。
> `/admin/api/claim/captcha-config` 的 `enabled` 是**验证码池开关**，与账号 `enabled` **语义无关**，不得混入豁免。

**处置**：**改为双写**（列 INTEGER 0/1 + `data.enabled` bool）。

**验证判据**（Go 版实现后，用探针账号 + 只读回读，**严禁对生产账号操作**）：
```bash
# 1) 建探针 → 2) 关 → 3) 只读核对两处都变 → 4) 开（复原）→ 5) 删
# 期望：列 1→0→1，data.enabled true→false→true，API 输出同步
```

### CR-27 🟡 `meta.value` 是 TEXT（数字存字符串）

**实测**：`PRAGMA table_info(meta)` = `(0,'key','TEXT',0,None,1) (1,'value','TEXT',1,None,0)`；
`quota_refresh_interval` 的 `typeof` = **text**，内容 `"600"`。

**后果**：Go 若把 `600` 写成 INTEGER → 老进程若做 `int(value)` 可能仍可用（SQLite 宽松），
但**类型不一致**会导致 `typeof` 探测差异。

**处置**：`meta` 值一律**按字符串**读写。

### CR-03 🟠 `enabled` 脏值 → ADR-002 已知对拍差异

**实测**：`laohao-bigmodel-2420b3c2` 的 `data.enabled = "True"`（string），
其余 5 行为 `true`（bool）；表列 6 行全为 integer `1`。

**裁定（ADR-002 + ADR-003）**：Go 版**规范化**为统一 bool 输出，不复现脏值；
`SetEnabled` **双写**（CR-24）。

**后果**：**必然产生一处 L2 结构差异**（`string → bool`）。

> 🔴 **前提条件（t1 实测补充）**：该豁免**只在「对拍期间不对该账号执行 toggle」时成立**。
> 实测证明老服务**内存持有账号状态**，且一次 toggle 会把 DB 内 `"True"` 洗为 `true`(bool)
> —— 此时老服务侧差异**消失**（前提消失，非通过）。
> 对拍用例必须**避免 toggle `laohao-bigmodel-2420b3c2`**。详见 `account-model.md` §4.3.2。

**处置**：**必须走对拍器显式豁免清单**，条目须含：字段路径 `accounts[].enabled`、
差异形态 `string → bool`、理由「ADR-002 规范化裁定」、批准人「用户」。
🔴 **严禁静默归一化** —— 豁免必须在报告中显式列出，留审计痕迹。

**验证判据**：
```bash
python3 -c "
import sqlite3,json
c=sqlite3.connect('file:data/accounts.db?mode=ro',uri=True).cursor()
for i,(d,) in enumerate(c.execute('select data from accounts')):
    v=json.loads(d).get('enabled'); print(i, type(v).__name__, repr(v))"
# 期望：1 行 'str' 'True'，5 行 'bool' True
```

---

## 6. 状态语义风险

### CR-25 🟠 `is_cooling()` ≠ `status == cooling`

**`[黑盒]` 证据**（`test_gateway_risk_cooldown.py`）：
- §204-211：`cooling` + 未到期 → `is_cooling() True`；`cooling_until` 过期 → `is_cooling() False`。
- §56：**`disabled` 账号 `is_cooling() False`**（注释：「封禁不是冷却，不计恢复倒计时」）。
- §243 / §264-269：验证码池门控用 `a.status != DISABLED and not a.is_cooling()`。

**后果**：Go 若用 `status == cooling` 替代 `is_cooling()` →
- 已到期的 cooling 账号仍被排除（多排一个可用账号）；
- 验证码池门控误开（对 `disabled` 判断错）。

**处置**：实现**独立** `IsCooling()`（判 `cooling_until > now`）与 `IsSelectable()`。

### CR-30 🟠 429 语义：架构文档状态图过时

**冲突**：
- `[docs]` `zcode2api-architecture.md` §5 状态机：`active --> cooling: 429 限流 / 连接失败`。
- `[黑盒]` `test_gateway_risk_cooldown.py` §3-4：**「429 频控：账号不冷却，原地等待重试」**；
  §112-116 断言 429 耗尽后 `status == ACTIVE`、`cooling_until is None`、`risk_strikes == 0`。

🔴 **按 ADR-001 优先级 3（黑盒测试）> 4（文档）**：**429 不导致 `cooling`**。

**后果**：Go 若照架构文档实现 → 429 时误冷却账号 → 与老服务行为差异 + 可用账号池缩小。

---

## 7. 配置与部署风险

### CR-26 🟠 生产进程环境**无任何 `ZCODE_*`** → `.env` 由应用自读

**实测**（`/proc/505437/environ`，**只读键名**）：
```
HOME, INVOCATION_ID, LANG, LANGUAGE, LOGNAME, NO_PROXY, PYTHONUNBUFFERED,
SHELL, SYSTEMD_EXEC_PID, USER, no_proxy
```
🔴 **没有 `ZCODE_PORT` / `ZCODE_HOST` / `ZCODE_DATA_DIR` / `ZCODE_ADMIN_KEY` / `ZCODE_NODE_PATH`**。

**结论**：`systemd` unit **无 `EnvironmentFile=`**，`.env` 由**应用自身加载**。
（生产确实监听 9120 → 应用读了 `.env`。）

**后果**：Go 版必须**自己实现 `.env` 解析**；若指望 systemd 注入 → 端口/路径全部走默认值 → 起错端口。

**验证判据**：
```bash
PID=$(systemctl show -p MainPID --value zcode2api)
tr '\0' '\n' < /proc/$PID/environ | cut -d= -f1 | grep '^ZCODE_' ; echo "exit=$? (期望无输出/非0)"
```

### CR-23 🔴 `.env` 行尾 `\r` 未剥离 → 鉴权静默 401

**实测**：会话初期该文件为 **CRLF**；直接 `KEY=$(grep ... .env | cut -d= -f2-)` 得到的值**带尾随 `\r`**
→ 所有管理接口返回 **401 `{"detail":"鉴权凭证无效"}`**；剥离 `\r` 后 → **200**。
（该文件随后被规范化为 LF，mtime `2026-10-15 19:22`；当前 `CRLF: False, bare-CR: 0`。）

**后果**：Go 的 `.env` 解析器若不剥离 `\r` → 任何 CRLF 部署（含 Windows 编辑、`scp` 传输）
都会**静默**导致鉴权失败，且**报错文本与「密钥错」完全一样**（极难排查）。

**处置**：Go 解析 `.env` 时**无条件 `TrimRight(line, "\r\n")`**。

**验证判据**：
```bash
python3 -c "print('CRLF:', b'\r\n' in open('.env','rb').read())"   # 当前期望 False；解析器仍须剥离
```

### CR-17 🟠 `ZCODE_PORT` 默认 3000，生产 9120，且 3000 被占用

**实测**：`.env.example` 默认 `3000`；`.env` 实测 `9120`；`systemd` unit 注释写 `@127.0.0.1:9120`；
`ExecStart` **无 `--port`** → 端口全由 env 决定。

**后果**：Go 默认必须是 `3000`（契约一致）；但**联调验证必须显式用 9120**，
否则撞上本机已占用的 3000。

### CR-19 🟠 相对路径必须按 `WorkingDirectory` 解析

**实测**：`ZCODE_DATA_DIR=data`（相对），unit 的
`WorkingDirectory=/opt/zcode2api` → 实际库路径
`.../external/zcode2api/data/accounts.db`。

**后果**：Go 若以**二进制所在目录**解析 → systemd 切换后读**另一个库**（可能是空库）。

### CR-22 🔴 `NO_PROXY` 是上游直连的生命线

**实测**（unit 原文）：
```
Environment=NO_PROXY=localhost,127.0.0.1,::1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,zcode.z.ai,api.z.ai,open.bigmodel.cn
Environment=no_proxy=<同上>
Environment=PYTHONUNBUFFERED=1
```
进程环境实测**确实含** `NO_PROXY` / `no_proxy`（CR-26 输出中可见）。

**后果**：宿主有透明代理（mihomo）。Go 版切换 systemd 时若**删掉/漏抄**这两个变量，
上游请求可能走代理 → 风控/地区拦截（历史上曾致 403/地区错误）。

**处置**：新 unit **必须原样保留** `NO_PROXY` / `no_proxy`（Go 标准库自动遵守）。
`PYTHONUNBUFFERED=1` 可移除（Go 无对应需求）。

**验证判据**：
```bash
systemctl cat zcode2api | grep -i 'no_proxy'
# 新 unit 部署后：systemctl show -p Environment <新unit> | grep -i no_proxy
```

### CR-20 🟠 `ZCODE_NODE_PATH` 绝对路径 + `CAPTCHA_POOL_MIN=3`

**实测**：`.env` 的 `ZCODE_NODE_PATH=/usr/local/bin/node`（19 字符，**绝对路径**）；
`CAPTCHA_POOL_MIN=3`（**开启**预解池）；`GET /admin/api/claim/captcha-config` → **200**
`{"enabled":true,"scene_id":"11xygtvd","region":"cn","prefix":"no8xfe"}`。

**后果**：
- 若 Go 版**不实现** Node 验证码求解器，则 `claim`（抢包）链路**不可用** —— 但 `claim` 是否属
  「33 端点契约」范围需 captain 确认（**本清单标为能力缺口**）。
- 无论是否实现，**必须保留该变量的读取与默认值**，且路径无效时**不得崩溃**。

### CR-21 🟡 `ZCODE_CAPTCHA_RETRIES` 4 vs `MAX_CAPTCHA_RETRIES` 3

**实测/文档冲突**：`.env.example` 的 `ZCODE_CAPTCHA_RETRIES=4`；
`zcode2api-architecture.md` §162 的代码常量 `MAX_CAPTCHA_RETRIES = 3`。
**是否同一参数未实测**（`unverified`）。

### CR-28 🟢 静态资源 `?v=2.5.1` vs `/meta` 的 `2.5.3`

**实测**：页面引用 `/static/css/app.css?v=2.5.1` 等；`GET /meta` 返回 **`2.5.3`**；
`/openapi.json` 的 `info.version` = **`2.5.3`**；`info.title` = **`zcode-hub`**。
另有 `data/release_version.json` = **`{"version":"3.15.3"}`**（**上游客户端版本**，第三个版本号）。

**后果**：三个版本号并存（`2.5.1` 资源指纹 / `2.5.3` 服务版本 / `3.15.3` 上游版本）。
Go 版若统一为一个 → `/meta` 对拍差异。

**验证判据**：
```bash
curl -s http://127.0.0.1:9120/meta; echo
curl -s http://127.0.0.1:9120/openapi.json | python3 -c "import json,sys;i=json.load(sys.stdin)['info'];print(i)"
```

---

## 8. 对拍器必须实现的豁免清单（汇总）

| # | 字段路径 | 老服务 | 新服务 | 理由 | 批准人 |
|:--:|---------|--------|--------|------|--------|
| X-1 | `accounts[].enabled`（`laohao-bigmodel-2420b3c2`） | `"True"` (string) | `true` (bool) | ADR-002 规范化裁定 | 用户 |
| X-2 | 响应头 `server` | `uvicorn` | `Go-http-server`（或空） | 实现语言差异，不应伪装 | captain |
| X-3 | `GET /admin/api/accounts` 中 `stats.calls`/`stats.fail` | 随真实请求漂移 | 随真实请求漂移 | 计数器，非契约 | captain |

> 🔴 **豁免必须显式列出**，不得静默归一化（ADR-002 §52）。

---

## 9. 未实测并显式标注的项

- **网关鉴权的 401/403 具体语义** —— 生产 `gateway_key` 为空，只能从 `[黑盒]` 取（`unverified` 于生产）。
- `/v1/messages` 与 `/v1/chat/completions` 的**成功路径**（为避免消耗真实额度，**未探测**）。
- `exhausted` 状态的**触发与恢复细节** —— 仅 `[docs]`，`unverified`。
- `MAX_ACCOUNT_ATTEMPTS` / `MAX_CAPTCHA_RETRIES` 的真实值（`[docs]` 且互相冲突）。
- round-robin 游标**是否持久化**（DB 无对应存储，推定仅内存）。
- `schema_version=4` 的**迁移历史**（需新建库才能观察，会破坏生产状态，未做）。
- 上游协议细节（`X-Aliyun-Captcha-Verify-Param` 等头的真实取值）—— 属上游协议层，另见 `docs/development/05-upstream-protocols.md`。

---

## 10. 新增风险（t1 实测补充）

### CR-31 🟠 老服务**内存持有账号状态**，DB 外部改动不即时可见

**实测**（t1 本次新增，自建探针账号 `probe-dirty-ae9c2738`，假凭证，事后删除、零残留）：

| 步骤 | 操作 | DB 内 `data.enabled` | 老服务 API 输出 |
|:--:|------|:---:|:---:|
| 1 | 建探针号 | `true` (bool) | `true` (bool) |
| 2 | **用 sqlite3 直接**把 DB 该键改成 `"True"` (string) | `"True"` (str) | — |
| 3 | 立刻 `GET /admin/api/accounts`（**不重启服务**） | `"True"` (str) | 🔴 **`true` (bool)** |
| 4 | `POST /enabled {false}` | `false` (bool) | `false` (bool) |
| 5 | `POST /enabled {true}` | `true` (bool) | `true` (bool) |

**两条硬结论**：

1. 🔴 **老服务不重读 DB**：第 3 步 DB 已是 `"True"`，服务输出仍为 `true` ——
   证明老服务在**内存中持有账号对象**，API 输出取自内存，**不是**每次请求查库。
   （反证：若每次读库并规范化，`laohao-bigmodel-2420b3c2` 就不会输出未规范化的 `"True"`。）
2. 🔴 **脏值会被 toggle 洗掉**：一次 toggle 后 DB 内该键由 `"True"`(str) 变为 `bool`
   —— **脏值不永久驻留**，驻留至该账号被 toggle 一次为止。

**后果**：

- 🔴 **对拍必须控制 toggle 面**：CR-03 的豁免条目（`accounts[].enabled: "True"→true`）
  **只在「对拍期间不对 `laohao-bigmodel-2420b3c2` 执行 B-07」时成立**。
  一旦 toggle 该账号，老服务侧即变 `true`(bool)，差异**消失**（**前提消失，不是通过**）。
- 🟠 **Go 版行为等价性**：Go 若实现为「每次请求读库」，则外部直接改库后**立刻**可见 ——
  与老服务（需重启或 toggle 才反映）**行为不同**。
  该差异**仅在外部直接改库时**可观测（正常运维不这么做），
  故标 🟠 而非 🔴；但**若 Go 版实现了热重载/每次查库，需在对拍说明中标注**。
- 🟠 **切换期双进程共存**：老服务内存态与新服务库态可能**短暂不一致** ——
  切换脚本必须**先停老服务**，避免用「老服务内存里的旧值」当验收基线。

**处置**：对拍用例（Liche）须：① 对 `laohao-bigmodel-2420b3c2` **只读不写**；
② 所有 toggle 类用例**一律用假探针账号**（测后清理，判据见下）；
③ 记录基线时**显式标注**该值取自服务内存而非库。

**验证判据（探针账号法，生产零污染）**：
```bash
# 1) 建假账号（假凭证 → 自动调度领取会失败，无害）
# 2) sqlite3 直接把 data.enabled 改成 "True"
# 3) 立刻 GET /admin/api/accounts —— 若输出 bool true（而非 "True"）即证明内存持有
# 4) toggle 一次 → 只读核对 DB 内该键变 bool
# 5) DELETE 清理 → 核对 账号数=6、probe 残留=0、integrity_check=ok
```
**t1 实测清理结果**：账号数 `6`、probe 残留 `0`、`PRAGMA integrity_check` = `ok`。
