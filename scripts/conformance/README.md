# conformance —— zhipucode2api 对拍验证框架

> 这是**裁判**，不是运动员。本目录的一切内容都不实现业务，只回答一个问题：
> **Go 版 zhipucode2api 与 Python 版 zcode2api 的对外契约是否等价？**
> 切换 systemd 之前必须过这关（见 `docs/contract/cutover-checklist.md` 阶段 B）。

事实来源纪律：遵循 **ADR-001**（`docs/contract/adr-001-source-of-truth.md`）——
运行中服务的真实 HTTP 响应 > 生产库实测 > 黑盒测试 > 描述现状的文档 > 设计蓝图。
本框架的预期形态**全部来自 9120 实测**，不采信 `docs/development/03-data-formats.md`
（该文档是未来蓝图，与生产不符）。`cases.json` 里每个 `drift-anchor` case 就是这条
纪律的落地：文档说有、实测是 404 的端点，以 404 为准锁进契约。

---

## 1. 文件清单

| 文件 | 作用 |
|---|---|
| `harness.py` | 参数化对拍器（双 base URL、三级判定 + ST-DOM 结构层、归一化、豁免、报告）。零第三方依赖，系统 python3 可跑 |
| `cases.json` | 端点清单（请求定义 + kind + tag + 预期等级）。**新增/收紧对拍范围只改这个文件** |
| `normalize.rules.json` | 归一化规则集，每条含 `why`（辩护理由） |
| `allowlist.json` | 已知差异豁免清单。默认空；当前 3 条：ADR-002 enabled（`case:"*"` 形态锚定）+ 2 条 ADR-006 页面豁免（带 `requires_check`，豁免≠放行） |
| `sentinel.json` | 敏感值硬防线配置（内置规则可覆盖 + `safe_paths` 白名单，默认空=全用内置） |
| `db_check.py` | ADR-003 双写不变量检查器：只读 `accounts.db`，验证「列 enabled 与 data JSON 语义恒一致」+ `--after-disabled/--after-enabled` 专项判据 |
| `dom_crosscheck.py` | ADR-006 口径三方对账：captain 模板基线 vs live 属性口径 vs live 正则口径，逐名归因（A−B 全为 `<script>` JS 模板片段 → text 断言守卫）|
| `selftest-baseline-vs-itself.txt` | 裁判自测存证（门禁 B9；含 v2 sentinel 增量） |
| `dom-selftest-evidence.txt` | DOM 验证器防假绿自测证据（11 项断言 + live 四页 ST-DOM + 三方交叉核对原始输出） |
| `tests/` | 6 个黑盒测试的可指向改造版 + `test_adr003_enabled_semantics.py`（写行为覆盖）+ `conftest.py` |
| `latest/report.{json,md}` | 最近一次运行的机器可读/人读报告 |

## 2. 怎么跑

```bash
cd scripts/conformance

# ① 裁判自测（B9）：baseline=candidate=9120，同一服务对拍自身。
#    预期：引擎自测 24/24 + 全部 safe case 达预期等级 + sentinel 命中全指纹化。
python3 harness.py --baseline http://127.0.0.1:9120 --engine-selftest \
    --out-dir latest

# ② 正式对拍（B4–B6）：Go 候选在隔离端口就绪后（C1：非 9120，读同一份库或副本）
python3 harness.py --baseline http://127.0.0.1:9120 \
    --candidate http://127.0.0.1:9121 --out-dir reports/$(date +%F-%H%M)

# ③ 追加真实上游 case（消耗额度，谨慎）：
python3 harness.py --baseline ... --include-tag expensive
# ④ 只跑部分端点：
python3 harness.py --filter AD-

# 密钥：默认从 ../../zcode2api/.env 读 ZCODE_ADMIN_KEY 键名，
# case 里只写 ${ZCODE_ADMIN_KEY} 占位符；任何值不落报告（见 §5）。
```

黑盒测试套件（B7）：

```bash
# 打老服务（或任何候选实例）：只读断言直接跑，写/上游断言默认 skip
Z2A_BASE_URL=http://127.0.0.1:9120 \
  /opt/zcode2api/.venv/bin/python \
  -m pytest tests -q

# 打隔离候选实例（独立库！）才允许开闸：
Z2A_BASE_URL=http://127.0.0.1:9121 Z2A_ALLOW_MUTATION=1 Z2A_ALLOW_REAL_UPSTREAM=1 \
  pytest tests -q
```

三道安全门（默认全关，**绝不拿共享生产 9120 开玩笑**）：
- `Z2A_ALLOW_MUTATION=1`：允许管理写（设 gateway_key、清监控、rotate 指纹等）。
  打生产会临时把真实用户挡在 401、毁掉队友正在观察的数据。
- `Z2A_ALLOW_REAL_UPSTREAM=1`：允许产生真实上游调用（耗账号额度）。
  遵守冒烟红线：不并发、不重试轰炸、单次 ≤6 个上游请求（docs/testing/04 §4）。
- 代理隔离：harness 显式 `ProxyHandler({})`、测试 `trust_env=False`——
  对拍目标恒为回环直连，系统代理（mihomo 127.0.0.1:7890）绝不许掺进测量路径
  （本环境实测存在畸形 all_proxy，掺入即假阳性来源）。

## 3. 三级判定（L1/L2/L3）与引擎设计

| 级 | 判据 | 数据形态 | 备注 |
|---|---|---|---|
| L1 | 状态码一致 + transport 失败类别一致 | 原始 | 最硬的一级 |
| L2 | 结构一致：**逐位置 JSON 类型树**（对象键集合、数组逐下标、标量 null/bool/int/float/str） | 原始数据（仅经“结构对齐层”排序） | 键缺失/多键/类型不同/数组长度全暴露 |
| L3 | 归一化后逐值（canonical JSON 字节级） | 归一化数据 | header 子集差异也会拦 L3 |

**为什么 L2 用原始数据**：归一化只许处理“同一服务重放必然不同”的叶子值与数组
顺序；若 L2 也吃归一化数据，规则就能把真差异洗白（裁判自己放水）。键名、键集合、
类型、状态码、错误文案结构**永不归一化**（captain 2026-10-15 指令）。

**结构对齐层（pre-sort）与差异抹除的分界**：`sort_by` 只允许用**全局唯一锚**
（`$.accounts` 按 `id`）对齐位置——它是为了“同账号同位置比字段”，不是消灭差异；
对齐后每个账号的 `enabled`/`fingerprint`/`quota` 仍逐位置原样比对。不加这层，
Go 与 SQL 返回顺序的细微不同会把脏数据差异错位到别的账号头上（假阳性）。
⚠️ **CR-35 修正（2026-10-15，Hilkiah t5 体检证伪我原理由）**：账号列表的**顺序本身
是持久契约**（实测=provider 分组序 zai,bigmodel × 组内 created_at 升序；稳定、跨端点
一致、UI 可见；非 rowid——rowid 组内乱序且账号重建会漂移）。因此 pre-sort 洗掉了这个
用户可见面的定罪能力 → 补 **ORDER 断言层**：case 带 `order_keys:{path,by}` 时，在
归一化之前对两侧**原始**数组逐位比 key 序列（AD-accounts expect=L1+L2+L3+ORDER）。
职责分离：sort 对齐字段，ORDER 定罪顺序。引擎哨兵 `order-catches-reorder-despite-sort`
（同内容乱序下 L2/L3 绿而 ORDER 必红）证明该层不是永真摆设；候选实现侧 store 已按
CR-35 排序（c8461bf）。判据侧+实现侧双保险。

**响应头子集**：默认只比 `content-type`（MIME 主干）与 `location`。
不比 `server`（uvicorn vs net/http 是框架指纹非接口契约）、不比 `date`、
不比 `transfer-encoding`（HTTP 库分块策略）。要收紧改 `cases.json`。

**FLAKY 语义（诚实裁判三态）**：baseline 是被 cron 巡逻（每 2 分钟）与真实用户
流量持续写入的**共享生产服务**。首轮不一致会重试（默认至多 3 轮）：
- 某轮一致 → `pass` 但标 `recovered`（FLAKY-pass），各轮结果全在报告里；
- 每轮都不一致 → `fail`（重试穷尽=系统性差异，不是环境噪声）；
- 判定绝不静默：`attempts` 数组与差异明细都落盘。

## 4. 归一化：什么可以、凭什么可以

每条规则的完整理由写在 `normalize.rules.json` 的 `why` 字段（报告里逐条渲染，
供将来质疑对拍结论时引用辩护）。可归一化的**准入判据**：

> **同一服务、同一输入、连续两次调用，该字段的值必然不同，且该不同不承载任何
> 接口契约语义。**

### 4.1 三问判据（CR-35/36/37/38 血泪沉淀，加/改归一化规则前必答）

> 准入判据管「**值**能不能折叠」，但**容器级**归一化（sort/replace_typed）会连带
> 抹掉**顺序**这个独立维度——顺序是否契约与值是否易变是两回事。CR-16b（方向写反）、
> CR-35（accounts 顺序被 sort_by 洗绿）、CR-37（entries 顺序被 replace_typed 洗绿）
> 三次栽在同一条：准入判据没覆盖「顺序」。凡归一化规则声称「某维度非契约」，先答：
>
> **① 折叠的是【容器】还是【标量】？** 容器才可能藏顺序维度；标量 replace 无此风险。
> **② 折叠后【哪些维度仍可辨】？** 逐维度构造对照**实测**（别读代码推断）。
>    replace_typed 的维度账目：丢 = {内容, 长度, 顺序}；保 = {类型, 键存在性(L2)}。
> **③ 被折叠的维度里，有没有【持久契约混在进程态里被一并扔掉】？**
>    内容可辩护为进程态 ≠ 顺序也是。三次事故全部漏答此问。
>
> ③ 答「有」→ 该维度必须由**不经过归一化的单侧断言**兜住（本框架=ORDER 层：
> accounts 相等模式锚老服务实测序；entries 方向模式断言 ts 降序）。CR-38 已穷举：
> 规则中容器级恰 3 条，逐条对应 CR-35/16b/37，**无第四处漏网**。
>
> **空样本语义**（CR-37 陷阱的工程化）：两侧皆空=可见 skip（`ORDER:n/a(no-sample)`，
> 报告/stdout 明示，绝不静默当 PASS）；**单侧空=定罪 FAIL**（对拍时基线有样本而
> 候选无 = 候选未记录流量本身即缺陷证据）。自比模式下 ORDER:PASS 只证明**引擎
> 自洽**，跨服务顺序证据仅在 candidate≠baseline 的正式对拍中产生（summary.
> order_context 字段 + stdout 语境行显式区分）。

满足准入的字段类别（captain 2026-10-15 归纳 + 本框架细化）：

| 类别 | 例 | 手法 |
|---|---|---|
| 服务器墙钟/落库时刻 | `$.ts`、`last_used_at`、`recent_results.at` | 值 replace |
| 物理耗时 | `t_first/t_total`、detail 里 `2.7s` 子式 | replace / 定点 regex |
| 随机 id | `req_id`、`flow_id`、SSE chunk id | 值 replace |
| 轮询/累计计数 | `use_count`、`stats.calls` | 值 replace |
| 账号轮询顺序 | monitoring 条目的 `account` 字段 | 值 replace（账号集合与状态由 accounts 端点专门对拍） |
| 进程自有流量史 | `recent_results`、`monitoring.entries` | **replace_typed 类型保持折叠**（L2 仍确认“存在且为数组”） |
| LLM 生成文本 | content/delta | replace（usage token 数**不归一**：token 口径差异必须 FAIL 供人工裁决） |

明确**不可**归一化：字段名/键集合、字段类型、状态码、错误文案（整体结构）、
`enabled` 的值与类型（脏数据必须显形，见 §6）、业务时间窗（`expires_at/starts_at`
等计划窗口）、`fingerprint.*`（入池后落库、两服务读同一库应全同）。

**created_at 浮点精度裁定**（captain 情报 3）：**完整保留，不做秒级截断**。
理由：`created_at` 是 DB 持久值（REAL 微秒，如 1789147671.7793698），两服务读
**同一份** accounts.db，且两侧响应都由 harness 同一个 `json.loads` 解析成 float64
比对——真实等值必然 bit 级相等；若候选端发生精度丢失（尾数变 0/四舍五入），
那正是本对拍要抓的数据保真缺陷，截断归一化会把这类真 bug 洗白。
真正随时间变化的字段（`last_used_at` 等）已按上表单独处理。

**密钥类字段**：`admin_key`/`gateway_key`/`token_masked` 用 `secret: true` 规则替换为
`<secret:sha256:前8位>` 指纹——明文永不落报告，但**等值判定能力保留**：两侧值不同
→ 指纹不同 → 照样 FAIL。这不同于 replace（值信息完全抹掉），专为“值本身敏感但
必须可比”的字段设计。

### 4.5 敏感值硬防线（sentinel）——默认全拦，不靠“记得设计脱敏”

起因：探索期 `GET /admin/api/settings` 的明文 `admin_key` 曾进入会话上下文
（captain 已轮换作废）。教训升华为机制：**对拍器能安全跑在任意候选服务上的
前提是默认拦截**，而不是逐端点人工记得脱敏。

机制（`harness.py` 的 `Sentinel`；响应体进 L2/L3 **之前**的最外层闸门）：
- **字段名命中**（admin_key/gateway_key/api_key/jwt_token/secret/password/
  verify_param/authorization/cookie/token…）且值为字符串 → `<secret:sha256前8>` 指纹。
- **值形态命中**（32hex、32hex.16al、32hex×三段、`eyJ…` 标准 JWT、`sk-` 前缀、
  文本内嵌 JWT——非 JSON 的 HTML 体同样扫）→ 同样指纹化。
- **等值判定保留**：同值→同指纹→L3 可比对；异值→指纹不同→照样 FAIL
  （引擎自测 `sentinel-different-secret-still-red` 断言）。脱敏≠洗白。
- **非字符串值**命中字段名：只记 `logged-only` 事件、不改值（防污染 L2 类型树）。
- **白名单 `safe_paths`**（sentinel.json）：仅豁免形态巧合的确认安全路径，
  须逐条给 reason；**只收窄误伤、不放开防线**（名单外一切照常拦，
  引擎自测 `sentinel-safelist-exempts-declared-path-only` 断言）。
- 报告呈现：`report.md` 专节列出每处命中的 位置/检测器/动作/指纹（零明文）。

生产 live 实弹（2026-10-15）：25 case 命中 32 处（6×token_masked + admin_key +
gateway_key + 序列步重复计数 + gateway_key_set logged-only），等值判定全保留、
L3 全 PASS、交付文件**新值+旧值双口径扫描 0 残留**。

**轮换后泄漏自检判据**（本次实践固化）：key 轮换后必须用【旧值+形态】双口径
扫工作树、git 可达历史与**不可达对象**（`git fsck --unreachable` + 逐对象 cat）——
只用 .env 当前值反查会漏旧值残留。本次三层扫描均 0 命中：明文从未进入文件/git 对象。

## 5. 报告怎么读

- `report.json` 机器可读（CI 门禁）；`report.md` 人读。
- 每 case：`expect`（契约要求达到的等级）→ `verdict`。预期等级之外的实测等级
  也全部记录（例如 monitoring 预期 L1+L2，L3 达成与否如实可见）。
- 差异明细含 `path`（JSON 位置）、`kind`（type/missing/length/value/header）、
  两侧原值（>200 字符截断；`unmet` 字段指出哪一级未达预期）。
- `normalize_trace`：每条规则实际命中位置/次数——证明 L3 通过不是“规则空转”。
- `canonical_equal_raw`：归一化前是否已字节级相等（自测里应大量 true；
  false + L3 pass 说明是归一化吸收了合理漂移，trace 给出证据）。
- **豁免命中年鉴**：`allowed` 数组与报告尾部“豁免命中（审计痕迹）”——
  每条含 allowlist id、原始差异、理由、批准人。差异从不因豁免而消失，只是
  定罪 → 备案。

## 6. 豁免清单（allowlist）纪律

1. **默认空**：一切差异默认 FAIL。新差异想放行，必须走批准流程写条目。
2. 条目五要素齐备才生效：`case` + `path`（glob，`[*]`/`*` 单段通配）+
   `why` + `approved_by`（非空）+（结构级差异额外 `covers_structure: true`）。
3. 值级豁免用形态锚定：`baseline`/`candidate`（类型名）与 `baseline_value`/
   `candidate_value`（原值形态）可叠加，**过宽即漏洞**——引擎自测
   `ADR-002-exemption-no-overmatch` 专门守卫这点。
4. 当前唯一正式条目 `ADR-002-enabled-laohao`：用户已拍板 enabled 规范化
   （不复现脏值，见 `docs/contract/adr-002-enabled-normalization.md`）。
   老服务对账号 `laohao-bigmodel-2420b3c2` 透传 data JSON 的字符串 `"True"`，
   Go 版返回布尔 `true`。差异被精确锚定：仅 baseline=str "True" 且
   candidate=bool true 命中；任何账号任何其它形态漂移（bool→int 等）不豁免。
   **对拍报告里这条会以“豁免命中”出现，而不是消失。**

## 7. 裁判自证可信（B9 的门禁逻辑）

“同一个服务对拍自己全绿”只证明**无假阳性**；若比对写死恒真，它也全绿——那是
最危险的假绿（private-bench 教训：假绿三形态）。故本框架的自测是三段式：

1. **正向自测**：baseline==candidate==9120 → 24 个 safe case 全 L1/L2/L3 绿。
2. **离线漂移注入**（`--engine-selftest`，24 项）：状态码漂移/int→str 类型漂移/
   `enabled` 脏数据形态/缺键/多键/无规则值漂移/数组乱序/transport 差异/
   豁免全链路/过度豁免守卫/唯一锚排序 —— **每种真差异都必须被抓住**。
3. **在线破坏试验**（sabotage，见存证）：候选指向死端口 → 必须全红；
   抽掉 `$.ts` 规则 → `AD-accounts` L3 必须红；规则补回 → 必须绿。

三段齐绿，才叫可信的裁判。

## 8. 当前基线预期结果（对拍 Go 候选时逐条对照）

| 锚点 | 预期（9120 实测锁定） |
|---|---|
| `GET /health` | **404**（文档说有 → 漂移锚，候选不得“好心”补实现） |
| `GET /admin/api/pool` `/quota` `/claim/history` | **404** `{"detail":"Not Found"}` |
| `GET /v1/models`（匿名/错 key） | 200（生产未配 gateway_key → 放行是现状契约） |
| `GET /admin/api/status`（匿名） | **200**（admin 面唯一实测豁免点） |
| admin 无凭证 / 错凭证 | 401 `缺少鉴权凭证` / `鉴权凭证无效`（文案逐字） |
| `POST /v1/chat/completions` 缺 model | 400 `{error:{message,type:"invalid_request_error"}}` |
| 全局 404 | `{"detail":"Not Found"}`（FastAPI 形态，Go 须复刻） |
| `GET /`、`/admin` | 307 + Location（禁 follow，Location 参与比对） |
| `GET /admin/api/settings` | admin_key/gateway_key 以指纹参与等值比对；**运行时 key 是 DB `meta.admin_key` 优先于 env**（captain 实测），候选读 key 的优先级语义须一致 |
| accounts `enabled` | 5×bool + 1×str "True"（laohao）；候选若按 ADR-002 规范化 → §6 豁免条目承接 |
| SSE / 真实生成 | 默认不跑（tag=expensive）；结构面（帧序/[DONE]/finish_reason）为契约 |

## 9. 6 个黑盒测试的改造映射（captain 指令 4：改造不了就记录，不悄悄删）

| 原文件 | 可指向（live 双指） | 隔离实例专属（门控） | 无法用于对拍（原因） |
|---|---|---|---|
| `test_gateway_auth.py` | 未配置放行断言 | 401/403/200/双通道/models 同闸门（需 PUT settings 写 key：`Z2A_ALLOW_MUTATION`；200 分支另加 `Z2A_ALLOW_REAL_UPSTREAM`） | 无（seed_account 前置由“实例已有账号”替代，语义等价保留） |
| `test_gateway_monitoring.py` | 页面含「请求监控」 | clear→200+entries==[]；对话后条目字段断言（endpoint/model/ok/status/t_first/t_total/preview 逐字） | `account=="mon-1"`（前置=mock 隔离池）、mock 固定 token 数、not_found 场景、空池 503 |
| `test_gateway_openai.py` | 缺 model 400 断言（逐字） | 流式协议形态、真实非流式结构 | mock 固定文本/usage、上游载荷观测（system 块、max_tokens 钳制）、空池 503 |
| `test_claim.py` | claim/claim/manual 无 key 401、preview 无 key 401、未知账号 404（逐字） | captcha-config 结构断言、缺参数回执、指纹 rotate（写库改真实身份，仅隔离实例） | mock 场景值（mock-claim-plan/已经领取过/3007 重试/桩计数）、auto_claim 进程内直调 |
| `test_oauth_login.py` | 未知 flow expired、无 key 401（逐字） | start 形态（expires_in 300/https authorize_url；真实 flow_id 是 32hex，`mock-flow-` 前缀断言记为 mock 产物） | oauth_state 注入、_login_flows 私有字典、兑换链上游观测、TTL 时钟快进 |
| `test_gateway_connect_fail.py` | —（整文件） | — | connect_fail_first mock 注入 + 进程内 cooling 断言；真实上游不可按指令断连 |
| `test_gateway_risk_cooldown.py` | —（整文件，18 条逐名记录） | — | 3012/429/500 全部需 mock 场景；断言观测进程内 Status/settings monkeypatch；3012 在真实环境会**真的禁号** |

> 无法对拍部分的候选验证路径：受控 mock 套件（docs/testing/04 §2 故障注入矩阵
> 契约）在独立实例上复跑原断言语义；这属 t4 之后的验收配套设施，不在本任务范围。
> harness 对拍负责“同一请求 → 同一响应形态”的字节/结构面，两者互补。

## 10. ADR-003 · enabled 双写行为覆盖（captain 终裁新增）

老服务实测事实：`SetEnabled` 为**双写**——`accounts.enabled` 列（INTEGER 0/1）与
`data` JSON 的 `enabled` 字段恒同步（建号/停用/启用/重复停用/改名五路验证）。
脏值 `"True"` 仅在读语义层规范化（ADR-002 不改库）→ **永久驻留**，因此豁免覆盖
的是【任何透出 data.enabled 的端点形态】，不只列表端点。

覆盖三件套（对应 captain 两条判据）：

| 层 | 工具 | 判据 |
|---|---|---|
| 写→读行为序列（HTTP 面） | `harness.py` 的 `steps` case（SEQ-*，逐步双打聚合判定；自测含 SEQ-readonly-smoke 全绿）+ `tests/test_adr003_enabled_semantics.py`（mutation 门控、`zzconf-*` 假账号建了删、绝不打共享 9120） | **判据 2 读出一致性**：停用后 API 必须显示 false（「只写列不写 data」缺陷在此显形）；重新启用后回到布尔 true |
| 存储双写（DB 面） | `db_check.py --after-disabled/--after-enabled --account <id>`（只读 mode=ro，WAL 安全） | **判据 1**：`列=0 且 data.enabled 语义 False`（停用时）；列与 data 任一半边没写即 FAIL |
| 形态审计（常驻脏值） | `db_check.py --audit`（基线体检：9120 实测 6 账号 0 违反、类型分布 `{bool:5, str:1}`）+ ADR-002 豁免条目（`case:"*"` 形态精确锚定） | 语义一致是【硬不变量】；str/bool 形态差异是【规范化话题】——报告呈现，定罪与否由清单条目决定 |

**候选验收串联操作**（隔离实例，Go 候选用 DB 副本）：
```bash
Z2A_BASE_URL=http://127.0.0.1:9121 Z2A_ALLOW_MUTATION=1 pytest tests/test_adr003_enabled_semantics.py -q
python3 db_check.py --db <候选副本库> --after-disabled --account <假账号id>   # toggle 后、恢复前插入跑
python3 harness.py --baseline http://127.0.0.1:9120 --candidate http://127.0.0.1:9121 --include-tag mutating
python3 db_check.py --audit   # 对生产基线复跑，确认不变量仍 0 违反
```

**端点覆盖核查结论**（captain 问「是否所有透出 data.enabled 的端点都进 cases」）：
- `/admin/api/accounts` → 透出，已入 cases（AD-accounts）且豁免形态锚定 ✓
- `/admin/api/status` → 实测响应只有 `providers/gateway_key_set/quota_pool`，**不透出** enabled ✓
- `/admin/api/monitoring` → entries 实测 15 键（无 enabled 字段），**不透出** ✓
- `/admin/api/bundle/export` → PBKDF2+AES-GCM **加密二进制**（盐/IV 随机 → 字节对拍本身不成立）；其数据等价由 INT-021（导出→清库→导入往返）覆盖，属黑盒测试职责 ✓
- `/login/poll`、`/claim/preview` → 未在共享生产实测（后者 expensive 门控）；若隔离实测发现透出，按同格式补豁免条目并**独立批准**，不预先扩面。

## 10A. ADR-006 · 页面 DOM 结构等价验证器（html-dom case）

页面 HTML **重写以清空他人血缘**（ADR-006 选项 C，用户裁定）→ 字节对拍必然失败。
替代判据 = `kind:"html-dom"` case 的 **ST-DOM 结构等价层**（t9 的验收判据）：

| 判据层 | 参与 verdict？ | 内容 |
|---|---|---|
| **ST-DOM** | 🔴 必须 PASS（进 expect 与重试条件） | pre-order 元素列表逐位置比对：`tag`+`id`+`class` 集合+`data-*` 全字典+归一化直接文本（含 `<script>` 源码文本）+ 资源引用全集（src/href 路径+版本串+域名，ADR-006 第 5/6 条） |
| L1 | ✅ | 状态码（页面类 case 保留） |
| L3 字节 | **observed**（记录不判） | 同源自测仍显示 PASS；重写后空白/注释/属性序漂移属预期 |

**天然忽略**（= ADR-006"允许差异"清单）：空白/缩进/换行、注释（HTMLParser 不产注释
节点）、属性顺序（dict 语义）；class 多空格归一。**引擎自测 11 项**（`--dom-selftest`，
并入 `--engine-selftest`）逐条守住假绿/假红两端：自对自全等价、删 id/改 class/改
文本/删节点/资源版本串漂移/JS 模板文本改动**必报**，空白·注释·属性序变化**不报**。

**豁免≠放行（机制化）**：`allowlist.json` 的页面豁免条目（`ADR-006-ui-*-page`，
批准人=用户裁定）带 `requires_check:"html-dom-equivalence"` 字段——该 case 的
ST-DOM 不 PASS 则**整体强制 FAIL**（`requires_check_gate`），哪怕字节级差异"看起来
已处理"；反向防呆：`classify_diffs` 显式忽略带 `requires_check` 的条目充当 diff
豁免（防"结构验证必须过"被误用成"结构差异可豁免"）。引擎自测含门三态断言。

**口径交叉核对**（`dom_crosscheck.py`，captain 基线 vs 我的 live 提取）：
四页全维度 `A−C=0 ∧ B−A=0`——**captain 模板基线与 live 响应正则零差异**（老服务
渲染零内容丢失），`A−B`（模板有、真实标签无）全部落在 `<script>` JS 模板串
（accounts：`${a.id}`/`claim-cap-btn`/`claim-cap-holder` 3 个 id + 63 个 class 片段；
monitoring：25 个 class；含 JS 运行时动态创建的 id）→ 这些片段由 ST-DOM 的 **text
断言**逐字守卫（改一个字符即 diff）。**两边口径都对、零未归因差异**：模板基线适合
当 t9 施工图纸（含 JS 文本），live 属性口径是验收判据（HTTP 响应对齐 HTTP 响应）。

## 10B. t13 管理 API 验收就绪：B-01..B-22 覆盖矩阵 + capture/asserts/status-pin

### 覆盖矩阵（case ↔ 契约 B 组，41 safe 全绿 + lifecycle 实弹通过）

| B # | 端点 | safe 对拍 | mutating（隔离实例） | 备注 |
|---|---|---|---|---|
| B-01 | GET verify | AD-verify + AD-verify-anon | — | pin 200/401 |
| B-02 | GET accounts | AD-accounts（ORDER 双 spec：id 序 + recent_results 嵌套方向）| — | |
| B-03 | POST accounts | AD-create-notokens(400) + AD-noauth-post(401) | **SEQ-account-lifecycle** 建号步 | 成功形态在 lifecycle |
| B-04 | DELETE accounts | AD-delete-unknown（deleted:0） | lifecycle 删除步 | |
| B-05 | GET status | AD-status / AD-status-anon | — | |
| B-06 | PUT accounts/{id} | AD-put-unknown(404) | lifecycle rename 步 | |
| B-07 | POST {id}/enabled | AD-toggle-unknown(404) | lifecycle toggle 步（**读回 enabled=false = ADR-003 双写黑盒实证**）| |
| B-08 | POST {id}/rotate | AD-rotate-unknown(404) | lifecycle rotate 步（ok+fingerprint 结构断言）| |
| B-09 | POST accounts/refresh | — | 归 **expensive**（打上游）| 单侧形态由黑盒测试覆盖；对拍需隔离+mock 上游 |
| B-10 | POST {id}/refresh | — | 同上 expensive | |
| B-11 | POST login/start | AD-noauth-post 型 | MUT-login-start（已扩步：capture flow_id → poll）| |
| B-12 | GET login/poll/{flow} | AD-login-poll-unknown（expired 型） | MUT-login-start 第 2 步（真 flow）| |
| B-13 | GET claim/preview | — | expensive | UP-preview |
| B-14 | POST claim | — | expensive（真实领包）| 输出非确定+消耗额度，黑盒 INT 主责 |
| B-15 | GET claim/captcha-config | AD-captcha-config | — | pin 200 |
| B-16 | POST claim/manual | AD-manualclaim-missing（400 校验层先拒；t8 曾列未测→契约实测背书转正）| 成功形态 expensive | |
| B-17 | GET settings | AD-settings（admin_key 走 sentinel 指纹）| — | |
| B-18 | PUT settings | — | MUT-settings-put-empty（{} 恒等；写路径→门）| |
| B-19 | GET export | AD-export（**secret 明文→sentinel 双道指纹化**，同值同指纹仍判等）| — | |
| B-20 | POST import | AD-import-empty（{}→count:0） | 成功回灌=INT-021（隔离库）| |
| B-21 | GET monitoring | AD-monitoring（ORDER 方向 desc + 空样本三态）| — | |
| B-22 | POST monitoring/clear | — | MUT-monitoring-clear（毁队友观察数据，默认禁跑）| |

### 新机制（本轮，防"一致地错"假绿）

- **`status` 绝对锚**：case/步级 `"status": 200|[401,200]` → L1 = 两侧相等【且】
  各等于契约实测码。动机=本轮真实事故复盘：worktree 里 env 相对路径解析不到 →
  admin key 全缺 → **两侧一致 401 → 旧 L1/L2/L3 全绿**，仅 ORDER 报红。
  pin 值全部来源=契约/t8 实测（GW-models 三条首 pin 凭印象写 401，L1 detail 实测
  200 当场纠正=未配网关 key 放行语义，note 记录若配置须改 list）。
- **`expand` 缺键保留 `${KEY}` + execute 发送前守卫硬 FAIL**：未解析占位符绝不出网。
- **capture/asserts**：步级 `"capture": {"ID": "$.ids[0]"}` 存**每侧各自**值；后续步
  path/json/asserts 里 `{{ID}}` 各插各的。单侧谓词 eq/in/re/type/len_eq/
  find_by_id(+field)/find_by_id_missing 跑在 sentinel 前原始响应上。**自动变量
  `{{SIDE}}`(b/c)**：同服务自比两侧假数据不碰撞——lifecycle 实弹第一发正是被
  这抓出的设计缺陷（同 token 两侧建号→第二侧去重 count:0→capture 报红）。
  未解析变量=case 硬 error，不带字面量打服务。
- **ORDER 嵌套方向（工作B 答案）**：recent_results `at` 升序（G-14 t5 更正后口径
  `[-1]` 最新）已由生产 6/6 账号实测钉死；AD-accounts order_keys 升级双 spec
  列表。CR-38 三处容器级至此**全部有独立顺序断言**。
- **expect-gated 重试**：重试只看 expect 内层级（observed 漂移不空耗三轮）。

### ASSERT 层语义与它的"一致地错"排查（captain 指派复核，5 处修正）

**语义**：断言是【单侧绝对判据】——谓词字面值写死在 cases.json，baseline 的
asserts 只跑在 baseline 的响应上，candidate 只跑在自己的响应上（与 L1-L3 的
跨侧相对比较正交）。它的"一致地错"暴露面不在"两侧同挂"（那会红），而在
**"两侧各以不同错法碰巧同过同一条弱谓词"** + **谓词/机制自身有洞**。逐项排查
结论（全部有哨兵，引擎 51/51）：

| # | 风险 | 处置 |
|---|---|---|
| ① | **Python `True == 1` 陷阱**：candidate 把契约的 `count:1` 写成 `true`，泛 eq 假绿 | `eq_strict`：bool 与数字永不互判（int/float 同值互判——JSON 里 1/1.0 是同一数值非契约面）；`in` 谓词同改 |
| ② | **共享数据源**（对拍最强"同错源"：Go 误连生产库/同 .env）——两侧各建各删假账号，L2/L3 与 find_by_id 全绿 | 新谓词 `find_by_prefix_missing:"liche-lc-dummy-{{OTHER}}"`：本侧列表出现**对侧**假账号前缀即红；自动变量 `{{OTHER}}`/`{{SAME_SOURCE}}` 支持。诚实边界：同源自比两假号必同库互见 → `unless_shared_source` 如实 **skip**（承认无证据，不伪造红也不伪造绿）；**跨服务正式对拍时它才是真探针** |
| ③ | **空配置静默**：`"asserts":[]` 或只配键不配谓词 → 不执行任何断言且 ASSERT 缺席 | 按【键存在】判定 + 空清单=配置错误红（`asserts:[]` 比不配更危险：它伪装成已覆盖）；无谓词裸 `{}` 同样红 |
| ④ | **写序列重试重放**：FLAKY 重试=整段生命周期重跑 → 二次 create 命中去重、mid-fail 留假账号残留放大 | mutating tag 强制 `retries=0`（单轮；写序列的"偶发一致"语义不成立） |
| ⑤ | **谓词值凭印象**（= CR-36 在断言层的形态） | 每条谓词值标注来源：B-03/B-04/B-07 契约实测、lifecycle 首发实弹（生产 name 回填实证）、`[-1]` 最新=G-14 t5 更正后 6/6 实测；**GW-models pin 凭印象写 401 被实测 200 纠正**为反面教材留痕 |

**结论**：ASSERT 层与 status-pin 同构（绝对锚），结构上免疫"两侧一致地错"，
但①②③是机制自身的洞——不查就会以"绝对锚"的名义假绿。已全堵，哨兵
`assert-eq-strict-bool-vs-int` / `assert-shared-source-contamination-detect` /
（③④由实现强制，⑤靠标注）入引擎自测。

### 对拍 vs 黑盒测试的分工边界

B-09/B-10/B-14 等**真实上游消耗**端点：输出非确定 + 真实副作用，跨服务逐值对拍
语义上不成立；黑盒测试（mock 上游）主责，harness 覆盖其**零消耗形态**
（401/404/400 负型 + 结构）。正式对拍报告引用本表，防止"矩阵绿"误读为全行为等价。

## 11. 已知边界与后续待办（暴露矛盾不藏）

1. `PUT /admin/api/settings` 的请求形态已由契约 B-18 [实测]（{} → 200 三字段未覆盖），
   `MUT-settings-put-empty` case 备妥（mutating 门）——首次隔离实例跑 auth 套件时若
   405/422，按实测修正该 fixture（改前置不改断言）。
2. monitoring 预期 L1+L2：`keep` 上限与 entries 折叠策略若将来要吃 L3，需要
   baseline 侧受控流量环境（写路径 + clear 门控跑通后再收紧 `expect`）。
3. 自测运行在共享生产服务上：队友加/删账号、cron 巡逻都会进 diff 明细，框架以
   FLAKY 重试 + 每轮存证处置，绝不用“容忍差异”的规则掩盖。
4. 本目录 case 覆盖 33 端点中的实测可达面（含 4 个文档-实现漂移锚）；候选就绪后
   应把 404 锚全部转绿，再按 cutover-checklist 阶段 C 谈切换。
