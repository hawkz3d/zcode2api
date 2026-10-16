# zhipucode2api

**ZCode 账号运营 + 网关一体机** —— Go 单二进制实现。

把池内 ZCode Coding Plan / Start Plan 账号额度，统一转换为标准
**Anthropic Messages API**（`/v1/messages`）与 **OpenAI Chat Completions API**
（`/v1/chat/completions`），暴露给任意 Agent（Claude Code、Cline、DSH 等）。

自带完整的账号运营控制台：多账号池轮询、额度用完自动换号、实时用量监控、
免登录限时套餐自动领取、OAuth 免密登录入池、免浏览器的阿里云无痕验证求解 —— 开箱即用，自托管部署。

---

## 项目定位

| 项 | 值 |
|---|---|
| 语言 | Go 1.26.4 |
| 直接依赖 | 1 个（`modernc.org/sqlite`，纯 Go 无 cgo） |
| 监听 | `127.0.0.1:9120`（由 `ZCODE_PORT` / `ZCODE_HOST` 配置） |
| 数据 | SQLite（`accounts` + `meta` 两表） |
| 交付形态 | 静态编译单二进制 + systemd unit |
| 规模 | 约 10 200 行实现 + 5 500 行测试，11 个包 |

### 相对上游的功能增量

本实现不只是一个转写。在协议等价的基础上，以下功能是本项目自行设计并实现的
（均可由 `git log` 与 `docs/contract/review-evidence/local-patches-semantics.md` 追溯）：

| 功能 | 作用 |
|---|---|
| **验证码惰性化** | 对话网关首请求不带验证码 token，仅在收到挑战时才取并用原账号重试。首包延迟 20–42s → 0.8–0.9s |
| **思考链透传** | 上游 `thinking_delta` 映射为 OpenAI 兼容的 `reasoning_content`，思考过程不再被静默丢弃 |
| **双通道分流** | `bigmodel/` 模型前缀或 `x-provider` 头 → 订阅账本；默认 → plan 账本。前缀在转发前剥离 |
| **双密钥体系** | 后台管理密钥与网关调用密钥分离，各自独立轮换 |
| **动态版本门槛** | 领取活动包时使用现行客户端版本，避免版本过低看不到活动 |
| **业务错误回执** | 上游以 HTTP 200 承载业务错误码时（如 1005 `exceed quota limit`）翻译为规范的 502 错误体 |
| **连接加固** | 直连出口 + `NO_PROXY` 白名单，防止系统级代理污染网关链路 |
| **额度 entitlements 解析** | 解析 `plans[].entitlements[]` 结构，额度口径与官方一致 |
| **多账号池与故障转移** | 按账本轮询、续试、exhausted/cooling 分级处理 |

以及本重写新增的基础设施：自动化契约对拍框架（125 个 case 分层判定 + 反洗白哨兵）、
只读数据库快照、切换/回滚脚本（九项 preflight）、危险端点裸调静态守卫。

### 设计目标

1. **契约等价** —— 27 个契约端点（3 网关 A 组 + 22 管理 API B 组 + 2 元数据 D 组）逐字段兼容，
   由 `scripts/conformance/` 的自动化对拍验证（55 个 case，L1 状态码 / L2 结构 / L3 值三级判定）
2. **数据兼容** —— 直接读写既有 SQLite 库，零迁移；序列化保真（未知字段与原始字节往返不丢）
3. **依赖面最小** —— 直接依赖仅一个纯 Go SQLite 驱动，无 cgo、无运行时依赖

---

## 快速开始

```bash
# 构建（产出静态单二进制）
go build -o bin/zhipucode2api ./cmd/zhipucode2api

# 运行（本地调试用非 9120 端口，避免抢占生产）
ZCODE_PORT=19120 ZCODE_HOST=127.0.0.1 ./bin/zhipucode2api serve
```

### 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `ZCODE_PORT` | `3000` | 监听端口（**生产用 9120**） |
| `ZCODE_HOST` | `0.0.0.0` | 监听地址 |
| `ZCODE_DATA_DIR` | `data` | 数据目录。⚠️ **建议用绝对路径**（相对路径相对 WorkingDirectory 解析，见 `docs/contract/adr-005-data-dir-and-env.md`） |
| `ZCODE_ADMIN_KEY` | `<弱默认>` | 后台密钥。⚠️ **运行时以 DB `meta.admin_key` 为准**（env 仅启动输入） |
| `ZCODE_NODE_PATH` | `node` | 验证码求解所用的 Node 可执行文件路径 |
| `ZCODE_SOLVER_PATH` | 自动推导 | 验证码求解脚本路径（默认在部署布局下推导，可用此变量覆盖） |
| `CAPTCHA_POOL_MIN` | `3` | 预解池下限。**Go 版无池** ⇒ 该值退化为配置兼容（惰性化不依赖它，见下文） |

---

## 核心能力

- **标准网关**：`POST /v1/messages`（Anthropic 协议）+ `POST /v1/chat/completions`（OpenAI 协议）
  + `GET /v1/models`，流式与非流式透传，思考链（`reasoning_content`）完整映射。
- **多账号轮询与故障转移**：请求按通道分账本轮询；额度用完 / 限流 / 鉴权失效自动换下一个可用账号；
  凭证不可用时扫候继续试，全败才回 503（形态不变）。
- **双通道分流**：`bigmodel/` 模型前缀或 `x-provider: bigmodel` 头 → 订阅账本（任意 provider 的 API Key）；
  默认 → plan 账本（仅 JWT 凭证）。前缀在转发前剥离。
- **实时用量监控**：内存环形缓冲记录每次转发（15 字段：账号/模式/耗时/token/错误原文），
  经 `/admin/api/monitoring` 暴露；重启即清零（定位为运维实时观测而非审计）。
- **套餐自动领取**：`POST /admin/api/claim` 对 JWT 账号领取限时套餐；遇验证码被拒（3007）自动换码重试一次。
- **OAuth 免密登录**：浏览器授权 → 自动保存 JWT 入池 → 自动兑换 API Key 作为同账号回退通道。
- **验证码惰性化**：对话网关**首请求不带** `x-aliyun-captcha-verify-param`，仅当上游返回挑战信号时才
  求解并用**原账号**重试（上界 3 次）。这是与领取链路**相反**的语义 —— 后者每次必取。
- **后台管理 UI**：登录、账号池增删改启禁、一键导入导出、网关/后台密钥与监控参数设置。

---

## 目录结构

```
cmd/zhipucode2api/     主程序入口（serve 等子命令）
internal/
  config/              环境变量配置加载
  models/              账号领域模型（含无损 JSON 视图）
  store/               SQLite 存储层（WAL、保真序列化、只读快照）
  gateway/             网关端点 + 账号选择 + 验证码惰性化
  upstream/            上游 HTTP 客户端
  adminapi/            管理 API（22 端点）
  captcha/             验证码求解客户端（外部 black-box 进程）
  pages/               HTML 页面与静态资源
docs/contract/         冻结的契约文档（实现与验收的唯一依据）
scripts/conformance/   对拍框架（harness + cases + 归一化规则 + 豁免清单）
scripts/cutover.sh     切换/回滚脚本（含 preflight 九项检查）
web/static/            静态资源
```

---

## 契约文档（实现前必读）

`docs/contract/` 是本项目的**规格唯一来源**，实现与验收均以其为准：

| 文件 | 内容 |
|---|---|
| `endpoints.md` | 27 端点逐条契约（方法/鉴权/请求/响应/错误码 + 实测证据） |
| `sqlite-schema.md` | 数据库逐字 DDL + Go 兼容点 |
| `env-contract.md` | 环境变量契约与优先级 |
| `account-model.md` | 账号模型、状态机、`enabled` 双份存储 |
| `compat-risks.md` | 风险清单（每条含现象/证据/判据） |
| `IMPLEMENTER-CHEATSHEET.md` | **写代码前先看这张**（致命陷阱 + 易漏细节） |
| `adr-00*.md` | 架构决策记录（事实来源优先级 / 字段规范化 / 写入语义 / 密钥政策 / 数据目录） |
| `DELIVERY-REPORT.md` | 交付报告：达成项、已知未闭合、方法论沉淀 |

---

## 验证

### 单元测试

```bash
go test ./...
```

### 契约对拍（双实例逐字段比对）

```bash
python3 scripts/conformance/harness.py \
  --baseline http://127.0.0.1:9120 \
  --candidate http://127.0.0.1:19120 \
  --env-file <path-to-.env>
```

判定分层：**L1** 状态码 / **L2** 结构（JSON 类型树）/ **L3** 归一化后逐值 /
**ORDER** 顺序契约（不经归一化，防折叠洗绿）/ **ASSERT** 单侧断言 / **ST-DOM** 页面结构等价。

已知差异须在 `scripts/conformance/allowlist.json` 显式登记（默认空 = 不容忍任何差异），
每条豁免必须写明差异形态、理由与批准人，且**豁免命中仍会以审计痕迹出现在报告里**。

### 框架自检

```bash
# 引擎自测（55 项断言，含哨兵：证明判据本身可证伪）
python3 scripts/conformance/harness.py --engine-selftest

# 三向路由核对（契约 / 参考服务 / 本服务）
python3 scripts/conformance/route_matrix.py --old http://127.0.0.1:9120 --new http://127.0.0.1:19120
```

---

## 容器化

`Dockerfile` 提供多阶段构建，产出**完全静态**的二进制（`modernc.org/sqlite` 是纯 Go 驱动，
故 `CGO_ENABLED=0` 可编译出无动态依赖的二进制，实测 `ldd` 输出 `not a dynamic executable`）。

### 镜像分层

```
builder   golang:1.26-alpine    CGO_ENABLED=0 静态编译
solver    node:22-alpine        node + happy-dom（依赖清单在 docker/solver-package.json）
runtime   alpine:3.20           二进制 + node 运行时 + 求解器
```

页面、静态资源、身份块全部经 `go:embed` 编入二进制，运行阶段**无需任何额外文件**。

### 两个构建目标

| target | 内容 | 适用 |
|---|---|---|
| `runtime` | 网关 + 验证码求解器 | 需要挑战重试的完整部署 |
| `runtime-minimal` | 仅网关 | 不需要求解器；收到挑战时直通上游应答 |

### 验证码求解器是外部依赖

求解器源码**不在本仓库内**，通过额外的构建上下文注入（BuildKit `--build-context`）。
未提供该上下文时构建会**明确失败**（fail-loud）—— 宁可构建失败，也不产出
"看起来正常但求解器缺失"的镜像。要构建无求解器的镜像，用 `--target runtime-minimal`。

### 构建与运行

```bash
# 完整版（需先准备 captcha_node 目录，含 solver.js）
docker build \
  --build-context captcha=../zcode2api/captcha_node \
  -t zhipucode2api:latest .

# 精简版（无求解器）
docker build --target runtime-minimal -t zhipucode2api:minimal .

# 运行
docker run -d --name zcode-gateway \
  -p 127.0.0.1:9120:9120 \
  -v /path/to/state:/data \
  -e ZCODE_ADMIN_KEY=<后台密钥> \
  zhipucode2api:latest
```

**挂载约定**：`/data` 是数据目录（SQLite 库），容器以非 root 用户（uid 10001）运行，
故宿主目录需对该 uid 可写。`ZCODE_DATA_DIR` 默认已指向 `/data`。

**健康检查**：内置 `HEALTHCHECK` 请求 `/v1/models`。若部署时配置了 `gateway_key`，
该端点会返回 401 —— 仍证明进程在监听，属预期。

### 当前实测状态

| 项 | 结果 |
|---|---|
| `runtime-minimal` 构建与运行 | ✅ 镜像 **19.5 MB**；容器启动、`/v1/models` → 200、6 账号加载 |
| `runtime`（含求解器）构建与运行 | ✅ 镜像 **161 MB**；容器启动、服务正常、**容器内真实求解成功**（产出 VERIFY_PARAM） |
| 静态编译 | ✅ `ldd` → `not a dynamic executable` |
| 挂载权限 | ⚠️ 数据目录须对容器内 uid **10001** 可写（SQLite 需写 WAL）；否则启动报 `unable to open database file (14)` |
| 健康检查 | ✅ 内置 `HEALTHCHECK` 生效（`Up (health: starting)`） |

> 构建过程中的一个实测坑：node 是 C++ 实现，从 `node:22-alpine` 单独复制二进制到
> 裸 `alpine` 会缺 `libstdc++.so.6` 与 `libgcc_s.so.1`。**镜像构建会成功，但求解器
> 运行时才报错**（静默失效）。故 `runtime` 阶段显式 `apk add libstdc++ libgcc`。

## 部署（systemd）

systemd 单元位于 `scripts/zhipucode2api.service`。

⚠️ **两个关键约束**（详见 `docs/contract/adr-005-data-dir-and-env.md`）：

1. **`EnvironmentFile` 优先于 `Environment=`**（systemd 行为，与行序无关）——
   故 unit 使用**双 EnvironmentFile**：先加载共享 `.env`（密钥），再加载 `zhipucode2api.env`
   （以绝对路径覆盖数据目录）
2. **不要给 `ZCODE_DATA_DIR` 加 `os.MkdirAll`** —— 目录不存在时 SQLite 会**响亮失败**（可见），
   而目录存在但为空时会**静默创建空库**（0 账号、无告警）。宁可启动失败，也不要静默跑空库。

### 安全提示

- 凭据文件（`.env`、`accounts.db`）应设为 `600`，其目录 `700`。
  ⚠️ 验证"降权后是否可访问"时须确认测试主体**真的降权了** —— 对有 sudo 权限的用户用
  `sudo -u` 不构成降权测试（测的是 root 恒可读写）。
- `/admin/api/export` 是**凭据导出端点**，响应体按契约回显全部账号明文 secret。
  自动化脚本调用它时必须丢弃响应体（`-o /dev/null`）或只取字段名；
  仓库内有守卫脚本 `scripts/conformance/guard-dangerous-endpoints.sh` 做静态拦截。

---

## 免责声明

本项目仅供**学习、研究、个人实验与内部验证**使用，不提供任何形式的商业授权、适用性保证或结果保证。

使用者需自行承担全部风险。作者不对因使用、修改、分发、部署或依赖本项目而产生的任何直接或间接损失
——包括但不限于账号封禁、额度或数据丢失、服务中断、法律风险或第三方索赔——承担任何责任。

使用前请自行确认：你的使用方式符合所访问服务的条款、协议与所在地法律；
你部署环境中涉及的账号、凭据与数据，其来源及使用权限合法。

请勿将本项目用于违反服务条款、协议、法律或平台规则的场景。

---

## 许可

**GNU Affero General Public License v3.0** —— 见 [LICENSE](LICENSE) 与 [NOTICE](NOTICE)。

AGPL-3.0 的关键义务：若你修改本程序并通过网络向他人提供服务，
必须向这些使用者提供对应的完整源代码。自托管自用不受此约束。
