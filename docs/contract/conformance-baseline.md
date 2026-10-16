# 对拍预期基线（从 baseline 实测导出）

> 本文件由 `scripts/conformance/latest/report.json` + `allowlist.json` 导出，记录**老服务（baseline）**的真实行为。
> 新服务（candidate）对拍时，应逐 case 达到 `expect` 声明的等级。

- 导出时间：2026-10-15 21:02
- case 总数：**25**（引擎另含 4 项自测）
- baseline：`http://127.0.0.1:9120`

## 判定等级说明

| 等级 | 含义 |
|---|---|
| L1 | 状态码一致 |
| L2 | 响应结构（JSON 类型树逐位置）一致 |
| L3 | 归一化后逐值一致 |
| n/a | 该 case 不适用此级（如 HTML 页面无 JSON 结构）|

## 逐 case 预期

| # | case id | 方法 | 路径 | 期望等级 | 标签 | 说明 |
|:--:|---|---|---|---|---|---|
| 1 | `GW-models-anon` | GET | `/v1/models` | **L1+L2+L3** | safe,gateway,read | 网关匿名可达（生产 gateway_key 未配置=放行，实测 200）。静态清单含 created_at 常量。契约锁定：models 清单内容 + 键序无关性。 |
| 2 | `GW-models-nokey-gate` | GET | `/v1/models` | **L1+L2+L3** | safe,gateway,read | 与 GW-models-anon 同请求重复发：若两读不一致说明服务态不稳定，裁判先于对拍暴露它。 |
| 3 | `GW-models-wrong-key` | GET | `/v1/models` | **L1+L2+L3** | safe,gateway,negative | gateway_key 未配置时错误 key 也应放行（放行语义=不校验）。若 candidate 实现了『配置了才校验』则与此一致；不一致即契约差异。 |
| 4 | `GW-chat-invalid-model` | POST | `/v1/chat/completions` | **L1+L2+L3** | safe,gateway,negative,validation | 缺 model → 400 {error:{message,type=invalid_request_error}}。网关校验层，零上游流量。中文错误文案是契约（必须逐字一致）。 |
| 5 | `GW-chat-malformed-json` | POST | `/v1/chat/completions` | **L1+L2+L3** | safe,gateway,negative | 残缺 JSON → 老服务实测 400/422（对拍时定案）。框架默认错误形态（FastAPI detail vs Go 手写）是最易漂移点，必须锚死。 |
| 6 | `GW-method-not-allowed` | POST | `/v1/models` | **L1+L2+L3** | safe,gateway,negative | GET-only 端点打 POST → 405 形态（FastAPI {"detail":"Method Not Allowed"}）。 |
| 7 | `GW-health-actual` | GET | `/health` | **L1+L2+L3** | safe,read,drift-anchor | ⚠ 文档 04-api-spec §1.5 说存在 /health，实测 404。契约以运行事实为准：candidate 也必须 404（除非文档修实）。此 case 是『文档-实 |
| 8 | `AD-status` | GET | `/admin/api/status` | **L1+L2+L3** | safe,admin,read | providers/gateway_key_set/quota_pool 计数。quota_pool 是池规模（队友加/删账号会动它）→ 外部写风险由 FLAKY 重试处置。 |
| 9 | `AD-status-anon` | GET | `/admin/api/status` | **L1+L2+L3** | safe,admin,negative | 实测：status 匿名可达（200）。该豁免行为是契约（无论有意与否），candidate 必须复刻。 |
| 10 | `AD-accounts` | GET | `/admin/api/accounts` | **L1+L2+L3** | safe,admin,read | 对拍核心 case：全池账号（DB 持久数据）。归一化只覆盖时钟/计数/进程史（recent_results/entries 折叠、use_count 等），enabled 的 b |
| 11 | `AD-settings` | GET | `/admin/api/settings` | **L1+L2+L3** | safe,admin,read,secret-bearing | 响应含 admin_key 明文——由 secret 规则以 sha256 指纹替代后比对（等值能力保留，明文不落报告）。 |
| 12 | `AD-monitoring` | GET | `/admin/api/monitoring` | **L1+L2** | safe,admin,read,volatile | 实时请求日志（进程态）。entries 数组被 replace_typed 折叠 → L2 守卫键存在+数组类型；L3 只比 keep 计数等剩余标量（keep 可能随日志上限变化 |
| 13 | `AD-login-poll-unknown` | GET | `/admin/api/login/poll/nonexistent-flow-xyz` | **L1+L2+L3** | safe,admin,read,negative | 未知 flow_id → 200 {"status":"expired"}（契约：幂等、无 404）。零上游。 |
| 14 | `AD-pool-doc-drift` | GET | `/admin/api/pool` | **L1+L2+L3** | safe,admin,drift-anchor | 文档说有 /admin/api/pool，实测 404 {"detail":"Not Found"}。以运行事实为准锚死。 |
| 15 | `AD-quota-doc-drift` | GET | `/admin/api/quota` | **L1+L2+L3** | safe,admin,drift-anchor | 文档 §额度 说 GET /admin/api/quota，实测 404。锚死 404 形态（FastAPI 默认 detail）。 |
| 16 | `AD-claim-history-drift` | GET | `/admin/api/claim/history` | **L1+L2+L3** | safe,admin,drift-anchor | 文档说存在，实测 404。锚死。 |
| 17 | `AD-noauth-accounts` | GET | `/admin/api/accounts` | **L1+L2+L3** | safe,admin,negative | 缺凭证 → 401 {"detail":"缺少鉴权凭证"}。中文文案逐字对拍。 |
| 18 | `AD-badkey-accounts` | GET | `/admin/api/accounts` | **L1+L2+L3** | safe,admin,negative | 错凭证 → 401 {"detail":"鉴权凭证无效"}。与缺凭证文案不同型 → 也是契约。 |
| 19 | `AD-noauth-post` | POST | `/admin/api/login/start` | **L1+L2+L3** | safe,admin,negative | 写端点缺 key → 401（鉴权在业务逻辑前，零上游）。黑盒测试 test_admin_key_required 的对拍版本。 |
| 20 | `UI-monitoring-page` | GET | `/admin/monitoring` | **L1+L3** | safe,page | HTML 页（非 JSON → L2 n/a），空白折叠后字节对拍：静态资源版本号、标题『zcode-hub · 请求监控』都是契约面。 |
| 21 | `UI-login-page` | GET | `/admin/login` | **L1+L3** | safe,page | 登录页 HTML 字节对拍。 |
| 22 | `UI-root-redirect` | GET | `/` | **L1+L3** | safe,page | 307 且 Location 值参与比对（headers_compared 含 location）。禁 follow：重定向形态本身是契约。 |
| 23 | `UI-admin-redirect` | GET | `/admin` | **L1+L3** | safe,page | /admin → 307。 |
| 24 | `SEQ-readonly-smoke` | GET | `/admin/api/status` | **L1+L2+L3** | safe,sequence,read | steps 序列机制的只读验证 case（ADR-003 写序列 SEQ-toggle 的基础设施同套）：三步按序双打、逐步对拍、聚合判定。零副作用，进自测。 |
| 25 | `ERR-unknown-path` | GET | `/definitely/not/a/route` | **L1+L2+L3** | safe,negative | 全局 404 形态锚：FastAPI {"detail":"Not Found"} vs Go http.NotFound 的 text 差异是重灾区，必须逐字节。 |

## 当前 baseline 判定（自测可信基线：baseline vs baseline，25/25 全 pass）

| case id | L1 | L2 | L3 | verdict |
|---|---|---|---|---|
| `GW-models-anon` | ✅ | ✅ | ✅ | pass |
| `GW-models-nokey-gate` | ✅ | ✅ | ✅ | pass |
| `GW-models-wrong-key` | ✅ | ✅ | ✅ | pass |
| `GW-chat-invalid-model` | ✅ | ✅ | ✅ | pass |
| `GW-chat-malformed-json` | ✅ | ✅ | ✅ | pass |
| `GW-method-not-allowed` | ✅ | ✅ | ✅ | pass |
| `GW-health-actual` | ✅ | ✅ | ✅ | pass |
| `AD-status` | ✅ | ✅ | ✅ | pass |
| `AD-status-anon` | ✅ | ✅ | ✅ | pass |
| `AD-accounts` | ✅ | ✅ | ✅ | pass |
| `AD-settings` | ✅ | ✅ | ✅ | pass |
| `AD-monitoring` | ✅ | ✅ | ✅ | pass |
| `AD-login-poll-unknown` | ✅ | ✅ | ✅ | pass |
| `AD-pool-doc-drift` | ✅ | ✅ | ✅ | pass |
| `AD-quota-doc-drift` | ✅ | ✅ | ✅ | pass |
| `AD-claim-history-drift` | ✅ | ✅ | ✅ | pass |
| `AD-noauth-accounts` | ✅ | ✅ | ✅ | pass |
| `AD-badkey-accounts` | ✅ | ✅ | ✅ | pass |
| `AD-noauth-post` | ✅ | ✅ | ✅ | pass |
| `UI-monitoring-page` | ✅ | — | ✅ | pass |
| `UI-login-page` | ✅ | — | ✅ | pass |
| `UI-root-redirect` | ✅ | — | ✅ | pass |
| `UI-admin-redirect` | ✅ | — | ✅ | pass |
| `SEQ-readonly-smoke` | ✅ | ✅ | ✅ | pass |
| `ERR-unknown-path` | ✅ | ✅ | ✅ | pass |

## 豁免清单（allowlist）

**当前 1 条**（默认空清单；一切差异默认 FAIL，只有明确批准的才进清单）

### `ADR-002-enabled-laohao`

- **case**：`*`　**path**：`$.accounts[*].enabled`
- **差异形态**：baseline=`str` (值 `True`) → candidate=`bool` (值 `True`)
- **覆盖结构级差异**：True
- **批准人**：用户拍板 · captain 转达（AgentTeams t4 · 2026-10-15） · 见 docs/contract/adr-002-enabled-normalization.md
- **理由**：用户裁定 enabled 字段规范化（不复现脏值）：老服务对账号 laohao-bigmodel-2420b3c2 透传 data JSON 中的字符串 "True"（生产库脏数据；表列 enabled 实为 INTEGER 全 1），Go 版按 ADR-002 统一返回布尔 true。规范化只在读语义层、不改库 → 脏值永久驻留，故 case='*'：任意对拍 case 中出现同形态 accounts[*].enabled 漂移均命中本条；形态精确锚定（仅 baseline=str "True" 且 candidate=bool True），其他账号其他形态（如 bool→int）不匹配、照常 FAIL。端点覆盖核查（captain 2026-10-15 指令）：/admin/api/status 实测响应仅 providers/gateway_key_set/quota_pool，无 enabled；/admin/api/monitoring entries 实测 15 键（req_id/ts/endpoint/model/stream/preview/account/mode/ok/status/error/t_first/t_total/input_tokens/output_tokens），无 enabled；/admin/api/bundle/export 为 PBKDF2+AES-GCM 加密二进制（盐/IV 随机 → 本身不可字节对拍，其数据等价由 INT-021 导出→清库→导入往返覆盖）；/login/poll 与 /claim/preview 若隔离实测发现透出 data.enabled，届时按同格式补条目并独立批准，不预先扩面。审计链：docs/contract/adr-002-enabled-normalization.md + adr-003-enabled-write-semantics.md

---

## 对拍使用方法

```bash
# 起新服务（非 9120 端口，避免抢生产）
ZCODE_PORT=19120 ZCODE_HOST=127.0.0.1 ./bin/zhipucode2api serve &

# 对拍
./scripts/conformance/harness.py \
  --baseline=http://127.0.0.1:9120 \
  --candidate=http://127.0.0.1:19120

# 报告：scripts/conformance/latest/report.{json,md}
```