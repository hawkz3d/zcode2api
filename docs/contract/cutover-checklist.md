# 切换检查表（Cutover Checklist）

> 铁律：**任一验收项不过 → 不切换。** 宁可继续待命，不得带病上线。

## 阶段 A · 实现完成前的前置检查

- [ ] A1 老仓生产版本已固化提交（`f4f6dc6`：生产在跑的 quota/store/gateway 演进 + .gitignore 安全修复）
- [ ] A2 老仓工作区干净（`git status --porcelain` 为空）
- [ ] A3 `.env*` 与 `*.bak*` 已被 `.gitignore` 覆盖，`git add -A` 不会扫入任何密钥
- [ ] A4 密钥已轮换（旧 `ZCODE_ADMIN_KEY` 已失效：探测旧 key 应得 401）
- [ ] A5 密钥备份残渣已移出仓库（隔离目录 + chmod 600）
- [ ] A6 历史全量备份已存在（bundle + git-dir-original + uncommitted.diff）
- [ ] A7 目标仓名 `zhipucode2api` 在 Gitea 可用（已确认 404/not found）
- [ ] A8 老服务 systemd 单元与 cron 依赖清单已盘点（见下）

## 阶段 B · 验收（全部必须通过）

- [ ] B1 `go build ./...` + `go vet ./...` 零错误
- [ ] B2 `go test ./...` 全绿
- [ ] B3 DB 字段级读等价：新老服务读同一 accounts.db，逐账号逐字段一致
- [ ] B4 对拍 L1：33 端点状态码全一致
- [ ] B5 对拍 L2：33 端点响应结构（键集合 + 类型）全一致
- [ ] B6 对拍 L3：读接口归一化后值严格一致
- [ ] B7 6 个黑盒测试重指向新服务后全绿
- [ ] B8 真实账号成功调用 `/v1/messages` 并拿到非空回复
- [ ] B9 对拍器自测可信（baseline==candidate 时全 L3 一致，无假阳性）
- [ ] B10 无任何明文密钥进入新仓（扫描通过）

## 阶段 C · 切换

- [ ] **C-1 🔴 确认新 unit 的 `ZCODE_DATA_DIR` 指向老库绝对路径**（ADR-005）
  - `ZCODE_DATA_DIR=data` 是相对路径，相对 `WorkingDirectory` 解析
  - 新仓 `WorkingDirectory` 不同 → 会打开**全新的空库**（SQLite 自动创建，**静默 0 账号**）
  - 已修：unit 显式设 `ZCODE_DATA_DIR=/opt/.../external/zcode2api/data`
- [ ] **C-2 🔴 确认 `EnvironmentFile` 指向老仓 `.env` 且无 `-` 前缀**（ADR-005）
  - 原配置指向不存在的 `zhipucode2api/.env` + `-` 前缀 → 静默用默认弱 key、找不到 node
  - 已修：改指老仓 `.env`，**去掉 `-`**（缺失即启动失败，宁可可见的失败也不要静默不安全）
- [ ] **C-3 🔴 切换后立即验证 `/admin/api/accounts` 返回 6 个账号（非 0）**
  - ⚠️ **CR-39 升级（t5b 审查）**：C-3 原判据**不足以**拦 A2——服务能在 0 账号库上**正常启动**
    且通过 C-1/C-2 的文本检查（`freshFile` 使守卫在已存在库上完全不检查账号数），
    C-3 只能在切换**之后**才发现问题。**加固判据（二选一）**：
    ```bash
    # ① 启动日志断言（推荐，切换前即可判）
    journalctl -u zhipucode2api -n 20 | grep -q 'pool: 6 accounts' || echo "🔴 空库风险"
    # ② 进程环境 + 库内容双确认
    sudo cat /proc/$(systemctl show -p MainPID --value zhipucode2api)/environ | tr '\0' '\n' | grep ZCODE_DATA_DIR
    ```
  - 前提：CR-39 修复（守卫判据与 `freshFile` 解耦，或拦截时清理自建产物，或加启动日志账号数）
- [ ] **C0 🔴 先改 `z2a_gateway_guard.sh` 的 unit 识别**（否则双守护抢端口）
  - 现状：脚本只在 `systemctl is-enabled zcode2api.service` 为真时 `exit 0` 让位
  - 切换后该 unit 被 disable → 脚本**不再让位** → 走 `nohup` 兜底逻辑**拉起旧服务** → **抢 9120**
  - 修法：判断改为同时识别新老 unit：
    ```bash
    if systemctl is-enabled --quiet zcode2api.service 2>/dev/null || \
       systemctl is-enabled --quiet zhipucode2api.service 2>/dev/null; then
    ```
  - **此项必须在 stop 老 unit 之前完成**，否则下一次 cron 触发（2 分钟内）即抢端口
- [ ] C1 新服务在**非 9120 端口**先起起来跑通全部验收（避免与老服务抢端口）
- [ ] C2 停老 unit：`systemctl stop zcode2api && systemctl disable zcode2api`
- [ ] C3 装新 unit：`cp scripts/zhipucode2api.service /etc/systemd/system/ && systemctl daemon-reload && systemctl enable --now zhipucode2api`
- [ ] C4 验证 listener：`ss -tlnp | grep 9120` 指向新进程
- [ ] C5 验证数据：管理接口能列出全部账号（条数与老服务切换前一致）
- [ ] C6 验证网关：`/v1/models` 200 且 `/v1/messages` 真实可用
- [ ] C7 验证 cron 兼容：`z2a_patrol.sh` 与 `weekend_claim_watch.sh` 仍能取到 key 并成功调用
  - 这两个脚本的 `ROOT` 由 `$(dirname)` 动态解析、`API_BASE` 固定 9120 → **老仓目录保留则零改动**
  - 若归档老仓：须迁 `scripts/` 并更新 crontab 那一行（详见 `cutover-external-deps.md`）
  - 🔴 **两条此前遗漏的外部依赖**（t15 演练发现，2026-10-15）：
    - `workspace/dsh-zcode-plan/lib/index.js` **硬编码 `accountsDb` 指向老仓**
      → **归档老仓即断**。原检查表称「dsh-zcode-* 不需改」**不成立**
    - `workspace/scripts/z2a-patches/entitlements.py` 的 `TARGET` 指向老仓 `app/quota.py`
      → Go 版须**在源码层原生实现该补丁语义**，否则切换即功能回退
  - ⚠️ **归档后会断链**（t15 实测：`grep: .env: No such file`）→ 见 `retire-plan.md` §三「归档后 cron 冒烟」
- [ ] C8 验证 DSH 侧：`dsh-zcode-connect` 插件能正常拉数据（读 .env，无需改动）
- [ ] C9 观察窗口：至少 30 分钟无异常，日志无 panic

## 阶段 D · 回滚预案（切换前必须可用）

- [ ] D1 回滚命令已就绪：`systemctl disable --now zhipucode2api && systemctl enable --now zcode2api`
- [ ] D2 老仓仍在原地、可立即启动（不要先删！）
- [ ] D3 数据未被破坏：新服务全程**只读**老库（写操作需明确设计后才允许）
- [ ] D4 已记录切换前账号快照（条数 + 各账号 status），用于切换后比对

## 阶段 E · 归档与改名（**仅在 C 阶段稳定通过后**）

- [ ] E1 新仓推 Gitea `hawkzed/zhipucode2api`
- [ ] E2 老仓历史重写为单提交（去除历史来源）→ 推 `hawkzed/zcode2api`
- [ ] E3 改名：先把 `zhipucode2api` 仓内容作为 `zcode2api`（或按用户最终决定）
- [ ] E4 老仓本地目录归档（不要直接 rm，移到 archive/ 或备份目录）
- [ ] E5 更新 cron 脚本里的路径引用（若有）
- [ ] E6 验证：Gitea 上 `zcode2api` 只有我们自己的提交，`git log` 无他人作者

## 已知依赖清单（切换影响面）

> 详细分析见 `cutover-external-deps.md`（含每项的实测依据与修法）

| 依赖方 | 形式 | 取值方式 | 切换是否需改 |
|---|---|---|---|
| systemd `zcode2api.service` | 常驻进程 | ExecStart 调 cli.py serve | **需换新 unit** |
| cron `z2a_gateway_guard.sh` | 每 2 分钟 | 探测端口 + systemd 感知 | 否（端口不变即可） |
| cron `z2a_patrol.sh` | 每 2 分钟 | 从 `.env` 读 key → 打 admin API | 否（同端口同 env） |
| cron `weekend_claim_watch.sh` | 按需 | 从 `.env` 读 key → 打 admin API | 否 |
| DSH `dsh-zcode-connect` | 客户端插件 | 读 `.env` 的 ZCODE_ADMIN_KEY | 否（同文件同键名） |
| `dsh-zcode-quota` | 客户端插件 | 打 admin API | 否 |

**关键洞察**：只要新服务**监听同一端口 9120、复用同一 `.env`、保持同一 admin API 契约**，除 systemd unit 外所有依赖方**零改动**。

## 附录 · monitoring 对拍的三条操作要点（Catherine 实测坐实，2026-10-16）

`AD-monitoring` 的 ORDER 判据在**跨服务**语境下需要两侧都有样本。以下三条是实测结论，
不遵守会得到**假红**（或把真缺口误判成判据问题）：

1. **两侧都要发预置流量，不是只发候选。**
   只发一侧 → 另一侧 0 条 → ORDER 判 `pass=False`（CR-37：一侧有样本一侧没有 = 缺陷证据，
   **不是** `no_sample` 可见跳过）。legacy `reqlog.begin` 位于 `_dispatch` **之前**
   （`gateway.py:240`），故只要请求过了本地校验（object + 非空 model + 非空 messages）
   就会记录，**与上游结果无关**。

2. **候选每次重启后必须重发。** reqlog 是**纯内存**环形缓冲（`deque(maxlen=500)` 语义），
   重启即清零且**不自愈**。这正是早期误判「基线恒空」的原因（实际是被 `MUT-monitoring-clear` 清过）。

3. **`MUT-monitoring-clear` 若排在 `AD-monitoring` 之前，两者之间必须补发预置流量** ——
   该 case 会把环清空，否则基线侧也变 0 条。

**推荐预置命令**（零 token 成本：`bigmodel/` 前缀命中订阅通道走真实转发，
上游立即以 400 / `1211 Unknown Model` 拒绝，不消耗 token）：

```bash
for P in 9120 19120; do
  curl -s -m 15 -X POST "http://127.0.0.1:$P/v1/chat/completions" \
    -H "Authorization: Bearer $ZCODE_ADMIN_KEY" -H 'Content-Type: application/json' \
    -d '{"model":"bigmodel/zzz-preseed","messages":[{"role":"user","content":"preseed"}]}' >/dev/null
done
```

> 🔴 **凭据必须用 `ZCODE_ADMIN_KEY`，不要用 `ZCODE_GATEWAY_KEY`**（2026-10-16 修正，
> 由 Catherine 在 t13 验收中指出，captain 实测确认）。
>
> `ZCODE_GATEWAY_KEY` 在 `.env` 中是**被注释掉的**（实测 `.env:12` = `#ZCODE_GATEWAY_KEY=`），
> 因此 `source .env` 后**该变量不存在**、展开为空串。生效键名集合只有
> `ZCODE_PORT / ZCODE_HOST / ZCODE_DATA_DIR / ZCODE_ADMIN_KEY / ZCODE_NODE_PATH / CAPTCHA_POOL_MIN / ZCODE_QUOTA_REFRESH_INTERVAL`。
> （判定要点：`grep -c '^ZCODE_GATEWAY_KEY='` = 0 会因为 `#` 前缀而**误报"该键不存在"**；
> 正确写法是 `grep -n 'GATEWAY_KEY' .env` → 命中第 12 行的注释行。契约 `env-contract.md` §3.2 与
> `compat-risks.md` 均已正确记载"被注释"这一事实，本条文档此前引用变量名而未核对注释状态。）
> 两者混用会让命令**静默失效**：
> - **当前**（`meta.gateway_key` 为空，实测长度 0）：空 Bearer 也能过 → 命令**碰巧**有效，
>   掩盖了错误。三种写法（空 Bearer / ADMIN_KEY / 完全不带头）实测均 200/400 并被记录。
> - **cutover 后一旦配置 `gateway_key`**：该命令 401 → 预置流量发不出去 → 两侧 0 条 →
>   `AD-monitoring` **假红**，而排查者会误判为判据缺陷。
>
> 用 `${ZCODE_ADMIN_KEY}` 还有第二个理由：**与 harness 同源** ——
> `cases.json` 的 `AD-monitoring` 用的正是 `"Authorization": "Bearer ${ZCODE_ADMIN_KEY}"`，
> 由 `--env-file` 注入。文档命令与对拍实际凭据同源，避免"手工复现与自动对拍凭据不同"的排查陷阱。

建议**每侧发两条**：单元素数组无法自证降序方向，双元素才能证明 `direction:desc`。
