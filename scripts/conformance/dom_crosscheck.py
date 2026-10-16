#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""t10 · DOM 结构口径交叉核对（Liche）。

三方对账，把「captain 模板基线」与「live 属性口径」「live 正则口径」逐名归因：
  A. baseline：docs/contract/frontend-baseline/*.structure.md 的 id/class/data/资源清单
     （captain 从老仓 frontend/admin/*.html 【模板源文件】正则提取——含 <script> 里
      JS 模板字符串的 `id="${a.id}"` 这类【非标签属性】片段）
  B. live-attr：本验证器 extract_dom 的真实 HTML 标签属性口径（对拍/验收的规范口径
     ——t9 交付物是 HTTP 响应，ST-DOM 比对的就是这个口径）
  C. live-regex：对 live 响应【原文】直接正则 `id="..."`/`class="..."`/`data-xx=`
     ——与 captain 同法，但对象是渲染后的响应（含 <script> 文本）

归因规则：
  A−B（baseline 有、真实标签没有）→ 必须能在 C 找到（模板文本里确实存在，只是位于
    <script>/JS 模板串，不是标签属性）→ 归因为「JS/动态模板片段」，这些片段由
    ST-DOM 的【text 等价】断言守卫（改一个字符就报 diff）。
  A−C（baseline 有、渲染响应里根本没有）→ 真未归因差异 → 计 bad，人工核查。
  B−A（真实标签有、baseline 没列）→ 同样计 bad（baseline 漏记）。
资源引用：baseline 写 `?v={{APP_VERSION}}`，渲染后为 `?v=2.5.1`；比对时先替换。

用法：python3 dom_crosscheck.py [--base URL]
输出：逐页三方汇总表 + 明细归因 + 汇总结论。退出码 0=全可归因，1=有未归因差异。
"""
import argparse
import re
import sys
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import extract_dom, dom_resources  # noqa: E402

HERE = Path(__file__).resolve().parent
PAGES = {  # name -> (live path, baseline file stem)
    "login": ("/admin/login", "login"),
    "accounts": ("/admin/accounts", "accounts"),
    "monitoring": ("/admin/monitoring", "monitoring"),
    "settings": ("/admin/settings", "settings"),
}
RX_ID = re.compile(r'id="([^"]*)"')
RX_CLS = re.compile(r'class="([^"]*)"')
RX_DATA = re.compile(r'\b(data-[a-z0-9-]+)=')
RX_RES = re.compile(r'(?:href|src)="([^"]+)"')


def find_baseline_dir():
    """向上找 docs/contract/frontend-baseline（主仓与 worktree 通用）。"""
    d = HERE
    for _ in range(6):
        d = d.parent
        cand = d / "docs" / "contract" / "frontend-baseline"
        if cand.is_dir():
            return cand
    raise SystemExit("frontend-baseline 目录未找到")


def parse_structure_md(path: Path):
    """按 `## 标题` 切块，抽取 id/class/data/资源/可见文本 清单。"""
    txt = path.read_text(encoding="utf-8")
    out = {"ids": set(), "classes": set(), "data": set(), "resources": set(),
           "texts": []}
    for sec in re.split(r"^## ", txt, flags=re.M):
        if not sec:
            continue
        head, _, rest = sec.partition("\n")
        code = "\n".join(re.findall(r"```\n(.*?)```", rest, flags=re.S))
        h = head.lower()
        if "id" in h and "保持" in head:
            out["ids"] = set(code.split())
        elif "class" in h:
            out["classes"] = {c for tok in code.split() for c in tok.split()}
        elif "data" in h:
            out["data"] = set(code.split())
        elif "资源" in head:
            out["resources"] = set(code.split())
        elif "可见文本" in head:
            out["texts"] = [ln for ln in code.splitlines() if ln.strip()]
    return out


def live_fetch(base, path):
    r = urllib.request.Request(base + path)
    op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with op.open(r, timeout=20) as resp:
        return resp.read().decode("utf-8", "replace")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:9120")
    args = ap.parse_args()
    bad_total = 0
    bl_dir = find_baseline_dir()
    print("baseline 目录:", bl_dir)
    for page, (path, stem) in PAGES.items():
        sp = bl_dir / ("%s.structure.md" % stem)
        bl = parse_structure_md(sp)
        body = live_fetch(args.base, path)
        nodes = extract_dom(body)
        attr_ids = {n["id"] for n in nodes if n["id"]}
        attr_cls = {c for n in nodes for c in n["classes"]}
        attr_data = {k for n in nodes for k in n["data"]}
        rx_ids = set(RX_ID.findall(body))
        rx_cls = {c for m in RX_CLS.findall(body) for c in m.split()}
        rx_data = set(RX_DATA.findall(body))
        rx_res = set(RX_RES.findall(body))
        bl_res = {u.replace("{{APP_VERSION}}", "2.5.1") for u in bl["resources"]}

        print("═══ %s (%s) ═══" % (page, path))
        print("  维度        baseline  live-attr  live-regex   归因列")
        for dim, a, b, c in (
                ("id", bl["ids"], attr_ids, rx_ids),
                ("class", bl["classes"], attr_cls, rx_cls),
                ("data-*", bl["data"], attr_data, rx_data),
                ("资源", bl_res, set(dom_resources(nodes)), rx_res)):
            a_only_c = a - c                  # baseline 有、live 正则无 → 未归因
            a_only_b = a - b                  # baseline 有、真实标签无
            b_only_a = b - a                  # 标签有、baseline 未列 → baseline 漏记
            jsfrag = a_only_b & c             # A−B 但 C 有 → JS/动态模板片段，可归因
            unattr = a_only_c | b_only_a
            if unattr:
                bad_total += len(unattr)
            print("  %-9s %8d %10d %11d   A−B(模板片段)=%d  A−C(未归因)=%d  B−A(漏记)=%d%s"
                  % (dim, len(a), len(b), len(c), len(jsfrag), len(a_only_c),
                     len(b_only_a), "  ⚠️" if unattr else ""))
            if a_only_c:
                print("      ⚠️ baseline有-live无:", sorted(a_only_c)[:8])
            if b_only_a:
                print("      ⚠️ live标签有-baseline未列:", sorted(b_only_a)[:8])
        dyn_ids = sorted((bl["ids"] - attr_ids) & rx_ids)
        if dyn_ids:
            print("  JS/动态 id 片段 %d 个：%s" % (len(dyn_ids), "、".join(dyn_ids[:12])))
            print("  （这些片段位于 <script> 文本 → 由 ST-DOM text 断言逐字守卫）")
        print()
    print("══ 结论：%s ══" % ("全部三方差异可归因 ✓（模板片段由 text 断言守卫，"
                              "属性口径零未归因差异）" if bad_total == 0
                              else "%d 处未归因差异 ⚠️ 需人工核查" % bad_total))
    return 1 if bad_total else 0


if __name__ == "__main__":
    sys.exit(main())
