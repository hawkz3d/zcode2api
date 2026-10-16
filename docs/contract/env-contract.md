# env-contract.md — zhipucode2api 环境变量契约（冻结版）

> 本文是 Go 版 `zhipucode2api` **读取环境变量的唯一规格**。
>
> 🔴 **本文遵循 `adr-001-source-of-truth.md`**：事实来源优先级 = ①运行中服务的真实 HTTP 响应
> ②生产库真实 schema/数据（只读探测）③黑盒测试 ④描述现状的文档 ⑤`docs/development/03-data-formats.md`
> **仅作未来设计参考、不得作为实现依据** ⑥任何"设计要点/扩展"章节**默认未实现**。
> 本文**每条断言都标注证据来源**；无证据者标 `unverified`。
>
> **规格先于实现声明**：证据来自 ①`.env` / `.env.example`（**只读键名与长度，值一律脱敏**）
> ②生产进程 `/proc/<pid>/environ` **只读键名** ③`systemd` unit 原文 ④运行中服务的 HTTP 实测
> ⑤SQLite `meta` 表只读探测。**未纳入本轮观测** `zcode2api/app/**/*.py`、`cli.py`。
>
> **脱敏纪律**：`.env` 与 `meta` 的**值从未被回显**。本文只用键名 + 长度 + 类型结论。
> 密钥一律写作 `<redacted>`；弱默认值一律写作 `<弱默认，见实现>`（**不落明文**）。

---

## 0. 证据来源标注约定

| 标记 | 含义 |
|:---:|------|
| `[实测]` | 本次直接执行命令/HTTP 探测所得，附命令或输出 |
| `[unit]` | `/etc/systemd/system/zcode2api.service` 原文（`systemctl cat`） |
| `[env.example]` | `.env.example` 原文（文件行号） |
| `[env]` | 生产 `.env` 实测（**仅键名/长度/生效状态**） |
| `[adr]` | captain 裁定的 `adr-001` 结论（captain 亲自复核，本人采信） |
| `[docs]` | 文档断言，**未实测**；`docs/03` 类文档不作为实现依据 |
| `unverified` | 无任何证据 |

---

## 1. 五个必需变量（任务书范围）

| # | 变量 | 类型 | 默认值 | 生产实测值 | Go 读取优先级 |
|:--:|------|------|---------------|-----------|--------------|
| 1 | `ZCODE_PORT` | int | **`3000`** `[env.example]` | **`9120`** `[实测]`（§1.1） | env → 默认 3000 |
| 2 | `ZCODE_HOST` | string | **`0.0.0.0`** `[env.example]` | **`127.0.0.1`** `[env]`（§1.2） | env → 默认 `0.0.0.0` |
| 3 | `ZCODE_DATA_DIR` | path | **`data`** `[env.example]` | `data` `[env]` | env → 默认 `data` |
| 4 | `ZCODE_ADMIN_KEY` | string | **`<弱默认，见实现>`** `[adr]` | 32 字符 `[env]`（值未回显） | 🔴 **DB `meta.admin_key` 优先**（§1.4） |
| 5 | `ZCODE_NODE_PATH` | path | **`node`** `[env.example]` | **`/usr/local/bin/node`** `[env]` | env → 默认 `node` |

> 🔴 **`ZCODE_ADMIN_KEY` 默认值三处打架**（ADR-001 已裁定）：
> `.env.example` / README 写 `change-me`，`docs/development/03-data-formats.md` §54 写 `zcode`，
> **实现是 `os.getenv(..., "<弱默认>")`**。按 ADR-001 §42「默认值以**实测行为**为准」，
> 本文**不落任何默认值明文**，统一写 `<弱默认，见实现>`。
> 生产实际用的是 `.env` 的值 + DB `meta.admin_key`（§1.4 实测优先级）。

### 1.1 `ZCODE_PORT` —— 默认值与生产值的冲突（必读）

| 来源 | 值 | 标记 |
|------|-----|:---:|
| `.env.example` | `ZCODE_PORT=3000` | `[env.example]` |
| `systemd` unit `ExecStart` | `.venv/bin/python cli.py serve`（**无 `--port`**） | `[unit]` |
| `systemd` unit `Description` | `... OpenAI-compatible API **@127.0.0.1:9120**` | `[unit]` |
| `.env`（生产） | 4 字符 → 与 `9120` 长度吻合 | `[env]` |
| **实测** | 服务实际监听 **9120**（全部端点实测均在此端口） | `[实测]` |

**结论**：默认 **3000**，生产通过 `.env` 覆盖为 **9120**。

> ⚠️ **端口 3000 在本机被占用**（另一监听者），因此**不可**用「默认 3000」做联调验证 ——
> 必须显式传 `ZCODE_PORT=9120` 或读 `.env`。Go 版默认值仍必须是 3000（契约一致性），
> 否则对拍「不带 env 启动」场景会差异。见 `compat-risks.md` CR-17。

### 1.2 `ZCODE_HOST` —— 生产为回环，默认仍是 0.0.0.0

| 来源 | 值 | 标记 |
|------|-----|:---:|
| `.env.example` | `ZCODE_HOST=0.0.0.0` | `[env.example]` |
| **`.env` 实测** | **`127.0.0.1`**（9 字符，**仅回环**） | `[env]` |
| 实测可达性 | `http://127.0.0.1:9120` 可达（回环） | `[实测]` |
| `systemd` unit | 注释写「监听 127.0.0.1:9120」，unit **未** `Environment=ZCODE_HOST=...` | `[unit]` |

**结论**：默认 `0.0.0.0`（bind 全部接口），**生产经 `.env` 收窄为 `127.0.0.1`**。

> 🔴 **实测修正**：此前把 unit 注释当作叙述，但 `.env` 实测确为 `127.0.0.1` —— 注释是**准确的**。
> 生产网关**不监听外部网卡**，仅本机可用。
>
> **安全含义**：老服务默认 `0.0.0.0`（全部网卡）+ 网关 key 为空 = 匿名可用；
> 生产用两重收窄（`ZCODE_HOST=127.0.0.1` + 后续设 `gateway_key`）控制暴露面。
> Go 版**默认值必须仍是 `0.0.0.0`**（契约一致），但部署时必须照抄 `ZCODE_HOST=127.0.0.1`。见 CR-18。

### 1.3 `ZCODE_DATA_DIR`

| 来源 | 值 | 标记 |
|------|-----|:---:|
| `.env.example` | `ZCODE_DATA_DIR=data` | `[env.example]` |
| 实测 | `data/accounts.db` 位于 `external/zcode2api/data/` | `[实测]` |

**结论**：默认 `data`（相对 `WorkingDirectory` 解析）。`systemd` unit 的
`WorkingDirectory=/opt/zcode2api` → 实际路径
`/opt/zcode2api/data`。

> Go 版**必须以 `WorkingDirectory` 为基准解析相对路径**（不是二进制所在目录），
> 否则 systemd 切换后读不到同一个库。见 CR-19。

### 1.4 `ZCODE_ADMIN_KEY` —— DB 优先（关键，ADR-001 裁定项）

**证据链（三层）**：

1. `.env.example` 原文注释 `[env.example]`：
   > `# 后台管理密码（首次启动写入 meta 表，之后以数据库为准）`
2. `docs/development/03-data-formats.md` §54 `[docs]`：
   > `| ZCODE_ADMIN_KEY | zcode | 后台密码初值（之后以 DB meta 为准） |`
3. **`[adr]` captain 亲自复核的运行时实测**：**轮换密钥时只改 env 不写库 → 不生效；
   必须同时 `cli.py set-admin-key` 才生效** ⇒ **DB `meta.admin_key` 优先于 env**。
4. **`[实测]`**：`meta.admin_key` 存在且长度 32；`.env` 的 `ZCODE_ADMIN_KEY` 值作
   `?app_key=` 与 `Authorization: Bearer` **均 200** → 两处当前值一致（与上述结论不矛盾）。

**默认值冲突（三处打架）**：`.env.example` / README 写 `change-me`；
`docs/03` §54 写 `zcode`；**实现为 `os.getenv(..., "<弱默认>")`** `[adr]`。
🔴 按 ADR-001「默认值以**实测行为**为准」，本文**统一写 `<弱默认，见实现>`，不落明文**。

**Go 读取优先级（硬规则）**：
```
1. 打开 DB，读 meta['admin_key']
2. 若存在且非空 → 用它（忽略 env）
3. 若不存在 → 用 env ZCODE_ADMIN_KEY，并写入 meta（首次播种）
4. env 也缺失 → 用 <弱默认>，并写入 meta
```
**违反后果**：`PUT /admin/api/settings {"admin_key":"新值"}` 会被 env 覆盖 → 改密失效。

> 🔴 **回写语义（Go 必读）**：`SetAdminKey` 必须**写 DB**，不能只改内存/env。
> 证据：`[adr]` 实测「改 env 后需同时写库才生效」。

### 1.5 `ZCODE_NODE_PATH`

| 来源 | 值 | 标记 |
|------|-----|:---:|
| `.env.example` | `ZCODE_NODE_PATH=node` | `[env.example]` |
| **`.env` 实测** | **`/usr/local/bin/node`**（19 字符，绝对路径） | `[env]` |

**语义**：验证码求解器（Node 子进程）的可执行路径。
**实测**：生产指向 **DSH 自带的 Node v24.4.0**（`/usr/local/bin/node`），
而非 PATH 里的 `node` —— 说明该变量在真实环境**必须显式配置**（裸 `node` 可能不在
systemd 的 PATH 中；`/proc/<pid>/environ` 实测无 `ZCODE_*`，见 CR-26）。

**`CAPTCHA_POOL_MIN` 实测为 `3`**（见 §3.4），故预解池**处于开启状态**；
`GET /admin/api/claim/captcha-config` 实测 `enabled:true`（scene_id/region/prefix 均非空）。

> Go 版若**不实现** Node 求解器（Go 无 Node 依赖），必须保留该变量的**读取与默认值**（配置兼容），
> 并让 `ZCODE_NODE_PATH` 缺失/路径无效时**不崩溃**。见 CR-20。

---

## 2. 五个变量的完整优先级矩阵

| 变量 | env 存在 | DB 存在 | 最终取值 | 证据 |
|------|:---:|:---:|------|------|
| `ZCODE_PORT` | ✅ | ✗（无对应 meta 键） | env 值 | `[env]` `9120` + `[实测]` 服务监听 9120 |
| `ZCODE_HOST` | ✅ | ✗ | env 值 | `[env]` `127.0.0.1` |
| `ZCODE_DATA_DIR` | ✅ | ✗ | env 值 | `[env]` `data` + `[实测]` 库路径 |
| `ZCODE_ADMIN_KEY` | ✅ | ✅ | **DB 值** | `[adr]` 轮换实测 + `[env.example]` 注释 + `[实测]` meta 存在 |
| `ZCODE_NODE_PATH` | ✅ | ✗ | env 值 | `[env]` 绝对路径 |

**`meta` 表实测仅 3 键**：`admin_key`、`gateway_key`、`quota_refresh_interval` ——
**没有** port/host/data_dir/node_path 的 meta 行。故这 4 个变量**只能来自 env + 默认值**。
（`quota_refresh_interval` 是第 6 个配置项，见 §3。）

> 🔴 **`meta` 键名白名单（实测 3 键）**：`CLAIM_*`、`POOL_*`、`QUOTA_STAGGER_*` 等
> **实测均不存在**（ADR-001 已裁定不入契约）。

---

## 3. `.env` 键清单（实测，值全部脱敏）

生产 `.env`（`external/zcode2api/.env`，1844 字节，权限 `rwxrwxrwx`，属主 `appuser`）
**实测：12 个生效键 + 13 个注释键，共 45 行，行尾为 LF（无 CRLF、无裸 CR）** `[env]`。

> 🔴 **ADR-001 要求 3**：契约只覆盖**服务自身读取**的变量。`Z2A_SMTP_*` 属**通知脚本**（cron 消费），
> 单列 §3.3，**不计入服务契约**。

### 3.1 服务自身读取的生效键（7 个）

| 分组 | 键 | 实测值（脱敏） | 备注 |
|------|----|:---:|------|
| 服务 | `ZCODE_PORT` | `9120` | 覆盖默认 3000 |
| 服务 | `ZCODE_HOST` | `127.0.0.1` | **收窄到回环**（默认是 `0.0.0.0`） |
| 服务 | `ZCODE_DATA_DIR` | `data` | 相对路径 |
| 鉴权 | `ZCODE_ADMIN_KEY` | `<redacted>`（32 字符） | 🔴 密钥（DB 优先，见 §1.4） |
| 验证码 | `ZCODE_NODE_PATH` | `/usr/local/bin/node` | 绝对路径（DSH 自带 Node） |
| 验证码 | `CAPTCHA_POOL_MIN` | **`3`** | 预解池**开启**（`.env.example` 未列此键） |
| 额度 | `ZCODE_QUOTA_REFRESH_INTERVAL` | `600` | 与 DB `meta` 的 `600` **一致** |

### 3.2 服务读取但**非 env 注入**的配置项

| 键 | 来源 | 实测值 | 备注 |
|----|------|:---:|------|
| `gateway_key` | **DB `meta`** | 长度 **0**（空串） | `.env` 的 `ZCODE_GATEWAY_KEY` **被注释**；故实测网关**完全放行**（CR-07） |
| `quota_refresh_interval` | **DB `meta`** | `"600"`（TEXT） | 与 `.env` 同值；DB 优先（CR-27） |

### 3.3 非服务契约：`Z2A_SMTP_*`（5 个，由 cron 通知脚本消费）

| 键 | 实测值（脱敏） |
|----|:---:|
| `Z2A_SMTP_HOST` | `smtp.qq.com` |
| `Z2A_SMTP_PORT` | `465` |
| `Z2A_SMTP_USER` | `<redacted>`（16 字符） |
| `Z2A_SMTP_PASS` | `<redacted>`（16 字符） |
| `Z2A_MAIL_TO` | `<redacted>`（16 字符） |

> ⚠️ **这 5 个键不属于 Go 服务契约**（ADR-001 要求 3）：它们是 `z2a` **通知脚本**的配置，
> 由 cron 独立消费。Go 版**不应读取**它们，也**不得**因缺失而报错或崩溃。
> 通知链路的迁移属**独立任务**，不在本次「33 端点契约等价」范围。

### 3.4 `CAPTCHA_POOL_MIN` 的语义（实测）

`.env` 中该键的注释原文（逐字节选，`...` 处为脱敏截断）：
> `# 风控防治（2026-09-12）：预解池 MIN=...`
> `# 每枚都要 fetch zcode.z.ai 首页 → 实测 13h 解 1583 次（118 次/小时），`
> `# 是巡逻脚本的 100 倍量级，且"每 30 秒访问首页"是典型机器人特征。`
> `# 置 0 = ...`

**实测取值 `3`** → 预解池**开启**，池下限 3 枚。
⚠️ 注释里写着「置 0 = 关闭」，但**当前值不是 0** —— 注释是历史决策记录，值才是当前状态。

Go 版必须支持 `CAPTCHA_POOL_MIN=0` 语义（关闭预解池）**与**非 0 语义（维持池下限），
因生产实测为非 0。见 CR-20。

### 3.5 注释键（13 个，全部使用默认值）

| 键 | 默认（`.env.example`） | 语义 | 标记 |
|----|---------------------|------|:---:|
| `ZCODE_GATEWAY_KEY` | 空 | 网关 API Key（空 = 不校验）；实测 DB `gateway_key` 亦为空 → **网关完全放行** | `[env.example]` |
| `ZAI_UPSTREAM_URL` | `https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages` | JWT 通道上游 | `[env.example]` |
| `ZAI_FALLBACK_URL` | `https://api.z.ai/api/anthropic/v1/messages` | 回退上游 | `[env.example]` |
| `BIGMODEL_UPSTREAM_URL` | `https://open.bigmodel.cn/api/anthropic/v1/messages` | BigModel 上游 | `[env.example]` |
| `UPSTREAM_USER_AGENT` | `ZCode/3.0.1` | 上游 UA | `[env.example]` |
| `ZCODE_CAPTCHA_RETRIES` | `4` | 验证码重试次数（架构文档写 `MAX_CAPTCHA_RETRIES=3`，**冲突**） | `[env.example]` |
| `ZCODE_CAPTCHA_TIMEOUT` | `40` | 求解超时（秒） | `[env.example]` |
| `CAPTCHA_CACHE_TTL` | `45000` | 缓存 TTL（ms） | `[env.example]` |
| `ZCODE_COOLING_SECONDS` | `300` | 冷却时长 | `[env.example]` |
| `ZSW_PASSWORD` | 空 | `.zsb` 导出口令（**未实现蓝图**，见下） | `[env.example]` |
| `ZCODE_BUNDLE_PASSWORD` | 空 | 备选口令（`ZSW_PASSWORD` 优先，**未实现**） | `[env.example]` |
| `ZCODE_CREDENTIAL_SECRET` | — | `enc:v1` secret 覆盖（**未实现**） | `[docs]` |
| `ZCODE_MOCK_UPSTREAM` | — | 测试用 mock 上游（**仅测试**） | `[docs]` |

> 注：`ZCODE_QUOTA_REFRESH_INTERVAL` 在 `.env` 中**已生效**（值 `600`，见 §3.1）；
> 它在 `.env.example` 里是**注释样例**（默认 `60`）。

> 🔴 `ZCODE_CAPTCHA_RETRIES` 冲突：`.env.example` = `4`，`zcode2api-architecture.md` = `MAX_CAPTCHA_RETRIES = 3`。
> **以 `.env.example` 为准**（可执行样例），架构文档的数字是**叙述**。见 CR-21。

### 3.6 🔴 未实现的键（ADR-001 明令不入契约）

以下键**实测不存在**，或对应功能为**未实现蓝图**，Go 版**不得实现、不得读取**：

| 键 | ADR-001 裁定 | 证据 |
|----|-------------|------|
| `ZCODE_MASTER_SECRET` | **env 实测不存在**；凭证加密**未实现**（明文存储） | `[adr]`；`[实测]` `.env` 无此键，`.env.example` 中为空 |
| `CLAIM_*` | 实测不存在 | `[adr]` |
| `POOL_MAX_ATTEMPTS` / `POOL_COOLDOWN_SECONDS` / `POOL_EXHAUSTED_RETRY_SECONDS` | **env 实测不存在**；仅 `docs/03` 蓝图 | `[adr]` |
| `QUOTA_STAGGER_MAX_MS` | 实测不存在 | `[adr]` |
| `ZSW_PASSWORD` / `ZCODE_BUNDLE_PASSWORD` | `.zsb` 封包**未实现**（roadmap 未勾选） | `[docs]` roadmap §25 |

> 🔴 **Go 版必须读明文、写明文**凭证（CR-15）。引入加密会导致老 Python 进程**读不出凭证**。


---

## 4. systemd 注入的环境变量（实测 unit 原文）

`/etc/systemd/system/zcode2api.service` 的 `[Service]` 段**实测注入 3 个变量**：

```
Environment=NO_PROXY=localhost,127.0.0.1,::1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,zcode.z.ai,api.z.ai,open.bigmodel.cn
Environment=no_proxy=localhost,127.0.0.1,::1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,zcode.z.ai,api.z.ai,open.bigmodel.cn
Environment=PYTHONUNBUFFERED=1
```

| 变量 | 作用 | Go 对应 |
|------|------|---------|
| `NO_PROXY` / `no_proxy` | 🔴 **绕过宿主 HTTP 代理直连上游**（`zcode.z.ai`/`api.z.ai`/`open.bigmodel.cn`） | Go `net/http` **同样遵守 `NO_PROXY`**（`httpproxy` 包），**无需改代码**，但**必须保持 systemd 里的这两个变量** |
| `PYTHONUNBUFFERED=1` | Python 日志实时刷盘 | Go 无对应需求，切换后**应移除**（无害，但会误导） |

> 🔴 **`NO_PROXY` 是生产可用性的关键**：宿主有透明代理（mihomo），若上游走代理会被风控/地区拦截。
> Go 版切换 systemd 时**必须原样保留 `NO_PROXY`/`no_proxy`**，否则上游请求可能被代理劫持。
> 这是「切换 systemd」步骤的高危项。见 CR-22。

**unit 其它实测要点**：
- `User=appuser`、`Group=appuser`
- `WorkingDirectory=/opt/zcode2api`
- `ExecStart=/opt/zcode2api/.venv/bin/python cli.py serve`（**无 `--port`** → 端口全由 env 决定）
- `Restart=always`、`RestartSec=5`
- `StandardOutput`/`StandardError` = `append:/opt/logs/z2a_serve.log`
  （**非 sticky 目录**，注释说明 `fs.protected_regular=2` 会拦截 `/tmp`）
- **无 `EnvironmentFile=`** → `.env` 由**应用自己加载**（不是 systemd 注入）

> ⚠️ **`.env` 的加载方是应用本身**（unit 无 `EnvironmentFile`，但生产 9120 端口生效 → 应用读了 `.env`）。
> Go 版必须自己实现 `.env` 解析（键名、`KEY=VALUE`、`#` 注释、**行尾 `\r` 剥离**）。
> 见 `compat-risks.md` CR-23（行尾陷阱**实测**：会话初期该文件为 CRLF，取出的值带尾随 `\r` →
> 管理接口一律 **401 `鉴权凭证无效`**；剥离 `\r` 后 **200**。该文件随后被规范化为 LF（mtime 19:22），
> 但 **Go 解析器必须无条件剥离 `\r`** —— 否则任何 CRLF 部署都会静默失效）。

---

## 5. CLI 契约（实测 `cli.py --help`）

实测 `cd external/zcode2api && .venv/bin/python cli.py --help` 输出（逐字）：

```
ZCode Hub

用法:
  python cli.py serve [--port 3000]        启动网关 + 后台 UI
  python cli.py login zai [--no-browser]   通过 OAuth 登录 Z.AI 并自动加入账号池
  python cli.py add-account zai <name> <jwt|key>   添加轮询账号
  python cli.py accounts [zai|bigmodel]    查看账号列表
  python cli.py remove-account <provider> <id|name>
  python cli.py quota                      查看各账号实时额度
  python cli.py status                     查看配置概览
  python cli.py set-admin-key <key>        设置后台密码
  python cli.py export [file]              导出账号
  python cli.py import <file>              导入账号
```

**注意**：`serve` 的 `[--port 3000]` **显式默认 3000**，与 `ZCODE_PORT` 默认一致。
CLI 参数 `--port` 应**优先于** env（常规 CLI 约定，未实测覆盖顺序）。

> Go 版 CLI 子命令对齐属**下游任务**（不在本文冻结范围），但 `serve --port` 的默认 3000 应保持一致。

---

## 6. Go 读取实现清单（可直接落地）

| # | 规则 | 依据 |
|:--:|------|------|
| E-1 | `.env` 解析：`KEY=VALUE`、`#` 注释、**剥离 `\r`**、不解析引号转义 | CR-23 实测 |
| E-2 | `.env` 从 `WorkingDirectory` 读取（非二进制目录） | §1.3、CR-19 |
| E-3 | 环境变量**优先于** `.env` 文件（systemd 注入可覆盖文件） | 常规约定（**未实测**） |
| E-4 | `ZCODE_PORT` 默认 **3000**（int） | `.env.example` |
| E-5 | `ZCODE_HOST` 默认 **`0.0.0.0`** | `.env.example` |
| E-6 | `ZCODE_DATA_DIR` 默认 **`data`** | `.env.example` |
| E-7 | `ZCODE_ADMIN_KEY` 默认 **`change-me`**，但**DB `meta.admin_key` 优先** | §1.4 |
| E-8 | `ZCODE_NODE_PATH` 默认 **`node`**，缺失不崩溃 | §1.5 |
| E-9 | `ZCODE_QUOTA_REFRESH_INTERVAL` 默认 **60**，DB `meta.quota_refresh_interval`（`600`）优先 | §3.3、`sqlite-schema.md` §3.3 |
| E-10 | `ZCODE_GATEWAY_KEY` 默认**空**（= 不校验）；DB `meta.gateway_key` 优先 | §3.2 |
| E-11 | `ZCODE_COOLING_SECONDS` 默认 **300** | `.env.example` |
| E-12 | `CAPTCHA_POOL_MIN` 默认 **0**（关闭预解池）；生产实测为 `3`（开启） | §3.3 |
| E-13 | `ZCODE_CAPTCHA_RETRIES` 默认 **4**、`ZCODE_CAPTCHA_TIMEOUT` 默认 **40** | `.env.example`（**不是**架构文档的 3） |
| E-14 | 保留 `NO_PROXY`/`no_proxy` 语义（Go 标准库自动遵守） | §4、CR-22 |
| E-15 | 启动日志**不得**打印任何密钥值（含 `ZCODE_ADMIN_KEY`/`ZCODE_GATEWAY_KEY`/`Z2A_SMTP_PASS`） | 脱敏纪律 |

---

## 7. 未实测并显式标注的项

- `ZCODE_ADMIN_KEY` 默认值到底是 `change-me` 还是 `zcode`（生产已被覆盖，无法实测）——
  本文取 `.env.example` 的 `change-me`。
- **env 与 `.env` 文件的覆盖顺序**（E-3）：未实测（生产两者一致，无法区分）。
- `CAPTCHA_POOL_MIN` 非 0 时的**池行为细节**（解池频率、失败重试）：未实测。
- `ZCODE_NODE_PATH` 指向的 Node 是否**真的被调用过**：未直接观测（无子进程采样）。
- CLI `--port` 与 env `ZCODE_PORT` 的**覆盖顺序**（未实测）。
- `ZCODE_HOST` 设为 `127.0.0.1` 时的监听行为（unit 注释提到但未配置）。
- 其余注释键（`ZAI_UPSTREAM_URL` 等）**当前是否真的生效**：它们均被注释，故取 `.env.example` 默认值，
  但**生产运行时的真实上游 URL 未实测**（需抓包，属上游协议层，见 `05-upstream-protocols.md`）。
- `.env` 中 12 个已赋值键的**具体值**（按脱敏纪律未纳入本轮观测取）。
