#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""t19 · 静态资源头部/条件请求探针（ST-static-304 的【零引擎改动】替代方案）。

captain 两次裁定不给 harness 加 capture 引擎能力（1372 行已验证裁判的风险/收益）——
本脚本不碰裁判，独立回答「304 覆盖是否缺失」这个问题：

  1) 每个静态资源 GET → 记录 ETag 存在、Cache-Control 缺席（与页面 no-store 相反）；
  2) 拿自身 ETag 发 If-None-Match 条件请求 → 断言返回 **304**（条件请求语义成立）；
  3) 拿错误 ETag 发条件请求 → 断言返回 **200**（非盲应 304——防"见头就 304"的假实现）；
  4) 两侧都跑（--old/--new）：只比【行为模式】(200/300/404 序列一致)，
     不比 ETag 值——stat 族 ETag 跨服务必不同值（t9 已证 ETag≠md5(content)），
     这正是它进不了 harness headers_compared 的原因，也是本探针存在的意义。

用法：
  python3 static_probe.py                          # 仅老服务（基线自检）
  python3 static_probe.py --new http://127.0.0.1:19120   # 切换验收期：两侧行为矩阵
退出码：任一断言不成立 → 1（可作为 cutover 检查项挂进 checklist 的静态面）。
"""
import argparse
import sys
import urllib.error
import urllib.request

RES = ["/static/css/app.css", "/static/js/auth.js",
       "/static/js/header.js", "/static/js/toast.js"]
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def get(url, headers=None):
    req = urllib.request.Request(url, headers=headers or {})
    try:
        with OPENER.open(req, timeout=10) as r:
            return r.status, dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers or {})
    except Exception as e:  # noqa: BLE001
        return None, {"transport": type(e).__name__}


def probe_base(label, base):
    ok_all = True
    print("── %s (%s) ──" % (label, base))
    results = {}
    for path in RES:
        st, h = get(base + path)
        etag = h.get("ETag") or h.get("etag")
        cc = h.get("Cache-Control") or h.get("cache-control")
        st304, _ = get(base + path, {"If-None-Match": etag or "absent"})
        st_wrong, _ = get(base + path, {"If-None-Match": '"not-a-real-etag-zzz"'})
        checks = [
            ("GET 200", st == 200),
            ("ETag 存在", bool(etag)),
            ("无 Cache-Control", not cc),
            ("If-None-Match(自身etag) → 304", st304 == 304),
            ("If-None-Match(错误etag) → 200（非盲 304）", st_wrong == 200),
        ]
        marks = " | ".join("%s%s" % (n, "✓" if o else "✗") for n, o in checks)
        print("  %-24s %s" % (path, marks))
        ok = all(o for _, o in checks)
        results[path] = (st304, st_wrong, ok)
        ok_all = ok_all and ok
    return results, ok_all


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--old", default="http://127.0.0.1:9120")
    ap.add_argument("--new", default=None)
    args = ap.parse_args(argv)
    ro, oko = probe_base("老服务", args.old)
    ok = oko
    if args.new:
        rn, okn = probe_base("新服务", args.new)
        ok = ok and okn
        print("── 跨侧【行为模式】一致性（不比 ETag 值——stat 族必不同）──")
        for p in RES:
            same = ro[p][:2] == rn[p][:2]
            print("  %-24s 模式 old=%s new=%s → %s" %
                  (p, ro[p][:2], rn[p][:2], "一致✓" if same else "不一致✗"))
            ok = ok and same
    print("== static_probe:", "全部断言成立 ✓" if ok else "存在失败 ✗ ==")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
