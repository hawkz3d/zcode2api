# `accounts.html` 结构等价基准

> 来源：老仓 `frontend/admin/accounts.html`（**只读提取结构，不复制代码**）
> 用途：ADR-006 选项 C 重写时的**施工图纸**与**验证依据**

## 统计

| 项 | 数量 |
|---|---|
| id | **43** |
| class（去重） | **98** |
| data-* 属性 | **3** |
| 静态资源引用 | **5** |
| 标签种类 | 24 |

## 必须保持的 id

```
${a.id} acct-list add-pane-login add-pane-paste add-provider add-tokens admin-header btn-claim-all btn-refresh-all claim-account claim-cap-btn claim-cap-holder claim-captcha-box claim-help claim-plan claim-plan-id claim-status claim-submit confirm-body confirm-title edit-name edit-token filter-bar import-file last-updated live-dot login-active login-idle login-label login-start-btn login-status login-url modal-add modal-claim modal-confirm modal-edit s-active s-exhausted s-quota s-total tab-login tab-paste tbl-count
```

## 必须保持的 class（去重）

```
${cls}
${fail>0?'u-bad':''}
${ok?'ok':'bad'}
${rate===null?'':(rate>=90?'u-ok':'u-warn')}
${strikes>0?'u-bad':''}
${t.classList.contains('ok')?'is-ok':'is-bad'}
acct-actions
acct-head
acct-list
acct-name
acct-name-line
acct-row${isDisabled?'
acct-scroll
acct-table
actions
active
active':''}
admin-main
badge
badge-${a.status}
cell
cell-sub
cell-sub${days<=7?'
cool-line
cred
cred-sub
dialog-actions
dialog-body
dialog-btn
dialog-btn-danger
dialog-btn-primary
dialog-field
dialog-help
dialog-label
empty-state
err-line
filter-bar
filter-chip
filter-chip${curFilter===k?'
filter-chip-count
input
is-bonus
is-disabled':''}
is-loading':''}
is-pending
live-dot
modal
modal-overlay
modal-title
notch
page-action-btn
page-action-btn-primary
page-actions
page-hd
page-sub
page-title
quota
quota-empty
quota-row
quota-row-exp
quota-row-fill
quota-row-name
quota-row-track
quota-row-val
quota-rows
recent
recent-ts
row-icon-btn
row-icon-btn${claiming.has(a.id)?'
row-icon-btn${loading?'
row-icon-danger
section-count-badge
section-head
section-meta
section-title
stat-cell
stat-grid
stat-icon
stat-label
stat-num
stat-top
status
switch
switch-slider
th-actions
tickbar
tickbar-empty
tier-b
tok
tt-body
tt-head
u-label
u-val
usage
usage-line
usage-mini
w-full
warn-line':''}
```

## 必须保持的 data-* 属性

```
data-active data-id data-tip
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
| `div` | 99 |
| `span` | 59 |
| `path` | 35 |
| `button` | 26 |
| `svg` | 14 |
| `option` | 9 |
| `input` | 7 |
| `script` | 4 |
| `line` | 3 |
| `select` | 3 |
| `meta` | 2 |
| `link` | 2 |
| `rect` | 2 |
| `circle` | 2 |
| `html` | 1 |
| `head` | 1 |
| `title` | 1 |
| `body` | 1 |
| `main` | 1 |
| `polyline` | 1 |
| `textarea` | 1 |
| `i` | 1 |
| `label` | 1 |
| `code` | 1 |

## 可见文本（前 60 条，须保持）

```
zcode-hub · 账号池
账号池
多账号轮询 · 额度用完自动切换下一个账号 · 实时用量监控
实时监控中
导入
导出
刷新额度
一键领取
新增
账户概览
账户总数
正常
额度用完
总额度（剩余）
账户明细
新增账号
粘贴凭证
授权登录
每行一个 Coding Plan JWT 或 API Key，已存在的会自动跳过
提供商
Z.AI (Coding Plan)
BigModel
取消
添加
生成授权链接
在任意浏览器打开下面的链接完成授权（本页保持打开即自动入池）：
复制
打开
等待授权中…
关闭
编辑账号
名称
Token
取消
保存
手动领取活动套餐
账号
活动套餐
自动选择（可领套餐中优先级最高）
关闭
领取
确认
取消
确认
0&&eff
`
暂无账号，点击右上角「新增」添加
凭证 / 账号
可用状态
最近请求
历史用量
额度信息
操作
0)sub=`
`:'';
  return `
调用
成功率
失败
风控
0&&eff
```
