# t13 adminapi 接线方案（t17，captain 实测产出）

> **背景**：t12 建了 `newRootMux()` 作为 gateway 与 pages 的集成点。
> t13（Catherine）正在实现 `internal/adminapi/`（22 端点，前缀 `/admin/api/`）。
> 本文给出**实测验证过的挂载方案**，t13 完成后可直接照做。

---

## 🔴 一、实测发现的真实陷阱：Go ServeMux 的 307 自动重定向

**captain 实测**（Go 1.26.4，`httptest` 复现 t12 路由形态 + `/admin/api/` 子树）：

| 请求 | 若用 `/admin/api/` 子树模式 | **老服务实测** | 是否冲突 |
|---|---|---|---|
| `/admin/api/status` | ✅ `ADMINAPI-SUBTREE` 200 | 200 | ✅ 一致 |
| `/admin/api/` | 200（adminapi 处理） | **404** | 🔴 **不一致** |
| `/admin/api` | 🔴 **307 → `/admin/api/`** | **404** | 🔴 **不一致** |
| `/admin/unknown` | ✅ pages 的 `/admin/` 子树正确处理 | 404 形态 A | ✅ 一致 |
| `/v1/models` | ✅ GATEWAY | 200 | ✅ 一致 |

**老服务实测原始输出**：
```
--- /admin/api ---
HTTP/1.1 404 Not Found
--- /admin/api/ ---
HTTP/1.1 404 Not Found
--- /admin/api/status ---
HTTP/1.1 200 OK
```

**结论**：`/admin/api` 与 `/admin/api/` 老服务都是 **404**。
若 adminapi 用**子树模式** `/admin/api/` 挂载，Go 会给这两个路径 **307/200**（不匹配契约）。

---

## 二、推荐方案：**精确路由逐条注册**（方案 A）

### 做法

adminapi **不做子树匹配**，而是**逐条注册精确路径**：

```go
// internal/adminapi/routes.go 导出
func Register(mux *http.ServeMux, deps Deps) {
    mux.HandleFunc("GET /admin/api/verify", ...)
    mux.HandleFunc("GET /admin/api/accounts", ...)
    mux.HandleFunc("POST /admin/api/accounts", ...)
    // …22 条按契约逐条
}
```

**注意**：Go 1.22+ 的 `ServeMux` 支持方法+路径模式（`"GET /x"`），
**但 `{id}` 路径参数**要写成 `"PUT /admin/api/accounts/{account_id}"`（Go 1.22+ 语法，`r.PathValue("account_id")` 取值）。

### 为什么推荐这个

| 优点 | 说明 |
|---|---|
| **无 307 陷阱** | 精确模式不触发 ServeMux 的子树重定向 |
| **自然处理 404** | 未注册的 `/admin/api/*` 路径会**落到 pages 的 `/admin/` 子树** → 返回页面级 404（形态 A） |
| **与 t12 的 newRootMux 零冲突** | 只需在 `newRootMux` 加一行 `adminapi.Register(mux, deps)` |

### ⚠️ 但需验证一点：404 的形态

`/admin/api/nonexistent` 落到 pages 的 `/admin/` 子树后，返回的是**页面级 404**（形态 A `{"detail":"Not Found"}`）。
**老服务对未知 admin/api 路径返回什么？** 需 t13 完成后实测确认（若老服务返回形态 A，则一致 ✅）。

---

## 三、候选方案 B：子树模式 + 显式拦 `/admin/api`

```go
mux.HandleFunc("/admin/api", func(w, r) {  // 精确注册，拦住无尾斜杠
    writeDetail(w, 404, "Not Found")       // 复刻老服务 404
})
mux.Handle("/admin/api/", stripAndServe)   // 但 `/admin/api/` 本身老服务是 404，仍需特判
```

**缺点**：要额外处理两个路径，且 `/admin/api/` 自身也要 404（子树模式下它会进 handler）。
**比方案 A 更容易出错。**

---

## 四、接线步骤（t13 完成后执行）

1. **改 `cmd/zhipucode2api/main.go`** 的 `newRootMux`：
   ```go
   func newRootMux(gw http.Handler, adminDeps adminapi.Deps) http.Handler {
       mux := http.NewServeMux()
       pages.Register(mux)
       mux.Handle("/v1/", gw)
       adminapi.Register(mux, adminDeps)   // ← 新增一行
       return mux
   }
   ```

2. **验证脚本**（t13 完成后跑）：
   ```bash
   # 起新服务后
   for p in /admin/api/status /admin/api/accounts /admin/api /admin/api/ /admin/api/nope; do
     o=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "http://127.0.0.1:9120$p")
     n=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "http://127.0.0.1:19120$p")
     [ "$o" = "$n" ] && echo "✅ $p ($o)" || echo "❌ $p 老=$o 新=$n"
   done
   ```

3. **对拍**：
   ```bash
   python3 scripts/conformance/harness.py --baseline=http://127.0.0.1:9120 --candidate=http://127.0.0.1:19120
   ```
   期望：`AD-*` 全部从 FAIL 转 PASS。

---

## 五、给 t13 实现者的请求（Catherine）

**若你的 `internal/adminapi/` 已导出 `Register(mux, deps)` 风格的函数** → 接线是加一行。
**若导出的是 `Handler() http.Handler`** → 需改为 `Register` 风格，或由接线方用精确路径逐条挂。

**建议**：导出 `Register(mux *http.ServeMux, deps Deps)`（与 `pages.Register` 风格一致，
这样 `newRootMux` 里两行风格统一）。

---

## 附：本次实测的完整输出

```
=== ServeMux 优先级实测（Go 1.26.4）===
  /admin/api/status        → ADMINAPI-SUBTREE[/admin/api/status]      [200]
  /admin/api               → <a href="/admin/api/">Temporary Redirect</a>. [307] Location=/admin/api/
  /admin/api/              → ADMINAPI-SUBTREE[/admin/api/]            [200]
  /admin/api/nope          → ADMINAPI-SUBTREE[/admin/api/nope]        [200]
  /admin/unknown           → PAGES-ADMIN-SUBTREE                      [200]
  /admin/login             → PAGES-ADMIN-SUBTREE                      [200]
  /admin                   → PAGES-ADMIN-307                          [200]
  /v1/models               → GATEWAY                                  [200]
  /static/css/app.css      → STATIC                                   [200]
```

**关键两行**：`/admin/api` → 307（Go 自动）、`/admin/api/` → 200（子树命中）——老服务**均为 404**。


---

# 补充（Lamias 独立复现，2026-10-16 00:36）

> 来源：分支 `wt/task-wiring-plan` @ `398322a`，含隔离 go 模块 `scripts/adminapi-mux-probe/`
> （`go test -v` 即得全优先级矩阵，captain 已复跑：PASS）。

## 🔴 我的方案 A 有一个必须补的坑：**flat register 会丢尾斜杠 307**

**老服务实测**（captain 复核）：
```
/admin/api/status/  →  307 Temporary Redirect     ← 尾斜杠必须 307
/admin/api          →  404
/admin/api/         →  404
/admin/api/nope     →  404
```

**Lamias 的场景 D 实测**（flat register + block + slash）：
```
/admin/api/status/  → 404  {"detail":"Not Found"}@API-cat     ← 🔴 丢了 307！
/admin/api/        → 404  ✅
/admin/api         → 404  ✅
```
而 **mount 形态保内部 wrapper**：`/admin/api/status/` → **307 绝对** ✅

**结论**：若只用「精确路由逐条注册」（我的方案 A），**`/admin/api/status/` 会退化成 404**，
而老服务是 307 → **对拍会挂**。

### 修正后的方案 A′（推荐）

```go
// ① exact register 每条 adminapi 路由（无 307 陷阱）
mux.HandleFunc("GET /admin/api/status", ...)
// …22 条

// ② 显式 block 三个变体 → 404 形态 A（与老服务一致）
mux.HandleFunc("/admin/api",     writeDetail404)
mux.HandleFunc("/admin/api/",    writeDetail404)

// ③ 🔴 外挂 slashRedirect：为每条 exact 路由补「尾斜杠 → 307 绝对」行为
//    否则 /admin/api/status/ 会是 404，而老服务是 307
```

**或**改用 mount 形态（保内部 wrapper）但需额外处理 `/admin/api` 与 `/admin/api/` 的 404。

**取舍**：A′ 需要自己写 slashRedirect（约 20 行）；mount 形态需自己写 block。
**Lamias 的实测表明两者工作量相当**，A′ 更可控（不依赖 ServeMux 的隐式行为）。

## 三条实测定律（Lamias 的 mux-probe，captain 复跑确认）

| # | 定律 | 证据 |
|---|---|---|
| 1 | `/admin/api/` 子树**优先于** pages 的 `/admin/`（**无吞并**） | 场景 A/B：`/admin/login` 仍归 pages |
| 2 | 子树注册后 `/admin/api`（无尾斜杠）被 Go **自动 307**，而老服务是 **404** | 场景 B；必须 exact 404-block |
| 3 | **mount 保内部 wrapper**（`/admin/api/status/` → 307 绝对），**flat register 丢之**（→404） | 场景 C vs D |

## 附带：验证脚本

`scripts/verify-adminapi-wiring.sh`（Lamias 交付）—— 22 项路由面验证器
（页面×4/资源×4/gateway/adminapi 归属 + block 四形态 + 尾斜杠 307 + 重定向×5，双侧对照 host 归一）。
**已对老服务自比 22/22 PASS**（脚本逻辑可信性已证）。t13/t14 接线后直接跑。
