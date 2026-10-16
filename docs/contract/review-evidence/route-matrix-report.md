# route-matrix-report · 三向端点核对（契约/老服务/新服务）

> 生成 2026-10-16 00:42:27+0800 · 仓 HEAD `2617029` · old=`http://127.0.0.1:9120` new=`http://127.0.0.1:19120`
> 复现：`python3 scripts/conformance/route_matrix.py --selftest && python3 scripts/conformance/route_matrix.py --report <本文件>`
> 探针纪律：全匿名 + 最小 body + 网关端点用 `"{bad"` 本地拒绝形态
> （零副作用零上游；404 细分 Not Found=路由不存在 vs 业务 404=路由存在）。

## 0. 自测（合成三向差异 → 四类判定能力，原始输出）

```
  [OK] st-drift  [('GET', '/alpha')]
  [OK] st-unrecorded  [('GET', '/zeta')]
  [OK] st-unimplemented  {'biz': [('GET', '/beta'), ('GET', '/zeta')]}
  [OK] st-unimplemented-builtin-split  {'builtin': []}
  [OK] st-builtin-not-in-business-count  {'biz': [('GET', '/beta'), ('GET', '/zeta')], 'builtin': [('GET', '/docs'), ('GET', '/redoc')]}
  [OK] st-extra-impl  {'extra': [('GET', '/alpha'), ('GET', '/metrics')], 'anchors': ['/omega']}
  [OK] st-transport-fail-not-convicted  {'drift': [], 'unimpl': [], 'indet': [('GET', '/alpha'), ('GET', '/beta'), ('GET', '/delta')]}
  [OK] st-clean-not-accused  []
```

## 1. 四类判定汇总（实测快照）

> **快照语境**：生成时 19120 dev 实例正在重启（t13 进行中，/v1/models 连接拒绝）——C 侧 66 项全部如实进【不定】桶，零定罪。A/B 两向（契约×老服务）不受影响、已定案：drift=0、漏记=0。
**[drift 契约有·老服务无]** 0: 无
**[unrecorded 老服务有·契约无(业务面)]** 0: 无
**[unimplemented 老服务有·新服务无]** 0: 无
**[extra_impl 新服务有·老服务无]** 0: 无
**[未实现·builtin 面(FastAPI docs 家族,非 33 缺口)]** 0: 无
**[⚠️ 不定（连接失败/探测不可达）]** 66: [('DELETE', '/admin/api/accounts'), ('GET', '/'), ('GET', '/admin'), ('GET', '/admin/accounts'), ('GET', '/admin/api/accounts'), ('GET', '/admin/api/claim/captcha-config'), ('GET', '/admin/api/claim/preview'), ('GET', '/admin/api/export'), ('GET', '/admin/api/login/poll/{}'), ('GET', '/admin/api/monitoring'), ('GET', '/admin/api/settings'), ('GET', '/admin/api/status')]
**[禁列复活(CR-12 多实现)]** 0: 无

## 2. 逐条覆盖矩阵（契约 33 端点 × 两侧实测状态码）

| # | 方法 | 路径 | 老服务(9120) | 新服务 | 判定 |
|---|---|---|---|---|---|
| B-01 | GET | `/admin/api/verify` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-02 | GET | `/admin/api/accounts` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-03 | POST | `/admin/api/accounts` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-04 | DELETE | `/admin/api/accounts` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-05 | GET | `/admin/api/status` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-06 | PUT | `/admin/api/accounts/{account_id}` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-07 | POST | `/admin/api/accounts/{account_id}/enabled` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-08 | POST | `/admin/api/accounts/{account_id}/fingerprint/rotate` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-09 | POST | `/admin/api/accounts/refresh` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-10 | POST | `/admin/api/accounts/{account_id}/refresh` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-11 | POST | `/admin/api/login/start` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-12 | GET | `/admin/api/login/poll/{flow_id}` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-13 | GET | `/admin/api/claim/preview` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-14 | POST | `/admin/api/claim` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-15 | GET | `/admin/api/claim/captcha-config` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-16 | POST | `/admin/api/claim/manual` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-17 | GET | `/admin/api/settings` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-18 | PUT | `/admin/api/settings` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-19 | GET | `/admin/api/export` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-20 | POST | `/admin/api/import` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-21 | GET | `/admin/api/monitoring` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| B-22 | POST | `/admin/api/monitoring/clear` | 401 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| A-01 | GET | `/v1/models` | 200 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| A-02 | POST | `/v1/messages` | 400 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| A-03 | POST | `/v1/chat/completions` | 400 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| D-01 | GET | `/meta` | 200 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| D-02 | GET | `/openapi.json` | 200 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| C-01 | GET | `/` | 307 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| C-02 | GET | `/admin` | 307 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| C-03 | GET | `/admin/login` | 200 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| C-04 | GET | `/admin/accounts` | 200 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| C-05 | GET | `/admin/monitoring` | 200 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |
| C-06 | GET | `/admin/settings` | 200 | ✗不定 | ⚠️ 不定（C 侧此刻不可达，非定罪） |

## 3. 附录 · C 方向历史快照（00:37，19120 存活时刻的真值）

同一工具逻辑、当时三态修复前（那次运行全部连接成功，数据有效）：

| 判定 | 内容 |
|---|---|
| ✅ 已到位（9） | A 组 3（/v1/models、/v1/messages、/v1/chat/completions）+ C 组 6 页面（/、/admin、/admin/login、/admin/accounts、/admin/monitoring、/admin/settings） |
| ⬜ 未实现（24 业务） | B 组 22 全部 + D-01 /meta + D-02 /openapi.json（t13 进行中，属预期现状） |
| ⬜ 未实现·builtin（3） | /docs、/redoc、/docs/oauth2-redirect（FastAPI 内置面，cutover 裁定项非 33 缺口） |
| 🟠 多实现 | **0**（探测面 + 禁列锚 24 + 启发式 9 全干净——CR-12 合规） |
| 🟡 漂移 / 🔴 漏记 | 0 / 0 |

## 4. 本轮工具自纠记录（诚实留痕）

- 00:37 与 00:39 两次快照 unimplemented 从 27→33 突变，追查为 **19120 dev 实例重启**、
  旧版 probe 把 **transport 失败当"路由不存在"** → 全体误定罪"未实现"。
- 修复：probe 三态（存在/不存在/**不定**），不定项从一切定罪桶剔除并单列；
  自测 `st-transport-fail-not-convicted` 固化（8/8 全绿）。
- 这是 status-pin 事故的姊妹教训：**"没探到" ≠ "没有"**——缺证据不判过，也**不判罪**。
