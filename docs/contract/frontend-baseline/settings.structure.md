# `settings.html` 结构等价基准

> 来源：老仓 `frontend/admin/settings.html`（**只读提取结构，不复制代码**）
> 用途：ADR-006 选项 C 重写时的**施工图纸**与**验证依据**

## 统计

| 项 | 数量 |
|---|---|
| id | **5** |
| class（去重） | **11** |
| data-* 属性 | **1** |
| 静态资源引用 | **5** |
| 标签种类 | 14 |

## 必须保持的 id

```
admin-header admin-key endpoint gateway-key quota-interval
```

## 必须保持的 class（去重）

```
admin-main
dialog-btn
dialog-btn-primary
hint
input
page-hd
page-sub
page-title
section-title
settings-card
settings-row
```

## 必须保持的 data-* 属性

```
data-active
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
| `div` | 17 |
| `code` | 4 |
| `script` | 4 |
| `label` | 3 |
| `input` | 3 |
| `br` | 3 |
| `meta` | 2 |
| `link` | 2 |
| `html` | 1 |
| `head` | 1 |
| `title` | 1 |
| `body` | 1 |
| `main` | 1 |
| `button` | 1 |

## 可见文本（前 60 条，须保持）

```
zcode-hub · 设置
设置
后台鉴权密钥与网关访问控制
鉴权
后台密码
用于登录此管理后台。修改后需用新密码重新登录。
网关 API Key（可选）
设置后，调用
/v1/messages
须携带
Authorization: Bearer <key>
或
x-api-key
。留空则不校验。
额度刷新间隔（秒）
保存
使用说明
· 账号额度、状态在「账号池」页实时刷新展示。
· 对话端点：
/v1/messages
（兼容 Anthropic Messages 协议）。
```
