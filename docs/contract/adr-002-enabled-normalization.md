# ADR-002 · `enabled` 字段类型漂移的处置裁定

- 状态：**已裁定**（2026-10-15）
- 裁定人：**用户（项目所有者）**
- 触发：captain 记录切换前基线快照时实测发现类型不一致

## 现象（实测证据）

`GET /admin/api/accounts` 返回 6 个账号，`enabled` 字段 JSON 类型不统一：

| 账号 id | `enabled` 响应值 | JSON 类型 |
|---|---|---|
| `oauth-login-ea92aa60` | `true` | bool |
| `oauth-apikey-c5c641a1` | `true` | bool |
| `oauth-login-8523aba4` | `true` | bool |
| `oauth-apikey-8808fbac` | `true` | bool |
| `laohao-bigmodel-2420b3c2` | `"True"` | **string** |
| `xinhao-2000w-02f71f86` | `true` | bool |

## 真因（captain 只读探测定位）

不是代码分支，是**库内脏数据 + 原值透传**：

- `accounts` 表顶层列 `enabled`：类型 `INTEGER`，6 行全为 `1`（**类型一致**）
- `accounts.data` 列（JSON 文本）内**另有**一个 `enabled` 字段：
  - `laohao-bigmodel-2420b3c2` → `"enabled": "True"`（字符串，脏数据）
  - 其余 5 个 → `"enabled": true`（布尔）
- 老服务 API 输出的是 **`data` JSON 里的那个值**，不是表顶层列 → 故只有 1 个账号呈现 `"True"`

## 裁定：**规范化**（用户决定）

Go 版 `zhipucode2api` 对 `enabled` 采用**统一 Boolean 输出**，不复现该脏值。

### 具体要求

1. **API 序列化层**：`enabled` 字段一律输出 JSON `true`/`false`。
   - 真值判定规则：接受 `bool`、`"True"/"true"/"1"`、数值 `1` 等形态，**归一为布尔**。
   - 对 `laohao-bigmodel-2420b3c2` 这类脏值，输出 `true`（其列值为 1，语义为启用）。
2. **存储层保持保真**：`AccountData` 仍以 `map[string]json.RawMessage` 保存原始字节。
   - **读进去、写回去不得丢字段、不得改脏值**（除非本次写操作明确修改该字段）。
   - 即：规范化只发生在**输出/读取语义**层，不污染**存储往返**。
3. **旧库脏值的向后兼容**：读到 `"True"` 字符串时不得报错，必须正确解析。
4. **写入路径**：`SetEnabled` 写 `enabled` 列（INTEGER）。**是否联动 data JSON 内的 `enabled` 字段，以 t1 契约的实测结论为准**（captain 已要求 Hilkiah 实测：调一次启用/停用接口后，只读对比列与 data JSON 是否都变）。

## 对验收的影响（重要）

- **对拍 L2（结构一致）会在此处产生已知差异**：
  - 老服务：`laohao-bigmodel-2420b3c2` → `"True"`（string）
  - 新服务：全部 → `true`（bool）
- 该差异**必须走对拍器的显式豁免清单**（Liche 的 harness 已支持），豁免条目须写明：
  - 字段路径、差异形态（`string → bool`）、理由（ADR-002 规范化裁定）、批准人（用户）
- **严禁静默归一化**——豁免必须在报告中显式列出，留审计痕迹。

## 为什么不选「忠实复现」

用户明确要求新项目**干净**，不做 bug 复制机。忠实复现会把一处已知脏数据语义永久固化进新代码，
并使未来的类型收紧（如前端 TS 类型、OpenAPI schema）持续受污染。

代价仅是一条有据可查的豁免记录，收益是新契约类型收敛且可被机器校验。

## 关联

- ADR-001（事实来源优先级）：本裁定的实测证据来源符合 ADR-001 的优先级 2（生产库真实数据）。
- t1 契约任务：`account-model.md` 需记录该字段的双份存储事实与输出来源。
- t4 对拍任务：harness 豁免清单需包含本条。
