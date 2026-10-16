# 实现者速查卡（写代码前必读）

> 契约正文在 `endpoints.md` / `sqlite-schema.md` / `account-model.md` / `env-contract.md`（合计 3000+ 行）。
> 本卡只列**最容易实现错、且错了对拍必挂**的点。**遇到疑问仍以正文为准。**

---

## 🔴 一、绝不能搞错的 9 条

### 1. 三套错误形态，不得统一
- `messages` 用 `"type":"invalid_request"`
- `chat/completions` 用 `"type":"invalid_request_error"` ← **多了 `_error`**
- 管理 API 用 FastAPI 默认 `{"detail":"..."}`
- 用同一个 ErrorResponse struct 会导致**三处全错**（CR-02）

### 2. `/v1/models` 无鉴权返回 **200**，不是 401
实测：无凭证 200、错误 key 也 200（`meta.gateway_key` 为空 → 放行）。
黑盒测试另断言：key 已配置时无凭证 401 / 错凭证 403（形态 B，未实测内容）。

### 3. 🔴 两端点对**同一个上游错误**的处理**完全相反** —— 这是最容易全错的一条

| 场景 | `/v1/messages`（A-02） | `/v1/chat/completions`（A-03） |
|------|------|------|
| 缺 `messages` | **200** 转发上游 + 上游原文 `{"code":1005,...}` | **400** 本地拒绝 `{"error":{"message":"必须提供 messages 数组","type":"invalid_request_error"}}` |
| 请求体 `[]`（合法非对象） | 🔴 **500** + `text/plain` + **21 字节** `Internal Server Error` | **400** + `{"error":{"message":"请求体必须是 JSON 对象","type":"invalid_request_error"}}` |
| 上游业务错误 `code=1005` | **200** 原样透传 `{"code":1005,"msg":"exceed quota limit","logid":...}` | 🔴 **502** + `{"error":{"message":"exceed quota limit","type":"upstream_error","code":1005}}` |
| 本地请求体结构校验 | **完全没有**（只校验 JSON 合法性） | **有完整三档**（非对象 / 缺 model / 缺 messages） |

**三条禁令**：
1. **禁止**给 `/v1/messages` 加请求体结构校验（缺 `messages` 必须转发，不是 400）。
2. **禁止**把 `/v1/messages` 的「合法非对象 → 500」"修"成优雅 400 —— **必须照抄崩溃行为**。
3. **禁止**两端点共用一条「上游错误 → 响应」路径 —— 一端 200 透传，一端 502 翻译。

（CR-32 / CR-33；错误路径已全部实测，命令见 `endpoints.md` A-02/A-03 分档表）

### 4. `AccountData` ≠ `AccountView` —— 两个不同的结构
- 存储（23 键）用 `AccountData`
- API 输出（21 键）用 `AccountView`：`token_masked` 替代 `jwt_token`+`api_key`，且**省略 `usage`**
- 复用同一 struct → 两处键序至少一处错（CR-14）

### 5. `data` JSON 序列化必须**有序**
Go map 迭代序随机 → 逐字节 diff 不稳定（CR-12）。
已有实现：`models.Serialize()`（verbatim 拼接）。

### 6. 重定向用 **307**（非 302），`Location` 为**绝对 URL**
`/` → 307 → `/admin`；`/admin` → 307 → `/admin/login`（**无条件**，服务端不知登录态）
三条网关尾斜杠同样 **307 + 绝对 URL**（`/v1/messages/`、`/v1/chat/completions/`、`/v1/models/`）。

### 7. `.env` 解析必须**无条件剥离 `\r`**
CRLF 部署会静默鉴权失败，且报错文本与"密钥错"一模一样（CR-23）

### 8. 保留 `NO_PROXY`/`no_proxy`
宿主有透明代理，缺了会被劫持上游 → 风控（CR-22）

### 9. 🔴 三个 error writer 的**同类型参数互换编译器抓不住**
`writeShapeB(w, status int, typ, message string)` —— 若调用处写成
`writeShapeB(w, 401, "缺少鉴权凭证", "authentication_error")`（两个 string 对调），
**编译通过**但输出变成 `{"error":{"type":"缺少鉴权凭证","message":"authentication_error"}}`，语义完全相反。
（t6 实测已发生此缺陷，CR-34）
**必须**：① 参数名自解释 + 调用处具名/加注释；② 测试**逐字断言 body 的 `type` 与 `message` 两个字段**（只断言状态码抓不到）。

---

## 🟡 二、容易漏的细节

| 项 | 事实 |
|---|---|
| `created_at`（模型） | 恒为 `"2025-01-01T00:00:00Z"` 占位常量，非真实时间 |
| `/admin/api/export` | **不是** `/admin/api/accounts/export`（后者实测 405） |
| 模型清单 | 实测只有 **2 个**（`GLM-5.3-Flash`、`GLM-5.3`），文档写 11 个是错的 |
| `max_tokens` | 钳制到 **131072**（含字符串形态 `"999999"`） |
| `system` 映射 | `chat/completions` 的 system 消息 → Anthropic `system` 数组的**末位**元素 |
| 文档-实现漂移 | `/health`、`/admin/api/pool`、`/admin/api/quota`、`/admin/api/claim/history` 实测 **404** —— **candidate 不得"好心"补实现** |
| `openapi.json` | `info.title` 是 `zcode-hub`（不是 `zcode2api`），`paths` 25 条 |
| 静态资源版本 | `?v=2.5.1`，而 `/meta` 返回 `2.5.3` —— 真实不一致，**照抄** |
| 相对路径基准 | 以 **`WorkingDirectory`** 为基准（CR-19），非二进制所在目录 |
| `enabled` 字段 | 读 **data JSON** 的值（data 优先、列兜底）；写必须**双写**（ADR-003） |
| `recent_results` 方向 | 🔴 **后插：新结果 append 到尾部，`[-1]` 最新、`[0]` 最旧**（CR-16；**t1 原写"前插"是错的**，t5 已更正）。**对拍归一化把该字段整体折叠 → 方向错误对拍抓不到**，必须单侧断言 |
| 生产脏值 | `laohao-bigmodel-2420b3c2` 的 `data.enabled` 是字符串 `"True"` → **规范化输出 bool**（ADR-002），对拍走豁免 |
| `accounts` 列表顺序 | 🔴 **`(provider 分组序, 组内 created_at 升序)`** —— **不是 rowid、不是 id**（CR-35）。Go 侧 `ORDER BY rowid` **错**，须 `ORDER BY provider, created_at`。**对拍 `sort_by:id` 把数组重排 → 顺序差异对拍看不见**，必须单侧断言 |
| `recent_results` 上限 | **20**（实测 6/6 账号 n 全部恰好 20）；超出丢**最旧**（截头） |
| `monitoring.entries` 顺序 | 🔴 **严格降序（新在前）**，`keep=500`（CR-37）。UI 首行=最新请求。**对拍 `$.entries` 用 `replace_typed` 折叠 → 方向错误对拍看不见**，必须单侧断言 |

---

## 🟢 三、验收判据（你的 verify 必须真跑并贴原始输出）

```bash
cd /opt/zhipucode2api
/usr/local/bin/go build ./... && echo BUILD_OK
/usr/local/bin/go vet ./...   && echo VET_OK
/usr/local/bin/go test ./... 2>&1 | tail -20
! grep -rniE 'eyJ[A-Za-z0-9_-]{20,}' --include='*.go' . && echo NO_LEAK_OK
```

**禁止只写"全绿"** —— 必须贴命令的**原始输出**。之前发生过"自述全绿但实际编译失败"。

---

## 🔵 四、规格先于实现边界

- ❌ 禁止读 `zcode2api/app/**/*.py`、`cli.py`、`captcha_node/`、`frontend/`
- ✅ 允许：`zcode2api/docs/**`、`tests/integration/**`、对 9120 的 HTTP 实测、SQLite 只读探测
- 🔐 密钥在 `zcode2api/.env`：只可 shell 变量原地引用，**严禁回显**，严禁写入任何交付物

---

## ⚪ 五、起服务自测（提交前）

```bash
# 用非 9120 端口避免抢生产
cd /opt/zhipucode2api
ZCODE_PORT=19120 ZCODE_HOST=127.0.0.1 ./bin/zhipucode2api serve &
./scripts/smoke.sh 19120    # 8 项 curl 断言
./scripts/conformance/harness.py --baseline=http://127.0.0.1:9120 --candidate=http://127.0.0.1:19120
```

冒烟脚本已用**老服务校准过**（`./scripts/smoke.sh 9120` → 9/9 全绿），可信。
