# 切换的外部依赖影响分析（captain 实测，2026-10-15）

> 本文回答一个问题：**切换后，除 systemd unit 外还有哪些东西必须改？**

## 结论速览

| 依赖方 | 位置 | 切换后是否需改 | 依据 |
|---|---|---|---|
| `zcode2api.service` | `/etc/systemd/system/` | **需换新 unit** | ExecStart 指向 cli.py |
| cron `z2a_patrol.sh` | crontab（每 2 分钟） | **暂可不改**（见下） | 路径动态解析 + 端口不变 |
| cron `z2a_gateway_guard.sh` | crontab（每 2 分钟） | **需评审** | 内含 `systemctl is-enabled zcode2api` 判断 |
| `dsh-zcode-connect` 插件 | DSH 插件 | **不需改** | 读 `.env` 文件 + 打 admin API，两者都不变 |
| `dsh-zcode-quota` 插件 | DSH 插件 | **不需改** | 打 admin API，端口不变 |

## 详细分析

### 1. cron `z2a_patrol.sh` —— 关键发现：路径是动态解析的

```bash
ROOT="${Z2A_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
API_BASE="${Z2A_API:-http://127.0.0.1:9120}"
ADMIN_KEY=$(grep "^ZCODE_ADMIN_KEY" "$ROOT/.env" | cut -d= -f2)
```

- `ROOT` 由脚本自身位置**动态推导**，非硬编码
- `API_BASE` 指向端口 9120（**新服务监听同一端口**）
- `ADMIN_KEY` 从 `$ROOT/.env` 读（**新服务复用同一 .env**）

**→ 只要老仓目录原地保留，这行 crontab 切换后仍能正常工作。**

**但它有一个隐患**：脚本读的是**老仓的 `.env`**。若将来老仓归档/删除：
- 该 cron 行必须改为指向新仓的 `scripts/z2a_patrol.sh`
- 且新仓需要有对应的 `.env`（或改用 `Z2A_ROOT` 环境变量指向保留的配置目录）

**建议**：归档老仓时，把 `scripts/` 一并迁到新仓，并同步改 crontab 那一行。

### 2. cron `z2a_gateway_guard.sh` —— 需要评审（关键）

```bash
if systemctl is-enabled --quiet zcode2api.service 2>/dev/null; then
  # systemd 已 enabled → guard 让位
```

**问题**：该脚本以 `zcode2api.service` 是否 enabled 来决定"是否让位给 systemd"。

切换后：
- `zcode2api.service` 将被 **stop + disable**
- `zhipucode2api.service` 将 enable

**→ guard 会认为"systemd 未托管"，转而尝试自己拉起服务！**

这会造成**双守护抢端口**风险。**必须在切换时同步修改该脚本**，把判断改为识别新 unit：

```bash
# 改为（同时识别新老 unit）
if systemctl is-enabled --quiet zcode2api.service 2>/dev/null || \
   systemctl is-enabled --quiet zhipucode2api.service 2>/dev/null; then
```

**已记入 cutover-checklist.md 阶段 C 的待办。**

### 3. DSH 插件 —— 不需改

`dsh-zcode-connect` 的取值逻辑（实测其 lib/index.js）：
```js
const text = await readFile(envFile, "utf8");
const m = text.match(/^ZCODE_ADMIN_KEY=(.*)$/m);
```
- 读 `.env` **文件**（非进程环境变量）→ 文件仍在老仓，内容不变
- 打 admin API（端口 9120 不变）

**→ 零改动。** 但若将来老仓 `.env` 被移动，插件的 `envFile` 配置需同步更新。

## 切换前必须完成的动作（汇总）

1. ☐ 修改 `z2a_gateway_guard.sh`：识别新 unit（**否则双守护抢端口**）
2. ☐ 决定老仓去留：
   - 保留 → cron 与插件**零改动**
   - 归档 → 须迁 `scripts/` + 更新 crontab + 更新插件 `envFile`
3. ☐ 新 unit 的 `EnvironmentFile` 指向正确的 `.env` 路径

## 实测命令（可复现）

```bash
# 确认 cron 中指向老仓的行
crontab -l | grep zcode2api

# 确认 guard 的 unit 判断逻辑
grep -n "systemctl is-enabled" workspace/scripts/z2a_gateway_guard.sh

# 确认插件的 .env 读取方式
grep -n "ZCODE_ADMIN_KEY" workspace/dsh-zcode-connect/lib/index.js | head -3
```
