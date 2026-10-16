# 阿里云验证码求解器：headless Chromium 可行性 spike（t2）

- 日期：2026-10-15
- 执行：Catherine（测试者 · 黑盒验证与断言）
- 任务：`t2` / attempt `8ff3005d-637b-4011-8729-ec2cc9e7e8a6`
- 目标：判定能否用**真实 headless Chromium** 替代 1748 行 Node 逆向脚本 `captcha_node/solver.js`
- 观测边界：**未纳入本轮观测** `solver.js` 源码，仅黑盒观察（argv / stdout / strace / 文件访问）；**未纳入本轮观测** `zcode2api/app/**`、`zcode2api/cli.py`、`zcode2api/frontend/**`

---

## 0. 判定：NO-GO

> **「真实浏览器 + 官方在线 SDK」这条路线产不出契约要求的 VERIFY_PARAM。**

浏览器管道本身是通的（能加载 SDK、能触发回调、能取回产物），但**产物的内容不合契约**：缺少 `securityToken`。

契约（`zcode2api/docs/development/05-upstream-protocols.md:82`）要求：

```
verifyParam = base64(JSON{certifyId, sceneId, isSign, securityToken})   # 4 字段
```

原型实得：`base64(JSON{certifyId, sceneId, isSign})` —— **3 字段，无 securityToken**，`certifyId` 仅 5 字符（真实解为 10 字符）。**该产物无效**。

---

## 1. 已实测事实

### 1.1 可用浏览器通道（Q1）

| 通道 | 状态 | 实测 |
|---|---|---|
| fygo-browser CDP（16002） | ✅ 可用 | `curl 127.0.0.1:16002/json/version` → `Chrome/152.0.7977.82`，`ws://127.0.0.1:16002/devtools/browser/8046724f-…`；**真实显示**（Xkasmvnc :12，1280x720，非 headless） |
| fygo Chromium 二进制独立拉起 | ✅ 可用 | `/opt/chromium/app/vendor/runtime/usr/lib/chromium/chromium`（288127832 B）；`--headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage --disable-blink-features=AutomationControlled --user-data-dir=<tmp> --remote-allow-origins=*` → 端口 19111 可用，`Chromium 152.0.7977.82` |
| 系统 `chromium` / `google-chrome` | ❌ 不存在 | PATH 与 `/opt/google/chrome` 均无；`/opt/google/chrome/chrome: No such file or directory` |
| playwright / puppeteer npm | ❌ 不存在 | `captcha_node/node_modules/` 仅 `happy-dom`、`ws`、`@types`、`buffer-image-size`、`undici-types` |
| playwright 浏览器缓存 | ❌ 不可用 | `/home/appuser/.cache/ms-playwright/` 本 shell 不可访问（No such file or directory），虽有 `chrome-headless-shell` 进程在跑 |
| mitmproxy（HTTPS 解密） | ❌ 不可用 | `.mitmproxy/` CA 存在，但 PATH 无 `mitmdump`、无 Python 模块；`find / -maxdepth 6 -name mitmdump` 60s 超时 |

**依赖说明**：Node v24.4.0（`/usr/local/bin/node`）自带原生 `WebSocket` + `fetch`，CDP 客户端**零 npm 依赖**即可实现（见 `scripts/captcha_spike/cdp.mjs`）。

### 1.2 浏览器管道确实通（Q2 的前半）

headless Chromium 内加载当前在线 SDK → 触发 `captchaVerifyCallback` → 回调返回 `{captchaResult:true, bizResult:true}` → 取回产物 → 写出 stdout，**全链路 ~1.9 s 跑通**。

- 在线 SDK 来源：`https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js`（sha1 `98eabfd3403370b42850d5fa7ba4378b3ab4a31d`，224977 B）
- 回调 params 是一个 **JSON 字符串**（非对象），解析后 keys = `["sceneId","certifyId","deviceToken","failover","err"]`
- `deviceToken` 1332–1732 B，前缀 `V0VC`

### 1.3 但产物无效：SDK 走降级分支（Q2 的后半 = 卡点）

`captchaVerifyCallback` 的 `err` 字段携带**服务端拒绝**：

```
{code:"INIT_FAIL",
 msg:"Error: {\"RequestId\":\"A504D547-510B-4EAF-8B8C-D4FF68E71C55\",
      \"Message\":\"Specified signature is not matched with our calculation!\",\"Code\":401}"}
```

`success` 回调在 `onError` **之前**触发，产出 76 B 的 `{certifyId,sceneId,isSign}` —— 这是 SDK 的降级分支，不是有效解。

### 1.4 决定性对照：真实显示环境**同样 401**（排除 headless 嫌疑）

用 `diag.mjs --cdp=ws://127.0.0.1:16002/devtools/browser/8046724f-…` 把**同一份在线 SDK** 跑进 fygo-browser 的**真实显示**（Xkasmvnc，非 headless）：

```json
{"extCdp":true,"elapsedMs":3797,
 "timeline":["boot","sdk-loaded","init-call","verifyCallback(1903)","success(76)","onError","init-resolved"],
 "initReturn":"resolved:{\"hasCertifyId\":true}",
 "onError":{"code":"INIT_FAIL","msg":"…\"Specified signature is not matched with our calculation!\",\"Code\":401}"},
 "successDecoded":{"keys":["certifyId","sceneId","isSign"],
                   "masked":{"certifyId":"6bb34","sceneId":"11xygtvd","isSign":true}}}
```

**401 与 headless 无关**——它是环境无关的固有限制。（`verifyType` 三种取值 default / `1.0` / `3.0` 全部命中同一 401。）

### 1.5 根因：securityToken 不在在线 SDK 里

| 事实 | 证据 |
|---|---|
| 在线 SDK **0 次**出现 `securityToken` | `grep -c securityToken AliyunCaptcha.js` → 0；`grep -i securityToken feilin.js` → 0 |
| 在线 SDK 的 3.0 分支只给 3 字段 | `"3.0"===r.verifyType&&r.success&&r.success(window.btoa(O()({certifyId:u.CertifyId,sceneId:r.SceneId,isSign:!0})))` |
| `securityToken` 只存在于**老版类式 SDK** | 缓存 `0022b3efd90664de1a8cd8a92be74b44cedd815a`（446146 B），`window.AliyunCaptcha = rS`（类/构造器） |
| 老版里它由**外部注入** | `…isSign:!0,securityToken:this.config.securityToken}),this.success&&this.success(n)` —— 全文件 `securityToken` 出现 2 次，**赋值点 0 个**，读取点 1 个 |
| 老版 SDK 在标准浏览器中**加载即崩** | `Uncaught TypeError: Cannot read properties of undefined (reading 'AES')` @ `old.js:1:311236`；`window.AliyunCaptcha` 仍为 `undefined` |
| 崩溃源于动态全局注入 | 老版有 **49 处** `window[...]` 动态全局读取（`tA=window[td+(tl(),tl)(27,228)]`），新版仅 2 处；`crypto` 计数：老版 0 / 新版 25 |

**解读**：`securityToken` 由 SDK 的**服务端交互**下发（不是客户端算的），而在线 SDK 已被移除该路径；老版 SDK 需要 solver.js 私有注入的 AES 全局环境才能挂载。

### 1.6 solver.js 黑盒画像（Q4 接口对齐）

```bash
cd captcha_node && node solver.js 11xygtvd cn no8xfe
# → exit 0，stdout: VERIFY_PARAM=<base64>，长度 280 B
# → base64 解码 keys = ["certifyId","sceneId","isSign","securityToken"]
# → certifyId 10 字符；securityToken 128 字符（= 96 字节 base64）
```

- **接口形态**：`node solver.js <scene> <region> <prefix>` → stdout `VERIFY_PARAM=<value>`；**与 Go 侧天然对齐**
- **退出码**：`0` = 成功；`6` = 失败（异常 scene `otherscene`、异常 prefix `zzzzzz` 均 exit 6，stdout/stderr 皆空）
- **缺参回退**：`node solver.js`（空 argv）→ exit 0，用默认值成功
- **region 敏感**：`11xygtvd cn no8xfe` ✅ / `11xygtvd sgp no8xfe` ✅（securityToken 不同）
- **黑盒网络**：`strace -f -e trace=write,writev,sendto,sendmsg` 的 `CONNECT` 目标 = `cloudauth-device-dualstack.cn-shanghai.aliyuncs.com:443`、`cn-shanghai.device.saf.aliyuncs.com:443`、**`no8xfe-verify.captcha-open.aliyuncs.com:443`**、`no8xfe.captcha-open.aliyuncs.com:443`、`upload.captcha-open.aliyuncs.com:443`、`zcode.z.ai:443`；全部经 `127.0.0.1:7890`（mihomo）
  - ⚠️ `no8xfe-verify.captcha-open.aliyuncs.com` 是**原型从未联系的 verify 端点** —— 这是 solver.js 与"纯 SDK 驱动"路线的关键行为差异
- **黑盒文件访问**：`strace -e trace=openat` 显示 solver.js 从 `.zcode-captcha-cdn-cache/` 读 `98eabfd…`（在线式）、`380f3fe5…`（FeiLin 指纹）、**`0022b3ef…`（含 securityToken 的老版）**、`2e89bfca…`（iconfont）——**运行时不请求 `o.alicdn.com`**（全部走本地缓存）
- **无网络即失败**：`unshare -rn node solver.js 11xygtvd cn no8xfe` → **exit 6，stdout 0 B，stderr 0 B**（静默失败，两次复现）

### 1.7 securityToken 的结构性质（Q5 补充）

8 次采样中有效 3–6 次，逐位分析：

- 长度恒定 **128 字符 base64 = 96 字节**
- **106 / 128 位固定**，变化位集中在 **64..85**
- 6 次采样的**最长公共前缀 = 64 字符（占 50%）**
- 样本形态（**已打码**，仅保留前 8 位以说明「前缀固定」这一结构性质）：
  `<8 位可见前缀…已打码>|<8 位变化段…已打码>` vs `<同上前缀>|<另一变化段…已打码>`
  —— 完整值（128 字符）不落盘；结构结论（106/128 位固定、最长公共前缀 64 字符）来自 6 次采样的逐位比对，不依赖单条样本原文。

**解读**：`securityToken` 是**固定密钥加密固定明文**的产物（含一小段变化量），**不是服务端全随机下发**。→ 理论上存在**纯 Go 复现**的可能（见 §4 路径 C），但需要知道密钥/明文结构，而那只能来自 `solver.js` 实现（本次 本轮不做）。

### 1.8 时序与并发（Q5）

| 指标 | 实测 |
|---|---|
| solver.js 单次延迟（5 次） | 2960 / 2771 / 2392 / 3590 / 2539 ms，**中位 ~2771 ms** |
| 3 并发总耗时 | **12835 ms**（约 4.3 s/个 → 存在串行化，非真并行） |
| 3 并发结果 | 3/3 成功，均 280 B |
| 原型（headless Chromium）端到端 | ~1.9 s（`elapsedMs:3797` 为真实显示环境含 SDK 下载） |
| 失败判定 | 退出码 6（stdout/stderr 双空，**不可从输出区分"网络失败"与"参数错误"**） |

---

## 2. 卡点汇总（可复现）

| # | 卡点 | 命令 / 现象 |
|---|---|---|
| 1 | 官方在线 SDK 被服务端拒绝 | SDK 内 `InitCaptchaV3` → `POST no8xfe.captcha-open.aliyuncs.com/` → **HTTP 400**，`{"Message":"Specified signature is not matched with our calculation!","Code":401}`；同现于 `captcha-open-b`、`upload.captcha-open` |
| 2 | 该 401 **与 headless 无关** | 真实显示环境（fygo-browser / Xkasmvnc）复现同一 401（§1.4） |
| 3 | 产物缺 `securityToken` | `success` 在 `onError` 前触发，只给 `{certifyId,sceneId,isSign}`（76 B） |
| 4 | 老版 SDK 无法在标准浏览器挂载 | `Cannot read properties of undefined (reading 'AES')` @ `1:311236` |
| 5 | `securityToken` 无法在客户端算出 | 老版 0 个赋值点，仅 `this.config.securityToken` 读取；在线版 0 次出现 |
| 6 | 无网络即静默失败 | `unshare -rn node solver.js …` → exit 6，**stdout/stderr 全空** |

**关键判断**：卡点 1–5 合起来说明——要产出 4 字段 VERIFY_PARAM，**必须复现 solver.js 对 SDK 的改造（注入 AES 全局 + 喂老版 SDK）**。那**正是逆向脚本本身**，不是"用真浏览器替代它"。因此本轮 spike 的结论是 **NO-GO**，而不是"换个 SDK 继续逆向"。

---

## 3. 为什么不是"部分 GO"

「部分 GO」的合理形态是：*浏览器管道可用，只差一点点*。实测不支持这个说法：

- 浏览器管道**确实可用**（§1.2），但**产物内容不可用**（§1.3）
- 缺的 `securityToken` **不是"一点点"**：它是契约 4 字段之一，且是服务端签名链的产物
- 缺它的原因**不在浏览器侧**，而在"在线 SDK 已移除该路径 + 老版 SDK 需私有注入环境"
- 因此把 Chromium 换成更"真"的浏览器、把 headless 换成有显示环境，**都不会改变结果**（已实测）

---

## 4. NO-GO 替代路径

### 路径 A（推荐，最低风险）：把 solver.js 当黑盒子进程保留

Go 侧 `os/exec` 调用，接口天然对齐（`argv → stdout VERIFY_PARAM=`）。**隔离在接口后面**，该依赖的许可单独处理。

### 路径 B：阿里云官方 OpenAPI（服务端签名）

用官方 `VerifyCaptcha` 服务端 API，需要 AccessKey 与签名实现。**未验证**——本轮只确认了 SDK 私有签名（HMAC-SHA1，`SignatureVersion=1.0`、`Version=2023-03-05`）在浏览器内被 401 拒绝。

### 路径 C：纯 Go 复现 `securityToken`（无需浏览器）—— 最有价值的中期方向

§1.7 显示它是"固定密钥 + 固定明文 + 小段变化"的加密产物，理论上可纯 Go 复现，**彻底摆脱 Node 与浏览器**。
**阻塞**：需要密钥/明文结构，只能来自 `solver.js` 实现——本次 本轮不做。
**建议**：作为独立任务立项，明确解除对 `solver.js` 的读取禁令后再做。

### 路径 D（否决）：换老版 SDK 继续逆向

即本轮验证掉的方向：老版 SDK 加载即崩（§1.5），且即便修复也仍是"另一种逆向"。**与替换 solver.js 的目的相悖，不建议。**

---

## 5. Go 侧集成架构取舍（Q6）

### 选项 ①：Go `os/exec` 调 Node 脚本

| 维度 | 评价 |
|---|---|
| 依赖 | 需 Node 运行时（~100 MB）+ `happy-dom` 依赖树 |
| 性能 | 单次 ~2.8 s（中位）；3 并发 ~12.8 s（**串行化明显**）；每解一次起一个进程 |
| 可靠性 | 已验证生产可用；退出码语义单一（0/6），**失败原因不可区分** |
| 改造成本 | **≈0**：接口已对齐（argv → stdout） |
| 依赖 | 保留 1748 行外部脚本，作为黑盒进程调用（见 ADR-009） |

### 选项 ②：Go 直连 CDP（原生 WebSocket）

| 维度 | 评价 |
|---|---|
| 依赖 | 零 Node；纯 Go（`net/http` + `gorilla/websocket`） |
| 性能 | 浏览器常驻 + 多标签复用，理论优于每次起进程 |
| 可靠性 | **前提不成立**：产物缺 `securityToken`（§1.3），该路线**当前不可交付** |
| 改造成本 | 需实现 CDP 客户端 + 目标/会话管理（本 spike 的 `cdp.mjs` 可作参考，约 200 行等价 Go） |
| 结论 | **暂不采用**；仅在路径 B/C 打通后才重新评估 |

### 建议

**短期取选项 ①**（`os/exec` 调 solver.js，接口隔离），**中期投路径 C**（纯 Go 复现 securityToken）。选项 ② 在路径 B/C 未通前**不投入**。

---

## 6. 复现方式

```bash
# 前提
NODE=/usr/local/bin/node
CHROME=/opt/chromium/app/vendor/runtime/usr/lib/chromium/chromium
export LD_LIBRARY_PATH=/opt/chromium/app/vendor/runtime/usr/lib/x86_64-linux-gnu:/opt/chromium/app/vendor/runtime/usr/lib/x86_64-linux-gnu/ssl:/opt/chromium/app/vendor/runtime/lib/x86_64-linux-gnu:/opt/chromium/app/vendor/runtime/usr/lib:/opt/chromium/app/vendor/runtime/lib

# ① solver.js 黑盒基线（期望 exit 0，stdout 280 B VERIFY_PARAM）
cd external/zcode2api/captcha_node && $NODE solver.js 11xygtvd cn no8xfe

# ② 原型：headless Chromium 驱动在线 SDK（期望 exit 0，但产物仅 3 字段）
cd external/zhipucode2api/scripts/captcha_spike && $NODE spike.mjs 11xygtvd cn no8xfe

# ③ 全量诊断：抓回调/请求/控制台（产物落 workspace/temp/captcha_spike/）
cd external/zhipucode2api/scripts/captcha_spike && $NODE diag.mjs 11xygtvd cn no8xfe --json

# ④ 决定性对照：跑进真实显示环境（fygo-browser CDP）
WS=$(curl -s 127.0.0.1:16002/json/version | grep -o 'ws://[^"]*')
cd external/zhipucode2api/scripts/captcha_spike && $NODE diag.mjs 11xygtvd cn no8xfe --cdp="$WS" --json

# ⑤ 无网络对照（期望 exit 6，双空输出）
cd external/zcode2api/captcha_node && unshare -rn $NODE solver.js 11xygtvd cn no8xfe; echo "exit=$?"

# ⑥ 黑盒网络/文件访问
strace -f -e trace=write,writev,sendto,sendmsg -o /tmp/st.txt $NODE solver.js 11xygtvd cn no8xfe
strace -e trace=openat -o /tmp/op.txt $NODE solver.js 11xygtvd cn no8xfe
```

---

## 7. 交付物与证据文件

**脚本**（`external/zhipucode2api/scripts/captcha_spike/`）

| 文件 | 作用 |
|---|---|
| `cdp.mjs` | 零依赖 CDP 客户端（connect / send / on / once / waitForCdp） |
| `spike.mjs` | 最小原型：`node spike.mjs [scene] [region] [prefix]`，exit 0/2/3/4 |
| `diag.mjs` | 全量诊断：抓 `captchaVerifyCallback` 参数、请求、控制台、时间线；支持 `--cdp=` 驱动外部浏览器 |
| `oldsdk.mjs` | 老版 SDK 挂载探测（证伪路径 D） |

**原始证据**（`workspace/temp/captcha_spike/`，已扫描无明文凭据）

`diag1.json`、`diag2.json`、`diag_proxy.json`、`vt_default.json`、`vt_1.0.json`、`vt_3.0.json`、`oldsdk1.json`、`diag_realdisplay.json`（§1.4 决定性对照）、`run1.json`、`strace_solver*.txt`、`strace_open*.txt`、`solver_netns.txt`、`AliyunCaptcha.js`（在线 SDK）、`feilin.js`

---

## 8. 未完成 / 未验证项

- ⬜ **路径 B 未验证**：未测试阿里云官方 `VerifyCaptcha` OpenAPI 是否能替代（需 AccessKey，本次未获授权）
- ⬜ **路径 C 未开展**：`securityToken` 的密钥/明文结构未解（受 观测限制）
- ⬜ **并发上限未探**：只测到 3 并发；未测更高并发下的成功率与内存
- ⬜ **长稳未测**：未做小时级连续求解的稳定性观察
- ⬜ **失败原因不可区分**：solver.js 的 exit 6 无法区分"网络失败 / 参数错误 / 上游拒绝"，Go 侧需要自行加超时与重试语义
- ⬜ **`no8xfe-verify` 端点未解**：solver.js 会联系它而原型不会，该端点在链路中的确切作用未确认

---

## 9. 给 Go 重写的直接结论

1. **不要**用"浏览器驱动官方 SDK"替换 solver.js —— 已证伪（NO-GO）
2. **不要**用"老版 SDK 逆向"替换 —— 加载即崩，且目的相悖
3. **短期**：保留 solver.js 作为黑盒子进程，Go 用 `os/exec` 调用，接口 `argv → stdout VERIFY_PARAM=`
4. **中期**：立项"纯 Go 复现 securityToken"（路径 C），这是唯一能摆脱 Node 运行时依赖的方向
5. **注意**：solver.js 失败时 stdout/stderr 双空、仅 exit 6，Go 侧必须自行实现超时（`ZCODE_CAPTCHA_TIMEOUT` 默认 40s）与重试（`ZCODE_CAPTCHA_RETRIES` 默认 4）
