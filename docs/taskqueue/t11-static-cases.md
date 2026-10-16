# t11 · 补静态资源对拍覆盖（待派 · 依赖 t10 完成）

> **为何排队**：t10 与 t11 同域（`scripts/conformance/`），框架正确拦截并行。
> t10（html-dom 验证器）是关键路径（t9 的验收判据），故优先。
> t10 完成后，captain 派发本任务。

## 背景

对拍框架现有 case 中 **`static/*` 完全无覆盖**（captain 实测确认）。
若新服务漏实现或内容不符，当前对拍**不会发现**。

依据：`docs/contract/static-assets-spec.md` §四。

## 新增 case（captain 已实测基线）

| case id | 方法 | 路径 | 判定 | 基线事实 |
|---|---|---|---|---|
| `ST-css-app` | GET | `/static/css/app.css` | L1+L3 | 23787 B，`text/css; charset=utf-8` |
| `ST-js-auth` | GET | `/static/js/auth.js` | L1+L3 | 2700 B，`text/javascript; charset=utf-8` |
| `ST-js-header` | GET | `/static/js/header.js` | L1+L3 | 1273 B，同左 |
| `ST-js-toast` | GET | `/static/js/toast.js` | L1+L3 | 1367 B，同左 |
| `ST-static-dir-redirect` | GET | `/static` | L1 | **307** → `/static/` |
| `ST-static-404` | GET | `/static/nonexist.js` | L1 | **404** 形态 A |

**注意**：静态资源带 `etag`、**无** `cache-control`（与页面相反——页面带 `no-store`）。

## 🔴 `ST-static-304`：本任务不做

Liche 提议用 steps + 新增 `capture`（步间变量提取）实现「先 GET 拿 ETag、再发条件请求」。

**captain 决定暂不做**，理由：
1. 需**改动已验证的裁判工具引擎**（1372 行），风险与收益不匹配
2. `capture` 是**通用引擎能力**，应单独设计 + 独立回归验证，不该塞进"补 case"任务
3. 当前优先级：t10（html-dom 验证器）就绪 > 静态资源 304 覆盖

**若 Liche 认为 304 覆盖不可缺**：在交付说明里给**不改引擎的替代方案**，或记录为已知缺口。

## 交付
- `cases.json` 增加 6 个 case
- 自测输出（baseline==candidate 全绿）
- 报告新增后 case 总数
