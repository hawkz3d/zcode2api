# ADR-003 · `enabled` 写入语义与读取优先级终裁

- 状态：**已终裁**（2026-10-15）
- 裁定人：captain（Euphelia），基于实测证据
- 触发：Lamias 在 ADR-002 落实中指出 ADR-002 内部两句话读法冲突（"列权威" vs "data 优先"），要求终裁

## 冲突点

ADR-002 同时写了：
- 要求 2 列表：「`data` 缺失/`null` → 回退到表顶层 `enabled` 列」→ 隐含 **data 优先**
- 要求 2 末句：「**列 `enabled` 列是权威来源**」→ 隐含 **列优先**

在 data 与列**冲突**时，两种读法结果不同。生产库 6 行两读法结果一致（全 true），故未暴露。

## 实测（captain 用假账号实验，生产零污染）

方法：建假账号 `zzprobe-enabled-da033997`（假凭证），反复调 B-07 切换 enabled，每次用 **VACUUM INTO 只读快照**观察「表列」与「data JSON」的同步变化。

| 操作 | 列 `enabled` | data JSON `enabled` | 是否一致 |
|---|---|---|---|
| 建号初始 | `1` (int) | `true` (bool) | ✅ |
| POST /enabled {false} | `0` (int) | `false` (bool) | ✅ |
| POST /enabled {true} | `1` (int) | `true` (bool) | ✅ |
| POST /enabled {false}（重复） | `0` (int) | `false` (bool) | ✅ |
| PUT /accounts/{id}（改名） | `0` (int) | `false` (bool) | ✅ 未被误改 |

**结论：老服务对 `enabled` 是「双写」——列与 data JSON 始终同步。**

实验后已清理：账号总数回到 6、probe 残留 0、`integrity_check=ok`、6 个真实账号状态原样。

## 终裁

### 1. 读取优先级：**data 优先，列兜底**（与 Lamias 现有实现一致，保持不变）

理由：
- 老服务 API 输出的确是 **data JSON 里的值**（ADR-002 真因分析已证实）
- 正常路径下两者恒等（双写），故优先级只在**异常数据**下才有可观测差异
- 选 data 优先 = **忠实复现老服务对外行为**（契约优先于洁癖）
- 若未来想让列权威，属"修正老服务行为"，需另立 ADR + 对拍豁免

### 2. 写入语义：**必须双写**（列 + data JSON）

理由：实测证实老服务双写，新服务若只写列会导致：
- 新服务 API 输出（读 data）与写入（只写列）**自相矛盾** —— 停用账号后输出仍显示 enabled=true
- 这是**功能性缺陷**，不是风格问题

**对 t3 的修正要求**：`SetEnabled` 必须**同时写** `enabled` 列（INTEGER）与 `data` JSON 内的 `enabled` 字段（bool），保持与老服务一致。
- 写 data 时须**保真**：只改 `enabled` 这一个键，其余键与原字节不动（Lamias 的 RawMessage 设计已支持）
- 数据形态：正常写 **bool**（不复现 `"True"` 字符串脏值——那是历史遗留，ADR-002 已裁定规范化；但**已存在的脏值不得被无关操作改写**）

### 3. `EffectiveEnabled()` 保持现状

Lamias 实现的「data 可归一优先、列兜底」**正确，无需改动**。

### 4. 与 ADR-002 的关系

ADR-002 的"列是权威来源"一句**本条予以澄清**：指的是"列是 NOT NULL 且语义基准"，**不是**读取优先级。读取优先级以本 ADR 为准（data 优先）。ADR-002 其余条款（规范化输出、存储保真、豁免留痕）全部维持有效。

## 对验收的影响

- 对拍豁免仍只需 **1 条**（`laohao-bigmodel-2420b3c2` 的 `"True"` → `true`），本 ADR 不新增豁免。
- 但**多了两条必须验证的行为等价**：
  1. 新服务 `SetEnabled` 后，只读快照须显示**列与 data 双双变化**（与老服务一致）
  2. 停用再启用后，API 输出与实际状态**一致**（防止"只写列"缺陷）

请 Liche 在对拍 cases 中覆盖这两条（可用假账号，测后清理）。
