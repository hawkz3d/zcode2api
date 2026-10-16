# ADR-005 · 切换时的数据目录与 .env 处置

- 状态：**已裁定**（2026-10-15）
- 裁定人：captain（基于实测）
- 触发：captain 做切换准备时发现——若按当前 unit 配置切换，**新服务会读到空库**

## 问题（实测）

`ZCODE_DATA_DIR=data` 是**相对路径**，由进程相对于 `WorkingDirectory` 解析。

| | 老服务 | 新服务（当前 unit 配置） |
|---|---|---|
| `WorkingDirectory` | `.../external/zcode2api` | `.../external/zhipucode2api` |
| `ZCODE_DATA_DIR` | `data` | `data`（未在 unit 设置，用默认） |
| **实际解析到的库** | `zcode2api/data/accounts.db` | `zhipucode2api/data/accounts.db` |
| 该文件是否存在 | ✅ 存在（53248 B，6 账号） | ❌ **不存在** |

**后果**：切换后新服务会打开一个**全新的空库**，6 个账号全部消失。
（SQLite 对不存在的库文件会**自动创建**，所以不会报错，只会静默看到 0 个账号——这是最危险的失败形态。）

**附加问题**：新 unit 的 `EnvironmentFile=-.../zhipucode2api/.env` 指向一个**不存在**的文件。
`-` 前缀使其静默忽略，于是 `ZCODE_ADMIN_KEY`、`ZCODE_NODE_PATH` 等全部落回默认值
→ 鉴权失效（默认弱 key）+ 找不到 node（验证码求解失败）。

## 裁定：切换时**共用同一份数据与配置**

### 1. 数据目录：新服务读**老库**（零迁移）

在 `zhipucode2api.service` 中**显式设置绝对路径**：

```ini
Environment=ZCODE_DATA_DIR=/opt/zcode2api/data
```

**理由**：
- **零数据迁移** —— 不需要拷贝/同步 DB，避免切换瞬间的数据分歧
- **回滚安全** —— 若需回滚，老服务立即读到同一份数据，切换期间的数据变更不丢失
- **避免双写** —— 若两个服务各持一份库，切换窗口内的写入会分裂
- 与现有 cron/插件（读老仓 `.env`、打 9120）保持一致

**注意**：这要求切换期间**只有一方在运行**（stop 老 → start 新），不得同时运行。

### 2. `.env`：新 unit 直接**复用老仓的 .env**

```ini
EnvironmentFile=/opt/zcode2api/.env
```

**理由**：
- 单一配置源 —— 密钥轮换只需改一处，两个服务（切换前后）都跟随
- cron 脚本、`dsh-zcode-connect` 插件都读这份 `.env`，保持一致
- 避免密钥在多个文件间复制（复制是泄漏与失配的温床）

**去掉 `-` 前缀**：让 `.env` 缺失成为**启动失败**而非静默降级——
宁可起不来（立刻可见），也不要带着默认弱 key 跑起来（静默不安全）。

### 3. 待办：老仓归档时需同步

若将来老仓目录被归档/删除：
- `ZCODE_DATA_DIR` 与 `EnvironmentFile` 都指向其中 → **必须先迁移**
- 建议届时把 `data/` 与 `.env` 迁到独立位置（如 `/opt/appdata/zcode2api-state/`），
  两个 unit 同时改指该位置

## 🔴 补充实测：`EnvironmentFile` 优先于 `Environment`（2026-10-15 captain 实测）

**背景**：Lamias 的独立验证指出一个切换日风险——`.env` 里也定义了 `ZCODE_DATA_DIR`，
而 unit 里同时有 `Environment=ZCODE_DATA_DIR=<绝对路径>`。**两者谁生效？**

他推断「systemd 按出现顺序合并，`Environment` 行在后 → 应胜出」。

**captain 实测推翻了这个推断**（在临时 unit 上跑，不碰生产）：

```ini
[Service]
EnvironmentFile=/tmp/test-env-file          # TESTVAR=from_EnvironmentFile
Environment=TESTVAR=from_Environment_line   # 行在后
ExecStart=/bin/sh -c 'echo $TESTVAR'
```
```
RESULT: TESTVAR=from_EnvironmentFile        ← EnvironmentFile 胜出
```

**换顺序再测一次**（`Environment` 在前、`EnvironmentFile` 在后）：
```
RESULT2: TESTVAR=from_EnvironmentFile       ← 仍是 EnvironmentFile 胜出
```

**结论：`EnvironmentFile` 的值优先覆盖 `Environment=`，与行序无关。**

> ⚠️ **版本边界注记（Lamias 建议，captain 采纳）**：
> 上述结论是**在本机 systemd 实测所得**，不代表跨版本通用。
> - 本机版本：`systemd 252 (252.39-1~deb12u1)`，内核 `6.18.18.c952-trim`
> - 官方 `man systemd.exec` 明确写了 `Environment=` 之间「later overrides earlier」，
>   但对 `EnvironmentFile` 与 `Environment` **之间的优先级没有明文规定**
> - 因此该行为**属于实现细节**，不得作为跨版本通例引用
>
> **唯一判据 = 目标机器上的实际进程环境**（见下方「切换日验证」）。
> 若将来 systemd 升级，必须重跑该验证，不可依赖本条结论。

### 对本 unit 的直接影响（🔴 必须修）

当前 unit：
```ini
EnvironmentFile=/…/zcode2api/.env          # 其中 ZCODE_DATA_DIR=data（相对路径！）
Environment=ZCODE_DATA_DIR=/…/zcode2api/data
```

按实测行为，**生效的是 `.env` 里的 `data`（相对路径），而不是我们设的绝对路径**——
也就是说，**ADR-005 的修复被 `.env` 静默覆盖了，问题依然存在**。

### 修法（三选一，推荐 ①）

1. **`.env` 里也改成绝对路径**（推荐）
   - 单一来源、最不容易再被覆盖
   - 但要注意：`.env` 同时被 cron 脚本与 DSH 插件读取，改前确认它们不依赖相对语义
2. **unit 里改用 `EnvironmentFile` 覆盖**：把绝对路径写进一个**后加载的** `EnvironmentFile`
   - systemd 按**文件出现顺序**处理多个 `EnvironmentFile=`，后者覆盖前者
   - 但比 ① 更绕、更易错
3. **让新服务忽略相对路径**：启动时若 `DATA_DIR` 是相对路径 → 拒绝启动（fail fast）
   - 见 Lamias 建议的启动守卫，可作为纵深防御保留

### 切换日验证（必须用实测值，不能读 unit 文本）

```bash
# 在真机上确认 systemd 实际合并出的值
sudo systemctl show zhipucode2api -p Environment
# 或启动后看进程实际环境
sudo cat /proc/$(systemctl show -p MainPID --value zhipucode2api)/environ | tr '\0' '\n' | grep ZCODE_DATA_DIR
```

**判据**：必须是**绝对路径**且指向 6 账号的库；若显示 `data` → 修复未生效。

## 附：Lamias 独立验证的 A1/A2 精确化（captain 复现确认）

我原判断「会静默读到空库」在**当前仓状态下不成立**，实际有两种形态：

| 形态 | 条件 | 表现 |
|---|---|---|
| **A1** | `zhipucode2api/data/` **不存在**（= 当前真实现状） | SQLite **只创建文件、不创建父目录** → `unable to open database file (14)` → **LOUD 启动失败**（crash loop 可见） |
| **A2** | 该目录**存在且为空**（只差一个 `mkdir`） | open 成功且零告警 → 建空表 → **accounts=0** → WAL 三件套静默落盘 → 后续启动复用空库 → **静默灾难** |

captain 用 python sqlite3 复现，两者行为一致（引擎级行为，与 Go/驱动无关）。

**由此得出一条实现守则（写给实现方）**：
> **`cmd/serve` 永远不要加 `os.MkdirAll(DataDir)` 这类"友好建目录"。**
> 那会把 A1 的响亮失败亲手升级成 A2 的静默空库。
> 宁可启动失败（立刻可见），也不要静默跑在空库上。

## 对切换检查表的影响

新增前置项（已写入 `cutover-checklist.md`）：
- [ ] **C-1** 确认新 unit 的 `ZCODE_DATA_DIR` 指向**老库绝对路径**
- [ ] **C-2** 确认 `EnvironmentFile` 指向**老仓 .env**（且无 `-` 前缀）
- [ ] **C-3** 切换后立即验证：`/admin/api/accounts` 返回 **6 个账号**（非 0）

## 实测验证命令

```bash
# 确认老库位置与账号数
ls -la /opt/zcode2api/data/accounts.db
# 切换后（新服务）应返回 6
curl -s -H "Authorization: Bearer $K" http://127.0.0.1:9120/admin/api/accounts | python3 -c "import sys,json;print(len(json.load(sys.stdin)))"
```

---

## ✅ 修复有效性实测（captain，2026-10-15 21:06）

用**真实的两个 EnvironmentFile**（老仓 `.env` + `zhipucode2api.env`）在 systemd 下跑实际合并：

```bash
sudo systemd-run --unit=envprobe \
  --property=EnvironmentFile=/…/zcode2api/.env \
  --property=EnvironmentFile=/…/zhipucode2api/scripts/zhipucode2api.env \
  /bin/sh -c 'echo "FINAL_DATA_DIR=$ZCODE_DATA_DIR"'
```

**输出**：
```
FINAL_DATA_DIR=/opt/zcode2api/data
```

**结论**：
- 最终生效的是**绝对路径**（来自后加载的 `zhipucode2api.env`）✅
- **不是**老 `.env` 里的相对 `data` → 证明覆盖有效
- 这是**实测**而非推断，可复现

**剩余**：`systemd-analyze verify` 报 `bin/zhipucode2api is not executable: No such file or directory` —— 这是预期的（t6 尚未产出二进制），**不是 unit 配置问题**。

**切换日复核命令**（保留）：
```bash
sudo systemctl show zhipucode2api -p Environment
sudo cat /proc/$(systemctl show -p MainPID --value zhipucode2api)/environ | tr '\0' '\n' | grep ZCODE_DATA_DIR
```
