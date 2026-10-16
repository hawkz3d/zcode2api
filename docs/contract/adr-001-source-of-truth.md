# ADR-001 · 契约事实来源的优先级裁定

- 状态：**已裁定**（2026-10-15）
- 裁定人：captain（Euphelia）
- 触发：Lamias 在 t3 预工作中实测发现 `docs/development/03-data-formats.md` 与生产库"全不符"

## 背景

重写 zhipucode2api 时，团队发现**文档与实测打架**。若不裁定优先级，各成员会按各自采信的事实写代码，最终无法对齐。

实测证据（Lamias 只读探测 + captain 复核）：

| 项 | `docs/03-data-formats.md` 描述 | 生产库实测 |
|---|---|---|
| accounts 表 | 带 `label`、`cooling_until` 列 | 无这些列；列为 `id,provider,name,mode,status,enabled,created_at,data` |
| 额外表 | `claim_history` | 不存在；只有 `accounts` + `meta` |
| data 存储 | AES-256-GCM 加密 | **明文 JSON** |
| 主密钥 | `ZCODE_MASTER_SECRET` | 该 env 不存在 |
| 索引 | （未提） | `idx_acc_provider`、`idx_acc_status` |
| `ZCODE_ADMIN_KEY` 默认 | `zcode` | 实现为 `os.getenv(..., "<弱默认>")`，README 写 `change-me` |

结论：`docs/03` 是 **zcode-hub 的"未来设计"蓝图**（该文档自己写着"设计要点（继承 z2a 并扩展）"、"这是对 z2a 明文存储缺陷的修复"），**不是对现存 zcode2api 的描述**。

## 裁定：事实来源优先级（从高到低）

1. **运行中服务的真实 HTTP 响应** —— 最高权威。契约要兼容的是"老服务实际行为"，不是"文档声称的行为"。
2. **生产库的真实 schema 与数据**（只读探测）—— 决定存储层兼容。
3. **黑盒测试**（`tests/integration/**`）—— 描述已被验证的外部行为。
4. **`docs/development/04-api-spec.md`** 等**描述现状**的文档。
5. **`docs/development/03-data-formats.md`** —— **仅作"未来设计参考"，不得作为实现依据**。
6. 任何"设计要点"/"扩展"章节 —— 默认视为**未实现**，除非实测证实。

## 强制要求

- 契约文档中每条断言必须标注**证据来源**（实测命令 / 文件行号 / 探测输出）。无证据的断言标 `unverified`。
- 引用 `docs/03` 的内容必须先实测验证，不符者以实测为准，并在 `compat-risks.md` 记录该文档不可信。
- 若未来真要实现 `docs/03` 的设计（加密存储等），那是**独立的增强需求**，不属于本次"等价重写"范围。

## 本次直接影响

- `sqlite-schema.md` 必须以生产库 `PRAGMA table_info` 实测输出为准（Lamias 的 `EnsureSchema` DDL 已按实测写好）。
- `env-contract.md` 只覆盖**实际生效**的环境变量；`ZCODE_MASTER_SECRET`、`CLAIM_*`、`POOL_*` 等未实现项**不入契约**。
- `ZCODE_ADMIN_KEY` 默认值以**实测行为**为准：env 缺失时的回退值（弱默认）需记录，但生产实际用的是 `.env` 里的值 + DB `meta.admin_key`。运行时优先级 = **DB meta 优先于 env**（实测：改 env 后需同时写库才生效）。
