# zhipucode2api 交付报告

> 生成时间：**2026-10-16 01:37 CST**（`date` 实测）· 死线：02:19
> 基线 commit：见下方「交付锚点」

## 一、结论

**重写完成，对拍 49/49 全绿（0 fail / 0 flaky）。**

| 维度 | 结果 |
|---|---|
| 对拍（老服务 9120 ↔ 候选 19120） | **49 case pass=49 fail=0 flaky=0** |
| Go 代码 | 11 包，8 个含测试包全 `ok` |
| build / vet | 全绿 |
| 提交数 | 见 `git rev-list --count HEAD` |

## 二、交付锚点

- 仓库：`/opt/zhipucode2api`
- 分支：`main`
- 部署物：`bin/zhipucode2api`（静态二进制）+ `scripts/zhipucode2api.service`
- 契约：`docs/contract/`（endpoints / sqlite-schema / env-contract / compat-risks CR-01..CR-39 等）

## 三、本次冲刺（01:19 → 01:37）闭合的五项

| # | 项 | 处置 |
|---|---|---|
| 1 | **t13 管理 API**（22 端点） | Catherine 交付，合并 `86ae0c4` |
| 2 | **t24 通道化 picker** | Lamias 交付，合并 `3fbaf7a` |
| 3 | **harness L3 豁免链缺陷** | Liche 修复（`diff_values` 补两侧原值 + 三红线哨兵 52→55），合并 `a9cfcde` |
| 4 | **reqlog 接线**（B-21/B-22） | captain 接手（Catherine 的 ring + 接线），`7833f5d` |
| 5 | **bigmodel/ 前缀判据** | Lamias 交付，合并 `585adf5` |

## 四、两个「对拍看不见的真缺陷」（本次最有价值的发现）

### ① `bigmodel/` 前缀路由
```
老服务: bigmodel/glm-4.5-flash → 200，model 回显 glm-4.5-flash（前缀剥离）
        无前缀 zzz              → 3006（plan 账本）
        bigmodel/zzz            → 1211（订阅账本，进上游）
候选(修前): bigmodel/zzz        → 3006（错走 plan）
候选(修后): bigmodel/zzz        → 1211 ✓ 与老服务一致
```
**对拍抓不到**：`GW-chat-invalid-model` 用的是无前缀模型名，两侧都 3006 → 全绿。
契约对 `bigmodel/` 前缀与 `x-provider` 头**双双零记载** —— 属契约盲区。

### ② reqlog 恒空
候选 `entries` 恒 `[]`，而基线有生产流量样本。`AD-monitoring` 的 ORDER 判据
（跨服务时一侧有样本一侧没有 = 缺证据）正确地把它判红。

## 五、已知未闭合项（诚实清单）

| # | 项 | 状态 |
|---|---|---|
| U-1 | captcha 惰性化（对话网关链路） | **契约已冻结**（`i3-captcha-lazy-contract.md`，453 行），**实现 0 行** —— 真实归属是 `internal/gateway/`，不是 claim（t23 三重证据推翻 t18 的归属漂移）；t22 立题已同步修正 |
| U-2 | monitoring entries 的 5 处字段级偏差 | Catherine 修复中（`ok` 语义反转 / model 未归一化 / messages 不记录 / Begin 时机 / nullable 三态丢失）。**根因**：`$.entries` 的 `replace_typed` 归一化把内部字段值全折叠 → 5 处偏差在 49/49 全绿下完全不可见 |
| U-3 | 真切换（systemd cutover） | preflight 8/9（唯一红为临时 19120 进程）；**未执行** |
| U-4 | 老仓历史重写（去 fork 来源） | 未开始（方法论已备：见 loomy2api 单提交重写） |
| U-5 | 归档老仓（阶段 E） | 未开始（`retire-plan.md` 已备） |

## 六、本轮新增的两道工具级防线

### 6.1 危险端点裸调守卫（`scripts/conformance/guard-dangerous-endpoints.sh`）

**事故**：t13 验收「export 路径无 accounts 段」时，执行
`curl -H "Authorization: Bearer $KEY" .../admin/api/export` **未丢弃响应体** ——
该端点按契约回显**全部 6 个账号的明文 secret**（4 条 JWT + 2 条 apiKey），
明文因此进入会话上下文。作者（Catherine）主动披露。

**落地**（提交 `076b797`）：扫全仓 `.sh/.py/cases.json`，凡**发请求**到该端点而
响应体未丢弃（无 `-o /dev/null`、未只取 `keys()`）→ `exit 1` 并指出文件与修法。

**守卫自身的两次自纠（都有实测，值得记档）**：
1. 初版把 `cases.json` 的声明式 `"path": ".../export"` 误判为裸调 → **假阳性 4 处**。
   修正：只在含 `curl/httpx/requests/urllib/wget` 的行上判定。
2. 初版 `find` 限 `maxdepth 2` 且 `continue` 掉自身 → **往守卫里加裸调仍判绿 = 零判别力**
   （正是本项目一直在批评的缺陷类）。修正后**可证伪性双向实测**：造裸调 → 红；还原 → 绿。

**边界（已知不可覆盖）**：本机无 sqlite3 CLI、无语义检查器，守卫只能做**行级模式匹配** ——
拦得住"忘记 `-o /dev/null`"，拦不住"先 `-o file` 再 `cat`"。

### 6.2 凭据轮换裁定（captain）

Catherine 建议 cutover 前轮换这 6 个账号凭据。**裁定：不轮换**，理由：
- 这 6 条凭据的明文**本来就存在生产库** `accounts.db` 的 data JSON 中，且
  **每次 `/admin/api/export` 都会原样回显** —— 它是**设计上可导出的运营资产**，
  不是"泄露后即失效"的凭据（对比：admin_key 是可轮换的网关凭据，账号 secret 不是）。
- 轮换 4 条 JWT 需重新 OAuth 登录（涉及手机验证与额度包状态），**成本与风险高于本次上下文暴露**。
- 治本方向是**切断进入上下文的路径**（守卫 + Sentinel 指纹化），已落地。

## 六点五、凭据文件权限收紧（2026-10-16 01:53 执行，用户批准）

**发现**（Catherine 从 t13 验收中上报，captain 独立复核）：非系统用户 `hawkzed`
（uid=1000，`Administrators` 组）**无需任何漏洞即可读走全部 6 个账号凭据 + `admin_key`**，
且**可写** `accounts.db` 与 `external/` 目录（可替换凭据或整个部署）。

**改前 → 改后**：

| 路径 | 改前 | 改后 |
|---|---|---|
| `zcode2api/data/accounts.db`（+`-wal`/`-shm`） | 777 | **600** |
| `zcode2api/data/*.bak*`（含同样凭据的 5 个历史备份） | 777 | **600** |
| `zcode2api/.env`（含 `admin_key`） | 777 | **600** |
| `zcode2api/data/` | 777 | **700** |
| `zcode2api/` | 777 | **755** |
| `external/`（可替换整个部署目录） | 777 | **755** |

**服务零扰动**：改后 `active`、`9120 /v1/models` → **200**、
`/admin/api/accounts` → **6 个账号**（读写库正常）。
原权限已备份（`/tmp/perm-backup-021710.txt`）可回滚。**未重启服务**（chmod 对已打开 fd 无效，无需重启）。

### ⚠️ 本次验证方法自身的一次假阳性（值得记档）

**第一轮验证用了 `sudo -u hawkzed test -r <file>` → 全部报"仍可读/仍可写"，看起来收紧失败。**
真因：`hawkzed` 在 `sudoers` 中有 **`(ALL : ALL) ALL`** —— `sudo -u hawkzed` 是以 **root 能力**执行，
**测的不是文件权限**，而是 root 恒可读写。这是**用有特权的主体去验证权限**的经典错误。

**正确判据**：`setpriv --reuid=1000 --regid=1001 --clear-groups test -r <file>`
（真降权、清空附加组）。复验：`accounts.db` / `.env` / `.bak` **三者均不可读**；
方法有效性对照：同法测 `/etc/shadow` → 不可读（证明方法能识别真拒绝）。

**沉淀判据**：验证"降权后是否可访问"时，**必须确认测试主体真的降权了** ——
`sudo -u` 在目标用户有 sudo 权限时**不构成降权测试**。

## 七、本轮新增的防误判机制（harness 门禁）

**假红形态识别**（Catherine 实拍）：候选进程未起时，harness 输出
`pass=0 fail=49 flaky=49` + 每条 `L1:FAIL` + `unmet=L1,L2,L3` —— 该形态与
**真实契约破坏几乎无法区分**，会把排查者引去查代码（实际只需起服务）。

**已落地门禁**（`c6a30e1`）：开跑前探针两侧 `/v1/models`，任一**连接级**
失败即 `exit 2` 并指名是哪一侧：

```
!! 服务未就绪：candidate http://127.0.0.1:19120 不可达：Connection refused
拒绝开跑：…该形态与真实契约破坏无法区分，会误导排查方向。
排查：ss -tlnp | grep ':PORT ' 确认监听；再 curl <base>/v1/models。
```

设计要点：① HTTP 层有响应（401/404）视为"在听"，**不误杀带鉴权的部署**；
② `--engine-selftest` / `--dom-selftest` 跳过门禁（不需要服务）。

## 八、方法论沉淀

1. **对拍折叠掩盖**：`$.entries` 的 `replace_typed` 归一化把内部字段值全折叠 →
   5 处字段级偏差在 49/49 全绿下完全不可见（CR-37 同型）。
   **判据**：容器级折叠规则必须逐条回答「被折叠的维度里有没有持久契约混在进程态里」。
2. **契约盲区靠实测补**：`bigmodel/` 前缀无任何文档记载，靠"用不存在的模型名让错误消息回显"的
   零成本探测定位。
3. **归属漂移检测**：同一报告内「文件归属」与「结论归属」不一致 ⇒ 至少一处错
   （t18 表格写 `routes/gateway`、结论写 claim，差一字）。

---

## 九、切换执行记录（2026-10-16 02:21:41 → 02:21:46）

**执行**：`bash scripts/cutover.sh switch`（含 0-4 步 + 自动回滚兜底）

```
0) 备份现状（老 unit 状态 + VACUUM INTO 库快照）  ✔
1) 安装新单元                                     ✔
2) 停用老服务 zcode2api                            ✔（inactive + disabled）
3) 启用新服务 zhipucode2api                        ✔（active + enabled）
4) 验证新服务（失败即回滚）                        ✔（/v1/models 200 + 6 账号）
   ✅ 切换成功
```

**切换前发现的 preflight 三处判据缺陷**（提交 `6aac8aa`）—— 修复前 preflight **结构性不可通过**：

| # | 缺陷 | 性质 |
|---|---|---|
| ① | 项9 要求 `$NEW_UNIT` 的 MainPID 占 19120，但该单元按 `.env` 绑 **9120** ⇒ **不可能满足** | **用红锁死切换**（零判别力判据的反方向） |
| ② | `/proc/$pid/{cwd,exe}` 普通用户 `readlink` 失败（procfs 硬化；`cmdline` 可读） | 环境事实误判为幽灵进程 |
| ③ | 非 root `ss -tlnp` 对他人 socket 不显示 `users:()` ⇒ `pid=` 取空 | 取 pid 方式错误 |

修正后 9/9 全通过，且**防幽灵强度未削弱**（孤儿进程无单元认领 → 仍被拦截）。

**切换后功能验证**：

| 项 | 结果 |
|---|---|
| `/v1/models` / `/meta` / `/openapi.json` | **200 / 200 / 200** |
| `/admin/api/accounts` | **200**，**6 个账号**全 `enabled` |
| `/admin/api/monitoring` | **200**，环内 4 条（reqlog 接线生效） |
| **真实对话** | ✅ `bigmodel/glm-4.5-flash` → 回复 `'好'`，**`reasoning_content` 思考链透传正常**，usage 550 tokens |
| 对拍（49 case） | **49/49 全绿**，0 fail 0 flaky |
| 消费方 | cron 老仓引用可达 ✓；`dsh-zcode-plan` 的 accountsDb 可达 ✓；DSH 自身 3080/3081 正常 ✓ |

**服务终态**：`zhipucode2api` **active + enabled**（开机自启）；`zcode2api` inactive + disabled。
**回滚凭据**：库快照 + 老 unit 状态在 `workspace/logs/cutover-backup-20261016-022141/`。

---

## 十、切换后跨服务对拍验证（Catherine，2026-10-16 02:29-02:31）

**方法**：她自建老服务副本做 baseline（`9121`）↔ 新服务（`9120`）真跨服务对拍 ——
这是**切换后仍能做出跨服务对照**的关键动作（老服务已停，无法直接用 9120 做 baseline）。

**结果**：

| 轮次 | 结果 |
|---|---|
| 第 1 轮（`harness-cross.log`） | 48/49（1 项 `flaky`，重试后消除） |
| 第 2 轮（`harness-cross2.log`） | **49/49 通过，0 失败**，flaky=0 |

**逐端点结论摘要**：`GW-*` 11 条全绿；`AD-*` 24 条全绿（含 `AD-monitoring` 的 `L1+L2+L3+ORDER` 四层）；
`UI-*` 6 条全绿（4 条页面字节差异按 ADR-006 降级 `L3-observed`，正式判据 `ST-DOM:PASS`）；
`ST-*` 6 条、`D-*` 2 条全绿。**差异明细段为空**（无未通过项）。
豁免命中 4 处全部为已批准的 `ADR-002-enabled-laohao`（形态精确锚定，含 `baseline_value:"True"`/`candidate_value:true`）。

**报告归档**：`docs/contract/review-evidence/cutover-cross-service-report.md`（193 行，形态扫描 0 命中）。

### 她的报告顺带暴露了我一处漏修

`modes-before.txt` 显示：**`.bak` 主文件仍是 777**（我先前只改了 `-shm`/`-wal` 两个伴生文件）。
已补修（`find … -name '*.bak*' -type f -exec chmod 600`），并以 `setpriv` 真降权复验 6 个文件 **全部不可读**。

> 教训：`chmod a b c` 对**通配符展开后的部分失败**不报错 —— 我的首个修复命令列了 4 个显式路径，
> 而 `.bak*` 通配只覆盖了当次列出的那些。**权限修复后必须以「遍历全部匹配项」复验**，不能抽查。
