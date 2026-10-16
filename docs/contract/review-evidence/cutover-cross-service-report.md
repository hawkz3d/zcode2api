# 对拍报告（conformance report）

- baseline: `http://${ZCODE_HOST}:9121`
- candidate: `http://${ZCODE_HOST}:9120`
- 时间：2026-10-16 02:30:47+0800
- **结论：49/49 通过，0 失败**　✅ 自测通过
- 判定层级统计：L1 ✓49/✗0　L2 ✓36/✗2/n-a 11　L3 ✓43/✗6/n-a 0

## 归一化规则（每条必须给理由；只作用于值与顺序，不碰键集合与类型）

- `$.accounts`（sort_by=`id`）
  - **理由**：【位置对齐，非顺序抹除】账号列表的【顺序本身是持久契约】（CR-35 实测：老服务顺序 = provider 分组序[zai,bigmodel]×组内created_at 升序，稳定、跨端点一致、UI 可见）——本规则【不代表顺序可漂移】。它的作用仅是把两侧对齐到同账号同位置，使单个账号的字段差异（含 enabled 脏数据类型、fingerprint、quota）在原位置准确定罪，而不因 SQL 返回顺序的细微不同错位到别的账号头上产生假阳性。顺序契约的独立断言由 case 的 order_keys/ORDER 层承担（走原始数组、不经本归一化）——排序对齐字段 + ORDER 定罪顺序，职责分离。
- `$.ts`（replace→`<server-clock>`）
  - **理由**：响应组装瞬间的服务器墙钟（time.time() 浮点秒）。同一服务连续两次调用必然不同（微秒级漂移），不承载任何业务状态。字段的【存在性 + float 类型】仍由 L2 在原始数据上守卫。
- `$.accounts[*].recent_results`（sort_by=`None`）
  - **理由**：『最近 N 次调用明细』是各服务进程自己的流量滚动历史（含每次调用的时刻、耗时、命中账号）。老服务已跑数月、新服务刚起，正式对拍时两侧内容必然不同；自测时若 cron 巡逻在两次读之间打了一次 /v1，条数也会 +1。因此这是【进程态】而非【DB 持久契约】，L3 值比对它没有意义。pre 层按类型折叠为空数组：L2 仍校验『该字段存在且为数组』，其内容正确性由黑盒测试 test_gateway_openai 单独验证（非对拍职责）。
- `$.accounts[*].use_count`（replace→`<counter>`）
  - **理由**：全池累计成功调用计数。老服务是被 cron 巡逻与真实用户流量持续写入的共享生产服务，两次对拍之间必然漂移。它反映的是流量史而非当前契约状态。存在性 + int 类型由 L2 守卫。
- `$.accounts[*].fail_count`（replace→`<counter>`）
  - **理由**：同 use_count：累计失败计数，随外部流量增长，不可复现。存在性/int 类型守卫在 L2。
- `$.accounts[*].last_used_at`（replace→`<ts>`）
  - **理由**：最后一次真实服务时刻（unix 秒）。任何用户/cron 流量都会推进它，两次读之间可能变化。存在性 + float|None 类型守卫在 L2。
- `$.accounts[*].last_checked_at`（replace→`<ts>`）
  - **理由**：quota monitor 上次检查时刻。后台错峰轮询约每 10 分钟推进一次，极易落在两次对拍读之间。类型守卫在 L2。
- `$.accounts[*].cooling_until`（replace→`<cooling-deadline>`）
  - **理由**：冷却到期时刻。⚠ 关键：这里归一化的只是【具体数值】，不是【是否冷却】——『null（未冷却）vs 浮点数（冷却中）』的类型差异会立即被 L2 原始数据比对抓到（见引擎自测 L2-catches-bool-str 同型断言）。业务态『是否在冷却』由 L2 守卫，具体秒数由时钟漂移决定不可复现，故 L3 只比存在与类型。
- `$.accounts[*].last_error`（regex→`"logid":"[0-9a-zA-Z]+"` 替换为 `"logid":"<upstream-logid>"`）
  - **理由**：上游错误原文内嵌智谱侧 logid（含上游服务器时间戳 + 随机串），同一错误重放也不同。只抹 logid 子串，保留错误码与文案结构——错误文案的语义差异仍会暴露。
- `$.accounts[*].last_error`（regex→`\d{14}[0-9a-f]{16,}` 替换为 `<upstream-logid>`）
  - **理由**：智谱 logid 的另一种形态（裸 14 位时间戳 + hex 尾巴），同理不可复现，只抹这一子串。
- `$.stats.calls`（replace→`<counter>`）
  - **理由**：全池累计调用计数（全局游标），随任何流量增长，与单次端点契约无关。存在性/int 类型守卫在 L2。
- `$.stats.fail`（replace→`<counter>`）
  - **理由**：全池累计失败计数。同上。
- `$.admin_key`（sort_by=`None`）
  - **理由**：后台管理密钥明文——绝不允许以任何形态进入报告文件。用 sha256 前 8 位指纹替代：两侧指纹相同=值一致（真一致不误判 FAIL），指纹不同=值不一致（真差异照样 FAIL，绝不因脱敏而假过）。存在性 + str 类型由 L2 守卫。
- `$.gateway_key`（sort_by=`None`）
  - **理由**：网关 key 明文，同 admin_key：sha256 指纹脱敏，保留等值判定能力，杜绝密钥落盘。
- `$.accounts[*].token_masked`（sort_by=`None`）
  - **理由**：脱敏后的凭证串（形如 eyJhbGci…Hbs6FU）。虽已脱敏，头尾片段仍可能推断凭证指纹，且随轮换变化；用哈希指纹比对『两侧脱敏后是否指向同一凭证』，明文与形态都不落报告。
- `$.entries`（sort_by=`None`）
  - **理由**：请求监控实时日志流：记录的是各进程自己承载的 gateway 流量（含 cron/用户请求的账号、耗时、preview）。正式对拍时老/新服务各有独立流量史，内容必然不同；自测时读到的是实时增长列表。这是【进程态】非持久契约。pre 层按类型折叠为空数组，L2 仍校验『monitoring 返回 entries 键且为数组』+ keep 计数；每条记录字段的正确性由黑盒测试 test_gateway_monitoring.py 单服务验证（非对拍职责）。**CR-37 修正**：本折叠只覆盖【内容/长度/顺序】三个维度——内容=进程态折叠正当；但【方向】（ts 降序新在前，UI 可见）是独立持久契约，不得随折叠丢失——顺序定罪由 AD-monitoring 的 order_keys 方向模式承担（两侧各自断言降序，不经本折叠）。类型+键存在由 L2 在折叠后的类型保持空值上仍守卫。
- `$.choices[*].message.content`（replace→`<llm-text>`）
  - **理由**：真实上游 LLM 生成文本不保证跨调用确定性（采样温度/上游版本漂移）。契约面（对象类型、finish_reason、usage 键结构、[DONE] 终止）由 L2 + 未归一化字段守卫。仅用于 expensive 的 /v1/* 生成端点，默认不在自测集。
- `$.id`（replace→`<resp-id>`）
  - **理由**：OpenAI 风格响应 id（chatcmpl-xxxx），上游随机。仅服务 expensive 端点。
- `$.created`（replace→`<ts>`）
  - **理由**：OpenAI 风格响应秒级时间戳。仅服务 expensive 端点。
- `$.__sse_frames__[*].id`（replace→`<chunk-id>`）
  - **理由**：SSE 分片响应 id 上游随机。仅 expensive 流式端点。
- `$.__sse_frames__[*].created`（replace→`<ts>`）
  - **理由**：SSE 分片秒级时间戳。仅 expensive 流式端点。
- `$.__sse_frames__[*].choices[*].delta.content`（replace→`<llm-text>`）
  - **理由**：SSE 流式生成文本非确定。帧序、object 类型、finish_reason、[DONE] 终止符仍逐项守卫。仅 expensive 流式端点。
- `$.exported_at`（replace→`<exported-at>`）
  - **理由**：B-19 export 响应的墙钟时间戳（服务器落盘时刻），与 $.ts 同理：同服务连续两次调用必然不同且无契约语义。version/结构仍逐值对拍。

## 已知差异豁免清单：3 条（默认空，一切差异默认报错）
- `ADR-002-enabled-laohao` case=`*` path=`$.accounts[*].enabled` 结构豁免=True — 用户裁定 enabled 字段规范化（不复现脏值）：老服务对账号 laohao-bigmodel-2420b3c2 透传 data JSON 中的字符串 "True"（生产库脏数据；表列 enabled 实为 INTEGER 全 1），Go 版按 ADR-002 统一返回布尔 true。规范化只在读语义层、不改库 → 脏值永久驻留，故 case='*'：任意对拍 case 中出现同形态 accounts[*].enabled 漂移均命中本条；形态精确锚定（仅 baseline=str "True" 且 candidate=bool True），其他账号其他形态（如 bool→int）不匹配、照常 FAIL。端点覆盖核查（captain 2026-10-15 指令）：/admin/api/status 实测响应仅 providers/gateway_key_set/quota_pool，无 enabled；/admin/api/monitoring entries 实测 15 键（req_id/ts/endpoint/model/stream/preview/account/mode/ok/status/error/t_first/t_total/input_tokens/output_tokens），无 enabled；/admin/api/bundle/export 为 PBKDF2+AES-GCM 加密二进制（盐/IV 随机 → 本身不可字节对拍，其数据等价由 INT-021 导出→清库→导入往返覆盖）；/login/poll 与 /claim/preview 若隔离实测发现透出 data.enabled，届时按同格式补条目并独立批准，不预先扩面。审计链：docs/contract/adr-002-enabled-normalization.md + adr-003-enabled-write-semantics.md（批准：**用户拍板 · captain 转达（AgentTeams t4 · 2026-10-15） · 见 docs/contract/adr-002-enabled-normalization.md**）
- `ADR-006-ui-login-page` case=`UI-login-page` path=`$body-bytes` 结构豁免=False **requires_check=`html-dom-equivalence`（豁免≠放行：html-dom-equivalence 不过则整体 FAIL）** — ADR-006 选项 C：页面 HTML 重写以去除历史来源，字节级差异（空白/注释/属性顺序/模板引擎）为【预期差异】；字节 L3 已降级 observed，本条登记契约意图——正式判据是 ST-DOM 结构等价，且 requires_check 保证【结构不过=整体 FAIL，豁免≠放行】（批准：**用户裁定（ADR-006 选项 C，2026-10-15）**）
- `ADR-006-ui-monitoring-page` case=`UI-monitoring-page` path=`$body-bytes` 结构豁免=False **requires_check=`html-dom-equivalence`（豁免≠放行：html-dom-equivalence 不过则整体 FAIL）** — 同上：monitoring 页重写，字节漂移预期；判据转移到 ST-DOM（requires_check 强制）（批准：**用户裁定（ADR-006 选项 C，2026-10-15）**）

## 敏感值硬防线（sentinel）：本轮命中 46 处

以下位置的响应值命中「疑似密钥形态」，**已替换为 sha256 指纹**——
等值判定能力保留（同值同指纹可比对；异值指纹不同照样 FAIL），明文零落盘：

- `AD-status` [baseline] `$.gateway_key_set` 检测器=field-name 命中=gateway_key_set 动作=logged-only(non-string) 指纹=-
- `AD-status` [candidate] `$.gateway_key_set` 检测器=field-name 命中=gateway_key_set 动作=logged-only(non-string) 指纹=-
- `AD-accounts` [baseline] `$.accounts[0].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:ba1dd67f>
- `AD-accounts` [baseline] `$.accounts[1].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:56f8ba90>
- `AD-accounts` [baseline] `$.accounts[2].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:6fa88952>
- `AD-accounts` [baseline] `$.accounts[3].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:0b6a4c42>
- `AD-accounts` [baseline] `$.accounts[4].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:9b72cb52>
- `AD-accounts` [baseline] `$.accounts[5].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:f41c2530>
- `AD-accounts` [candidate] `$.accounts[0].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:ba1dd67f>
- `AD-accounts` [candidate] `$.accounts[1].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:56f8ba90>
- `AD-accounts` [candidate] `$.accounts[2].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:6fa88952>
- `AD-accounts` [candidate] `$.accounts[3].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:0b6a4c42>
- `AD-accounts` [candidate] `$.accounts[4].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:9b72cb52>
- `AD-accounts` [candidate] `$.accounts[5].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:f41c2530>
- `AD-settings` [baseline] `$.admin_key` 检测器=field-name 命中=admin_key 动作=fingerprinted 指纹=<secret:aefd31f9>
- `AD-settings` [baseline] `$.gateway_key` 检测器=field-name 命中=gateway_key 动作=fingerprinted 指纹=<secret:12ae32cb>
- `AD-settings` [candidate] `$.admin_key` 检测器=field-name 命中=admin_key 动作=fingerprinted 指纹=<secret:aefd31f9>
- `AD-settings` [candidate] `$.gateway_key` 检测器=field-name 命中=gateway_key 动作=fingerprinted 指纹=<secret:12ae32cb>
- `AD-monitoring` [candidate] `$.entries[15].input_tokens` 检测器=field-name 命中=input_tokens 动作=logged-only(non-string) 指纹=-
- `AD-monitoring` [candidate] `$.entries[15].output_tokens` 检测器=field-name 命中=output_tokens 动作=logged-only(non-string) 指纹=-
- `SEQ-readonly-smoke` [baseline] `$.gateway_key_set` 检测器=field-name 命中=gateway_key_set 动作=logged-only(non-string) 指纹=-
- `SEQ-readonly-smoke` [candidate] `$.gateway_key_set` 检测器=field-name 命中=gateway_key_set 动作=logged-only(non-string) 指纹=-
- `SEQ-readonly-smoke` [baseline] `$.accounts[0].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:ba1dd67f>
- `SEQ-readonly-smoke` [baseline] `$.accounts[1].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:56f8ba90>
- `SEQ-readonly-smoke` [baseline] `$.accounts[2].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:6fa88952>
- `SEQ-readonly-smoke` [baseline] `$.accounts[3].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:0b6a4c42>
- `SEQ-readonly-smoke` [baseline] `$.accounts[4].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:9b72cb52>
- `SEQ-readonly-smoke` [baseline] `$.accounts[5].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:f41c2530>
- `SEQ-readonly-smoke` [candidate] `$.accounts[0].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:ba1dd67f>
- `SEQ-readonly-smoke` [candidate] `$.accounts[1].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:56f8ba90>
- `SEQ-readonly-smoke` [candidate] `$.accounts[2].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:6fa88952>
- `SEQ-readonly-smoke` [candidate] `$.accounts[3].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:0b6a4c42>
- `SEQ-readonly-smoke` [candidate] `$.accounts[4].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:9b72cb52>
- `SEQ-readonly-smoke` [candidate] `$.accounts[5].token_masked` 检测器=field-name 命中=token_masked 动作=fingerprinted 指纹=<secret:f41c2530>
- `AD-export` [baseline] `$.providers.zai[0].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:503a1b14>
- `AD-export` [baseline] `$.providers.zai[1].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:2b42a7d9>
- `AD-export` [baseline] `$.providers.zai[2].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:149752e8>
- `AD-export` [baseline] `$.providers.zai[3].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:df8007ab>
- `AD-export` [baseline] `$.providers.bigmodel[0].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:afc6c7e1>
- `AD-export` [baseline] `$.providers.bigmodel[1].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:76aa30ae>
- `AD-export` [candidate] `$.providers.zai[0].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:503a1b14>
- `AD-export` [candidate] `$.providers.zai[1].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:2b42a7d9>
- `AD-export` [candidate] `$.providers.zai[2].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:149752e8>
- `AD-export` [candidate] `$.providers.zai[3].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:df8007ab>
- `AD-export` [candidate] `$.providers.bigmodel[0].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:afc6c7e1>
- `AD-export` [candidate] `$.providers.bigmodel[1].secret` 检测器=field-name 命中=secret 动作=fingerprinted 指纹=<secret:76aa30ae>

## 逐端点结论

| id | 方法 | 路径 | 预期 | 结论 | L1 | L2 | L3 | ORDER | ST-DOM | 重试轮 | 备注 |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `GW-models-anon` | GET | `/v1/models` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 网关匿名可达（生产 gateway_key 未配置=放行，实测 200）。静态清单含 created_at 常量。契约锁定：models 清单内容 + 键序无关性。｜status pin 200 依据=生产 gateway_key 未配置的实测放行语义（t13 准备期 L1 detail 复测钉死；若配置网关 key，本 pin 需按契约 401 分支改 list [200,401] 并在隔离实例验证）。 |
| `GW-models-nokey-gate` | GET | `/v1/models` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 与 GW-models-anon 同请求重复发：若两读不一致说明服务态不稳定，裁判先于对拍暴露它。｜status pin 200 依据=生产 gateway_key 未配置的实测放行语义（t13 准备期 L1 detail 复测钉死；若配置网关 key，本 pin 需按契约 401 分支改 list [200,401] 并在隔离实例验证）。 |
| `GW-models-wrong-key` | GET | `/v1/models` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | gateway_key 未配置时错误 key 也应放行（放行语义=不校验）。若 candidate 实现了『配置了才校验』则与此一致；不一致即契约差异。｜status pin 200 依据=生产 gateway_key 未配置的实测放行语义（t13 准备期 L1 detail 复测钉死；若配置网关 key，本 pin 需按契约 401 分支改 list [200,401] 并在隔离实例验证）。 |
| `GW-chat-invalid-model` | POST | `/v1/chat/completions` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 缺 model → 400 {error:{message,type=invalid_request_error}}。网关校验层，零上游流量。中文错误文案是契约（必须逐字一致）。 |
| `GW-chat-malformed-json` | POST | `/v1/chat/completions` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 残缺 JSON → 老服务实测 400/422（对拍时定案）。框架默认错误形态（FastAPI detail vs Go 手写）是最易漂移点，必须锚死。 |
| `GW-method-not-allowed` | POST | `/v1/models` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | GET-only 端点打 POST → 405 形态（FastAPI {"detail":"Method Not Allowed"}）。 |
| `GW-health-actual` | GET | `/health` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | ⚠ 文档 04-api-spec §1.5 说存在 /health，实测 404。契约以运行事实为准：candidate 也必须 404（除非文档修实）。此 case 是『文档-实现漂移』锚点。 |
| `AD-status` | GET | `/admin/api/status` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | providers/gateway_key_set/quota_pool 计数。quota_pool 是池规模（队友加/删账号会动它）→ 外部写风险由 FLAKY 重试处置。 |
| `AD-status-anon` | GET | `/admin/api/status` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 实测：status 匿名可达（200）。该豁免行为是契约（无论有意与否），candidate 必须复刻。 |
| `AD-accounts` | GET | `/admin/api/accounts` | L1+L2+L3+ORDER | **pass** | ✓ | ✓ | ✓ | ✓ | — | 1 | CR-35 顺序是持久契约：ORDER 断言（原始数组逐位比 id 序列，不经归一化/pre-sort）守卫 store.go ORDER BY rowid 与老服务(provider分组,created_at升序) 不一致时对拍洗绿的盲区。sort_by:id 仍保留做位置对齐（字段差异不错位）——两职责分离。对拍核心 case：全池账号（DB 持久数据）。归一化只覆盖时钟/计数/进程史（recent_results/entries 折叠、use_count 等），enabled 的 bool vs str 脏数据【无规则】→ L2 逐账号逐位置类型树守卫。fingerprint/quota/subscription/plan 等 DB 字段必须 L3 全同（两服务读同一 accounts.db）。 嵌套方向（CR-16b/工作B）：每账号 recent_results 的 at 序列升序（[-1] 最新，G-14 更正后口径）——replace_typed 折叠内容后顺序维度由 ORDER 嵌套模式守卫；空数组（新账号无历史）=合法零样本。 |
| `AD-settings` | GET | `/admin/api/settings` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 响应含 admin_key 明文——由 secret 规则以 sha256 指纹替代后比对（等值能力保留，明文不落报告）。 |
| `AD-monitoring` | GET | `/admin/api/monitoring` | L1+L2+ORDER | **pass** | ✓ | ✓ | ✓ | ✓ | — | 1 | CR-37：entries 内容=进程态（replace_typed 折叠正当），但【方向】是与内容无关的持久契约——实测 ts 严格降序（新在前）、连查稳定、UI 首行=最新（forEach+slice 渲染）。ORDER 用方向模式：两侧各自独立断言降序（内容必然不同，相等无意义）。空数组=无样本→fail 显式暴露（MUT-monitoring-clear 后/重启后不自愈，绝不静默 PASS）。 |
| `AD-login-poll-unknown` | GET | `/admin/api/login/poll/nonexistent-flow-xyz` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 未知 flow_id → 200 {"status":"expired"}（契约：幂等、无 404）。零上游。 |
| `AD-pool-doc-drift` | GET | `/admin/api/pool` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 文档说有 /admin/api/pool，实测 404 {"detail":"Not Found"}。以运行事实为准锚死。 |
| `AD-quota-doc-drift` | GET | `/admin/api/quota` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 文档 §额度 说 GET /admin/api/quota，实测 404。锚死 404 形态（FastAPI 默认 detail）。 |
| `AD-claim-history-drift` | GET | `/admin/api/claim/history` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 文档说存在，实测 404。锚死。 |
| `AD-noauth-accounts` | GET | `/admin/api/accounts` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 缺凭证 → 401 {"detail":"缺少鉴权凭证"}。中文文案逐字对拍。 |
| `AD-badkey-accounts` | GET | `/admin/api/accounts` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 错凭证 → 401 {"detail":"鉴权凭证无效"}。与缺凭证文案不同型 → 也是契约。 |
| `AD-noauth-post` | POST | `/admin/api/login/start` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 写端点缺 key → 401（鉴权在业务逻辑前，零上游）。黑盒测试 test_admin_key_required 的对拍版本。 |
| `UI-monitoring-page` | GET | `/admin/monitoring` | L1+ST-DOM | **pass** | ✓ | n/a | obs✗ | — | ✓ | 1 | ADR-006 判据：ST-DOM 必须 PASS；字节级 L3 仅 observed 记录（重写清空来源，空白/注释/属性序允许漂移）。HTML 页（非 JSON → L2 n/a），空白折叠后字节对拍：静态资源版本号、标题『zcode-hub · 请求监控』都是契约面。 |
| `UI-login-page` | GET | `/admin/login` | L1+ST-DOM | **pass** | ✓ | n/a | obs✗ | — | ✓ | 1 | ADR-006 判据：ST-DOM 必须 PASS；字节级 L3 仅 observed 记录（重写清空来源，空白/注释/属性序允许漂移）。登录页 HTML 字节对拍。 |
| `UI-root-redirect` | GET | `/` | L1+L3 | **pass** | ✓ | n/a | ✓ | — | — | 1 | 307 且 Location 值参与比对（headers_compared 含 location）。禁 follow：重定向形态本身是契约。 |
| `UI-admin-redirect` | GET | `/admin` | L1+L3 | **pass** | ✓ | n/a | ✓ | — | — | 1 | /admin → 307。 |
| `SEQ-readonly-smoke` | GET | `/admin/api/status` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | steps 序列机制的只读验证 case（ADR-003 写序列 SEQ-toggle 的基础设施同套）：三步按序双打、逐步对拍、聚合判定。零副作用，进自测。 |
| `ERR-unknown-path` | GET | `/definitely/not/a/route` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 全局 404 形态锚：FastAPI {"detail":"Not Found"} vs Go http.NotFound 的 text 差异是重灾区，必须逐字节。 |
| `GW-messages-nonobj` | POST | `/v1/messages` | L1+L3 | **pass** | ✓ | n/a | ✓ | — | — | 1 | 契约 CR-32 锚：合法非对象体 → 老服务 **500 text/plain 恰 21 字节**（未捕获异常形态）。candidate 若「好心修成 400」即挂——t8 审计已实测复现（3 形态 []/"str"/null 全 500）。零上游。 |
| `GW-chat-nonobj` | POST | `/v1/chat/completions` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | CR-32 对照锚：同体在 chat 端点 → 400 形态B {"请求体必须是 JSON 对象","invalid_request_error"}。与 GW-messages-nonobj 构成【同输入双端点语义相反】的成对锁定。零上游。 |
| `GW-messages-badjson` | POST | `/v1/messages` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | CR-01 锚：messages 非法 JSON → 400 type=invalid_request（无 _error 后缀）；chat 同场景是 invalid_request_error。两个 type 值不得统一。零上游。 |
| `UI-accounts-page` | GET | `/admin/accounts` | L1+ST-DOM | **pass** | ✓ | n/a | obs✗ | — | ✓ | 1 | t9 主战场：baseline 模板口径 43 id / 98 class / 3 data-*；live 属性口径由 ST-DOM 守卫 |
| `UI-settings-page` | GET | `/admin/settings` | L1+ST-DOM | **pass** | ✓ | n/a | obs✗ | — | ✓ | 1 | baseline 5 id / 11 class / 1 data-* |
| `AD-verify` | GET | `/admin/api/verify` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-01 契约实测 200 {"status":"ok"} 15B，前端 auth.js verifyKey() 探测面。零副作用。 |
| `AD-verify-anon` | GET | `/admin/api/verify` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-01 负型：无凭证 → 401 形态 A（逐字对拍中文 detail）。 |
| `AD-captcha-config` | GET | `/admin/api/claim/captcha-config` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-15 实测 200 4 键 {enabled,scene_id,region,prefix} 70B（契约值 scene_id=11xygtvd/region=cn/prefix=no8xfe）。region 系动态拉上游 configs——若 candidate 环境同源应同值；FLAKY 红则按实测收紧。零 zcode 配额消耗。 |
| `AD-export` | GET | `/admin/api/export` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-19 GET 只读。响应含明文 secret（CR-06 最危险端点）→ Sentinel 字段名+形态双道命中自动指纹化，两侧同值同指纹 L3 仍判等，明文零落盘。结构 version/providers{zai,bigmodel}[ExportItem{name,mode,secret}] 逐键对拍。路径陷阱：/admin/api/export 无 accounts 段（CR-06）。 |
| `AD-delete-unknown` | DELETE | `/admin/api/accounts` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-04 实测：body 为 id 数组，不存在 id → 200 {"deleted":0}，零副作用。路径 CR-04：集合级 DELETE 非 /{id}。 |
| `AD-put-unknown` | PUT | `/admin/api/accounts/__liche-no-such-id__` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-06 实测：不存在 id → 404 {"detail":"账号不存在"}。零副作用（未命中账号无写入）。 |
| `AD-toggle-unknown` | POST | `/admin/api/accounts/__liche-no-such-id__/enabled` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-07 实测：不存在 id → 404 同文（注意：真实假 id 域写成功是另一面，由 lifecycle 覆盖）。 |
| `AD-rotate-unknown` | POST | `/admin/api/accounts/__liche-no-such-id__/fingerprint/rotate` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-08 实测：不存在 id → 404。零副作用。成功形态(200 ok+fingerprint)由 lifecycle rotate 步单侧断言覆盖。 |
| `AD-manualclaim-missing` | POST | `/admin/api/claim/manual` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-16 契约实测：缺 account_id → 400 {"detail":"缺少 account_id"}——校验层拒绝在验证码逻辑之前，零副作用。t8 报告曾列此项为【刻意未测】：当时无契约实测背书不敢在生产撞验证码；现 B-16 [实测] 已钉死形态，转正为 case。 若本 case 出现超时/验证码副作用迹象=契约该实测失效，立即降 mutating 并报告。 |
| `AD-import-empty` | POST | `/admin/api/import` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-20 契约实测：{} → 200 {"count":0}（空导入零副作用）。若 candidate 把空导入实现成脏写（count≠0/结构变形）当场定罪。 |
| `AD-create-notokens` | POST | `/admin/api/accounts` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | B-03 契约实测：{} → 400 {"detail":"请输入至少一个 Token / API Key"}（校验层拒绝，零副作用）。与 lifecycle 正型（建号成功形态）互补。 |
| `ST-css-app` | GET | `/static/css/app.css` | L1+L3 | **pass** | ✓ | n/a | ✓ | — | — | 1 | 静态资源面首个覆盖（t19）。实测基线 23788B text/css; charset=utf-8（任务书表 23787 为记录偏差，按「以实测为准」原则取实测）。带 etag（stat 族非内容 hash，t9 已证 → 不进 headers 比对）无 cache-control（与页面 no-store 相反，见 static_probe.py）。L3=折叠后 body 等值（跨侧 Go embed 同文件即绿）。 |
| `ST-js-auth` | GET | `/static/js/auth.js` | L1+L3 | **pass** | ✓ | n/a | ✓ | — | — | 1 | 实测 2701B text/javascript; charset=utf-8（任务书 2700，同 -1 模式：疑测量管道差异，取实测）。auth.js 是页面鉴权的行为核心文件。 |
| `ST-js-header` | GET | `/static/js/header.js` | L1+L3 | **pass** | ✓ | n/a | ✓ | — | — | 1 | 实测 1274B（任务书 1273）。 |
| `ST-js-toast` | GET | `/static/js/toast.js` | L1+L3 | **pass** | ✓ | n/a | ✓ | — | — | 1 | 实测 1368B（任务书 1367）。 |
| `ST-static-dir-redirect` | GET | `/static` | L1 | **pass** | ✓ | ✗ | ✗ | — | — | 1 | 307 挂起（禁 follow）。Location 是【绝对 URL 含自身 host:port】（t9 契约修正：/static 系 Starlette Mount 特性=绝对，页面 307 系相对——不对称合法）⇒ 跨对拍两侧 Location 值必不同（端口不同）是【预期差异】，故 expect 只 L1；location diff 由 headers 机制如实记录进 observed。 |
| `ST-static-404` | GET | `/static/nonexist.js` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | 404 形态 A {"detail":"Not Found"}（22B JSON）。任务书保守给 L1，我升 L1+L2+L3：body 是 JSON 结构可比、Go t12 已实现「双 404 兜底各归其主」——若 Go 对 static miss 输出非 JSON 或 detail 变形（非「好心换形态」所能豁免），当场定罪。升级理由超出任务书最低要求，无生产副作用。 |
| `D-meta` | GET | `/meta` | L1+L2+L3 | **pass** | ✓ | ✓ | ✓ | — | — | 1 | D-01 元数据端点（t20 实现）。实测逐字节 19B {"version":"2.5.3"} application/json 无 charset；GET-strict（HEAD→405 allow:GET 是实测，页面级 pattern 不加方法前缀防 Go 自动镜像 HEAD）。版本 2.5.3 与静态资源 ?v=2.5.1 的不一致是真实面，照抄。 |
| `D-openapi` | GET | `/openapi.json` | L1+ASSERT | **pass** | ✓ | ✗ | ✗ | — | — | 1 | D-02（t20 手写 5431B 文档）。captain 裁定不逐字节（FastAPI 生成形不可手写复现）⇒ expect 止 L1+ASSERT：断言层钉可观测契约面（3.1.0/title=zcode-hub/version=2.5.3/paths=22）；L2/L3 跨侧必异进 observed 不判（expect-gated）。Go 若把 title 写成 zhipucode2api，两侧各自跑 ASSERT 当场定罪——这是本 case 的核心价值。 |

## 页面结构等价（ST-DOM · ADR-006 正式判据；L3 obs = 字节仅记录不判）

| case | ST-DOM | 节点 B/C | id B/C | class(去重) B/C | data-* B/C | 资源数 | 结构差异 |
|---|---|---|---|---|---|---|---|
| `UI-monitoring-page` | ✓ | 116/116 | 27/27 | 26/26 | 4/4 | 5 | 0 |
| `UI-login-page` | ✓ | 25/25 | 1/1 | 12/12 | 0/0 | 4 | 0 |
| `UI-accounts-page` | ✓ | 161/161 | 40/40 | 35/35 | 1/1 | 5 | 0 |
| `UI-settings-page` | ✓ | 44/44 | 5/5 | 11/11 | 1/1 | 5 | 0 |

资源引用清单（两侧集合完全一致是 ADR-006 第 5/6 条不变量）：
- `UI-monitoring-page` [baseline]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/header.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- `UI-monitoring-page` [candidate]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/header.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- `UI-login-page` [baseline]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- `UI-login-page` [candidate]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- `UI-accounts-page` [baseline]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/header.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- `UI-accounts-page` [candidate]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/header.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- `UI-settings-page` [baseline]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/header.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`
- `UI-settings-page` [candidate]: `/static/css/app.css?v=2.5.1`, `/static/js/auth.js?v=2.5.1`, `/static/js/header.js?v=2.5.1`, `/static/js/toast.js?v=2.5.1`, `https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css`

## 差异明细（仅未通过项）
## 豁免命中（审计痕迹）
- `AD-accounts` [L2] → allowlist `ADR-002-enabled-laohao`：{"path": "$.accounts[0].enabled", "kind": "type", "baseline": "str", "candidate": "bool", "baseline_value": "True", "candidate_value": true} — 用户裁定 enabled 字段规范化（不复现脏值）：老服务对账号 laohao-bigmodel-2420b3c2 透传 data JSON 中的字符串 "True"（生产库脏数据；表列 enabled 实为 INTEGER 全 1），Go 版按 ADR-002 统一返回布尔 true。规范化只在读语义层、不改库 → 脏值永久驻留，故 case='*'：任意对拍 case 中出现同形态 accounts[*].enabled 漂移均命中本条；形态精确锚定（仅 baseline=str "True" 且 candidate=bool True），其他账号其他形态（如 bool→int）不匹配、照常 FAIL。端点覆盖核查（captain 2026-10-15 指令）：/admin/api/status 实测响应仅 providers/gateway_key_set/quota_pool，无 enabled；/admin/api/monitoring entries 实测 15 键（req_id/ts/endpoint/model/stream/preview/account/mode/ok/status/error/t_first/t_total/input_tokens/output_tokens），无 enabled；/admin/api/bundle/export 为 PBKDF2+AES-GCM 加密二进制（盐/IV 随机 → 本身不可字节对拍，其数据等价由 INT-021 导出→清库→导入往返覆盖）；/login/poll 与 /claim/preview 若隔离实测发现透出 data.enabled，届时按同格式补条目并独立批准，不预先扩面。审计链：docs/contract/adr-002-enabled-normalization.md + adr-003-enabled-write-semantics.md（批准 用户拍板 · captain 转达（AgentTeams t4 · 2026-10-15） · 见 docs/contract/adr-002-enabled-normalization.md）
- `AD-accounts` [L3] → allowlist `ADR-002-enabled-laohao`：{"path": "$.accounts[0].enabled", "kind": "value-type", "baseline": "str", "candidate": "bool", "baseline_value": "True", "candidate_value": true} — 用户裁定 enabled 字段规范化（不复现脏值）：老服务对账号 laohao-bigmodel-2420b3c2 透传 data JSON 中的字符串 "True"（生产库脏数据；表列 enabled 实为 INTEGER 全 1），Go 版按 ADR-002 统一返回布尔 true。规范化只在读语义层、不改库 → 脏值永久驻留，故 case='*'：任意对拍 case 中出现同形态 accounts[*].enabled 漂移均命中本条；形态精确锚定（仅 baseline=str "True" 且 candidate=bool True），其他账号其他形态（如 bool→int）不匹配、照常 FAIL。端点覆盖核查（captain 2026-10-15 指令）：/admin/api/status 实测响应仅 providers/gateway_key_set/quota_pool，无 enabled；/admin/api/monitoring entries 实测 15 键（req_id/ts/endpoint/model/stream/preview/account/mode/ok/status/error/t_first/t_total/input_tokens/output_tokens），无 enabled；/admin/api/bundle/export 为 PBKDF2+AES-GCM 加密二进制（盐/IV 随机 → 本身不可字节对拍，其数据等价由 INT-021 导出→清库→导入往返覆盖）；/login/poll 与 /claim/preview 若隔离实测发现透出 data.enabled，届时按同格式补条目并独立批准，不预先扩面。审计链：docs/contract/adr-002-enabled-normalization.md + adr-003-enabled-write-semantics.md（批准 用户拍板 · captain 转达（AgentTeams t4 · 2026-10-15） · 见 docs/contract/adr-002-enabled-normalization.md）
- `SEQ-readonly-smoke` [L2] → allowlist `ADR-002-enabled-laohao`：{"path": "$.accounts[0].enabled", "kind": "type", "baseline": "str", "candidate": "bool", "baseline_value": "True", "candidate_value": true} — 用户裁定 enabled 字段规范化（不复现脏值）：老服务对账号 laohao-bigmodel-2420b3c2 透传 data JSON 中的字符串 "True"（生产库脏数据；表列 enabled 实为 INTEGER 全 1），Go 版按 ADR-002 统一返回布尔 true。规范化只在读语义层、不改库 → 脏值永久驻留，故 case='*'：任意对拍 case 中出现同形态 accounts[*].enabled 漂移均命中本条；形态精确锚定（仅 baseline=str "True" 且 candidate=bool True），其他账号其他形态（如 bool→int）不匹配、照常 FAIL。端点覆盖核查（captain 2026-10-15 指令）：/admin/api/status 实测响应仅 providers/gateway_key_set/quota_pool，无 enabled；/admin/api/monitoring entries 实测 15 键（req_id/ts/endpoint/model/stream/preview/account/mode/ok/status/error/t_first/t_total/input_tokens/output_tokens），无 enabled；/admin/api/bundle/export 为 PBKDF2+AES-GCM 加密二进制（盐/IV 随机 → 本身不可字节对拍，其数据等价由 INT-021 导出→清库→导入往返覆盖）；/login/poll 与 /claim/preview 若隔离实测发现透出 data.enabled，届时按同格式补条目并独立批准，不预先扩面。审计链：docs/contract/adr-002-enabled-normalization.md + adr-003-enabled-write-semantics.md（批准 用户拍板 · captain 转达（AgentTeams t4 · 2026-10-15） · 见 docs/contract/adr-002-enabled-normalization.md）
- `SEQ-readonly-smoke` [L3] → allowlist `ADR-002-enabled-laohao`：{"path": "$.accounts[0].enabled", "kind": "value-type", "baseline": "str", "candidate": "bool", "baseline_value": "True", "candidate_value": true} — 用户裁定 enabled 字段规范化（不复现脏值）：老服务对账号 laohao-bigmodel-2420b3c2 透传 data JSON 中的字符串 "True"（生产库脏数据；表列 enabled 实为 INTEGER 全 1），Go 版按 ADR-002 统一返回布尔 true。规范化只在读语义层、不改库 → 脏值永久驻留，故 case='*'：任意对拍 case 中出现同形态 accounts[*].enabled 漂移均命中本条；形态精确锚定（仅 baseline=str "True" 且 candidate=bool True），其他账号其他形态（如 bool→int）不匹配、照常 FAIL。端点覆盖核查（captain 2026-10-15 指令）：/admin/api/status 实测响应仅 providers/gateway_key_set/quota_pool，无 enabled；/admin/api/monitoring entries 实测 15 键（req_id/ts/endpoint/model/stream/preview/account/mode/ok/status/error/t_first/t_total/input_tokens/output_tokens），无 enabled；/admin/api/bundle/export 为 PBKDF2+AES-GCM 加密二进制（盐/IV 随机 → 本身不可字节对拍，其数据等价由 INT-021 导出→清库→导入往返覆盖）；/login/poll 与 /claim/preview 若隔离实测发现透出 data.enabled，届时按同格式补条目并独立批准，不预先扩面。审计链：docs/contract/adr-002-enabled-normalization.md + adr-003-enabled-write-semantics.md（批准 用户拍板 · captain 转达（AgentTeams t4 · 2026-10-15） · 见 docs/contract/adr-002-enabled-normalization.md）
