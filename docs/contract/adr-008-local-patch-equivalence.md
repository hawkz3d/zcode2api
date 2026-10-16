# ADR-008 · 本地补丁等价：契约等价之外的第二类等价

- 状态：**已裁定**（2026-10-16）
- 裁定人：captain（基于 t18 审计与 captain 复核）
- 触发：t15 切换演练发现 `z2a-patches/entitlements.py`；t18（Lamias）扩大审计后定为系统性问题

## 一、问题性质

**老服务跑的不是「原版 zcode2api」，而是「打过大量本地补丁的版本」。**

captain 复核的规模：
| 项 | 实测 |
|---|---|
| 老仓 commit 总数 | **50**（含 dengyie 46 / mango 2 / HawkZed 2） |
| `.bak` 文件数 | **45**（Lamias 按 8 个系列去重后 15） |
| 正式补丁脚本 | `z2a-patches/entitlements.py`（89 行） |

**这意味着两类等价**：

| 类型 | 含义 | 本项目状态 |
|---|---|---|
| **契约等价** | 33 端点的对外行为与老服务一致 | ✅ 已实现并对拍验证中 |
| **补丁等价** | 老服务的**实际行为** vs 「原版」的差异 | 🔴 **t18 首次系统审计** |

**只做契约等价是不够的** —— 契约是从**当前老服务**提取的，理论上应已覆盖补丁效果；
**但契约文档是人写的，可能漏记**（I-4 就是实例）。

## 二、审计结论（t18 交付，分支 `wt/task-patches`）

三类清单：

### ✅ 已覆盖（5 项，实锤）
- `max_tokens` 钳 131072（`forward.go:145-146`）
- entitlements 输出形态（契约 B-02 + live 复证两账号 3 亿 Weekend Build `source:"plan"`）
- models 无损透传
- 等

### 🔴 缺失（I-1..I-8）

| 编号 | 内容 | 严重度 |
|---|---|---|
| **I-4** | **`reasoning_content` 三重零**（见下） | 🔴 **最需决策** |
| **I-3** | captcha 惰性化（首包 20~42s → 0.9s 的**对外可见延迟差异**，契约无延迟维度） | 🔴 次高危 |
| **I-2** | 动态版本门槛（活动包可见性开关，黑盒不可见） | 🟠 |
| **I-8** | 订阅百分比窗口（quota.py 代码核实的新链，契约自述「非空未实测」） | 🟠 |
| 其余 I-1/I-5/I-6/I-7 | 见报告 | 🟡 |

### 不适用
编排脚本（不影响服务行为）

## 三、🔴 I-4 裁定：**必须修**（captain 已核实三重零）

**captain 独立复核**：

| # | 证据 | 结果 |
|---|---|---|
| 1 | 契约提及 `reasoning_content` | **0 次**（`grep -c docs/contract/*.md`） |
| 2 | 对拍覆盖 SSE delta 内容 | **0 case**（`cases.json` 中 delta 出现 0 次） |
| 3 | `chat.go` 丢弃 thinking delta | **确认**：`case "content_block_delta"` 只读 `d["text"]`，`text == "" → continue` |

```go
// internal/gateway/chat.go:441-448（实测）
case "content_block_delta":
    d, _ := ev["delta"].(map[string]any)
    if d == nil { continue }
    text, _ := d["text"].(string)
    if text == "" { continue }        // ← thinking 型 delta 在此被丢弃
    writeSSE(chatChunk(...))
```

**后果**：切换后 **DSH 用户静默失去思考链显示**，而**对拍永远全绿**（三重零 = 无人观测）。

**这是「契约等价 ≠ 功能等价」的最强实例**：
- 契约没写 → 实现没做 → 对拍没测 → **三层一致地漏**

### 裁定

**立为独立任务（t21）**，跨三个域：
| 域 | 改动 |
|---|---|
| 契约（docs/contract/） | 补 `reasoning_content` 的单样本折叠规则与契约描述 |
| 实现（internal/gateway/chat.go） | 补 `thinking_delta → reasoning_content` 映射 |
| 对拍（scripts/conformance/） | 补流内容 case（覆盖 SSE delta） |

**因跨三人辖区，captain 先出契约（文档域），再派实现与对拍。**

## 四、附带裁定：I-7 的读码许可

t18 报告称 I-7（双 key/双通道/darwin 伪装）为**线索级**，定性依据是 commit message/README/记忆库/黑盒实测，
全文标注 `〔未代码核实〕`。

**裁定：授予 diff-only 许可** —— 可读以下文件的 **`.bak↔current` 变更块**（不通读）：
`app/openai_compat.py`、`app/routes/gateway.py`、`app/claim.py`、`app/fingerprint.py`

**理由**：
- I-7 涉及「双 key/双通道」这类**功能性差异**，线索级不足以下实现判断
- diff-only 限定（只看变更块）可避免通读实现导致的规格先于实现污染
- 这是规格先于实现内的**受控取证**，不是"读实现抄逻辑"

## 五、方法论沉淀

**t18 揭示的取证通道**（可复用）：
> 老仓是 **git 仓** → `git log` 的 commit message 是**系统性取证通道**，
> 不依赖补丁目录是否自觉登记。

**这一条的普适性**：任何「对方的版本 vs 上游版本」的差异审计，
**优先查版本控制历史**，而不是只看显式的补丁文件。
