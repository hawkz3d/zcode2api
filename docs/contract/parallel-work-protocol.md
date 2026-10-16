# 并行写代码协议（防止共享树冲突）

> **建立原因**：2026-10-15 发生一次真实事故——两名成员在同一工作树并发写
> `internal/gateway/`，造成 `go build` 挂 + 一处「编译能过、语义全对调」的静默缺陷。
> 根因是调度未提供隔离工位。

## 一、铁律：一个包同一时刻只有一个写者

| 场景 | 做法 |
|---|---|
| 两人要改**同一包** | 一人做，另一人**等**或做**别的包** |
| 两人要改**不同包** | 可以并行，但各自**只碰自己 scope 的文件** |
| 共享文件（如 `go.mod`） | 由**任务 owner** 独占，他人不改 |

## 二、推荐：独立 git worktree（物理隔离）

```bash
cd /opt/zhipucode2api
git worktree add -q .worktrees/task-t6 -b wt/task-t6
# → 成员在 .worktrees/task-t6/ 内工作，与主树完全隔离
```

**规则**：
- 工位目录：`.worktrees/<slug>/`（已在 `.gitignore`）
- 分支：`wt/<slug>`
- 成员在工位内 commit → 完成后由 captain 合并或 cherry-pick 回 main
- **收工必须清理**：`git worktree remove --force .worktrees/<slug>` + `git branch -D wt/<slug>`
  （否则留下幽灵工位，且 `git status` 会看到 `.worktrees/` 有东西）

## 三、如果不建工位（改动很小、时间很短）

至少遵守：
1. **开工前声明 scope**：告诉 captain 你要改哪些文件
2. **写完立即 commit**（不要长时间留未提交状态——`f1f02cd` 事故的形态就是"工作树半成品被他人撞见"）
3. **提交后跑一次 verify 再报交付**

## 四、captain 的调度责任

1. **reassign 不等于打断** —— 实测框架的 `invalidateTaskAttempt` 只是撤销 attempt_id，
   **原 owner 若正在跑一个 turn，仍可能继续写文件**。reassign 后应：
   - 显式给原 owner 发**停止指令**
   - 或分配独立 worktree 从根本上隔离
2. **不要给两人派同一批文件的任务**
3. **新起一批任务时**，先看是否与在跑任务的文件集合重叠

## 五、中断恢复纪律（2026-10-15 新增）

**成员在被上游（provider）中断后恢复时，第一件事必须是核对任务归属，而不是接着写。**

```bash
# 恢复后的第一步
agent_teams_status        # 确认该任务是否仍在自己名下
```

**原因**：provider 中断（如 Qoder 403 额度耗尽）后，captain 可能已把任务 reassign 给他人。
若成员凭"我上次在写这个"的惯性继续，就会与新人并发写同一批文件。

**这是本次事故的直接成因**（Lamias 自述：「我在 Qoder 403 中断恢复后没有先核对自己的任务归属就开始续写」）。

## 六、共享树上文件权威性的判据（2026-10-15 新增）

**在共享工作树里，不能从文件内容推断它是"存量代码"还是"并发产物"。**

事故实例：Lamias 写 `gateway.go`（20:51）后 30 秒，`errors.go`（20:52）出现。
他推断这是"树上已有的高质量参考实现"，于是按它的签名去适配自己的调用。
**实际上那是 Catherine 30 秒前刚写的**，且她的签名是对的、他的调用反了 → 合起来错。

**判据**：
1. 看到陌生文件 → 先 `ls -la --time-style=+%H:%M:%S` 看 mtime
2. mtime 落在**本次任务时间窗内** → 极可能是他人并发产物，**先问 captain 归属**
3. 不要从"写得不错"推断权威性 —— 并发产物也可能写得不错

## 七、禁止扫目录的格式化工具（2026-10-16，t21 事故）

**事件**：Lamias 在 t21 用了 `gofmt -w internal/gateway/`，
**顺手改动了不属于他的文件**（Catherine 的 `gateway_test.go` / `identity.go` / `picker.go` 的注释对齐）。
他识别后全部回滚，commit 净含自己 2 文件 —— **处置正确，但事故本可避免**。

### 纪律

> ❌ **禁止** `gofmt -w <目录>` / `goimports -w <目录>` 这类**扫目录**的格式化
> ✅ **只对具体文件路径**：`gofmt -w internal/gateway/chat.go internal/gateway/chat_test.go`

### 为什么

| 后果 | 说明 |
|---|---|
| **无关 diff** | 污染 commit，review 时难以分辨"真实改动"与"格式化噪音" |
| **并发冲突** | 他人正在改同一文件时，格式化改动会与之冲突 |
| **难定位的回归** | 格式改动可能影响 `//go:embed`、`//go:build` 或注释指令 |
| **归属混乱** | 你的 commit 里出现他人文件的改动，违反"一个包同时只有一个写者" |

### 同类禁止（推广）

任何**会遍历目录并改写文件**的工具都要限定路径：
- `sed -i` 批量替换
- `perl -pi -e`
- IDE 的 "Format Project" / "Optimize Imports"（全项目范围时）

**判据**：运行前问一句「**这条命令会碰到哪些文件？**」
若答案包含"不是我要改的"，就限定路径。

## 八、事故记录（2026-10-15）

| 项 | 内容 |
|---|---|
| 现象 | `go build ./...` 挂在 `internal/gateway`（undefined: handleMessages / writeDetail / logWarning） |
| 真因 | t6 原 owner（Lamias）在 Qoder 403 中断后被 reassign 给 Catherine，但**未被真正打断**，仍写同一批文件 |
| 隐藏缺陷 | `writeShapeB(w, 401, "缺少鉴权凭证", "authentication_error")` 与签名 `(w, status int, typ, message string)` **实参对调** —— 两个 string 互换编译器零信号，输出语义完全相反 |
| 教训 | 并发写共享树 = 双方各自看都对，合起来错；且这类错**编译期与本地测试都抓不到**，只有对拍能发现 |
