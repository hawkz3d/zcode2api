# contract-audit.md — 契约对抗性核验报告（t8）

- 核验人：Liche（审查者）· 2026-10-15 21:16 完成
- 对象：`docs/contract/` 冻结契约（endpoints.md 33 端点 / compat-risks.md CR-01/02/32/33 / account-model.md / env-contract.md）
- 方法：**只做一件事——用对 9120 的实测证伪契约**。不复述契约原文当作结论；每条判定都指向本会话真实执行的命令与输出。
- 纪律：全程规格先于实现（未纳入本轮观测 `app/**`、`cli.py`、`captcha_node/`、`frontend/`）；密钥只内存引用，本报告及中间产物 0 明文（自检见 §4）；上游真实调用 ≤4 次且全为参数层拒绝（零 token 消耗）。

---

## 1. 核验结论摘要

| 核验 | 结论 | 一句话 |
|------|:---:|--------|
| ① 端点覆盖完整性（openapi 枚举 + 漂移锚 + 附录清单 + C/D 组逐页） | ✅ **契约正确** | 路由面 (method×path) 双向零差异：无漏记、无多记；4 条漂移锚全部复测为 404；附录"确认不存在"抽验 14 条全部成立；无新发现漂移。§0 的"22 = openapi 路由数"存在口径措辞歧义（P2，低）。 |
| ② 字段级核对（4 高风险端点逐字段/逐键序） | ✅ **契约正确** | B-02 顶层序 + 21 键序 + enabled 漂移（5 bool + laohao `str:"True"`）+ 全部子结构、B-05 键值、B-17 三键+32 长+空 gw、A-01 两模型+created_at 常量——与契约逐字一致；流量字段字节数随时间漂移属快照特性（P4，低）。 |
| ③ 错误形态 A/B/C 三套分组 + CR-32 双端点相反 + CR-33 翻译 | ⚠️ **契约主体正确，一处范围未闭合** | 形态 A 8 项、形态 B 7 项（含 CR-32 三种非对象体 messages **500/text-plain/恰 21 字节** 与 chat **400** 相反语义）全部实测复现；但 CR-33"chat 把上游业务错误翻译成 502"**仅在"上游 HTTP 200 + 体内嵌错误码"（1005 类）场景成立**——上游 HTTP-4xx 错误（3006）在 chat 端点是 **400 原样透传（形态 C），不翻译**（P1，中）。 |

**总判定**：契约质量高（43 项断言复测仅 1 处范围性偏差 + 3 处措辞/口径建议），可作为 t6/对拍实现依据；P1 必须在 t6 开工前修正表述，否则 Go 侧可能"过度翻译"。

---

## 2. 发现的问题（按严重度排序）

### P1 🟠 medium — CR-33"上游业务错误→502 翻译"的范围未闭合

- **契约原文位置**：`compat-risks.md` CR-33 标题与正文（"chat 把上游业务错误**翻译成 502**，messages 则 200 原样透传"）；`endpoints.md` §7 A-03 行"502（上游业务错误翻译）"。
- **实测证据**（本审计，2026-10-15 21:14）：
  ```
  POST /v1/chat/completions {"model":"no-such-model-t8", messages:[hi]}
  → HTTP 400, content-type: application/json, 84B
  → {"code":3006,"msg":"model not allowed","logid":"20261015…<logid>"}
  POST /v1/messages {"model":"no-such-model-t8","max_tokens":8,...}
  → HTTP 400, 84B，同体（code:3006 透传）
  ```
  即：**同一上游 4xx 业务错误（3006），chat 并未翻译成形态 B/502，而是与 messages 完全一致地原样透传（形态 C，含状态码 400）**。
- **差异描述**：契约两行的字面组合（"上游业务错误 → 502 翻译"）会被实现者读成**所有**上游业务错误都翻译。实际存在**两类上游错误**，行为不同：
  1. 上游 **HTTP ≠ 2xx**（如 3006 model not allowed、3001 parameter error）→ **两端点一致原样透传**（状态码+体，形态 C）；
  2. 上游 **HTTP 200 + 体内嵌业务错误码**（契约实测的 1005 exceed quota）→ 才分叉：messages 200 原样透传 / chat 翻译成 502 形态 B。
- **后果**：Go 若按"chat 对所有上游错误翻译"实现 → 3006/3001 场景对拍必挂（502+形态B vs 实测 400+形态C）。
- **建议修正**：CR-33 增补判定行——"翻译仅适用于【上游 HTTP 200 且 body 携带业务错误码】；上游 HTTP ≠ 2xx 的错误两端点均原样透传（含状态码）"。并将该边界从 `[docs 推定]` 升格为 `[实测]`（本审计 3006 双端点证据可直接引用）。同时建议 `endpoints.md` §7 A-03 行的"502（上游业务错误翻译）"改为"502（仅 200-内嵌错误型上游错误；4xx 上游错误透传——见 CR-33）"。

### P2 🟡 low — endpoints.md §0"22 与 openapi 路由数精确吻合"口径歧义

- **契约原文位置**：`endpoints.md` §0 计数口径表下"`/admin/api/*` 实测恰为 22 条，与任务书一致 —— 这是强证据"。
- **实测证据**：`GET /openapi.json` → 22 个 **paths** 键，其中 `/admin/api/*` 仅 **19** 个 paths；这 19 条 paths 展开 method 后恰为 **22 个 operation**（如 `/admin/api/accounts` 一条 path 贡献 GET/POST/DELETE 三条）。
- **差异描述**：22 这个数字本身正确（operation 口径），但"openapi 路由数"通常被读作 paths 数（19）。若 t6 实现者按"paths=22"去找 22 个 admin 路径会徒劳。
- **建议修正**：§0 补一句"（口径=openapi `paths×methods` 操作数；paths 键数为 19）"。

### P3 🟡 low — §7"缺 messages → 200 透传"是条件快照，未标注状态依赖

- **契约原文位置**：`endpoints.md` §7 A-02 行"200（缺 messages 透传）"；CR-02 形态 C 例"缺 messages → **200** … `code:1005 exceed quota limit`"。
- **分析**：1005 是**当时的账户额度状态**；同族 4xx 上游错误（3001/3006）实测走 HTTP 400 透传（本审计 `C-msg-emptyobj` → 400/3001）。"缺 messages"这一输入不决定 200——**上游当时怎么报错才决定**。契约把快照个例写成了无条件的端点行为。
- **本审计未复测该 case 的原因**：需真实上游调用且结果依赖账户实时状态（可能 200 真补全、消耗额度、不可复现），超出核验预算。
- **建议修正**：该行标注"（上游 HTTP 200-内嵌错误场景的快照，码值随账户状态；非'缺 messages 恒 200'）"，与 P1 的两类划分互为呼应。

### P4 🟡 low — 含流量字段端点的字节数声称应标"快照"

- **实测证据**：A-01 224B ✅ 精确复现、B-05 92B ✅、B-17 94B ✅、C-03 1897B ✅、`/meta` 19B ✅、`/openapi.json` 19913B ✅、`/static/css/app.css` 23788B ✅ —— 静态/低频端点全部逐字节复现；唯 **B-02 声称 14424B、实测 15795B**（`use_count`/`recent_results`/`stats.calls` 随真实流量增长），21 键结构零漂移。
- **建议**：契约给含流量字段端点的字节数加"（快照值，非复现判据）"标注；对拍 L3 已由归一化规则处理（见 `scripts/conformance/normalize.rules.json`），不需要契约改动之外的动作。

---

## 3. 验证过且契约正确的项（怎么验的）

### 3.1 端点覆盖（核验 1）

| 项 | 契约声称 | 实测 | 判定 |
|---|---|---|---|
| A 组路由存在性 | 3 条 | openapi 恰含 3 条 `/v1/*`，(method,path) 与契约**双向零差**（含参数名归一 `{account_id}`→`{}`） | ✅ |
| B 组 22 条 | 22 | openapi admin operations 展开 = 22，与契约 22 条一一对应，**无漏记无多记** | ✅ |
| C 组 6 页面 | 307/307/200×4 | `/`→307 `Location: http://127.0.0.1:9120/admin`（绝对 URL ✅）；`/admin`→307 `/admin/login` ✅；4 页面 200 text/html ✅；`/admin/login` 1897B ✅ 且 `cache-control: no-store` ✅ | ✅ |
| D 组 | /meta、/openapi.json | `/meta` → 19B `{"version":"2.5.3"}` ✅；openapi 19913B ✅、`info.title=zcode-hub` ✅ | ✅ |
| 漂移锚 ×4 | 实测 404 形态 A | `/health` `/admin/api/pool` `/admin/api/quota` `/admin/api/claim/history` 全部 404 `{"detail":"Not Found"}`（22B） | ✅ 全部属实 |
| 附录"确认不存在"抽 10 | 404 | `/healthz` `/v1/responses` `/v1/messages/count_tokens` `/v1/embeddings` `/admin/api/bundle/export` `/admin/api/reqlog` `/admin/api/version` `/admin/api/monitoring/stream` `/login` `/favicon.ico` 全 404 | ✅ |
| `bundle/export` POST 变体 | 不存在 | POST 亦 404 | ✅ |
| 内置路由 | docs/redoc 200、css 23788B、static 404、`/static` 307 | 全复现（css 字节数精确） | ✅ |
| 405+allow | `POST /v1/models→405 allow: GET` | 405 + `allow: GET` + 形态 A 31B ✅（加测 `DELETE /admin/api/settings` 同样 405/`allow: GET`） | ✅ |
| 大小写敏感 | `/V1/models→404` | 404 ✅ | ✅ |
| 尾斜杠 307 | §1.5 | `/admin/api/accounts/` → 307 + Location 绝对 URL ✅ | ✅ |

### 3.2 字段级（核验 2）——键序用 `object_pairs_hook` 原文解析，非 sorted

| 端点 | 断言 | 实测 |
|---|---|---|
| B-02 | 顶层序 `accounts,stats,providers,ts` | ✅ 逐字一致 |
| B-02 | AccountView 21 键序（id…fingerprint） | ✅ 21 键序逐字一致；`usage` 不出现 ✅ |
| B-02 | enabled 5 bool + laohao `str:"True"` | ✅ 逐 id 核对（见 raw JSON） |
| B-02 | fingerprint 7 键序 / quota item 8 键序 / recent_results 3 键序 / stats 8 键 | ✅ 全逐字一致 |
| B-05 | 键序 + `zai:4/bigmodel:2` + 92B | ✅（池恰好仍是 6 账号——队友探针号已清理） |
| B-17 | 键序 + admin_key **len 32**（值已脱敏）+ gw 空串 + 600 int + 94B | ✅ |
| A-01 | 224B、2 模型、4 字段序、created_at 常量 | ✅ |

### 3.3 错误形态（核验 3）

| 组 | 复现数 | 结果 |
|---|---|---|
| 形态 A | 8 项（401×3 含 claim 无 key、404×2、405、空 tokens 400 含空格原文 `"请输入至少一个 Token / API Key"`、账号不存在 404） | ✅ 全部逐字命中（含 detail 中文原文） |
| 形态 B | 7 项：messages badjson/空body → `invalid_request`（无后缀）+ 键序 `message` 前 `type` 后（75B）；chat badjson → `invalid_request_error`（81B）；**CR-32 三种非对象体 `[]`/`"str"`/`null` 在 messages 全 500 text/plain 恰 21B**；chat `[]` → 400 "请求体必须是 JSON 对象"；chat 缺 model → 400 "必须提供 model 参数" | ✅ 全中——**CR-32"同输入双端点相反"完全属实且稳定** |
| 形态 C | 3006（messages 400 透传 `{code,msg,logid}`）✅；3001（空对象 400 透传）✅；chat-3006 → 400 透传（→ **P1**）；1005→200/502 场景**本次无法受控复现**（依赖账户额度实时状态），维持契约 `[实测]` 记录 | ✅ 主体属实，边界见 P1 |

**未验证项（诚实列出）**：B-07 写路径形态（会改生产账号状态，t5/t6 已专项）；`缺少 account_id` 400（可能先触发验证码求解，存在真实计费风险，刻意不测）；1005/502 实时复现；SSE 流内错误帧（CR-33 第二段，需真流式消耗）；A-02 完整成功响应形态（t6 对拍职责）。

---

## 4. 可复现核验命令

中间产物：`/tmp/t8-raw-results.json`（43 checks，已脱敏）。核心复跑（无需脚本，只读为主，含 3 次参数层上游调用）：

```bash
B=http://127.0.0.1:9120
# ── 核验 1：路由枚举与 diff ──
curl -s $B/openapi.json | python3 -c "import json,sys; oa=json.load(sys.stdin)['paths']; print(len(oa), sorted(oa))"
#   期望: 22 paths；19 个 /admin/api/*；(method,path) 与 endpoints.md §2/§3 双向零差
for p in /health /admin/api/pool /admin/api/quota /admin/api/claim/history; do
  curl -s -o /dev/null -w "%{http_code} $p\n" $B$p; done        # 期望全 404
curl -s -o /dev/null -D - -X POST $B/v1/models --data '{}' | grep -iE "http|^allow"   # 405 + allow: GET
# ── 核验 2：键序（必须 object_pairs_hook，勿 sorted）──
K=$(grep -m1 '^ZCODE_ADMIN_KEY=' <OLD_REPO>/.env | cut -d= -f2-)   # 只进 shell 变量，勿 echo
curl -s $B/admin/api/accounts -H "Authorization: Bearer $K" \
 | python3 -c "import json,sys; d=json.load(sys.stdin,object_pairs_hook=list); \
print([k for k,_ in d]); a=dict(d)['accounts'][0]; print(list(a)); \
print({x['id']:(type(x['enabled']).__name__,x['enabled']) for x in dict(d)['accounts']})"
# ── 核验 3：CR-32 相反语义 + CR-33 边界（本审计 P1）──
curl -s -o /dev/null -w "messages[]: %{http_code} %{content_type} %{size_download}B\n" \
  -X POST $B/v1/messages -H 'content-type: application/json' --data '[]'      # 500 text/plain 21
curl -s -X POST $B/v1/chat/completions -H 'content-type: application/json' --data '[]'; echo  # 400 形态B
curl -s -X POST $B/v1/messages -H 'content-type: application/json' \
  -d '{"model":"no-such-model-t8","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}'
curl -s -X POST $B/v1/chat/completions -H 'content-type: application/json' \
  -d '{"model":"no-such-model-t8","messages":[{"role":"user","content":"hi"}]}'
#   P1 判据: 两者都应是 400 {"code":3006,...} 透传；若哪天 chat 变 502 说明上游/网关行为迁移，需更新 CR-33
# ── 对拍器整体回归（verify ③）──
cd <NEW_REPO> && python3 scripts/conformance/harness.py --baseline=$B --candidate=$B   # 期望 28/28 pass
```

**密钥零泄漏自查**（本报告 + 中间产物）：
```bash
grep -rniE '(eyJ[A-Za-z0-9_-]{20,}|[a-f0-9]{32}\.[A-Za-z0-9]{16,})' docs/contract/contract-audit.md || echo NO_LEAK_OK
python3 - <<'EOF'   # env 值精确反查（含旧值口径见记忆 lesson）
import pathlib,re
vs=[]
for l in pathlib.Path("/opt/zcode2api/.env").read_text().splitlines():
    if "=" in l and not l.strip().startswith("#"):
        v=l.split("=",1)[1].strip().strip('"').strip("'")
        if len(v)>=6: vs.append(v)
t=pathlib.Path("docs/contract/contract-audit.md").read_text()+pathlib.Path("/tmp/t8-raw-results.json").read_text()
print("env 值命中:", sum(v in t for v in vs))
EOF
```
（实测两次均 0 命中；settings 响应在驱动脚本中即按"只留长度不留体"处理。）

---

## 5. 对拍框架回灌（本任务附带，已生效）

`scripts/conformance/cases.json` 新增 3 个 safe 锚（零上游、把本审计证伪过的分支固化进对拍矩阵）：

| case | 锚定 | 实测 |
|---|---|---|
| `GW-messages-nonobj` | CR-32：非对象体 → **500 text/plain 21B**（candidate"好心修成 400"即挂） | L1 PASS / L3 PASS |
| `GW-chat-nonobj` | CR-32 对照：同体 chat → 400 形态 B 键序 message→type | 全绿 |
| `GW-messages-badjson` | CR-01/02：`invalid_request` 无 `_error` 后缀 + 键序 | 全绿 |

回灌后 harness 自测：**28 case 全绿（L1 28/L2 23 适用/L3 28），sentinel 32 命中全指纹化，flaky 0**。
