#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""t16 · 三向端点核对器（route matrix）——契约 A × 老服务 B × 新服务 C。

与 harness 互补：harness 按 case 做【行为级】逐条对拍；本器按【路由表】做
三向集合 diff，专补 harness 的三个盲区：
  ①漏实现（老服务有、新服务无）——case 只测已知端点，不测"新服务少了什么"；
  ②多实现（新服务有、老服务无）——CR-12 明令禁止"好心补实现"；
  ③契约漏记（老服务有、契约无）——契约不完整。
四类判定：
  drift        契约有、老服务无          （文档漂移，CR-12 已记录形态）
  unrecorded   老服务有、契约无（业务面）  （🔴 契约漏记）
  unimplemented 老服务有、新服务无        （🔴 切换后功能缺失）
  extra_impl    新服务有、老服务无        （🟠 契约违反/CR-12）

存在性探针纪律（零副作用、零上游消耗）：
  · 全部匿名探测——鉴权在业务逻辑之前（契约硬断言 t8 已实测），401=路由存在；
  · 写方法带最小 body（{} / []），鉴权层先拒，不落库不发上游；
  · 唯 /v1/messages、/v1/chat/completions 网关 key 未配置时匿名放行，
    `{}` 会被转发到真实上游（3001）——故用【非法 JSON "{bad"】触发本地
    400 拒绝（t8 CR-01/32 实测形态），零上游；
  · 404 细分：body detail=="Not Found"（FastAPI 路由不存在）vs 其它 404
    （如"账号不存在"=业务 404，【路由存在】）——模板路径 {param}→"__probe__"
    探到业务 404 仍判存在。
  · 多实现探测两层：契约"已实测确认不存在"锚清单 + 通用框架痕迹启发式
    （/metrics、/debug/pprof、/readyz、/swagger 等——两侧都探，只有
     "新有老无"才定罪）。

自证：--selftest 用合成三向集合验证四类报告能力（含 builtin 豁免不误报），
原始输出进报告。实跑：--old/--new/--report。

用法：
  python3 route_matrix.py --selftest
  python3 route_matrix.py --old http://127.0.0.1:9120 --new http://127.0.0.1:19120 \
      --report docs/contract/review-evidence/route-matrix-report.md
"""
import argparse
import json
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path

HERE = Path(__file__).resolve().parent
DEFAULT_CONTRACT = HERE.parents[1] / "docs" / "contract" / "endpoints.md"

# FastAPI 内置面：不属于 33 端点契约，单列呈现（不计漏记/多实现，不计未实现定罪）
BUILTIN = {("GET", "/docs"), ("GET", "/redoc"), ("GET", "/docs/oauth2-redirect")}
GATEWAY_LOCAL_REJECT = {"/v1/messages", "/v1/chat/completions"}   # "{bad" 防上游
NO_REDIRECT = type("NoRedirect", (urllib.request.HTTPRedirectHandler,),
                   {"redirect_request": lambda *a, **k: None})
OPENER = urllib.request.build_opener(NO_REDIRECT(),
                                     urllib.request.ProxyHandler({}))


def norm(p):
    """路径模板归一：{account_id} → {}（openapi 与契约的参名不同源）。"""
    return re.sub(r"\{[^}]*\}", "{}", p)


def probe(base, method, path_template, timeout=10):
    """匿名存在性探测 → (exists, status, note)。"""
    path = path_template.replace("{account_id}", "__probe__")
    path = re.sub(r"\{[^}]+\}", "__probe__", path)
    data = None
    if method in ("POST", "PUT", "DELETE"):
        if path in GATEWAY_LOCAL_REJECT:
            data = b"{bad"          # 本地拒绝形态，零上游（t8 实测）
        elif path == "/admin/api/accounts" and method == "DELETE":
            data = b"[]"
        else:
            data = b"{}"
    req = urllib.request.Request(base.rstrip("/") + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    try:
        with OPENER.open(req, timeout=timeout) as r:
            code, body, hdr = r.status, r.read()[:200], dict(r.headers)
    except urllib.error.HTTPError as e:
        code, body = e.code, e.read()[:200]
        hdr = dict(e.headers or {})
    except Exception as e:  # noqa: BLE001
        # 三态之【不定】：连接失败≠路由不存在——绝不得据此定罪
        # （实弹发现：t13 dev 实例重启瞬间全体 connection refused，
        #   旧版误报 unimplemented=33 全员到齐，与真值 24 相差 9）
        return None, None, "transport:%s" % type(e).__name__
    txt = body.decode("utf-8", "replace")
    if code == 404 and '"Not Found"' in txt:
        return False, code, "route-absent"
    if code == 404:
        return True, code, "route-exists(business-404)"
    if code in (301, 302, 303, 307, 308):
        return True, code, "redirect→%s" % hdr.get("Location", "?")
    return True, code, "status"


def parse_contract(md_path):
    txt = Path(md_path).read_text(encoding="utf-8")
    claims = []
    for m in re.finditer(r"^### ([ABD]-\d+) `(GET|POST|PUT|DELETE) ([^`]+)`", txt, re.M):
        claims.append((m.group(1), m.group(2), m.group(3)))
    for m in re.finditer(r"^\| (C-\d+) \| (GET) \| `([^`]+)`", txt, re.M):
        claims.append((m.group(1), m.group(2), m.group(3)))
    chunk = re.search(r"已实测确认不存在.*?[：:](.*?。)", txt, re.S)
    absent = sorted({p for p in re.findall(r"/[A-Za-z0-9_\-/\.]*[A-Za-z0-9_\-/]",
                                           chunk.group(1))}) if chunk else []
    absent = [p for p in absent if p != "/"]
    return claims, absent


def classify(claims, old_set, new_set, anchors=None, indet_old=None, indet_new=None):
    """claims: [(id, method, path_tpl)]；old/new_set: {(method, norm_path)}
    anchors=[(path, exists_on_old, exists_on_new)]：契约「已实测确认不存在」禁列，
    新侧存在而老侧不存在 = CR-12 禁列复活（多实现）。
    注：一条路由可同时落多个桶（/zeta 既漏记又未实现）——各桶语义独立。"""
    indet_old = indet_old or set()
    indet_new = indet_new or set()
    claim_pairs = {(m, norm(p)): i for i, m, p in claims}
    drift = sorted((set(claim_pairs) - old_set) - indet_old)
    unrecorded = sorted(((old_set - set(claim_pairs)) - BUILTIN) - indet_old)
    # 未实现分两面：业务面（契约 33 内）才是切换缺口；builtin 面（FastAPI docs
    # 家族）单列呈现——Go 不实现 /docs 属 cutover 裁定项，不得混进缺口计数。
    unimpl_all = sorted((old_set - new_set) - indet_new - indet_old)
    unimplemented = [x for x in unimpl_all if x not in BUILTIN]
    unimpl_builtin = [x for x in unimpl_all if x in BUILTIN]
    extra = sorted((new_set - old_set) - BUILTIN - indet_old)
    anchor_extra = sorted(p for p, o, n in (anchors or []) if n and not o)
    return {"drift": drift, "unrecorded": unrecorded, "unimplemented": unimplemented,
            "unimpl_builtin": unimpl_builtin,
            "extra_impl": extra, "anchor_extra": anchor_extra,
            "indeterminate": sorted(indet_old | indet_new),
            "claims_total": len(claim_pairs)}


def selftest():
    """合成三向差异样本 → 四类各必命中 + builtin 不误报。返回 [(name, ok, detail)]"""
    out = []
    claims = [("X-01", "GET", "/alpha"), ("X-02", "GET", "/beta"),
              ("X-03", "GET", "/gamma"), ("X-04", "GET", "/delta"),
              ("X-05", "POST", "/epsilon")]
    old = {("GET", "/beta"), ("GET", "/gamma"), ("GET", "/delta"),
           ("POST", "/epsilon"), ("GET", "/zeta"),        # 契约没记 → 漏记
           ("GET", "/docs")}                               # builtin，不判漏记
    new = {("GET", "/gamma"), ("GET", "/delta"), ("GET", "/alpha"),
           ("POST", "/epsilon"), ("GET", "/metrics"),     # 老服务无 → 多实现
           ("GET", "/docs")}
    r = classify(claims, old, new,
                 anchors=[("/omega", False, True), ("/zeta", True, False)])
    # 漂移：X-01 /alpha 契约有、老服务无
    out.append(("st-drift", [("GET", "/alpha")] == r["drift"], r["drift"]))
    # 漏记：/zeta 老服务有、契约无（/docs 属 builtin 面不得混入）
    out.append(("st-unrecorded",
                r["unrecorded"] == [("GET", "/zeta")], r["unrecorded"]))
    # 未实现：/beta 老有新无=业务缺口；/docs 两侧都有不得入任何桶——再加一例：
    # 构造 old 有 /docs new 无 → 必须落 builtin 单列而非业务计数
    out.append(("st-unimplemented",
                ("GET", "/beta") in r["unimplemented"] and ("GET", "/zeta")
                in r["unimplemented"], {"biz": r["unimplemented"]}))
    out.append(("st-unimplemented-builtin-split",
                not any(x in r["unimplemented"] for x in BUILTIN)
                and r["unimpl_builtin"] == [], {"builtin": r["unimpl_builtin"]}))
    r2 = classify(claims, old | {("GET", "/redoc")},
                  new - {("GET", "/docs")},
                  anchors=[("/omega", False, True), ("/zeta", True, False)])
    out.append(("st-builtin-not-in-business-count",
                ("GET", "/redoc") not in r2["unimplemented"]
                and ("GET", "/redoc") in r2["unimpl_builtin"]
                and ("GET", "/docs") in r2["unimpl_builtin"],
                {"biz": r2["unimplemented"], "builtin": r2["unimpl_builtin"]}))
    # 多实现：/alpha、/metrics（探测面）+ /omega（禁列锚复活）
    out.append(("st-extra-impl",
                {("GET", "/alpha"), ("GET", "/metrics")} <= set(r["extra_impl"])
                and ["/omega"] == r["anchor_extra"],
                {"extra": r["extra_impl"], "anchors": r["anchor_extra"]}))
    # 连接失败=不定：老侧探测失败的契约端点不得判 drift；
    # 新侧探测失败的老端点不得判 unimplemented——一律进 indeterminate
    r3 = classify(claims, {("GET", "/gamma"), ("GET", "/delta"),
                           ("POST", "/epsilon")},   # /beta /alpha 老侧 transport
                  {("GET", "/gamma"), ("POST", "/epsilon")},   # /delta 新侧 transport
                  anchors=[],
                  indet_old={("GET", "/beta"), ("GET", "/alpha")},
                  indet_new={("GET", "/delta")})
    out.append(("st-transport-fail-not-convicted",
                ("GET", "/alpha") not in r3["drift"]
                and ("GET", "/beta") not in r3["unimplemented"]
                and ("GET", "/delta") not in r3["unimplemented"]
                and r3["unimplemented"] == []   # gamma/epsilon 两侧同在，无真缺口
                and len(r3["indeterminate"]) == 3,
                {"drift": r3["drift"], "unimpl": r3["unimplemented"],
                 "indet": r3["indeterminate"]}))
    # 全到位端点（/gamma /delta /epsilon）不得出现在任何判罪桶
    clean = {("GET", "/gamma"), ("POST", "/epsilon")}
    allbad = set(r["drift"]) | set(r["unrecorded"]) | set(r["unimplemented"]) | set(r["extra_impl"])
    out.append(("st-clean-not-accused", not (clean & allbad), sorted(clean & allbad)))
    return out


def run_live(old_base, new_base, contract_path):
    claims, absent = parse_contract(contract_path)
    # B 枚举：openapi × methods + 契约 C 组（不在 openapi）+ builtin
    oa = json.loads(OPENER.open(urllib.request.Request(
        old_base.rstrip("/") + "/openapi.json"), timeout=15).read())
    live = set()
    for p, ops in oa["paths"].items():
        for m in ops:
            live.add((m.upper(), norm(p)))
    probe_set = sorted(live | {(m, tp) for _, m, tp in claims} | BUILTIN)
    old_set, old_detail, indet_old = set(), {}, set()
    for m, tp in probe_set:
        ex, code, note = probe(old_base, m, tp)
        old_detail[(m, norm(tp))] = (ex, code, note)
        if ex is True:
            old_set.add((m, norm(tp)))
        elif ex is None:
            indet_old.add((m, norm(tp)))
    new_set, new_detail, indet_new = set(), {}, set()
    for m, tp in probe_set:
        ex, code, note = probe(new_base, m, tp)
        new_detail[(m, norm(tp))] = (ex, code, note)
        if ex is True:
            new_set.add((m, norm(tp)))
        elif ex is None:
            indet_new.add((m, norm(tp)))
    # 禁列锚 + 启发式痕迹：两侧都探，只有「新有老无」定罪
    heur = ["/metrics", "/readyz", "/livez", "/debug/pprof/", "/swagger/index.html",
            "/robots.txt", "/.well-known/security.txt", "/api", "/healthz"]
    anchors = []
    for p in sorted(set(absent) | set(heur)):
        eo, oc, _ = probe(old_base, "GET", p)
        en, nc, _ = probe(new_base, "GET", p)
        anchors.append((p, eo, en, oc, nc))
    indet_anchor = [p for p, eo, en, _, _ in anchors if eo is None or en is None]
    r = classify(claims, old_set, new_set,
                 anchors=[(p, eo, en) for p, eo, en, _, _ in anchors
                          if eo is not None and en is not None],
                 indet_old=indet_old, indet_new=indet_new)
    r["anchor_status"] = [(p, oc, nc) for p, eo, en, oc, nc in anchors if en and not eo]
    r["heur_extra"] = [x for x in r["anchor_status"] if x[0] in heur]
    r["indeterminate"] = r["indeterminate"] + [("anchor:%s" % p) for p in indet_anchor]
    r["probe_set_size"] = len(probe_set)
    rows = []
    for i, m, tp in claims:
        ov = old_detail.get((m, norm(tp)), (False, None, "-"))
        nv = new_detail.get((m, norm(tp)), (False, None, "-"))
        rows.append((i, m, tp, ov, nv))
    return r, rows, absent


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--selftest", action="store_true")
    ap.add_argument("--old", default="http://127.0.0.1:9120")
    ap.add_argument("--new", default="http://127.0.0.1:19120")
    ap.add_argument("--contract", default=str(DEFAULT_CONTRACT))
    ap.add_argument("--report", default=None)
    ap.add_argument("--note", default=None,
                    help="核对时的服务状态语境（如 t13 进行中），如实写入报告头")
    args = ap.parse_args(argv)

    if args.selftest:
        results = selftest()
        print("== route_matrix 自测（合成三向差异 → 四类判定能力）==")
        for name, ok, det in results:
            print("  [%s] %s  %s" % ("OK" if ok else "XX", name, det))
        bad = sum(1 for _, ok, _ in results if not ok)
        print("-- 自测: %d/%d pass" % (len(results) - bad, len(results)))
        return 1 if bad else 0

    r, rows, absent = run_live(args.old, args.new, args.contract)
    ts = datetime.now().astimezone().strftime("%Y-%m-%d %H:%M:%S%z")
    head = subprocess.run(["git", "-C", str(HERE), "rev-parse", "--short", "HEAD"],
                          capture_output=True, text=True).stdout.strip()
    summary = [
        "三向路由核对 @ %s · old=%s new=%s · 仓 HEAD=%s" % (ts, args.old, args.new, head),
        "契约声称 %d 条(应 33=A3+B22+C6+D2) · 禁列锚+启发式探测 %d 条 · 探测全集 %d 路由" %
        (r["claims_total"], len(absent), r["probe_set_size"]),
        "",
        "[drift 契约有·老服务无] %d: %s" % (len(r["drift"]), r["drift"] or "无"),
        "[unrecorded 老服务有·契约无(业务面)] %d: %s" % (len(r["unrecorded"]), r["unrecorded"] or "无"),
        "[unimplemented 老服务有·新服务无] %d: %s" % (
            len(r["unimplemented"]),
            "\n    " + "\n    ".join(map(str, r["unimplemented"])) if r["unimplemented"] else "无"),
        "[extra_impl 新服务有·老服务无] %d: %s" % (len(r["extra_impl"]), r["extra_impl"] or "无"),
        "[未实现·builtin 面(FastAPI docs 家族,非 33 缺口)] %d: %s" % (
            len(r["unimpl_builtin"]), r["unimpl_builtin"] or "无"),
        "[⚠️ 不定（连接失败/探测不可达）] %d: %s" % (
            len(r["indeterminate"]), r["indeterminate"][:12] or "无"),
        "[禁列复活(CR-12 多实现)] %d: %s" % (
            len(r["anchor_extra"]),
            "\n    " + "\n    ".join("%s old=%s new=%s" % t for t in r["anchor_status"])
            if r["anchor_extra"] else "无"),
    ]
    print("\n".join(summary))
    if args.report:
        st_out = []
        for name, ok, det in selftest():
            st_out.append("  [%s] %s  %s" % ("OK" if ok else "XX", name, det))
        rep = ["# route-matrix-report · 三向端点核对（契约/老服务/新服务）", "",
               "> 生成 %s · 仓 HEAD `%s` · old=`%s` new=`%s`" % (ts, head, args.old, args.new),
               "> 复现：`python3 scripts/conformance/route_matrix.py --selftest && "
               "python3 scripts/conformance/route_matrix.py --report <本文件>`",
               "> 探针纪律：全匿名 + 最小 body + 网关端点用 `\"{bad\"` 本地拒绝形态",
               "> （零副作用零上游；404 细分 Not Found=路由不存在 vs 业务 404=路由存在）。",
               "", "## 0. 自测（合成三向差异 → 四类判定能力，原始输出）", "",
               "```", *st_out, "```", "",
               "## 1. 四类判定汇总（实测快照）", ""]
        if args.note:
            rep += ["> **快照语境**：%s" % args.note]
        rep += ["- " + l if not l.startswith("[") or not ("：" in l or ": " in l)
                else "**" + l.split("]")[0] + "]**" + l.split("]", 1)[1] for l in summary[3:]]
        rep += ["", "## 2. 逐条覆盖矩阵（契约 33 端点 × 两侧实测状态码）", "",
                "| # | 方法 | 路径 | 老服务(9120) | 新服务 | 判定 |",
                "|---|---|---|---|---|---|"]
        for i, m, tp, ov, nv in rows:
            eo, oc, _ = ov
            en, nc, _ = nv
            if en is None:
                verdict = "⚠️ 不定（C 侧此刻不可达，非定罪）"
            elif en is True:
                verdict = "✅ 到位(%s)" % nc
            else:
                verdict = "⬜ 未实现"
            rep.append("| %s | %s | `%s` | %s%s | %s%s | %s |" %
                       (i, m, tp,
                        oc if oc is not None else "✗探",
                        "" if eo else "",
                        nc if nc is not None else ("✗不定" if en is None else "✗"),
                        "", verdict))
        Path(args.report).write_text("\n".join(rep) + "\n", encoding="utf-8")
        print("report →", args.report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
