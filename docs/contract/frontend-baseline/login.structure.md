# `login.html` 结构等价基准

> 来源：老仓 `frontend/admin/login.html`（**只读提取结构，不复制代码**）
> 用途：ADR-006 选项 C 重写时的**施工图纸**与**验证依据**

## 统计

| 项 | 数量 |
|---|---|
| id | **1** |
| class（去重） | **12** |
| data-* 属性 | **0** |
| 静态资源引用 | **4** |
| 标签种类 | 13 |

## 必须保持的 id

```
key
```

## 必须保持的 class（去重）

```
btn
btn-primary
input
login-body
login-brand
login-card
login-form
login-shell
login-subtitle
login-title
tick
w-full
```

## 静态资源引用（含版本查询串）

```
/static/css/app.css?v={{APP_VERSION}}
/static/js/auth.js?v={{APP_VERSION}}
/static/js/toast.js?v={{APP_VERSION}}
https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css
```

## 标签使用统计（DOM 复杂度参考）

| 标签 | 次数 |
|---|---|
| `div` | 6 |
| `path` | 3 |
| `script` | 3 |
| `meta` | 2 |
| `link` | 2 |
| `span` | 2 |
| `html` | 1 |
| `head` | 1 |
| `title` | 1 |
| `body` | 1 |
| `svg` | 1 |
| `input` | 1 |
| `button` | 1 |

## 可见文本（前 60 条，须保持）

```
zcode-hub · 登录
HUB
后台管理
请输入后台密码以继续
继续
```
