# `monitoring.html` 结构等价基准

> 来源：老仓 `frontend/admin/monitoring.html`（**只读提取结构，不复制代码**）
> 用途：ADR-006 选项 C 重写时的**施工图纸**与**验证依据**

## 统计

| 项 | 数量 |
|---|---|
| id | **27** |
| class（去重） | **51** |
| data-* 属性 | **2** |
| 静态资源引用 | **5** |
| 标签种类 | 20 |

## 必须保持的 id

```
admin-header btn-clear filter-bar last-updated list live-count live-dot mon-tabs range-chips s-calls s-calls-spark s-calls-sub s-fail s-fail-sub s-in s-in-sub s-out s-out-sub s-rate s-rate-sub s-tok s-tok-sub s-ttfb s-ttfb-sub search stat-grid status-sel
```

## 必须保持的 class（去重）

```
${e.mode==='jwt'?'jwt':'other'}
${e.ok===true?'ok':e.ok===false?'bad':''}
acct-list
acct-table
active
active':''}
admin-main
badge
badge-active
badge-cooling
badge-invalid
empty-state
err-line
filter-bar
filter-chip${state.range===v?'
filter-chips
input
live-dot
mon-grid-agg
mon-grid-live
mon-head
mon-model
mon-name
mon-num
mon-preview
mon-row
mon-search
mon-select
mon-stream
mon-tab
mon-tab-count
mon-tabs
mon-time
page-action-btn
page-actions
page-hd
page-sub
page-title
section-head
section-meta
spark
stat-cell
stat-grid
stat-icon
stat-label
stat-num
stat-sub
stat-top
tickbar
tickbar-empty
tier-b
```

## 必须保持的 data-* 属性

```
data-active data-tab
```

## 静态资源引用（含版本查询串）

```
/static/css/app.css?v={{APP_VERSION}}
/static/js/auth.js?v={{APP_VERSION}}
/static/js/header.js?v={{APP_VERSION}}
/static/js/toast.js?v={{APP_VERSION}}
https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css
```

## 标签使用统计（DOM 复杂度参考）

| 标签 | 次数 |
|---|---|
| `div` | 57 |
| `span` | 51 |
| `path` | 22 |
| `svg` | 10 |
| `button` | 6 |
| `option` | 4 |
| `script` | 4 |
| `circle` | 3 |
| `meta` | 2 |
| `link` | 2 |
| `html` | 1 |
| `head` | 1 |
| `title` | 1 |
| `body` | 1 |
| `main` | 1 |
| `input` | 1 |
| `select` | 1 |
| `polyline` | 1 |
| `mints` | 1 |
| `i` | 1 |

## 可见文本（前 60 条，须保持）

```
zcode-hub · 请求监控
请求监控
网关请求实时观测 · 内存环形日志（保留最近 500 条，重启清零）
实时监控中
刷新
清空
全部状态
成功
失败
进行中
调用
成功率
失败
Token 总量
输入
输出
平均首字
账号
模型
实时
=0&&idx
成功
`;
  if(e.ok===null)return `
进行中
';
  return `
最近状态
成功率
调用
输入
输出
平均首字
最近请求
a+b,0)/g.ttfbs.length):'—';
    return `
`:`
所选时间范围内没有请求
时间
账号
模型
状态
首字
总耗时
输入
输出
内容 / 错误
`
`:`
所选时间范围内没有请求
`
```
