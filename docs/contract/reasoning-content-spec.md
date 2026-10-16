# `reasoning_content` 透传契约（I-4，t18 审计发现）

- 状态：**已冻结**（2026-10-16）
- 来源：t18（Lamias）diff-only 取证 + 记忆库历史实证 + captain 复核
- 背景：ADR-008（本地补丁等价）

## 一、为什么需要这份契约

**三重零盲区**（captain 逐条核实）：

| # | 层 | 状态 |
|---|---|---|
| 1 | 契约文档 | `grep -c reasoning_content docs/contract/*.md` = **0 次** |
| 2 | 实现 `chat.go` | `case "content_block_delta"` 只读 `d["text"]`，`text == "" → continue` → **thinking 型 delta 被丢弃** |
| 3 | 对拍 harness | `cases.json` 中 `delta` 出现 **0 次**（无流内容 case） |

→ **契约没写 → 实现没做 → 对拍没测** = 三层一致地漏。
**后果**：切换后 DSH 用户**静默失去思考链显示**，而对拍**永远全绿**。

## 二、业务语义

上游（Anthropic 格式）发送 `thinking` 块：

```
content_block_delta  type=thinking_delta   字段 thinking      ← 思考链内容
content_block_delta  type=signature_delta  字段 signature     ← 签名（不透传）
content_block_delta  type=text_delta       字段 text          ← 回答正文
```

**原版转换器**只处理 `text_delta` / `input_json_delta` → **思考链整段丢弃**。
**本地补丁**补了两处映射（这就是我们漏掉的功能）：

| 场景 | 映射 |
|---|---|
| **流式** | `thinking_delta` 的 `thinking` → SSE 帧的 **`delta.reasoning_content`** |
| **非流式** | 收集全部 thinking 块 join → `message.reasoning_content` |

DSH / pi-ai 按 `reasoning_content` 字段渲染思考链。

## 三、实现规格（t21 实现域可直接照做）

### 3.1 流式（`internal/gateway/chat.go`）

```go
case "content_block_delta":
    d, _ := ev["delta"].(map[string]any)
    if d == nil { continue }

    // 🔴 新增：thinking 型 delta → reasoning_content（不得丢弃）
    if dt, _ := d["type"].(string); dt == "thinking_delta" {
        if th, _ := d["thinking"].(string); th != "" {
            writeSSE(chatChunk(id, created, model,
                `"reasoning_content": `+jsonString(th), "null"))
        }
        continue
    }

    // 原有：text 型
    text, _ := d["text"].(string)
    if text == "" { continue }
    writeSSE(chatChunk(id, created, model, `"content": `+jsonString(text), "null"))
```

### 3.2 `signature_delta` 的处理

**不透传**（它是签名，不是内容）。**但也不能报错** —— 静默跳过。

### 3.3 非流式

收集全部 thinking 块 → join → 放入响应 `choices[0].message.reasoning_content`。

## 四、对拍覆盖（t21 对拍域）

**问题**：当前 harness 对 SSE delta 内容**零覆盖**（`delta` 在 cases.json 出现 0 次）。

**要补的 case**：
- 一条覆盖「上游返回 thinking_delta → 客户端看到 `reasoning_content`」的流内容 case
- **难点**：需要真实上游产生 thinking（消耗配额），或构造 mock

**captain 建议**：
- **优先做单侧断言**（不需要两侧对拍）：验证 Go 实现「收到 thinking_delta 时输出 reasoning_content」
  —— 用 mock 上游即可（gateway 已有 mock 缝）
- 若要做真实跨服务对拍，需**登记为 expensive case**（消耗配额），在切换验收时单跑一次

## 五、证据状态（诚实标注）

| 证据 | 状态 |
|---|---|
| 补丁代码（字段级映射） | ✅ **diff 级确证**（t18 diff-only 取证：`openai_compat.py` 变更块） |
| 历史实证 | ✅ 记忆库《ZCode 提速两修复》（2026-09-30）：修复后 **139 个 reasoning_content chunk 正常透传** |
| 本次实弹 | ⚠️ **未产生 thinking**（上游配额尽，返回 1005 流内错误）→ 恰好演示「单次实弹不可作契约锚」 |
| **跨服务对拍** | ❌ **未做**（需配额或 mock） |

**结论**：字段级规格**可信**（代码 diff + 历史实证双源）；
但**端到端对拍证据缺失** —— 这是已知缺口，不掩盖。

## 六、与其它任务的关系

| 任务 | 关系 |
|---|---|
| ADR-008 | 本项是该 ADR 的第一个落地任务 |
| t18 报告 | §P-4 详载（374 行报告） |
| t21（待派） | 实现 + 对拍 |
| I-9（相关） | 同属「补丁等价」缺失项，但属不同域（picker 选号） |
