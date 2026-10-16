# 前端结构等价基准

> 本目录是 **ADR-006（选项 C）** 的施工图纸与验证依据。
>
> 用途：重写页面 HTML 时，**必须保持**此处列出的 id / class / data-* / 静态资源引用 / 可见文本。
> 允许差异：空白、缩进、换行、注释、属性顺序、模板实现方式。
>
> **取证方式**：captain 从老仓 `frontend/admin/*.html` **只读提取结构**（不复制代码）。
> 提取脚本逻辑见 git 提交说明；任何人可用同样的正则在老仓重新提取核对。

## 文件

| 文件 | 说明 |
|---|---|
| `login.html.structure.md` | 登录页结构基准 |
| `accounts.html.structure.md` | 账号页（最大，760 行 / 43 id / 175 class） |
| `monitoring.html.structure.md` | 监控页 |
| `settings.html.structure.md` | 设置页 |

## 附带发现（captain 实测）

老仓模板**只有 `{{APP_VERSION}}` 一个变量**（共 15 处），**无 `{% if %}`/`{% for %}` 控制结构**。
值来自 `frontend/version`（`2.5.1`）。
→ 新服务**无需模板引擎**，字符串替换即可。
