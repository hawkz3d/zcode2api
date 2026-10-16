# zhipucode2api 契约冻结目录

## 这是什么

本目录是 `zhipucode2api` 的**唯一规格来源**。所有实现与验收都以本目录文档为准。

规格先于代码冻结：先写清"这个服务对外承诺什么"，再写实现。契约里每条端点、
每个字段、每种错误形态都附**实测证据**（HTTP 探测、SQLite 只读查询、日志分析），
不是从设计文档抄来的推测。

## 规格是怎么建立的

| 来源 | 说明 |
|---|---|
| 对运行中服务的 HTTP 实测 | 最权威。状态码、字节数、字段序、错误文体以实测为准 |
| SQLite 库只读探测 | DDL、索引、数据形态；`VACUUM INTO` 快照后离线读 |
| 公开协议文档 | 上游端点、鉴权头、错误码语义 |
| 集成测试 | 描述外部行为的纯 HTTP 黑盒测试 |

冲突时以**实测 > 数据库 > 黑盒测试 > 文档**为序（见 `adr-001-*.md`）。

## 文档索引

| 文件 | 内容 |
|---|---|
| `endpoints.md` | 27 端点逐条契约 |
| `sqlite-schema.md` | 数据库逐字 DDL + 兼容点 |
| `env-contract.md` | 环境变量契约与优先级 |
| `account-model.md` | 账号模型与状态机 |
| `compat-risks.md` | 风险清单（现象/证据/判据） |
| `IMPLEMENTER-CHEATSHEET.md` | 实现前必读的陷阱清单 |
| `verification-methodology.md` | 验证方法论（怎么证明一个判据真的有效） |
| `adr-*.md` | 架构决策记录 |
| `review-evidence/` | 各项核验的原始证据 |
| `DELIVERY-REPORT.md` | 交付报告 |

## 给实现者的话

写代码前先读 `IMPLEMENTER-CHEATSHEET.md` —— 那里汇总了最容易踩的坑。
契约里标 `[实测]` 的是亲手验过的，标 `[docs]` 的是文档断言但未实测，
标 `unverified` 的是无证据。**别把后两类当事实用。**
