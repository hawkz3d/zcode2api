# 本地补丁集语义提取与 Go 版覆盖核对（t18 · Lamias · 2026-10-16）

> **命题**（t15/Hilkiah 发现，captain 确认）：老服务当前运行的**不是上游原版**，
> 而是打了大量本地补丁的版本。我们的"契约等价"若只对齐**对外可见契约面**，
> 会丢掉两类东西：①契约面实测过但被**归一化/单样本折叠**的补丁语义；
> ②补丁产生**新的对外行为**但契约**尚未记录**（盲区）。切换后表现为**静默功能回退**
> （不崩溃，只是变差/消失）。
>
> **规格先于实现声明**：本任务只读了 `app/quota.py` 的**补丁相关 diff**（任务明许可）。
> 其余补丁涉及文件（openai_compat.py / routes/gateway.py / claim.py / fingerprint /
> telemetry / install / store.py / constants.py）**未纳入本轮观测实现**，其语义来自
> git commit message + README + 记忆库运营档案 + 契约实测。凡"仅凭 commit 未代码级核实"
> 者，下文标注 **〔未代码核实〕**，不作覆盖结论、只列核对线索。

---

## 一、补丁全集清单（三类来源）

### 来源 A · `workspace/scripts/z2a-patches/`（正式补丁脚本，1 个）
| 文件 | 目标 | 日期 | 性质 |
|---|---|---|---|
| `entitlements.py` | `app/quota.py` | 2026-09-18 | 解析 `plans[].entitlements[]`（幂等 MARKER 注入） |

### 来源 B · `app/*.bak-*`（直接编辑的痕迹，按系列聚合，15 个 .bak / 8 个源文件）
| 系列 | 触及源文件 | 日期 | 一句话语义 |
|---|---|---|---|
| `2key` | constants/models/quota/gateway/store/settings | 2026-10-10 | 双 key（gateway+auth 分离？）**〔未代码核实〕** |
| `chansplit` | store/gateway | 2026-10-10 | 双通道拆分（zcode 补偿包/bigmodel 订阅）**〔未代码核实〕** |
| `bizerr` | routes/gateway | 2026-10-01 | 网络错误转业务回执 + 验证码专用异常 **〔未代码核实〕** |
| `reasoning` | openai_compat | 2026-09-30 | **thinking_delta → reasoning_content 透传**（记忆库详录，本任务重点） |
| `connfix` | settings/routes-gateway | 2026-09-30 | 连接/NO_PROXY 加固（直连要求）**〔部分未代码核实〕** |
| `dynver` | claim/constants/quota/telemetry | 2026-09-30 | **动态版本门槛**（现行版才见活动包）——quota.py 已代码核实 |
| `lazycaptcha` | routes/gateway | 2026-09-30 | **captcha 惰性化**（首包 20~42s→0.9s）——记忆库详录 |
| `entitlements` | quota | 2026-09-18 | 见来源 A |

### 来源 C · 老仓 git 本地提交（`3f54f14` 初始导入之后，**共 49 条**，功能等价主力）
按主题归类（完整表见文末附录）：
- **网关语义**：`cf05761`(/v1/chat/completions)、`e96729c`/`49d566d`(**max_tokens 钳 131072 + 4xx 落日志**)、`264c668`(429/5xx 参数化重试、真风控才禁用)、`64ec67e`(Retry-After + 冷却倒计时)、`0eab254`(3012/405 指数退避+暂停验证码池)、`84e714e`(连击去重+后台冷却门+验证码池门控)、`b5c99e0`(模型表对齐余额窗口)、`112d1f1`(平台指纹固定 darwin + 剔除 x-stainless)。
- **领取/激活链**：`82e5eab`(入池即激活+自动领全部可领)、`bfd37b9`(激活事件上报移植+billing 全头对齐)、`5ec24ae`/`e9c58df`(领取失败全链路留痕+网络错误转业务回执)、`1007068`(安装序 task 持引用防 GC)、`65a547b`/`dd7ffdb`(首启安装序仿真+激活上报单一事实源)。
- **指纹**：`388e6de`/`e9d6aec`(每账号独立指纹池/随机合规档案)。
- **监控/UI**：`6c89af3`(监控页)、`a17b1b4`/`c5e62bb`/`dd3608f`(赠送池渲染)、`0240c23`(赠送池纳入耗尽判定/多套餐保留/同名窗口合并)、`b3ecde6`/`61c2f92`(账号池 6 列表格/成功率公式)。
- 〔来源 B/C 的 47 条标〔未代码核实〕者，仅由 commit message + 记忆库定性。〕

---

## 二、逐补丁语义提取 + Go 覆盖核对

### P-1 · entitlements 解析（活动包额度可见性）—— 🔴 **已覆盖（经契约+透传）**
**业务语义**（quota.py diff 代码核实）：上游把周末/活动包放在 `plans[].entitlements[]`、
`balances[]` 反而为空。原版只解析 `balances` → 领到的包在本地快照不存在 →
卡片显示"无有效额度"。补丁在 `fetch_quota` 追加 `plans[].entitlements[]` 解析，
产出 quota 项 `{total,used,remaining,expires_at,starts_at,effective_at,plan_name,source:"plan"}`，
含**同模型多包累加、过期取最晚**逻辑。

**可复现证据（补丁效果 live）**——对老服务（打补丁版）实测：
```
oauth-login-ea92aa60 → quota["GLM-5.3-Flash"] =
  {"total":300000000,...,"source":"plan","plan_name":"ZCode Weekend Build"}
```
（3 亿额度来自 Weekend Build，`source:"plan"` —— 未打补丁的原版此处为空。）

**Go 覆盖**：
- 契约 `endpoints.md:215-222` **已记录** `quota` 8 键结构含 `source:"plan"`、
  `effective_at`，`[实测]` 取值就是打补丁后的形态 → 契约对齐即自动含此语义。
- `internal/models/models.go`：`Account.quota` 走**无损透传**（`AccountData` map +
  原字节保留），不重写解析 → 只要 store 快照里有此键，输出必保结构。
- **结论：已覆盖**。核对结论——**前提**是 Go 侧 `fetch_quota` 实现（t13 adminapi /
  未来 quota 刷新）真的解析 `plans[].entitlements[]` 并落这个结构；契约只锁"输出形态",
  不锁"谁来解析"。**⚠️ 若 t13 的刷新链照原版只读 balances，则存储环节仍会丢包** →
  见缺失项 I-1。

### P-2 · 动态版本门槛（dynver）—— 🟠 **缺失（新对外行为，契约未记）**
**业务语义**（quota.py diff 代码核实 + 记忆库《活动包版本门槛定案》）：
原版 `_auth_headers` 用固定 `BILLING_APP_VERSION=3.11.2`；补丁改为运行时传
`release_version.current_billing_version()`（3.14.x），因为**带版本门槛的活动包
（如 trust 补偿包）只有现行版才在上游 preview 可见**。记忆库决定性对照：同 JWT 同头
仅改版本 → 3.11.2/3.10.2 plans=[] ；3.14.3/3.14.4 plans=[trust]。

**是否契约**：❌ 契约只记 `X-ZCode-App-Version` 参与资格（低版本判 1004），
**未记"现行版动态获取"这条链**——因为这是**上游请求侧**行为，33 端点对外契约看不见。
**Go 覆盖**：❌ Go 无 `release_version` 包，upstream/gateway 无版本门槛逻辑（adminapi
未落地，t13 进行中）。→ **缺失项 I-2**。

### P-3 · captcha 惰性化（lazycaptcha）—— 🟠 **缺失（新对外行为：首包延迟）**
**业务语义**（记忆库《提速两修复》详录，routes/gateway.py 未纳入本轮观测）：`.env CAPTCHA_POOL_MIN=0`
→ 池不预热 → 原版每请求走"同步现解"分支 → 首包 20~42s（解一次 2.4~42.5s）。补丁改
`if needs_captcha and captcha_retries>0:` 为**条件式**（首请求不带 token，收到挑战才取
token 重试）。记忆库实证 captcha 对 zcode-plan 通道是**软校验**（不带 6/6 全过、
`_detect_captcha_challenge` 触发次数=0）→ 惰性化安全，首包降到 0.9s。

**是否契约**：❌ **契约对"网关内部何时取 captcha"零记载**（黑盒看不见上游侧行为；
endpoints 仅在 B-16 记"claim 的 verify_param 原样转发上游头"——`endpoints.md:463`，属实）。
补丁产生的对外可见差异 = **首包延迟**（20~42s→0.9s），而契约/对拍均无延迟维度断言。
**Go 覆盖**：❌ t7 是 solver 客户端（供 claim 用）、t6 gateway 完全不接 captcha。
→ **缺失项 I-3**：Go 版 claim/pool 接线时 captcha 策略必须按"惰性+软校验"实现
（若照 .env CAPTCHA_POOL_MIN 预取池的旧模型实现，首包延迟回退 20~40s 且无对拍报警）。

### P-4 · reasoning_content 透传（openai_compat）—— 🔴 **最高危：契约盲区 + 归一化折叠**
**业务语义**（记忆库详录 + 本次对老服务 live 实测确证）：上游发 Anthropic `thinking` 块
（`content_block_delta.type=thinking_delta`，字段 `thinking`）+ `signature_delta`；
原版转换器只处理 `text_delta`/`input_json_delta` → **思考链整段丢弃**。补丁两处补映射：
流式 `thinking_delta → {reasoning_content}`、非流式收集 thinking 块 → `message.reasoning_content`。
DSH/pi-ai 按 `reasoning_content` 渲染思考链。

**证据（本次实弹 + 历史档案）**——2026-10-16 对老服务发 1 条 GLM-5.3 流式请求
（生产账号，单次取证、不重发）：
```
data: {"id":"chatcmpl-93d57e98…","delta":{"role":"assistant","content":""}}
data: {"error":{"message":"上游业务错误 code=1005: exceed quota limit",…}}
```
本次样本**未产生 thinking**——上游配额已尽（1005 流内错误），恰好演示了
"单次实弹不可作契约锚"的机制本身。补丁透传功能的历史实证见记忆库
《ZCode 提速两修复》（2026-09-30：修复后 **139 个 reasoning_content chunk 正常透传**，
〔历史数据，非本次测得〕）。复现命令（需上游有配额时执行一次即可）：
```bash
curl -N -s http://127.0.0.1:9120/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"GLM-5.3","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"…仔细推理…"}]}' \
  | grep -c 'reasoning_content'   # 补丁版老服务 >0；原版恒 0
```

**三重"看不见"——本任务最重要的发现**：
1. **契约盲区**：全库 `grep reasoning_content docs/contract/*.md` = **0 命中**（实测确认）。
2. **对拍无 case**：`grep reasoning scripts/conformance/{harness.py,normalize.rules.json,cases.json}`
   = **0 命中**（实测确认）——harness 连"剥除"动作都没有，而是**根本没有任何 SSE delta
   内容 case**（GW-* 全部是零上游静态错误路径）。"无 case"比"主动剥除"更基础：
   Go 不实现 reasoning 也不会引起对拍任何一层报警。
3. **单样本折叠**：`contract-audit.md` 的流样本与本次实弹都恰好无 thinking（前者上游
   未思考、后者配额尽）——有/无两形态必须都覆盖才能锚定。

**Go 覆盖**：❌ `internal/gateway/chat.go` 只处理 `text_delta`（`case "content_block_delta"`
读 `text_delta`），**无 `thinking_delta`→`reasoning_content` 分支**；`grep reasoning chat.go` = 0。
→ **缺失项 I-4，且是"契约零提及+对拍零 case"双盲的最高危项**：若不做，切换后
DSH 用户静默失去思考链显示，且**现有对拍全绿不会报警**。

### P-5 · max_tokens 钳制到 131072 —— 🟢 **已覆盖**
**语义**（来源 C `e96729c`/`49d566d`）：入站 `max_tokens` 钳到上游上限 131072（含字符串
形态），避免上游超限报错；并让钳制动作可观测。**契约**：✅ `IMPLEMENTER-CHEATSHEET.md:71`
明确记"钳制到 131072（含字符串形态 999999）"。**Go 覆盖**：✅ `internal/gateway/forward.go:145-146`（`clampMaxTokens`，`const limit = 131072`，
chat.go:201/messages 双入口调用）。**已覆盖**。

### P-6 · 监控页（6c89af3 等）—— 🟢 **已覆盖（在途）**
**语义**：新增 `/admin/monitoring` 请求实时观测页 + 内存环形日志（500 条，重启清零）。
**契约**：✅ endpoints B 组 + C 组页面。**Go 覆盖**：页面侧 ✅（我的 t9 交付 monitoring.html）；
数据侧 B 组监控 API → t13 adminapi（进行中）。→ 归类"已覆盖/在途"，非缺失（页面已在，
API 是 t13 本职）。

### P-7 · 429/5xx 重试 + 风控退避（264c668/0eab254/64ec67e/84e714e）—— 🟡 **部分覆盖，深度未核**
**语义**：429 尊重 Retry-After；3012/405 触发账号指数退避+暂停验证码池；并发连击去重；
**真风控才禁用账号**（参数化重试）。**契约**：endpoints 记了 A 组错误码，但"重试次数/退避算法/
连击去重"是**上游请求侧**内部策略，对外契约基本不显。**Go 覆盖**：❌→🟡
`grep -rn 'Retry-After\|429' internal/gateway internal/upstream`（排除测试）**实测零命中**
——Go 转发链目前**没有** Retry-After 消费与 429 重试的可见实现；对拍 GW-* 全绿不矛盾
（cases 零上游，触发不了 429 分支）。→ **缺失项 I-5**（行为差异需上游侧注入才可观察，
建议 t6 维护者专项核对，或在 harness 加 mock 上游 429 注入 case）。

### P-8 · 领取/激活/安装序链（82e5eab/bfd37b9/5ec24ae/e9c58df/1007068/65a547b/dd7ffdb）—— 🟠 **缺失（t13 范围）**
**语义**：入池即激活 + 自动领取全部可领套餐 + 首启安装序仿真 + 激活事件上报 + 领取失败全链路
留痕（无套餐/验证码失败不再静默）+ 网络错误转业务回执。**契约**：部分端点有记（B 组 claim），
但"入池自动领取全部""安装序仿真"是**编排行为**。**Go 覆盖**：adminapi 未落地（t13 进行中），
solver 客户端已备（t7）。→ 属 **t13 实现范围**，非"重写遗漏"，列此供 t13 不丢语义。

### P-9 · 指纹池（388e6de/e9d6aec）—— 🟢 **契约已记形态，Go 部分覆盖**
**语义**：设备指纹从固定模板→随机合规档案、每账号独立指纹池（消灭同设备关联信号）。
**契约**：✅ endpoints B-02 fingerprint 7 字段 + B-08 rotate 端点（实测形态齐全）。
**Go 覆盖**：⚠️ models/store 透传 fingerprint 结构（无损）；生成/轮转策略在 t13。
→ 形态覆盖，策略待 t13。非静默回退风险（结构在契约里锁死）。

### P-10 · 赠送池 UI/耗尽判定（0240c23/a17b1b4/c5e62bb/dd3608f）—— 🟢 **已覆盖**
**语义**：one_time 活动赠送池并入 balance 后去重渲染、纳入耗尽判定、多套餐全量保留、
同名窗口合并、总额度计入活动赠送。**契约**：这些**结果**体现在 quota 结构（P-1 同源）
与 status 计数，已随 P-1 的 `[实测]` 形态入库。**Go**：quota/status 透传 + status 聚合。
→ 已覆盖（依赖 P-1 的解析正确落地）。

### P-11 · 订阅百分比窗口解析（quota.py，dynver 系列内发现）—— 🟠 **缺失（契约半盲区）**
**业务语义**（quota.py diff 代码核实，许可内）：新增 `_parse_subscription` +
`fetch_subscription_quota`（调 `/api/monitor/usage/quota/limit`）——上游 **2026-02-12 后
不再返回绝对 token 数**，订阅窗口改以「已用百分比」呈现（`limits[]` 按 `nextResetTime`
升序 = 5h 窗、周窗；`TOKENS_LIMIT`/`TIME_LIMIT`(MCP 次数) 两类）；解析按多来源字段名
并集宽容取值（`percentage/used_percent/usedPercent/usage/…`），任一缺失不抛错
（「宁可少显示，不可让整条刷新链失败」）。
**是否契约**：⚠️ 半记录——`account-model.md:370` 记了分化规律「subscription 只对
apiKey 账号填充」，但 **:371 明言「非空结构（如 subscription.level 非 null）未实测」**
→ 窗口数组的**具体键形态不在契约里**（生产 6 账号无一触发非空 subscription 或采样时机问题）。
**Go 覆盖**：❌ 无 subscription 解析实现（属 t13 刷新链）。→ **缺失项 I-8**：
t13 若只实现 balances+entitlements 两源，apiKey 账号的订阅窗口将恒空——**与老服务
当前 live 的差异不可被对拍发现**（老服务该结构同样非空未锚，双侧都可能空 → L3 相等）。
实现建议：随 t13 照 §来源 B 的三源解析（balances / plans[].entitlements[] /
subscription percent），并在有 apiKey 账号数据时补一次契约实测把非空形态钉进
account-model §5.3。

---

## 三、三类清单汇总

### ✅ Go 版已覆盖
- **P-5** max_tokens 钳制 131072（`internal/gateway/helpers.go:26`）
- **P-1** entitlements 输出形态（契约已记 + models 无损透传；**但解析链落地待 t13 核**）
- **P-6** 监控页结构（t9 交付；数据 API 属 t13）
- **P-9** fingerprint 形态（契约锁死 + models 透传；生成/轮转属 t13）
- **P-10** 赠送池/耗尽（随 P-1 形态入库）

### 🔴🟠 Go 版缺失（按危险度排序）
- **I-4（最高危）** reasoning_content 透传 —— 契约零提及 + 对拍零 case + Go 零实现（实测三重确认）
- **I-3** captcha 惰性化 —— 契约 endpoints:103/594 还写着旧策略，与 live 矛盾
- **I-2** 动态版本门槛 —— 上游侧新行为，契约不可见
- **I-1** entitlements 解析链落地 —— 契约锁形态不锁解析，t13 若照原版只读 balances 仍丢包
- **I-5** 429/Retry-After/指数退避/连击去重 —— 上游策略，需专项核对
- **I-6** 领取/激活/安装序编排 —— 属 t13 范围，列此防丢语义
- **I-7** 双通道拆分 —— **§六确证一已升为确证**（diff 实读）；双 key 与 darwin 伪装维持
  线索级（X-1，许可清单未覆盖 identity/constants/models/store/settings）
- **I-9（新，§六确证一）** picker 三缺口：请求→通道候选集过滤（resolve 失败即 503
  而非试下一号）、bigmodel 通道跨 provider apiKey 池、exhausted 免费池豁免——
  t6 SCOPE NOTE 自认部分属后续任务，但 b/c 两点是老服务补丁特有语义，**契约对拍测不到**
- **I-8** 订阅百分比窗口解析（P-11，quota.py 代码核实；契约自述"非空未实测"=半盲区）

### ⚪ 不适用
- 无。补丁均为功能性，无"仅 Python 运维便利"类可忽略项。（部署脚本 `deploy-*.sh`、
  抢包 shell `z2a_patrol.sh` 等是**老仓 scripts/ 的运行时编排**，Go 版由 systemd unit +
  captcha 客户端替代，属"外部等价物"而非源码补丁，不计入本清单——见文末附录 B。）

---

## 四、缺失项实现建议（**只给方向，不改代码**）

> 均为**建议**，实际落地归属 t13（adminapi）/ 网关维护 / 契约维护，非本任务 scope。

- **I-4（reasoning_content）**：`internal/gateway/chat.go` 的 `content_block_delta`
  分支补 `thinking_delta → {"reasoning_content": ...}` 映射，非流式补 thinking 块收集。
  **同时补对拍 case**：现 harness 对 SSE delta 内容零覆盖（grep 实测 0 命中）——
  建议流式成功 case 的断言集包含"reasoning_content 出现次数>0（thinking 型上游）"
  与"thinking 缺席时正文不混入"两条，破单样本折叠。**判据建立前须确认上游有配额**
  （本次实弹撞 1005 配额尽，见 P-4）。

- **I-3（captcha 惰性化）**：t13/t7 组合的接线策略问题。Go 版领取/刷新链取 verify_param
  应走"用到才解"（t7 client 单次中位 ~2.8s 的现解成本，配 CAPTCHA_POOL_MIN=0 语义）；
  挑战自愈分支（unusual_activity/3012 重试）按记忆库《提速两修复》的形态保留。
  **前置**：契约 A 组可补一条"首包延迟无 captcha 阻塞"的行为注记（可选），
  至少让实现者知道 live 现行策略是软校验+惰性。

- **I-2（版本门槛）**：需一个 `release_version.current_billing_version()` 等价物
  （现行版来源：上游 configs 接口或常量），在 billing 请求头注入；**先小样实测确认
  Go 版走的通道是否真有版本门槛**（可能 GLM-5.3 无门槛、只有活动包领取链有 → 则归 I-6）。

- **I-1（entitlements 解析）**：t13 的 quota 刷新必须解析 `plans[].entitlements[]`
  含多包累加/最晚过期，产出契约 8 键；models 已无损透传，无需改结构。

- **I-5**：`internal/upstream` / gateway 转发链专项核对 Retry-After 尊重 + 指数退避曲线 +
  连击去重；建议对拍加"429 带 Retry-After"注入 case（现 cases.json 未覆盖此分支）。

- **I-7**：双 key/双通道/darwin 伪装需**先获得读代码许可**或读对应 commit diff 才能定性；
  本任务按规格先于实现约束未展开，列作待授权核对。

- **I-8（subscription 窗口）**：t13 刷新链按**三源**实现（balances / plans[].entitlements[]
  / subscription percent，P-11）；字段名并集宽容 + 单字段缺失不炸整链的容错形态照
  quota.py diff 已核实的语义复刻。出现非空样本时补一次契约实测（转维护者钉形态）。

- **I-9（picker 三缺口）**：`pickAccount` 增加按请求通道过滤的候选谓词（zcode 前缀=JWT 号、
  bigmodel 前缀=**全 provider 的 apiKey 号**且 `include_exhausted=true`）；resolve 失败/不适配
  的号**跳过续试**（对齐老服务 failover 语义）。签名改动局限 picker.go 两函数，
  建议与池写状态任务（t6 SCOPE NOTE 认账的 later task）同批落地。

- **I-4 补充（§六确证五已给字段级规格）**：thinking_delta→reasoning_content 映射与
  非流式 join 规格照抄 §六确证五，无需再考古。

---

## 五、方法与边界
- **规格先于实现**：仅 quota.py 补丁相关 diff 被读取（任务明许可）。其余补丁语义来自
  commit message / README / 记忆库运营档案 / **契约实测** / **对老服务的黑盒 live 探测**。
- **两类盲区的判据**：一条补丁语义"是否被契约覆盖"，不能只看 endpoints.md 有没有写，
  还要看对拍体系（harness/normalize/cases）对该维度**有没有任何 case**——
  `reasoning_content` 正是"契约零提及 + 对拍零 case + Go 零实现"三重零的样本
  （三处 grep 全 0 命中，本次实测）。
- **本报告不修改 internal/ 或 cmd/ 任何文件**（验收要求），只产出此 md。
- 〔未代码核实〕项：初版 47/49 commit 仅由 message 定性；**§六（captain diff-only 许可后）
  已把 openai_compat/gateway×3 系列/claim/quota 升为代码确证**，identity/双 key/其余
  仍为线索级（X-1 列明许可边界原因）。

---

## 附录 A：来源 C 本地 commit 全表（49 条，`git log 3f54f14..HEAD`）
见本报告生成时 `git log --oneline` 输出；主题归类见 §一来源 C。完整 hash：
f4f6dc6/24cb92a/1007068/dd7ffdb/65a547b/e9d6aec/388e6de/e9c58df/5ec24ae/82e5eab/
fcf826a/bfd37b9/a17b1b4/6c89af3/0240c23/dd3608f/7e07a27/bc61038/49d566d/e96729c/
cf05761/c5e62bb/61c2f92/b3ecde6/264c668/99e0771/b5c99e0/112d1f1/84e714e/64ec67e/
0eab254/…（余下以 `git log 3f54f14..HEAD` 为准）。

## 附录 B：老仓 scripts/（运行时编排，非源码补丁，不计入等价清单）
`z2a_patrol.sh`、`z2a_claim_job.sh`、`z2a_claim_report.py`、`z2a_schedule_claim.sh`、
`z2a_alert_mail.py`、`weekend_claim_watch.sh`、`deploy-*.sh` —— cron/外部编排层，
Go 版由 systemd（`zhipucode2api.service`）+ 领取链替代；其"升级上游后需重打补丁"的
运维含义由 §一来源 A/B 承担。

---

## §六 I-7 升级确证（captain diff-only 许可后追加，2026-10-16）

> 许可范围：`openai_compat.py / routes/gateway.py / claim.py / fingerprint.py` 的
> **.bak↔current 变更块**（含系列区间对 diff）。`app/identity.py` 不在许可清单内
> ——112d1f1（darwin 伪装）维持线索级不升级，见 X-1。
> 诚实注记：本节初稿曾写"picker 无 mode 过滤（grep 0 命中）"——**提交前实跑 grep 推翻**，
> picker.go 实际有通道分类；下文按真实覆盖状态修正为"部分覆盖+三点具体缺口"。
> （这是本项目第三次把臆测拦在交付前；教训固化：grep/行号类断言必须先执行再落笔。）

### 确证一 · 双通道拆分（gateway.py .bak-chansplit→current 变更块实读）
补丁语义（老服务侧）：
- 通道→账本绑定：请求侧 provider=zai → **只用 JWT 号**（活动/补偿包账本，需验证码）；
  provider=bigmodel → **只用 apiKey 号**（订阅/免费池账本）。两套账本互不打通，
  混池实测代价 = 先撞 2 个 1113 欠费号再切换，每次白花数秒且日志误导。
- **反直觉点 1**：bigmodel 通道候选集**跨 provider** 取全部 apiKey 号（4 把 apiKey 实测
  都能打 open.bigmodel.cn；只认 provider 字段会把 2 把 OAuth 兑换的 zai-key 无谓排除）。
- **反直觉点 2**：**exhausted 状态在 bigmodel 通道放行**（exhausted 按付费模型余额判定，
  免费池 glm-4.5-flash 实测全 200 可用）。

**Go 覆盖核对（picker.go 全文实读，本次）**：**部分覆盖**——
- ✅ 已有：账号→通道的凭证分类（`resolve` L172-184：zai+JWT→ChannelPlan、
  zai+apiKey→ChannelZaiKey、bigmodel+apiKey→ChannelBigModel；mode 大小写敏感 A-2 ✓）。
- ❌ **缺口 a**：pickAccount **不按请求模型/channel 过滤候选集**——全局轮询取一号再
  resolve，resolve 失败**直接 503**（L115 单号即返），而非老服务式"跳过该号继续试"。
  （t6 SCOPE NOTE L88-96 自认此点属后续池任务——此处补上 chansplit 语义侧的必要性证据。）
- ❌ **缺口 b**：bigmodel 通道无跨 provider apiKey 池（Go 候选仅 provider==bigmodel，
  zai+apiKey 号永远走 api.z.ai）——老服务 4 把 key 的池 Go 只剩 2 把。
- ❌ **缺口 c**：candidateOf 对 exhausted 一律剔除（L137 default 分支），无老服务
  bigmodel 通道的 exhausted 豁免（免费池仍可用）。
**归属建议**：a→pool/t13 后任务（与 failover 循环同批）；b、c→picker 扩展候选谓词
（search_providers + includeExhausted 两参数，签名改动小）。**切换前风险**：
bigmodel 前缀请求在 Go 池上候选减半、exhausted 号被误拒。→ 记入缺失清单（覆盖行级：
"部分覆盖-有具体缺口"）。

### 确证二 · 上游业务错误 502 形态（gateway.py bak-bizerr→bak-chansplit 区间）
变更块实锤：上游 **HTTP 200 承载业务错误体**（`{"code":1005,"msg":…}`，套餐不含该模型时
上游不打 4xx）→ 网关转 **502** `{"error":{"message":<原文>,"type":"upstream_error","code":<int>}}`
（原实现误报"上游响应格式异常"502 掩盖真因）；**stream:true 的 SSE 行内 JSON 错误**同样识别
下发（原实现静默丢→客户端"空回复"）。本次 t18 实弹撞到的 1005 流内错误行（P-4 证据节）
= 该链 live 输出，两侧证据闭合。
**契约关系**：`compat-risks.md` CR-33 记了该形态存在但"状态码待查"→ **本 diff 定案 502**，
转契约维护者回填。**对拍关系**：GW cases 零上游、无注入分支 → 漂移测不到，
建议 mock 上游注入 case（状态 502 + body 形态断言）。
**Go 覆盖**：t6 gateway 有 502/upstream_error 映射（契约 CR-33 对齐实现），此条**已覆盖**
（形态由对拍 L1+body 锚过）；残余风险仅"注入分支无 case"。

### 确证三 · captcha 惰性化实现形态（bak-lazycaptcha→bak-connfix 区间）—— I-3 确证
`if needs_captcha:` → `if needs_captcha and captcha_retries > 0:`；首请求不带 token，
挑战信号（`_detect_captcha_challenge` 三形态）才取 token 重试；自愈分支保留、
unusual_activity/3012 判定不受影响；上游收紧时挑战分支自动兜住。
connfix 区间仅日志文案（NO_PROXY 加固在 systemd unit 侧，记忆库已录）。
→ I-3 由"记忆库描述"升为"diff 实锤"；**Go 版接线时 t7 client 的调用时机必须按此形态**
（用到才解，POOL_MIN=0 语义），写死"总是取"即首包回退 20~40s。

### 确证四 · 版本门槛领取链（claim.py .bak-dynver→current）—— I-2 确证
`preview/claim/激活事件上报` **三处同源** `release_version.current_billing_version()`；
diff 注释原话：日活上报版本与领包版本错配 = **复刻门槛失效病根**；两次 attempt 复用
同一版本（取一次防漂移）；`X-ZCode-App-Version` 头与 `app_version` query 参均走动态值。
→ Go 领取链（t13/t21 后）必须实现"动态现行版+三处同源"，写死常量即回退"无可领套餐"
假阴性（记忆库 814 次巡逻事故形态）。

### 确证五 · reasoning 映射精确形态（openai_compat .bak-reasoning→current）—— 字段级规格
- 流式：`delta.type=="thinking_delta" && delta.thinking(str)` → chunk `{"reasoning_content": …}`
- 非流式：收集 `block.type=="thinking"` 的 `thinking` 串 → `message.reasoning_content="".join(parts)`
- signature_delta 不在映射面（透传与否为 Go 侧自由，契约未锁）。
→ I-4 的实现建议自本节起由"方向"升级为**字段级映射规格**——t21 实现者无需再考古。

### X-1 · 维持线索级（许可清单未覆盖，如实保留）
- **112d1f1 darwin 固定伪装 + 剔除 x-stainless**：diff 实际触及 `app/identity.py`
  （**不在 captain 批准清单**——批准的是 openai_compat/routes-gateway/claim/fingerprint）。
  commit message 明说后果："Linux 部署时 X-Platform 暴露真实平台、constants 伪装值成死代码"。
  Go 版 `picker.headers()` 按 account.fingerprint 出 `x-platform`（有号时正确）；**无指纹号时
  Go 不发 x-platform，老服务发 darwin 固定值**——差异存在但需 identity.py 许可才能定性，
  列作待授权核对（不升级）。
- **双 key 系列（2key .bak）**：触及 constants/models/store/settings（均非许可清单）→ 不定性。
  旁证：chansplit 中"4 把 apiKey"与 2key 同日出现，疑同波配置面改造，仅此登记。
- **388e6de 指纹池生成器本体**（fingerprint.py 6 套设备档案模板）：文件在许可清单内，
  但契约 B-02/B-08 已锁对外形态、接线三出已由 B3/C diff 旁证（`_auth_headers` 注释
  "按账号指纹档案出值"）→ 风险低，未展开 diff（生成域非服务面）。
