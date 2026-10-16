# I-3 captcha 调用时机接线契约（供 t13 接线者免考古）

| 项 | 值 |
|---|---|
| 产出者 | Hilkiah（架构师 · 契约冻结与拓扑），任务 **t23** |
| 日期 | **2026-10-16 01:17 CST**（`date` 实测） |
| 基线 | `1f39731`（`git rev-parse --short HEAD` 实测） |
| 性质 | **契约定义 + 归属厘清 + 引用纠正**。纯只读探测，未改 `internal/`、`cmd/` 任何文件 |
| 服务对象 | **t13 接线者（Catherine）**，以及未来任何实现 captcha 调用时机的人 |

> **本轮不读边界声明**：本报告的"惰性化代码形态"**来自黑盒观测** —— 我未纳入本轮观测 `zcode2api/app/**/*.py`、
> `cli.py`、`captcha_node/`、`frontend/`（观测边界）。形态来自 ①t18 的 `.bak` diff 实锤（二手，标 `[diff]`）
> ②记忆库逐字记录（标 `[记忆]`）③**我自己在黑盒测试与契约里的一手实测**（标 `[实测]`/`[黑盒]`）。
> 来源等级逐条标注，不混淆。

---

## §0 结论摘要（TL;DR）

**立题存在一个盲点，而它恰好决定了接线对不对：**

> **captcha 的调用时机在两条链路上语义相反。** 立题的单一表格会诱导实现者把
> "惰性化（用到才取）"套到 **claim 链路**上 —— 而 claim 链路**必须每次都取**。

| # | 结论 | 标记 |
|---|---|---|
| **C-1** | 惰性化的真实落点是 **`app/routes/gateway.py`（对话网关链路）**，不是 claim 链路 | `[记忆]`逐字 + `[diff]` |
| **C-2** | **claim 链路（`/admin/api/claim`）本来就"每次取"**，黑盒锚 `solve_count == 1`；惰性化**不适用**于它 | `[黑盒]` |
| **C-3** | t18 把 I-3 归属写成"claim/pool 接线"，与它**自己表格里**的 `routes/gateway` **自相矛盾** → 归属漂移 | `[实测]` |
| **C-4** | **Go 侧的真实缺口在 `internal/gateway/`**：t6 完全不接 captcha（**0 行代码**，仅 5 处注释） | `[实测]` |
| **C-5** | Catherine 的 `adminapi`（claim 链路）**每次取是对的**，不是缺陷 | `[实测]`（只读观察） |
| **C-6** | 契约里 captcha **调用时机零描述**；唯一的相邻句是 `endpoints.md:649`「403 验证码挑战 → 原账号重试」，它只覆盖**重试**、不覆盖**何时取 token** | `[实测]` |
| **C-7** | `POOL_MIN=0`（关闭预解池）与「惰性化」是**两件事**，t18 把二者绑为"配 POOL_MIN=0 语义"属概念混用 | `[实测]`+`[记忆]` |

---

## §1 立题修正：为什么必须按链路分表

### 1.1 两条链路的 captcha 语义相反

| 链路 | 端点 | token 从哪来 | 求解次数 | 惰性化适用？ |
|---|---|---|---|---|
| **对话网关** | `/v1/messages`、`/v1/chat/completions` | **首请求不带**；仅收到挑战信号才取 | 无挑战时 **0**；有挑战才 ≥1 | ✅ **这就是惰性化的对象** |
| **领取（claim）** | `/admin/api/claim` | **每次请求都要带**（主动发给上游 billing） | **每次 claim = 1**；3007 被拒 → 2 | ❌ **不适用** |
| **手动领取** | `/admin/api/claim/manual` | **转发调用方 token**，solver 不参与 | **恒 0** | ❌ 不适用（本就不取） |

**证据（一手黑盒锚点，`zcode2api/tests/integration/test_claim.py`）**：

| 用例 | 断言 | 行 | 含义 |
|---|---|---|---|
| `test_claim_success_sends_captcha_and_plan` | `stub.solve_count == 1` | L94 | **claim 每次都取**（1 次） |
| `test_claim_captcha_rejected_retries_once` | `solve_count == 2`、`invalidated == 1` | L117-118 | 3007 被拒 → **换码重试一次** |
| `test_claim_already_claimed_no_retry` | `solve_count == 1  # 非 3007 不重试` | L206 | **只有 3007 才重试** |
| `test_manual_claim_success_forwards_browser_param` | `solve_count == 0` | L303 | manual **转发**，solver 0 次 |
| `test_manual_claim_without_param_rejected` | `solve_count == 0` | L332 | 缺参数直接业务失败，不取 |

> ⚠️ **这是本报告最重要的一行**：claim 链路**没有**"首请求不带 token"这回事。
> 若实现者按立题表格在 claim 链路写"先不带、等挑战再取"，**领取会直接失败** ——
> 上游 billing 需要 `x-aliyun-captcha-verify-param`（`endpoints.md:463`：该参数被**原样转发**到上游头）。

### 1.2 惰性化的落点证据

**记忆库逐字**（`[记忆]`，文件 `ZCode_提速两修复：captcha_惰性化…（2026-09-30）.md`）：

> 【修复一·captcha 惰性化（**app/routes/gateway.py**）】首请求不带 token，仅当收到挑战信号才取 token 重试：
> `if needs_captcha and captcha_retries > 0:` 改为条件式调用（原为无条件 `if needs_captcha:`）。
> 挑战自愈路径完整保留（`_detect_captcha_challenge` 三形态 → `captcha_manager.invalidate()` → 取新 token → continue 重试），
> unusual_activity/3012 风控判定不受影响。若上游未来收紧校验，挑战分支自动兜住。

**t18 diff 实锤**（`[diff]`，`local-patches-semantics.md:342-348`）：`if needs_captcha:` → `if needs_captcha and captcha_retries > 0:`。

**t18 自己的表格**（`[实测]`，同文件 L33）：`| lazycaptcha | **routes/gateway** | 2026-09-30 | captcha 惰性化（首包 20~42s→0.9s）|`

⇒ **两处独立证据都指向 gateway 链路。** 立题/归属漂移见 §3。

### 1.3 惰性化为什么安全（软校验实测定案）

`[记忆]` 决定性实验（`ZCode_提速两修复…md` L14）：

> **A 组**不带 `X-Aliyun-Captcha-Verify-Param` × 6 → **全部 200**；
> **B 组**带真 token × 6 → 也全部 200（多花 2.4s 求解）；扩展验证不带 token × 11 全过。
> 且 grep 网关日志『验证码挑战』出现次数 = **0**（`_detect_captcha_challenge` 从未触发）。
> → 上游当前对 zcode-plan 通道的 captcha 校验是**"带了才验、不带就跳过"的软性形态**。
> 关键：求解发给**阿里云（第三方）**，不是 zcode；zcode 只看 token 有效性，
> 故**带 token 不增加 zcode 风控面，但增加延迟**。

> ⚠️ **时效性警示**：该定案为 **2026-09-30** 实测。上游可能收紧。契约必须让**挑战分支自动兜住**（§2.2），
> 这样即使软校验变成硬校验，行为仍正确 —— 只是延迟回退。**"软校验"是观测事实，不是可以依赖的设计前提。**

---

## §2 调用时机契约（按链路，可直接照此接线）

### 2.1 对话网关链路 —— 惰性化（**这是 I-3 的对象**）

| 场景 | **正确行为** | 依据 |
|---|---|---|
| **首个请求** | **不带** captcha token（不发 `x-aliyun-captcha-verify-param`） | `[记忆]` L15 + `[diff]` |
| **上游返回成功（200）** | 直接透传，**求解次数 = 0** | `[记忆]` A 组 6/6 + 11 全过 |
| **收到挑战信号** | **才取** token → `invalidate()` 旧池 → **原账号重试** | `[记忆]` L15 + `endpoints.md:649` |
| **挑战自愈路径** | **完整保留**，不被惰性化削掉 | `[记忆]` L15 |
| **`unusual_activity` / `3012` 判定** | **不受影响**（→ `disabled`，`risk_strikes+1`） | `[记忆]` L15 + `account-model.md:79` |
| **上游未来收紧** | 挑战分支**自动兜住** | `[记忆]` L15 |

**单侧判据（可执行，零配额成本）**：

| # | 判据 | 期望 | 可证伪性 |
|---|---|---|---|
| **G-1** | **无挑战场景**：正常对话请求后，solver 调用计数 | **= 0** | 构造：mock 上游恒 200 → 若计数 > 0，判据红 |
| **G-2** | **有挑战场景**：上游返回 403 验证码挑战后，solver 调用计数 | **= 1** 且带**正确三实参**（scene `11xygtvd` / region `cn` / prefix `no8xfe`，`endpoints.md:446`） | 构造：mock 上游恒挑战 → 若计数 = 0，判据红（**证明挑战分支没接上**） |
| **G-3** | 挑战后重试用的是**原账号**（不换号） | 账号 id 不变 | 换号则红 |

> **G-1/G-2 是一对，必须同时存在。** 只有 G-1 会漏掉"挑战分支根本没实现"（恒 0 也能过 G-1）；
> 只有 G-2 会漏掉"总是取"（恒 1 也过不了 G-1）。**单侧判据 = 半个判据。**

### 2.2 领取（claim）链路 —— **反向**：每次都要取

| 场景 | **正确行为** | 依据 |
|---|---|---|
| **每次 `POST /admin/api/claim`** | **取** token（1 次）并放进上游头 | `[黑盒]` `solve_count == 1`（L94） |
| **上游返回 3007（验证码校验失败）** | `invalidate()` + **换码重试一次**（总计 2 次求解） | `[黑盒]` L117-118 |
| **非 3007 的业务失败**（如"已经领取过"） | **不重试**（`solve_count` 保持 1） | `[黑盒]` L206 |
| **求解最终失败**（`CaptchaSolveError`） | **200 业务回执** `ok:false` + message 含"验证码求解失败"，**不是 500** | `[黑盒]` L208-222 |
| **manual 路径** | **转发**调用方 `captcha_verify_param`，solver **0 次** | `[黑盒]` L303/L332 |

> **Catherine 当前实现与本节一致**（只读观察，见 §6.1）：`claim.go:442` 在 `for attempt := 1..2` 循环**内**调 `solveCaptcha`；
> `claim.go:456` `if code == 3007 && attempt == 1 { invalidateCaptcha(); continue }`；`claim.go:647` manual 路径注释 `solver invoked 0 times`。

### 2.3 `CAPTCHA_POOL_MIN` 语义（**与惰性化解耦**）

`[实测]` 契约 `env-contract.md §3.4`（L206-219）：

| 值 | 语义 | 现状 |
|---|---|---|
| `0` | **关闭预解池** → 不预热 → 每请求走"同步现解" | 历史值 |
| 非 0（**实测 `3`**） | **维持池下限**（池开启），后台 `_refill_loop` 预热 | **现行值**（我实测 `.env` = `3`；契约 §3.4 亦实测 `3`） |

`[实测]` `.env` 逐行：`CAPTCHA_POOL_MIN=3`（第 29 行）。
`[记忆]` 时间线（`ZCode_14秒延迟…md` L15）：惰性化当天"顺带把 `.env` 的 `CAPTCHA_POOL_MIN=0` **改回 3**"。

**🔴 必须写清的解耦**：

> **`POOL_MIN=0` ≠ 惰性化。** 惰性化 = **条件式调用**（`and captcha_retries > 0`）；
> `POOL_MIN` = **池是否预热**。二者正交。
> - 池**关闭**（0）+ **无条件**取 → 每请求同步现解 → **首包 20~42s**（这是补丁前的病）
> - 池**关闭**（0）+ **条件式**取 → 首包 **0.9s**（惰性化生效）
> - 池**开启**（3）+ 条件式取 → 后台预热 + 首包 0.9s
>
> t18 P-3 写"配 `CAPTCHA_POOL_MIN=0` 语义"把二者绑在一起 —— **Go 版不应照抄这个绑定**。
> Go 侧正确做法：**实现条件式调用**（惰性化的本体），`CAPTCHA_POOL_MIN` 只影响**池预热策略**（Go 若无池，则该值退化为配置兼容，见 §6.2）。

### 2.4 重试耗尽

| 链路 | 耗尽后行为 | 依据 |
|---|---|---|
| **gateway** | 回传上游 `status` + `content-type` + **原文** | `endpoints.md:649` |
| **claim** | **200 业务回执** `ok:false`（非 500） | `[黑盒]` L208-222 |
| **求解器自身** | 超时 40s / 尝试 4 次（`ZCODE_CAPTCHA_TIMEOUT`/`ZCODE_CAPTCHA_RETRIES`） | `captcha-spike.md:274`、`env-contract.md:340`（E-13） |

> ⚠️ **常量冲突（未实测）**：`ZCODE_CAPTCHA_RETRIES` 默认 **4**（`.env.example`）vs `MAX_CAPTCHA_RETRIES` = **3**（架构文档）
> —— `account-model.md:106-107`、`env-contract.md:241`、`compat-risks.md:1003`（CR-21）。
> 二者**是否同一参数未判定**（`account-model.md:427` 标 `unverified`）。
> t7 已把 Go 侧 `Options.Attempts` 钉死为"**每次 Solve 的总尝试次数**"（`internal/captcha/doc.go` CR-21 注记）。

---

## §3 归属厘清：I-3 不是 `internal/captcha` 的问题

### 3.1 归属三层，逐层实测

| 层 | 现状 | 实测证据 |
|---|---|---|
| **① `internal/captcha/`（t7）** | ✅ **已交付、单测覆盖**。**零惰性/池代码**（正确 —— 它是无状态求解器客户端） | `grep -rniE 'lazy\|once\|sync\.\|cache\|refresh\|pool\|min' internal/captcha/*.go` → 仅 `MinParamLen` 命中，**无惰性/池** |
| **② `internal/gateway/`（t6）** | ❌ **完全不接 captcha：0 行代码，仅 5 处注释** | `git grep -n 'internal/captcha' HEAD -- '*.go'` → **零命中**；`grep -rn captcha internal/gateway/ internal/upstream/` → 5 处**全为注释**（`picker.go:26/60/92`、`gateway.go:26`） |
| **③ `internal/adminapi/`（t13，在制）** | 🟡 **已 import**（`wt/task-adminapi` @ `aeb5ba3`） | `.worktrees/task-adminapi/internal/adminapi/adminapi.go:18` `"zhipucode2api/internal/captcha"` |

> **注释自述**（`picker.go:92`）：`// MAX_ACCOUNT_ATTEMPTS, captcha retry - are a LATER task and deliberately` ——
> t6 作者**主动**把 captcha 留给后续任务。所以这不是"漏了"，是**范围切分**。

### 3.2 归属漂移（t18 报告内部自相矛盾）

`[实测]` `local-patches-semantics.md` 同一份报告内：

| 位置 | 说法 |
|---|---|
| **L33**（表格，文件归属列） | `lazycaptcha` → **`routes/gateway`** |
| **L84**（P-3 标题） | `captcha 惰性化（lazycaptcha）` |
| **L95-96**（P-3 结论） | "Go 版 **claim/pool 接线**时 captcha 策略必须按惰性+软校验实现" |
| **L234**（任务书） | "**t13/t7 组合**的接线策略问题。Go 版**领取/刷新链**取 verify_param 应走'用到才解'" |

⇒ **表格说 gateway，结论说 claim。** 而 §1 已证：claim 链路**必须每次取**，
把"用到才解"套到 claim 上 = **领取功能直接坏掉**。

**为什么这个漂移危险**：t13 恰好是 adminapi（claim 链路）。若接线者照 L95/L234 行事，
她会把"惰性化"实现进 claim 链路 —— 而她**已经写对了**（§6.1），漂移会把她**从对的方向拽到错的方向**。

**正确的 I-3 归属**：

> **I-3（captcha 惰性化）的 Go 落点是 `internal/gateway/`（对话网关链路），
> 需要 t6 的后续任务补齐（`picker.go` / `gateway.go` 自述的 "LATER task"）。
> 它**不是** t13 的职责，也不该被实现进 claim 链路。**

---

## §4 🔴 反向约束（防未来实现者踩坑）

> **写死「总是取 token」= 首包回退 20~40s，且不增加安全性。**

**逐条展开**：

| 断言 | 依据 |
|---|---|
| 单次求解 **2.4s（快）~ 42.5s（第 1 次未果重试后）**，极不稳定 | `[记忆]` L13 |
| 补丁前（无条件取）首包 **20~42s**；补丁后（条件式）**0.8~0.9s** | `[记忆]` L13/L16 + `[diff]` |
| 不带 token **× 6 → 全部 200**，扩展 **× 11 全过** | `[记忆]` L14 |
| 挑战信号触发次数 **= 0**（`_detect_captcha_challenge` 从未触发） | `[记忆]` L14 |
| 求解发给**阿里云（第三方）**，zcode 只看 token 有效性 ⇒ 带 token **不增加 zcode 风控面** | `[记忆]` L14 |

⇒ **"总是取"的净效果 = 纯延迟成本、零安全收益。** 实现者若凭"总是取更保险"的直觉写，
会在**无任何对拍报警**的情况下（契约/对拍**均无延迟维度**）把首包拖回 20~40s。

> 🔴 **且这条不会红**：`normalize.rules.json` 只比响应体，延迟不进任何断言
> ⇒ 这是一个**对拍永远看不见的回归**。所以本约束必须写进契约，不能指望对拍兜住。

**反向约束（写死给实现者）**：

```
✅ 对话网关链路：首请求不发 x-aliyun-captcha-verify-param；
                仅当上游返回挑战信号（403 验证码挑战 / unusual_activity / 3012 语境）才 Solve 并原账号重试。
❌ 禁止：无条件（"总是取"）求解 —— 首包 20~40s，零安全收益，对拍看不见。
❌ 禁止：把"用到才解"套到 claim 链路 —— claim 每次都必须带 token（否则领取直接失败）。
```

---

## §5 引用纠正：t18 L204 的 `endpoints:103/594` 不是 captcha 策略

### 5.1 被引位置的真实内容（`[实测]`，基线 `1f39731`，`endpoints.md` 共 891 行）

| 被引 | 实测内容 | 是 captcha 策略吗 |
|---|---|---|
| **`endpoints:103`** | **§1.4 响应头契约**（当前 L100 起）：`server: uvicorn`、`content-type: application/json` 等**响应头表** | ❌ **完全无关** |
| **`endpoints:594`** | **§A-02 请求头表**（当前 **L595** 起）：`POST /v1/messages` 的 `Content-Type`/`authorization` 行 | ❌ **无关**（且行号已漂移 1 行） |

> 注：t1 冻结时这两处曾在 L103/L594；当前文件已增长到 891 行，章节头分别在 **L100** 与 **L595**。
> 无论按旧号还是新号，**内容都不是 captcha 策略**。

### 5.2 契约里 captcha 的**真实**锚点（全量 grep）

`[实测]` `grep -rn 'needs_captcha|captcha_retries|总是取|惰性|挑战信号' docs/contract/*.md` →
**唯一命中** = `adr-008-local-patch-equivalence.md:43`（I-3 索引行本身）。

`[实测]` `grep -n 'captcha|验证码' docs/contract/endpoints.md` → 命中 8 处，**逐处都不是"调用时机"**：

| 行 | 内容 | 是否覆盖"何时取 token" |
|---|---|---|
| **649** | `403 验证码挑战 → 原账号重试；耗尽后回传上游 status + content-type + 原文` | 🟡 **部分**：覆盖**重试**，**不覆盖"首请求不带"** |
| 441 | `### B-15 GET /admin/api/claim/captcha-config` | ❌ 配置读取端点 |
| 448 | 黑盒 `test_captcha_config_endpoint` 断言同 4 字段 | ❌ |
| 456 | manual claim 请求体含 `captcha_verify_param` | ❌ |
| 463 | `captcha_verify_param` 被**原样转发**到上游头 | ❌ 转发形态，非时机 |
| 648 | `401/403（非验证码）→ invalid` | ❌ 错误分类 |
| 85 | 错误码枚举 `captcha_error(500)` | ❌ |
| 864 | 路由矩阵行 | ❌ |

### 5.3 准确表述（**供 t18 报告作者自行复测定稿**）

> **契约对 captcha 调用时机（"首请求不带 token / 挑战信号才取"）零描述。**
> 唯一相邻句是 `endpoints.md:649`，它只覆盖**挑战后重试**这一半，
> **不覆盖**惰性化的本体（"首请求不带"）。
> 正确锚点若必须给：**`endpoints.md:649`**（部分覆盖）+ **`adr-008-local-patch-equivalence.md:43`**（索引行）；
> 其余任何 `endpoints:103/594` 类引用均**不成立**。

> 📌 **我不代改 t18 的报告**（`local-patches-semantics.md`）—— 该文件的作者应自行复测定位。
> 本报告提供**可直接引用的正确锚点**，不修改他人交付物。

---

## §6 接线检查点（供 t13 接线者直接照用）

### 6.1 ✅ Catherine 当前实现：claim 链路**已正确**

`[实测]`（只读观察，`wt/task-adminapi` @ `aeb5ba3`）：

| 检查点 | 位置 | 状态 |
|---|---|---|
| claim 每次取 token | `claim.go:442`（`for attempt := 1..2` 循环**内**调 `solveCaptcha`） | ✅ 与 `[黑盒]` L94 一致 |
| 仅 3007 换码重试一次 | `claim.go:456` `if code == 3007 && attempt == 1 { a.invalidateCaptcha(); continue }` | ✅ 与 `[黑盒]` L117-118 一致 |
| manual 不调 solver | `claim.go:647` 注释 + **L604** `verifyParam == ""` → 200 业务失败 | ✅ 与 `[黑盒]` L303/L332 一致 |
| 求解失败 → 业务回执非 500 | `solveCaptcha` **L923-932** 返回 `claimError`；路由层转 `ok:false` | ✅ 与 `[黑盒]` L208-222 一致 |
| solver 可选（nil 不崩） | `adminapi.go:46-50` `Solver *captcha.Solver` 注释 "Optional" | ✅ |

> **Catherine 的 adminapi 不违反本契约**（claim 链路本就"每次取"）。
> 本契约对 t13 的**唯一**新增要求是：**不要**把"惰性化"搬进 claim 链路（§3.2 漂移会诱导这么做）。

### 6.2 🟡 接线观察（非本任务验收对象，供 t13 自查）

`[实测]` `solveCaptcha`（`claim.go:923-932`）返回 **`param, "", nil`** —— region 恒为空串。
而 `claimHeaders`（`claim.go:785-786`）`if strings.TrimSpace(region) != ""` 才设
`X-Aliyun-Captcha-Verify-Region` ⇒ **claim 链路当前不发 region 头**。

对比 `[黑盒]` L88（`test_claim_success_sends_captcha_and_plan`）：
`assert headers.get("x-aliyun-captcha-verify-region") == "sgp"` —— mock 环境缺省用 `config.region`。

> **这是 t13 实现范围内的一个观察项**，不在 t23 的 inScope（我不改 `internal/`）。
> 我如实记录，供 Catherine 自行复测：**若上游对 region 有校验，缺头会导致 3007 或业务失败。**
> 我不下"这是缺陷"的定论 —— 上游是否校验 region **未实测**（标 `unverified`）。

### 6.3 契约缺口（Go 侧仍需补齐的接线）

| # | 缺口 | 位置 | 依据 |
|---|---|---|---|
| **G-4** | **gateway 链路完全未接 captcha**（惰性化的本体没有实现载体） | `internal/gateway/`（0 行代码） | `picker.go:92` 自述 "LATER task" |
| **G-5** | 契约**无**"首请求不带 token"条目（只有 L649 的重试半句） | `endpoints.md` | §5.2 |
| **G-6** | 对拍**无** captcha 调用时机 case（延迟维度不可见） | `scripts/conformance/cases.json` | §4 |
| **G-7** | `CAPTCHA_POOL_MIN` 在 Go 侧**无消费点**（Go 无池） | 全树 | §2.3；`env-contract.md:217` 要求"必须支持" |

---

## §7 未验项与前置条件（**不伪造绿**）

### 7.1 已实测（可复现）

| # | 结论 | 复现命令（本报告 §8 全列） |
|---|---|---|
| 1 | Go 侧 `internal/captcha` 零引用 | `git grep -n 'internal/captcha' HEAD -- '*.go'` |
| 2 | gateway/upstream 中 captcha 仅 5 处注释 | `grep -rn captcha internal/gateway/ internal/upstream/` |
| 3 | `.env` `CAPTCHA_POOL_MIN=3` | `grep -nE '^CAPTCHA_POOL_MIN' external/zcode2api/.env` |
| 4 | monitoring 字段 = **15**（含 `t_first`/`t_total`） | §8 命令 6 |
| 5 | `endpoints.md` 章节行号（100/595/649） | §8 命令 5 |
| 6 | 契约 captcha 调用时机零描述 | §8 命令 4 |
| 7 | 黑盒 `solve_count` 五个锚点 | `grep -n 'solve_count' test_claim.py` |
| 8 | Catherine 实现状态（只读） | `grep -n 'solver\|Solve(' .worktrees/task-adminapi/internal/adminapi/claim.go` |

### 7.2 🔴 未验项（附理由，**不标绿**）

| # | 未验项 | 为什么没验 | 若要验，需要什么前置 |
|---|---|---|---|
| **U-1** | **惰性化的延迟数字**（首包 20~42s → 0.9s） | ①monitoring 的 `t_first`/`t_total` 实测仅 **3 条样本**，且 **`ok` 全为 `False`**（全是失败路径：`model not allowed` / `exceed quota limit`）⇒ **无成功路径样本** ②需真实对话流量 + claim 写 monitoring ③**会消耗真实账号额度**，须 captain 授权 | ①成功路径样本（真实对话）②或独立计时探针（绕过对拍）③captain 的额度授权 |
| **U-2** | **挑战信号"三形态"的具体判定条件** | 记忆库只写"`_detect_captcha_challenge` **三形态**"，**未展开**；具体形态在 `app/routes/gateway.py` —— **我本轮不读该文件** | 读 `app/routes/gateway.py`（**超出范围**）或让 t18 作者从 `.bak` diff 补出三形态原文 |
| **U-3** | **上游当前是否仍为"软校验"** | 定案时点 **2026-09-30**；此后未复测。上游可能已收紧 | 真实对话流量 × N 不带 token，观察是否 403 挑战（**耗额度**，须授权） |
| **U-4** | **上游是否校验 `region` 头** | 见 §6.2；需要真实 claim 请求（**耗额度/影响生产账号**） | captain 授权的受控 claim 实验 |
| **U-5** | `ZCODE_CAPTCHA_RETRIES`(4) vs `MAX_CAPTCHA_RETRIES`(3) 是否同一参数 | 契约自标 `unverified`（`account-model.md:427`）；两数字来源不同，无法从外部判定 | 读源码常量定义（**超出范围**）或 t18 从 diff 补证 |
| **U-6** | `POOL_MIN` 非 0 时的**池行为细节**（解池频率/失败重试） | `env-contract.md:351` 自标"未实测"；Go 侧无池 | 读 `app/captcha.py`（**超出范围**） |

> **U-1 的诚实边界**：captain 曾推论"延迟不可验"。我实测纠正过一次 ——
> `t_first`/`t_total` **字段存在**，故"契约无延迟维度"说的是**对拍框架**（只比响应体），
> **不是系统无可观测延迟**。但**字段存在 ≠ 可作锚**：现有 3 条样本全是失败路径、无成功样本
> ⇒ **该字段当前不可用**，U-1 仍标 `unverified`。

---

## §8 证据清单与复现命令

**环境**：`date` → `2026-10-16 01:17:37 CST`；`git rev-parse --short HEAD` → `1f39731`。
**老服务身份辨明**（H-2 纪律，发凭据前）：`systemctl show zcode2api -p MainPID --value` → `618152`；
`ss -tlnp | grep ':9120 '` → `pid=618152` ⇒ **MainPID 即监听者** ✅。

```bash
# 1. Go 侧零接线（核心）
cd /opt/zhipucode2api
git grep -n 'internal/captcha' HEAD -- '*.go'          # → 零命中
grep -rn 'captcha' internal/gateway/ internal/upstream/ # → 5 处注释，0 行代码

# 2. t7 无惰性/池代码（正确）
grep -rniE 'lazy|once|sync\.|cache|refresh|pool' internal/captcha/*.go  # → 零命中

# 3. 契约零描述
grep -rn 'needs_captcha|captcha_retries|总是取|惰性|挑战信号' docs/contract/*.md
#  → 唯一命中 adr-008-local-patch-equivalence.md:43（索引行）

# 4. endpoints.md captcha 全量命中
grep -n 'captcha|验证码' docs/contract/endpoints.md
#  → 649 = "403 验证码挑战 → 原账号重试"（部分覆盖）；103/594 类引用不成立

# 5. 章节行号（反驳 t18 引用）
grep -n '^### A-0|### 1.4 响应头契约' docs/contract/endpoints.md  # → 100 / 595

# 6. monitoring 字段集（消解 15 vs 16 分歧）
cd /opt/zcode2api
K=$(grep -m1 '^ZCODE_ADMIN_KEY=' .env | cut -d= -f2-)
curl -s -H "Authorization: Bearer $K" \
  'http://127.0.0.1:9120/admin/api/monitoring?limit=5' | python3 -c \
  'import json,sys; d=json.load(sys.stdin); es=d["entries"]; k=set();
   [k.update(e) for e in es]; print(len(k), sorted(k))'
unset K
#  → 15 ['account','endpoint','error','input_tokens','mode','model','ok',
#        'output_tokens','preview','req_id','status','stream','t_first','t_total','ts']

# 7. 黑盒锚点（claim 链路语义）
grep -n 'solve_count' /opt/zcode2api/tests/integration/test_claim.py
#  → L94=1 / L117=2 / L206=1 / L303=0 / L332=0

# 8. Catherine 接线（只读）
cd /opt/zhipucode2api
grep -n 'solver|Solve(' .worktrees/task-adminapi/internal/adminapi/claim.go
#  → L442 循环内 / L456 3007 条件 / L647 manual 0 次 / L923-932 solveCaptcha

# 9. env 实测
grep -nE '^CAPTCHA_POOL_MIN' /opt/zcode2api/.env  # → 29:CAPTCHA_POOL_MIN=3

# 10. 边界自证
git diff --stat -- internal/ cmd/   # → 0 行
```

**来源标记图例**：`[实测]` = 我本次命令输出 ｜ `[黑盒]` = `zcode2api/tests/integration/`（允许读）｜
`[diff]` = t18 的 `.bak` diff 实锤（二手）｜ `[记忆]` = `.dsh/memory/claude-memory/`（允许读）｜
`[docs]` = 契约文档 ｜ `unverified` = 未验。

---

## §9 本任务的方法论产出

### 9.1 立题修正（本报告的核心动作）

立题问"惰性化的可观测结果"，我实测后发现：

> **「某特性在契约里没有对应维度」≠「该特性已实现、只是没被判据覆盖」。**
> **判据完备性讨论之前，先确认被验对象是否存在。** 否则是在给一个空集设计判据。

在本案：Go 侧 `internal/gateway/` **根本没有 captcha 调用点** ⇒ 若照立题去设计"惰性化判据"，
是在给一个**不存在的对象**设计判据。

### 9.2 本报告新发现的判据缺陷类（建议入 `verification-methodology.md`）

> **「同一名称的语义在不同链路上可能相反。」**
> 立题给出的"captcha 调用时机"是**单一表格**，但实测证明 **gateway 链路（用到才取）** 与
> **claim 链路（每次必取）** 语义**相反**。单一表格会诱导实现者把正确语义套到错误链路上。
>
> **判据要求**：任何"行为规格"必须显式声明**适用对象**（哪个链路/哪个端点）；
> 未声明适用对象的规格 = 待验证的规格，不可直接实现。
>
> 与 §9.1 的关系：§9.1 问"**对象存在吗**"，本条问"**规格说的是哪个对象**"。
> 二者合起来 = 「先确认对象存在，再确认规格指向它」。

### 9.3 归属漂移的检测法（本次实战）

**同一份报告内，同一主题的"文件归属"与"结论归属"不一致 ⇒ 至少一处是错的。**
检测法：把报告的**表格文件列**与**结论段的 Go 归属**并排读（本案 L33 vs L95-96，一字之差即暴露）。

---

## §10 交付边界声明

| 项 | 状态 |
|---|---|
| 新增文件 | `docs/contract/review-evidence/i3-captcha-lazy-contract.md`（本文件） |
| 修改 `internal/` | **0**（`git diff --stat -- internal/ cmd/` 实测 0 行） |
| 修改 `cmd/` | **0** |
| 修改 t18 报告 | **0**（未碰 `local-patches-semantics.md`） |
| 是否消耗 captcha 配额 | **否**（全程只读探测 + 一次 monitoring 只读 GET） |
| 是否发送生产凭据 | **是（1 次，已辨明持有者）**：向 `127.0.0.1:9120`（MainPID 618152 = systemd 老服务本体）发 `Authorization: Bearer <admin_key>` 读 monitoring。**未发给任何身份未辨明的端口** |
| 未验项 | U-1..U-6（§7.2），**均如实标注，未伪造绿** |
