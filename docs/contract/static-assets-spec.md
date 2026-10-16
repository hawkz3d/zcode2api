# 静态资源与页面契约（captain 补充，2026-10-15）

> **为何单独成文**：`endpoints.md` C 组只描述了页面**路由层**（路径/状态码/类型），
> 未覆盖**内容与缓存语义**。而对拍要求页面**字节等同**（见下），故必须补全。
> 本文所有事实均为 captain 对 `http://127.0.0.1:9120` 的**实测**。

---

## 🔴 一、最重要的发现：页面必须**字节等同**，不是"重写"

对拍 case 的判定等级（`scripts/conformance/latest/report.json`）：

| case | expect | note 原文 |
|---|---|---|
| `UI-login-page` | L1+L3 | 「登录页 HTML **字节对拍**」 |
| `UI-monitoring-page` | L1+L3 | 「**空白折叠后字节对拍**：静态资源版本号、标题都是契约面」 |

**含义**：新服务输出的 HTML 必须与老服务**逐字节相同**（或空白折叠后相同）。

**这彻底改变了前端工作的性质**：
- ❌ **不是**"用 Go 重写前端模板"
- ✅ **而是**"把老服务的 HTML 原样搬运，逐字节保持"

**可行性已验证**（captain 实测）：
```
/admin/login    1897 字节  两次请求 sha256 一致 → 字节稳定，可对拍
/admin/accounts 39987 字节 两次请求 sha256 一致 → 字节稳定，可对拍
```
页面**无动态内容**（无时间戳/随机数；版本串 `v=2.5.1` 是固定常量）。

> ⚠️ **但字节等同与"去除历史来源"存在张力**：老仓 `frontend/` 是他人写的 104KB 前端。
> 照抄 HTML 意味着**把别人的前端代码带进新仓**。
> **这是需要用户裁决的问题** —— 见本文 §五。

---

## 二、页面响应头契约（实测，契约文档原缺）

| 路径 | 字节数 | `content-type` | `cache-control` |
|---|---:|---|---|
| `/admin/login` | **1897** | `text/html; charset=utf-8` | **`no-store`** |
| `/admin/accounts` | **39987** | `text/html; charset=utf-8` | **`no-store`** |
| `/admin/monitoring` | **14214** | `text/html; charset=utf-8` | **`no-store`** |
| `/admin/settings` | **3994** | `text/html; charset=utf-8` | **`no-store`** |

> ⚠️ **契约原只记载 `/admin/login` 带 `no-store`**；实测**全部页面**都带。
> 新服务必须对**所有**页面设置 `cache-control: no-store`。

**无 `etag`**（页面不做条件请求）。

---

## 三、静态资源契约（实测，契约文档原缺缓存语义）

| 路径 | 字节数 | `content-type` | `etag` | `cache-control` |
|---|---:|---|---|---|
| `/static/css/app.css` | **23788** | `text/css; charset=utf-8` | ✅ 有 | **无** |
| `/static/js/auth.js` | **2701** | `text/javascript; charset=utf-8` | ✅ 有 | **无** |
| `/static/js/header.js` | **1274** | `text/javascript; charset=utf-8` | ✅ 有 | **无** |
| `/static/js/toast.js` | **1368** | `text/javascript; charset=utf-8` | ✅ 有 | **无** |

### 条件请求（304）实测支持

```bash
ET=$(curl -sD - -o /dev/null http://127.0.0.1:9120/static/css/app.css | grep -i '^etag:' | tr -d '\r' | cut -d' ' -f2)
curl -s -o /dev/null -w '%{http_code}' -H "If-None-Match: $ET" http://127.0.0.1:9120/static/css/app.css
# → 304
```

**含义**：新服务必须实现 **ETag + If-None-Match → 304**，否则对拍（若加该 case）会挂。
ETag 值形如 `"ad63d861538d9f3306d0fc6e6e7c4cf9"`（32 位 hex，带引号）。

> ✅ **已证伪「ETag = 内容 md5」**（2026-10-15，t9 发现、captain 复核）：
> ```
> 老 etag:  ad63d861538d9f3306d0fc6e6e7c4cf9
> 内容md5:  04f0f1b7560bfab93a5507e5cff38a60     ← 不同
> ```
> Starlette 的 ETag 由 **stat 组合**（mtime+size 之类）生成，不是内容哈希。
> **对拍含义**：harness 不比 etag **值**（行为自洽即可：同一资源两次请求 etag 稳定、条件请求能 304）。
> 新服务可用任意自洽算法生成 etag。

### 路由细节（契约已载，此处复述以便集中查阅）

| 请求 | 状态 | 说明 |
|---|---|---|
| `/static` | **307** | → `/static/`（StaticFiles mount 规范化） |
| `/static/nonexist.js` | **404** | 形态 A |
| `/static/`（目录） | 待实测 | — |

---

## 四、🔴 对拍覆盖缺口（需补 case）

当前 29 个 case 中，**没有任何 `static/*` case**（captain 实测确认）：
```
cases.json 里 static/css 相关 case: 无
```

**缺口**：`app.css`、`auth.js`、`header.js`、`toast.js` 四个静态资源**未被对拍覆盖**。
若新服务漏实现或内容不符，**当前对拍不会发现**。

**建议补的 case**（转 Liche）：
| case id | 方法 | 路径 | 期望等级 | 说明 |
|---|---|---|---|---|
| `ST-css-app` | GET | `/static/css/app.css` | L1+L3 | 字节 + content-type |
| `ST-js-auth` | GET | `/static/js/auth.js` | L1+L3 | 字节 + content-type |
| `ST-static-304` | GET | `/static/css/app.css` + If-None-Match | L1 | 304 条件请求 |
| `ST-static-dir-redirect` | GET | `/static` | L1 | 307 → `/static/` |
| `ST-static-404` | GET | `/static/nonexist.js` | L1 | 404 形态 A |

---

## 五、🔴 需要用户裁决的问题

**矛盾**：两个目标冲突。

| 目标 | 要求 |
|---|---|
| **契约等价**（对拍字节等同） | 页面 HTML 必须与老服务逐字节相同 |
| **去除历史来源**（用户核心诉求） | 新仓不应含他人代码 |

老仓 `frontend/`（104KB，`accounts.html` 760 行等）是**他人（dengyie）写的**。
若为通过字节对拍而照抄，等于**把他人前端产物带进新仓**。

**三个选项**：

| 选项 | 做法 | 后果 |
|---|---|---|
| **A** 照抄 HTML（保字节对拍） | 把老仓 `frontend/` 内容原样放进新仓 | 来源未清干净（但功能等价） |
| **B** 重写 HTML（放弃字节对拍） | 自己写等价页面 | 需在对拍豁免清单登记「页面 HTML 不字节等同」 |
| **C** 折中 | 重写 HTML 但保持**结构等价**（DOM 结构/类名/id 一致，空白与注释可不同） | 部分对拍豁免；来源干净 |

**captain 建议**：**C**。理由——
- `frontend/` 是**模板/静态资源**，不是"算法"；重写它不涉及逆向知识
- 保持 DOM 结构等价即可保证**功能等价**（JS 选择器、事件绑定不变）
- 对拍登记一条豁免（页面 HTML 非字节等同），代价可接受
- 兼顾用户「清空来源」的核心诉求

**但这是用户的目标冲突，须用户拍板。**

---

## 六、实测命令（可复现）

```bash
# 页面字节数与响应头
for p in /admin/login /admin/accounts /admin/monitoring /admin/settings; do
  curl -s -D - -o /dev/null "http://127.0.0.1:9120$p" | grep -iE "^(content-type|cache-control|etag)"
done

# 静态资源
for p in /static/css/app.css /static/js/auth.js /static/js/header.js /static/js/toast.js; do
  curl -s -D - -o /dev/null "http://127.0.0.1:9120$p" | grep -iE "^(content-type|cache-control|etag)"
done

# 304 条件请求
ET=$(curl -sD - -o /dev/null http://127.0.0.1:9120/static/css/app.css | grep -i '^etag:' | tr -d '\r' | cut -d' ' -f2)
curl -s -o /dev/null -w '%{http_code}\n' -H "If-None-Match: $ET" http://127.0.0.1:9120/static/css/app.css

# 字节稳定性
curl -s http://127.0.0.1:9120/admin/login | sha256sum
curl -s http://127.0.0.1:9120/admin/login | sha256sum   # 两次应一致
```

---

# 附：SSE 与错误路径的交互（captain 补充实测，2026-10-15）

> 本项属 `endpoints.md` A-02/A-03 的补充。captain 独立实测发现一条**易错规则**。

## 🔴 规则：`stream:true` **不影响错误路径的响应类型**

实现者极易写出这样的逻辑：先设 `Content-Type: text/event-stream`，再校验请求 ——
那样**错误响应也会是 SSE**，与老服务不符。

**实测（三场景，均 `stream:true`）**：

| # | 请求 | `content-type` | body |
|:--:|---|---|---|
| 1 | `/v1/messages` + 未知模型 | **`application/json`**（非 SSE） | `{"code":3006,"msg":"model not allowed","logid":"…"}` |
| 2 | `/v1/messages` + 非法 JSON | **`application/json`** | `{"error":{"message":"请求体不是合法 JSON","type":"invalid_request"}}` |
| 3 | `/v1/chat/completions` + 未知模型 | **`application/json`** | `{"code":3006,"msg":"model not allowed","logid":"…"}` |

**结论**：只有**成功路径**才返回 `text/event-stream`。
**任何**提前失败（本地校验 / 上游业务错误）都返回 **JSON**。

## 实现要点

```go
// 正确顺序：先完成校验与上游建连，成功后才切 SSE
func handleMessages(w, r) {
    if err := validate(r); err != nil {
        writeJSON(w, err)          // ← JSON，不是 SSE
        return
    }
    resp, err := upstream.Do(r)
    if err != nil || isBusinessError(resp) {
        writeJSON(w, ...)          // ← 上游错误也是 JSON
        return
    }
    if body.Stream {
        w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
        w.Header().Set("Cache-Control", "no-cache")
        // … flush 循环
    }
}
```

## 成功路径的 SSE 契约（已在 `endpoints.md` 记载，此处摘录）

响应头：`200` + `content-type: text/event-stream; charset=utf-8` + `cache-control: no-cache` + `transfer-encoding: chunked`

帧格式（首帧即真实上游 chunk，非占位）：
```
data: {"id":"chatcmpl-<redacted>","object":"chat.completion.chunk","created":<int>,"model":"GLM-5.3-Flash","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}
data: {"error":{"message":"上游业务错误 code=1005: exceed quota limit","type":"upstream_error","code":1005}}
```
**流内错误也是 SSE 帧**（不是 HTTP 状态码）。

## 实测命令

```bash
# 期望 content-type: application/json（非 text/event-stream）
curl -s -D - -o /dev/null -X POST http://127.0.0.1:9120/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model":"no-such-model-xyz","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}' \
  | grep -i content-type
```
