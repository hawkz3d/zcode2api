# 审查证据归档

## t5 · Liche 的独立审查探针（被 Qoder 额度中断前留下）

**文件**：`t5-liche-probe-output.txt`（探针源码 `internal/store/zz_t5_review_test.go`，非项目代码，未入库）

**背景**：Liche 执行 t5（审查 t3）时，因 Qoder 上游 `403 Billing daily count exceeded` 被打断，
任务标记 failed。但其探针代码**已写完且可跑通**——captain 实测复跑，6 项断言全部 PASS。
证据价值高，故归档。

**探针覆盖的 6 项独立核实**（均由探针自己的命令产出，非复述实现者自述）：

| 项 | 断言 | 结果 |
|---|---|---|
| A | `Serialize` 对生产 data 列逐字节 round-trip（零修改应零字节变化） | ✅ 6/6 账号字节一致（2580~3238 bytes） |
| B | `SetEnabled` 双写 + 其余成员字节不动 + 脏值洗为 bool | ✅ 快照内验证，生产未碰 |
| C | 无关 Upsert（改名）不改写脏值 `"True"` | ✅ 字节保留 |
| D | `EnsureSchema` DDL vs 生产 `sqlite_master` 逐字对比 | ⚠️ 3/4 逐字一致，1 处差异（见下） |
| E | `EffectiveEnabled` 优先级方向 + `meta` typeof（G-8） | ✅ PASS |
| F | `created_at` REAL 精度 round-trip（G-9） | ✅ 6/6 Δ=0 |

### D 项差异的性质（captain 已核实，非缺陷）

```
live : "CREATE INDEX idx_acc_status   ON accounts (status)"   ← 生产库：3 个空格
fresh: "CREATE INDEX idx_acc_status ON accounts (status)"     ← 新库：1 个空格 + IF NOT EXISTS 前缀
```

- **语义等价**：SQLite 对多余空格不敏感，索引行为一致
- **但逐字不等**：若未来做「DDL 逐字对拍」会误报
- **结论**：**不是缺陷**。Lamias 的写法（1 空格 + `IF NOT EXISTS`）是正确且更规范的；
  生产库那 3 个空格是历史格式化残留，**不应刻意模仿**。
- **对拍含义**：DDL 类对比必须**归一化空格**后再比，或用 `PRAGMA table_info` / `index_list`
  等结构化接口而非 `sqlite_master.sql` 文本比对。已记入对拍注意事项。

### 对 t5 的影响

Liche 的探针**完成了 t5 的大部分实质工作**（6 项中的 A/B/C/E/F 全绿，D 项有解释）。
但 t5 仍需完成：
- 对 6 条 acceptance 的**独立结论**（非探针能自动覆盖的部分）
- 至少 3 处实现者未自曝的观察
- 对 ADR-002/ADR-003 落实的**代码阅读**核实（探针只能验行为，不能验"逻辑是否真实存在而非仅注释声称"）
- 对「生产库零污染」的独立检测命令与输出

故 t5 已 reassign 给 Hilkiah 继续完成。
