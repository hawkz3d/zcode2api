# 老仓退场方案（阶段 E）

> **前置**：阶段 C（切换）已稳定运行 ≥30 分钟，且阶段 D（回滚预案）仍可用。
> **铁律**：任何一步都可逆；不可逆操作放最后。

---

## 一、影响面盘点（captain 实测，2026-10-15）

| # | 引用方 | 引用内容 | 类型 |
|:--:|---|---|---|
| 1 | crontab | `*/2 * * * * <WS>/external/zcode2api/scripts/z2a_patrol.sh` | 每 2 分钟 |
| 2 | systemd | `zcode2api.service`（切换后 stop+disable） | 已处理 |
| 3 | 脚本 | `workspace/scripts/z2a_gateway_guard.sh` 内含 `ROOT=/…/external/zcode2api` | 每 2 分钟 |
| 4 | 脚本 | `workspace/scripts/z2a-patches/entitlements.py` 引用老仓 | 按需 |
| 5 | 插件 | `dsh-zcode-connect` 配置 `envFile` 指向老仓 `.env` | 常驻 |
| 6 | 数据 | `zcode2api/data/accounts.db`（**新服务正在用它**） | 🔴 关键 |
| 7 | 配置 | `zcode2api/.env`（**新 unit 的 EnvironmentFile**） | 🔴 关键 |

**🔴 第 6、7 项意味着：老仓目录在归档后仍必须保留 `data/` 与 `.env`**，否则新服务立刻挂。

---

## 二、方案：**分三步，先解耦再归档**

### 步骤 1：把 `data/` 与 `.env` 迁到独立状态目录（**切换稳定后立即做**）

```bash
STATE=/opt/appdata/zcode2api-state
mkdir -p "$STATE" && chmod 700 "$STATE"

# 复制（先不删源，验证后再删）
cp -a /…/external/zcode2api/data        "$STATE/"
cp -a /…/external/zcode2api/.env        "$STATE/.env"
chmod 600 "$STATE/.env"
```

然后改 `zhipucode2api.service`：
```ini
EnvironmentFile=/opt/appdata/zcode2api-state/.env
Environment=ZCODE_DATA_DIR=/opt/appdata/zcode2api-state/data
```

**验证**：`systemctl restart zhipucode2api` → `/admin/api/accounts` 仍返回 **6** 个账号。

### 步骤 2：把 `scripts/` 迁到新仓或独立位置

- `z2a_patrol.sh` → 复制到新仓 `scripts/`（或独立 `z2a-ops/`）
- 改 crontab 那一行指向新位置
- 保留 `Z2A_ROOT` / `Z2A_API` 环境变量覆盖能力（脚本本已支持）

### 步骤 3：归档老仓（**最后做，且不删**）

```bash
ARCH=/opt/appdata/archive
mkdir -p "$ARCH"
mv /…/external/zcode2api "$ARCH/zcode2api-retired-$(date +%Y%m%d)"
```

**注意**：`mv` 到同分区是改名（快），跨分区是复制+删除（慢）。先确认 `df`。

---

## 三、归档后的 cron 冒烟（t15 第三轮复核新增，Hilkiah 发现）

**背景**：Hilkiah 在 t15 实测中观察到——**归档老仓后 cron 会断链**：
```
grep: /…/zcode2api/.env: No such file or directory
```

**为什么 preflight 抓不到**：preflight 在**切换前**跑，此刻「脚本存在 + 已登记」恒真。
**归档之后**的断链属**阶段 E**，本就不该由 preflight 承担 —— 而应在**归档时点**设检查点。

### 🔴 归档后必须立即执行的冒烟

```bash
# ① cron 脚本仍可执行（不报 No such file）
bash -n <迁移后的 z2a_patrol.sh> && echo "语法 OK"
# ② 手动跑一次，确认能取到 key 并成功调用
Z2A_ROOT=<新位置> bash <迁移后的 z2a_patrol.sh> 2>&1 | tail -5
# ③ 关键判据：不出现 "No such file" 或 "ADMIN_KEY" 相关错误
```

**若失败** → 说明 `.env` 或脚本路径未正确迁移 → 立即从归档目录恢复，重做步骤 2。

### 顺序约束（t15 的 F-13）

| 检查项 | 原表述 | 修正 |
|---|---|---|
| E4（归档老仓）vs D2（老仓可立即启动） | 「D2 要求老仓可立即启动」与「E4 归档」**有顺序冲突** | **先确认 D2 不再需要**（切换稳定），再执行 E4 |
| E5（更新 cron 路径） | 原为「归档后更新」 | **必须与 E4 同批执行**，否则中间窗口 cron 断链 |

---

## 四、归档前的检查表

- [ ] E-1 切换后已稳定运行 ≥30 分钟，无异常
- [ ] E-2 `data/` 与 `.env` 已迁到独立状态目录，新服务验证通过（6 账号）
- [ ] E-3 crontab 已改指新位置，且实测巡逻脚本能跑通
- [ ] E-4 `z2a_gateway_guard.sh` 已改（既认新 unit，也改 ROOT）
- [ ] E-5 `dsh-zcode-connect` 的 `envFile` 已改指新状态目录
- [ ] E-6 老仓有完整备份（bundle + 全量拷贝）
- [ ] E-7 回滚路径已确认：老 unit 仍在 `/etc/systemd/system/`，可用 `mv` 移回

---

## 五、Gitea 上的仓库处置（与本地目录无关）

| 仓库 | 动作 |
|---|---|
| `hawkzed/zcode2api` | 按 loomy2api 方法论**重写为单提交**（去除历史来源）→ 强推 |
| `hawkzed/zhipucode2api` | 新建仓 → 推新代码 |

**顺序**：先推新仓（保底），再重写老仓（可逆性更差）。

若用户最终决定只保留一个仓名：先推 `zhipucode2api`，验证后再把内容作为 `zcode2api`，删旧仓。

---

## 六、风险与回滚

| 风险 | 缓解 |
|---|---|
| 迁 `data/` 时漏了 `-wal`/`-shm` | 用 `cp -a` 整目录复制；或先 `systemctl stop` 再复制 |
| 新位置权限不对（systemd 以 `appuser` 运行） | `chown -R appuser:appuser` + `chmod 700/600` |
| crontab 改了但脚本路径错 | 改完立即 `bash -n` + 手动跑一次 |
| 归档后才发现某处引用 | 归档用 `mv`（不是 `rm`），随时可移回 |
