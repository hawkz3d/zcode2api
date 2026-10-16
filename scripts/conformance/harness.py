#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
zhipucode2api 对拍验证 harness —— 交付「可信的裁判」，不是运动员。

对同一组 HTTP 请求（cases.json 定义）分别打【baseline=老 Python 服务】与
【candidate=新 Go 服务】，按三级标准判定契约等价：

  L1  状态码一致（含 transport 失败类别一致）。
  L2  结构一致：在【原始响应】上逐位置比对 JSON 类型树 —— 对象键集合、
      数组逐下标、标量类型（null/bool/int/float/str）。键缺失、多键、
      bool vs str、int vs float、数组长度差异必须在 L2 暴露。
      ⚠ 不用「整体键集合」粗算：6 个账号里 1 个 enabled 类型不同
        （真实脏数据 laohao-bigmodel-2420b3c2: str "True" vs bool true），
        只有逐位置类型树才能抓到。
  L3  值严格一致：在【归一化后的响应】上做 canonical JSON 逐字节比较，
      并输出逐路径差异明细。

为什么 L2 用原始数据、L3 才用归一化数据：归一化永远不能掩盖键集合与
类型差异（那是契约的结构面）；归一化只处理「同一服务连续两次调用也不
可能相同」的叶子值（时钟/耗时/随机 id/轮询游标）与数组顺序。

安全红线：
  - 密钥从 --env-file（默认 zcode2api/.env）读入内存，占位符 ${KEY}
    在 case 定义中引用；报告落盘前对任何等于 env 值的字符串做反向
    脱敏（替换回 ${KEY}），密钥绝不进入 report.json / report.md。
  - 默认只跑 tags 含 "safe" 的端点（只读/校验层负例/404 漂移锚），
    对真实上游有流量或有写副作用的 case 必须显式 --include-tag 才跑。

用法：
  # 自测（裁判对同一个服务对拍自身，验证零假阳性）
  python3 harness.py --baseline http://127.0.0.1:9120 --engine-selftest
  # 正式对拍（Go 候选就绪后）
  python3 harness.py --baseline http://127.0.0.1:9120 \
      --candidate http://127.0.0.1:9121 --out-dir reports/$(date +%F)
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
import re
import socket
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent

# ---------------------------------------------------------------------------
# .env 与占位符
# ---------------------------------------------------------------------------

def load_dotenv(path: Path) -> dict:
    env = {}
    if not path or not path.is_file():
        return env
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, _, v = line.partition("=")
        k, v = k.strip(), v.strip().strip('"').strip("'")
        if k:
            env[k] = v
    return env


def expand(value, env: dict):
    """递归替换字符串中的 ${KEY} 占位符（值不落盘、不打印）。
    env 缺键时【保留原占位符】——配合 execute 的发送前守卫硬 FAIL 整个 case；
    绝不吞成空串：'Bearer '（空凭证）会让两侧一致地 401 而假绿
    （worktree env 漏配事故的治本点）。"""
    if isinstance(value, str):
        return re.sub(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}",
                      lambda m: env.get(m.group(1), m.group(0)), value)
    if isinstance(value, dict):
        return {k: expand(v, env) for k, v in value.items()}
    if isinstance(value, list):
        return [expand(v, env) for v in value]
    return value


def redact(text, env: dict):
    """把任何等于 env 值的子串换成占位符 —— 报告落盘前的最后一道闸。"""
    if not isinstance(text, str):
        return text
    for name, secret in env.items():
        if secret and len(secret) >= 6 and secret in text:
            text = text.replace(secret, "${%s}" % name)
    return text


def redact_tree(node, env: dict):
    if isinstance(node, str):
        return redact(node, env)
    if isinstance(node, dict):
        return {k: redact_tree(v, env) for k, v in node.items()}
    if isinstance(node, list):
        return [redact_tree(v, env) for v in node]
    return node


# ---------------------------------------------------------------------------
# 基础工具
# ---------------------------------------------------------------------------

def type_name(v):
    if v is None:
        return "null"
    if isinstance(v, bool):          # bool 是 int 子类，必须先判
        return "bool"
    if isinstance(v, (int,)):
        return "int"
    if isinstance(v, float):
        return "float"
    if isinstance(v, str):
        return "str"
    if isinstance(v, list):
        return "array"
    if isinstance(v, dict):
        return "object"
    return type(v).__name__


def canonical_json(obj) -> str:
    return json.dumps(obj, sort_keys=True, ensure_ascii=False,
                      separators=(",", ":"))


def path_segs(path: str):
    """'$.accounts[2].enabled' -> ['$', 'accounts', '2']（数组下标不带括号语义）。"""
    toks = re.findall(r"(\$)|\.([^.\[\]]+)|\[(\d+|\*)\]", path)
    segs = []
    for dollar, key, idx in toks:
        if dollar:
            segs.append("$")
        elif key:
            segs.append(key)
        else:
            segs.append(idx)
    return segs


def match_segs(pattern_segs, actual_segs) -> bool:
    if len(pattern_segs) != len(actual_segs):
        return False
    return all(p == "*" or p == a for p, a in zip(pattern_segs, actual_segs))


# ---------------------------------------------------------------------------
# 归一化引擎
# ---------------------------------------------------------------------------

class Normalizer:
    """两阶段归一化引擎。规则类型（互斥）：

    —— 结构对齐层（pre 模式，作用于 L2 与 L3 的【原始数据】）——
      sort_by: 对匹配位置的数组按元素某字段的原始值做稳定排序。
        设计要点：排序必须发生在值归一化【之前】，用唯一锚（id/req_id/at）
        保证 baseline 与 candidate 两侧排序结果确定、可对齐；
        数组「顺序不是契约、集合+逐元素字段才是契约」时才可用此规则。

    —— 值归一化层（values 模式，只作用于 L3）——
      replace: 把匹配路径的值整体替换为占位符（键与类型守卫在 L2 原始数据上完成）
      regex:   对匹配路径的字符串值做 re.sub（只抹掉串内易变子式，如 logid）
      secret:  值替换为 <secret:sha256:前8位> —— 密钥类字段绝不允许以明文
               进报告，但哈希指纹仍保留【等值判定能力】：两侧指纹不同即值不同
               （真差异照样 FAIL），指纹相同即一致（不因脱敏而假过）。

    规则永远不删除键、不改变键集合 —— 键集合与类型差异永远由 L2 暴露。
    """

    def __init__(self, rules: list):
        self.pre_rules = []
        self.value_rules = []
        for r in rules:
            if not (isinstance(r, dict) and r.get("path")):
                continue
            pair = (path_segs(r["path"]), r)
            if "sort_by" in r or r.get("replace_typed"):
                self.pre_rules.append(pair)
            else:
                self.value_rules.append(pair)

    def _rules_for(self, rules, actual_segs):
        return [r for pat, r in rules if match_segs(pat, actual_segs)]

    def _walk(self, node, actual_segs, trace, mode):
        rules = self.pre_rules if mode == "pre" else self.value_rules
        for rule in self._rules_for(rules, actual_segs):
            if mode == "pre":
                if rule.get("replace_typed"):
                    trace.append({"rule": rule["path"], "stage": "pre-collapse-typed",
                                  "at": "$" + "".join(
                                      f"[{s}]" if s.isdigit() else f".{s}"
                                      for s in actual_segs[1:]),
                                  "kept_type": type_name(node),
                                  "why": rule.get("why", "")})
                    return {"array": [], "object": {}, "str": "",
                            "int": 0, "float": 0.0, "bool": True,
                            "null": None}[type_name(node)]
                if "sort_by" in rule and isinstance(node, list):
                    key = rule["sort_by"]
                    try:
                        out = sorted(node, key=lambda it: canonical_json(
                            it.get(key) if isinstance(it, dict) else it))
                        trace.append({"rule": rule["path"], "stage": "pre-sort",
                                      "by": key, "n": len(out),
                                      "why": rule.get("why", "")})
                        node = out
                    except TypeError:
                        pass
            else:
                if rule.get("secret"):
                    fp = hashlib.sha256(
                        json.dumps(node, ensure_ascii=False, sort_keys=True)
                        .encode("utf-8")).hexdigest()[:8]
                    trace.append({"rule": rule["path"], "stage": "secret-fingerprint",
                                  "at": "$" + "".join(
                                      f"[{s}]" if s.isdigit() else f".{s}"
                                      for s in actual_segs[1:]),
                                  "from_type": type_name(node),
                                  "why": rule.get("why", "")})
                    return "<secret:%s>" % fp
                if "replace" in rule:
                    trace.append({"rule": rule["path"], "stage": "value",
                                  "at": "$" + "".join(
                                      f"[{s}]" if s.isdigit() else f".{s}"
                                      for s in actual_segs[1:]),
                                  "from_type": type_name(node),
                                  "to": rule["replace"],
                                  "why": rule.get("why", "")})
                    return rule["replace"]
                if "regex" in rule and isinstance(node, str):
                    new = re.sub(rule["regex"], rule.get("with", "<redacted>"), node)
                    if new != node:
                        trace.append({"rule": rule["path"], "stage": "value-regex",
                                      "regex": rule["regex"],
                                      "at": "$" + "".join(
                                          f"[{s}]" if s.isdigit() else f".{s}"
                                          for s in actual_segs[1:]),
                                      "why": rule.get("why", "")})
                    return new
        if isinstance(node, dict):
            return {k: self._walk(v, actual_segs + [k], trace, mode)
                    for k, v in node.items()}
        if isinstance(node, list):
            return [self._walk(v, actual_segs + [str(i)], trace, mode)
                    for i, v in enumerate(node)]
        return node

    def align(self, body):
        """结构对齐（pre-sort），L2 与 L3 都先过这一层。"""
        trace = []
        return self._walk(copy.deepcopy(body), ["$"], trace, "pre"), trace

    def normalize(self, body):
        """完整管线：先对齐再值归一化。返回 (结果, 合并 trace)。"""
        aligned, tr1 = self.align(body)
        tr2 = []
        out = self._walk(aligned, ["$"], tr2, "values")
        return out, tr1 + tr2


# ---------------------------------------------------------------------------
# 敏感值硬防线（sentinel）—— captain 2026-10-15 指令
# ---------------------------------------------------------------------------

# 字段名形态（大小写不敏感、子串匹配 dict 键名）：命中即对该位置的
# 【字符串值】做 sha256 指纹脱敏。
SENTINEL_NAME_RX = re.compile(
    r"(admin_key|gateway_key|api_?key|jwt_?token|access_?token|refresh_?token"
    r"|client_?secret|client_?key|secret|password|passwd|credential"
    r"|verify_param|authorization|cookie|token)", re.I)

# 值形态：与字段名无关，字符串值本身像密钥就拦。
# （captain 点名：32hex、32hex.16char、JWT 三段式；另加通用 api-key 形态。
#   bearer-jwt-embedded 用 search：允许嵌在错误文本里的 JWT 也被拦。）
SENTINEL_VALUE_FORMS = [
    ("hex32", re.compile(r"^[0-9a-fA-F]{32}$"), True),
    ("hex32.16al", re.compile(r"^[0-9a-fA-F]{32}\.[0-9a-zA-Z_\-]{12,24}$"), True),
    ("hex32.16al-x3", re.compile(r"^[0-9a-fA-F]{32}\.[0-9a-zA-Z_\-]{12,24}\.[0-9a-zA-Z_.\-]{4,}$"), True),
    ("jwt-3seg", re.compile(r"^eyJ[A-Za-z0-9_\-]{4,}\.[A-Za-z0-9_\-]{4,}\.[A-Za-z0-9_\-]{4,}$"), True),
    ("sk-prefixed", re.compile(r"^sk\-[A-Za-z0-9_\-]{12,}$"), True),
    ("jwt-in-text", re.compile(r"eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}"), False),
]


class Sentinel:
    """对拍前【最外层】敏感值闸门：响应体先过这里，再进 L2/L3/报告。

    默认全拦（硬防线不靠“记得设计脱敏”）：
      - 字段名命中且值为字符串 → 替换为 <secret:sha256前8>。两侧同值→同指纹，
        【等值判定能力保留】：真不一致照样 FAIL，明文零落盘。
      - 任意字符串命中密钥形态（32hex / 32hex.16al / JWT 三段式 / sk- 前缀 /
        文本内嵌 JWT）→ 同样指纹化。
      - 字段名命中但值非字符串（bool/int/null/object）→ 不动值——改成字符串
        会污染 L2 类型树制造假差异；事件记 action="logged-only" 进报告，
        绝不静默。
      - safe_paths 白名单：显式声明安全的精确路径豁免（收窄形态误伤，例如
        确认无害的 id 恰为 32hex）；白名单只能【豁免已知安全】，
        不是放开防线——不在名单内的一切照常拦。
    """

    def __init__(self, config: dict | None = None):
        cfg = config or {}
        self.name_rx = re.compile(cfg["field_name_regex"], re.I) \
            if cfg.get("field_name_regex") else SENTINEL_NAME_RX
        if cfg.get("value_forms"):
            self.value_forms = [(n, re.compile(rx), anchored)
                                for n, rx, anchored in cfg["value_forms"]]
        else:
            self.value_forms = list(SENTINEL_VALUE_FORMS)
        self.safe_segs = [(path_segs(p), p) for p in cfg.get("safe_paths", [])]

    def _safe(self, segs) -> bool:
        return any(match_segs(pat, segs) for pat, _ in self.safe_segs)

    @staticmethod
    def _fp(v):
        return "<secret:%s>" % hashlib.sha256(
            json.dumps(v, ensure_ascii=False, sort_keys=True).encode("utf-8")
        ).hexdigest()[:8]

    @staticmethod
    def _path(segs):
        return "$" + "".join(f"[{s}]" if s.isdigit() else f".{s}" for s in segs[1:])

    def mask(self, node, segs=None, events=None):
        segs = segs if segs is not None else ["$"]
        events = events if events is not None else []
        if isinstance(node, dict):
            out = {}
            for k, v in node.items():
                ks = segs + [str(k)]
                if not self._safe(ks) and self.name_rx.search(str(k)):
                    if isinstance(v, str) and v != "":
                        events.append({"path": self._path(ks),
                                       "detector": "field-name", "match": str(k),
                                       "action": "fingerprinted",
                                       "fingerprint": self._fp(v)})
                        out[k] = self._fp(v)
                        continue
                    if v is not None and not isinstance(v, (str, dict, list)):
                        events.append({"path": self._path(ks),
                                       "detector": "field-name", "match": str(k),
                                       "action": "logged-only(non-string)",
                                       "type": type_name(v)})
                    elif isinstance(v, str):
                        events.append({"path": self._path(ks),
                                       "detector": "field-name", "match": str(k),
                                       "action": "fingerprinted",
                                       "fingerprint": self._fp(v)})
                        out[k] = self._fp(v)
                        continue
                out[k] = self.mask(v, ks, events)
            return out
        if isinstance(node, list):
            return [self.mask(v, segs + [str(i)], events)
                    for i, v in enumerate(node)]
        if isinstance(node, str) and node and not self._safe(segs):
            for fname, frx, anchored in self.value_forms:
                hit = frx.fullmatch(node) if anchored else frx.search(node)
                if hit:
                    events.append({"path": self._path(segs),
                                   "detector": "value-form", "match": fname,
                                   "action": "fingerprinted",
                                   "fingerprint": self._fp(node)})
                    return self._fp(node)
        return node

    def scan(self, body):
        ev = []
        return self.mask(body, ["$"], ev), ev


# ---------------------------------------------------------------------------
# HTML DOM 结构等价（ADR-006 选项 C 的替代验证器 —— case kind="html-dom"）
# ---------------------------------------------------------------------------
#
# 判据分层：
#   ST-DOM  🔴 必须 PASS —— 元素树按文档序逐位置比对
#           tag + id + class 集合 + data-* 全字典 + 归一化直接文本（含
#           <script>/<style> 的 JS/CSS 源码文本——JS 模板里的 ${a.id} 等
#           属"文本必须等价"面，t9 重写不得改动行为脚本内容）
#           + 资源引用清单（src/href 全集：路径+版本查询串+域名）
#   L3      降级为 observed —— 同源自测仍记录；重写后允许字节漂移，
#           不参与 verdict（expect 不含 L3 即不判）。
# 天然忽略（ADR-006"允许差异"清单）：空白/缩进/换行、注释、属性顺序。
# 豁免联动：带 requires_check="html-dom-equivalence" 的豁免条目 ⇒
#   ST-DOM 不 PASS 则整体强制 FAIL——【豁免≠放行】，机制化防"登记了
#   豁免就万事大吉"（ADR-006 原文警告）。

from html.parser import HTMLParser  # noqa: E402  (stdlib，零新依赖)

VOID_TAGS = {"area", "base", "br", "col", "embed", "hr", "img", "input",
             "link", "meta", "param", "source", "track", "wbr"}


class _DomBuilder(HTMLParser):
    """把 HTML 解析成 pre-order 元素列表。每元素:
       {tag, path, depth, id, classes(list,保序), data(dict), resources,
        text(归一化直接文本)}
    path = 祖先链 tag+同名兄弟序号，如 html[0]>body[0]>div[2]>span[0]。
    容忍不严格 HTML：endtag 不匹配时回退弹到最近的同名开标签。"""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.nodes = []
        self._stack = []        # [(tag, sibling_index, node_idx)]
        self._sibs = [{}]       # 每层 {tag: count}
        self._text = {}         # node_idx -> [chunks]

    def _cur_parent_idx(self):
        return self._stack[-1][2] if self._stack else None

    def _path(self, tag, idx):
        chain = [f"{t}[{s}]" for t, s, _ in self._stack]
        chain.append(f"{tag}[{idx}]")
        return ">".join(chain)

    def _open(self, tag, attrs, selfclosing=False):
        depth = len(self._stack)
        if depth >= len(self._sibs):
            self._sibs.append({})
        cnt = self._sibs[depth].get(tag, -1) + 1
        self._sibs[depth][tag] = cnt
        del self._sibs[depth + 1:]
        while len(self._sibs) < depth + 2:
            self._sibs.append({})
        d = {}
        classes = []
        data = {}
        resources = []
        for k, v in attrs:
            k = (k or "").lower()
            v = v if v is not None else ""
            if k == "id":
                d["id"] = v
            elif k == "class":
                classes = v.split()
            elif k.startswith("data-"):
                data[k] = v
            elif k in ("href", "src") and v:
                resources.append(v)
        node = {"tag": tag, "path": self._path(tag, cnt), "depth": depth,
                "id": d.get("id", ""), "classes": classes,
                "data": data, "resources": resources, "_idx": len(self.nodes)}
        self.nodes.append(node)
        self._text[node["_idx"]] = []
        if not selfclosing and tag not in VOID_TAGS:
            self._stack.append((tag, cnt, node["_idx"]))
            if depth + 1 < len(self._sibs):
                self._sibs[depth + 1] = {}
            else:
                self._sibs.append({})
        return node

    def handle_starttag(self, tag, attrs):
        self._open(tag, attrs)

    def handle_startendtag(self, tag, attrs):
        self._open(tag, attrs, selfclosing=True)

    def handle_endtag(self, tag):
        for i in range(len(self._stack) - 1, -1, -1):
            if self._stack[i][0] == tag:
                del self._stack[i:]
                del self._sibs[i + 1:]
                return
        # 无匹配的孤立 endtag：忽略（浏览器也这么做）

    def handle_data(self, data):
        idx = self._cur_parent_idx()
        if idx is None:
            idx = -1  # 顶层游离文本（极少见），-1 作丢弃桶
        self._text.setdefault(idx, []).append(data)

    def result(self):
        for n in self.nodes:
            chunks = [c.strip() for c in self._text.get(n["_idx"], [])]
            n["text"] = " ".join(c for c in chunks if c)
        return self.nodes


def extract_dom(html_text: str):
    p = _DomBuilder()
    p.feed(html_text)
    p.close()
    return p.result()


def dom_resources(nodes):
    """资源引用全集（src/href），排序去重。
    含版本查询串与域名——ADR-006 第 5/6 条不变量的独立断言。"""
    return sorted({u for n in nodes for u in n.get("resources", [])})


def _clip_text(t):
    return t if len(t) <= 160 else t[:160] + f"…(+{len(t) - 160})"


def diff_dom(bnodes, cnodes, limit=200):
    """文档序逐位置比对。返回差异列表（kind: node-count / tag / id /
    class-missing / class-extra / data / text / resource-missing /
    resource-extra / *-extra-node）。"""
    diffs = []
    nb, nc = len(bnodes), len(cnodes)
    if nb != nc:
        diffs.append({"path": "$dom", "kind": "node-count",
                      "baseline": nb, "candidate": nc})
    for i in range(min(nb, nc)):
        b, c = bnodes[i], cnodes[i]
        p = b["path"]
        if len(diffs) >= limit:
            break
        if b["tag"] != c["tag"]:
            diffs.append({"path": p, "kind": "tag",
                          "baseline": b["tag"], "candidate": c["tag"]})
        if b["id"] != c["id"]:
            diffs.append({"path": p, "kind": "id",
                          "baseline": b["id"] or None, "candidate": c["id"] or None})
        mb = set(b["classes"]) - set(c["classes"])
        mc = set(c["classes"]) - set(b["classes"])
        if mb:
            diffs.append({"path": p, "kind": "class-missing",
                          "baseline": sorted(mb), "candidate": None})
        if mc:
            diffs.append({"path": p, "kind": "class-extra",
                          "baseline": None, "candidate": sorted(mc)})
        if b["data"] != c["data"]:
            diffs.append({"path": p, "kind": "data",
                          "baseline": b["data"] or None, "candidate": c["data"] or None})
        if b["text"] != c["text"]:
            diffs.append({"path": p, "kind": "text",
                          "baseline": _clip_text(b["text"]),
                          "candidate": _clip_text(c["text"])})
    for i in range(min(nb, nc), max(nb, nc)):
        side, lst = (("baseline", bnodes) if nb > nc else ("candidate", cnodes))
        node = lst[i]
        diffs.append({"path": node["path"], "kind": side + "-extra-node",
                      "baseline": node["tag"] if side == "baseline" else None,
                      "candidate": node["tag"] if side == "candidate" else None})
    rb, rc = dom_resources(bnodes), dom_resources(cnodes)
    for u in sorted(set(rb) - set(rc)):
        diffs.append({"path": "$resources", "kind": "resource-missing",
                      "baseline": u, "candidate": None})
    for u in sorted(set(rc) - set(rb)):
        diffs.append({"path": "$resources", "kind": "resource-extra",
                      "baseline": None, "candidate": u})
    return diffs


def requires_check_gate(case_id, final_res, allowlist):
    """豁免≠放行的机制化：任何带 requires_check=html-dom-equivalence 且匹配本
    case 的豁免条目 ⇒ ST-DOM 必须 PASS，否则整体强制 FAIL。
    返回 (blocked: bool, why: str)。"""
    for a in allowlist:
        if not a.get("requires_check"):
            continue
        if not _case_match(case_id, a.get("case")):
            continue
        if a["requires_check"] == "html-dom-equivalence":
            if final_res.get("ST-DOM", {}).get("pass") is not True:
                return True, ("allowlist '%s' requires_check=html-dom-equivalence "
                              "but ST-DOM not PASS → exemption does NOT release"
                              % a.get("id", "?"))
    return False, ""


# ---------------------------------------------------------------------------
# ORDER 断言（CR-35：顺序【是】持久契约 —— 位置对齐归一化之外单独定罪）
# ---------------------------------------------------------------------------
# 背景：normalize.rules.json 的 $.accounts sort_by:id 只做【位置对齐】
# （字段差异不错位到别的账号头上），但【两侧是否同序】本身是用户可见
# 契约——老服务实测顺序 = (provider 分组序 zai,bigmodel × 组内 created_at
# 升序)，非 rowid 非 id（CR-35 完整证据）。若 candidate 排序语义不同
# （如 Go store.go 现用 ORDER BY rowid），pre-sort 会把顺序漂移洗成
# L2/L3 全绿 → 【对拍绿 ≠ 契约等价】。ORDER 断言在归一化之前、对两侧
# 原始解析数组逐位比 key 序列，专门堵这个盲区。
# 对拍语义：baseline 恒为老服务 → ORDER(baseline,candidate) 即
# 「candidate 顺序 == 老服务实测顺序」的单侧锚定断言（比硬编码 id 列表
# 抗账号池增删；引擎自测 order-* 三项证明机制非永真摆设）。

def order_check(raw_b, raw_c, spec):
    """ORDER 断言两模式（CR-35/CR-37）：

    A. 相等模式 spec={"path","by"}：两侧【原始】数组 key 序列逐位相等。
       语义=「候选顺序 == 老服务实测顺序」（baseline 恒为老服务 → 单侧锚定，
       动态、抗账号池增删；AD-accounts 用此）。

    B. 方向模式 spec={"path","by","direction":"desc"|"asc"}：两侧【各自独立】
       断言单调（非严格比较——相等项不判，方向才是契约）。
       语义=「顺序方向是持久契约而内容是进程态」的场景（monitoring.entries：
       两服务流量史必然不同，相等无意义；ts 降序=新在前才是 CR-37 契约）。
       空数组 → 该侧 None（无样本，不定罪也不放行）；聚合：任一 False→False，
       无 False 且非全 True → None（detail 说明），全证据成立才 True。
       expect 含 ORDER 时 None 计入 unmet → fail——CR-37 陷阱「clear 后无样本」
       必须显式暴露，绝不静默 PASS。

    任何一侧非数组/缺路径/元素缺 by 键 → fail（缺证据不判过）。
    语境提醒：self_mode（baseline==candidate）下 PASS 只证明引擎自洽；
    跨服务顺序等价的证据只在 candidate≠baseline 的对拍运行中产生。
    """
    def seq(raw):
        node = raw
        for seg in spec["path"].strip("$.").split("."):
            if not isinstance(node, dict) or seg not in node:
                return None
            node = node[seg]
        if not isinstance(node, list):
            return None
        return [it.get(spec["by"]) if isinstance(it, dict) else None
                for it in node]

    direction = spec.get("direction")

    # ---- 嵌套模式（CR-16b/工作B）："$.accounts[*].recent_results" ----
    # 每个容器元素内的数组【各自】方向断言；短数组(<2)不构成证据跳过；
    # 元素数组为空是合法进程态（新账号无历史）→ 单侧无样本 ≠ 定罪；
    # 任一样本组方向违规 → False。两侧皆零样本 → None（可见 skip）。
    if "[*]" in spec["path"]:
        pre, post = spec["path"].split("[*]", 1)
        def nested_groups(raw):
            cont = path_get(raw, pre.rstrip(".") or "$")
            if isinstance(cont, _Missing) or not isinstance(cont, list):
                return None                     # 容器缺失 → 配置/契约错误
            groups = []
            for el in cont:
                arr = path_get(el, "$" + post) if post else el
                if isinstance(arr, list) and len(arr) >= 2:
                    groups.append([it.get(spec["by"]) if isinstance(it, dict)
                                   else None for it in arr])
            return groups
        gb, gc = nested_groups(raw_b), nested_groups(raw_c)
        if gb is None or gc is None:
            return {"pass": False, "mode": "nested-direction:" + str(direction),
                    "detail": "container path %s missing/not list (b_ok=%s c_ok=%s)"
                              % (spec["path"], gb is not None, gc is not None)}
        def bad(groups):
            for s in groups:
                if any(v is None for v in s):
                    return True
                try:
                    if direction == "desc":
                        if any(a < b for a, b in zip(s, s[1:])):
                            return True
                    else:
                        if any(a > b for a, b in zip(s, s[1:])):
                            return True
                except TypeError:
                    return True
            return False
        any_sample = bool(gb) or bool(gc)
        viol = bad(gb) or bad(gc)
        verdict = False if viol else (True if any_sample else None)
        return {"pass": verdict, "mode": "nested-direction:" + str(direction),
                "groups": [len(gb), len(gc)],
                "no_sample": not any_sample and not viol,
                "detail": (None if verdict is True else
                           "nested direction violated" if viol else
                           "no array with ≥2 items on either side — visibly skipped")}

    sb, sc = seq(raw_b), seq(raw_c)
    if sb is None or sc is None:
        return {"pass": False,
                "detail": "path %s not array/missing (baseline_ok=%s candidate_ok=%s)"
                          % (spec["path"], sb is not None, sc is not None)}
    if not direction:                              # 模式 A：逐位相等
        if sb == sc:
            return {"pass": True, "mode": "equal", "n": len(sb), "seq_head": sb[:8]}
        first = next((i for i in range(min(len(sb), len(sc))) if sb[i] != sc[i]),
                     min(len(sb), len(sc)))
        return {"pass": False, "mode": "equal",
                "detail": {"first_diff_index": first,
                           "baseline_window": sb[first:first + 6],
                           "candidate_window": sc[first:first + 6]},
                "n": [len(sb), len(sc)]}

    def mono(s):                                   # 模式 B：单侧方向断言
        if not s:
            return None                            # 空数组 = 无样本
        if any(v is None for v in s):
            return False                           # 元素缺键，方向不可判
        try:
            if direction == "desc":
                return all(a >= b for a, b in zip(s, s[1:]))
            return all(a <= b for a, b in zip(s, s[1:]))
        except TypeError:
            return False                           # 不可比较类型（如混入 null）
    mb, mc = mono(sb), mono(sc)
    # 聚合语义（CR-37 精化）：
    #   任一 False → False（方向违规定罪）
    #   双侧 None → None + no_sample（环境无样本 → 可见 skip，报告/stdout 标 n/a，
    #              绝不静默当 PASS）
    #   单侧 None → False（对拍时基线有样本而候选无 = 候选未记录/输出空，
    #              本身即契约缺陷证据；反之同理）
    if mb is False or mc is False:
        verdict = False
        detail = "direction violated on a side (see per_side); baseline_head=%s candidate_head=%s" % (sb[:3], sc[:3])
        no_sample = False
    elif mb is None and mc is None:
        verdict, no_sample = None, True
        detail = ("no-sample on BOTH sides (empty reqlog: fresh restart or "
                  "monitoring/clear ran). ORDER dimension visibly skipped (n/a), "
                  "NEVER silently passed — see stdout ORDER:n/a(no-sample)")
    elif mb is None or mc is None:
        verdict, no_sample = False, False
        detail = ("one side has no sample while the other does — in cross-service "
                  "runs this IS a defect evidence (candidate must record gateway "
                  "traffic in same order); baseline_n=%d candidate_n=%d" % (len(sb), len(sc)))
    else:
        verdict, no_sample, detail = True, False, None
    return {"pass": verdict, "mode": "direction:" + direction,
            "per_side": {"baseline": mb, "candidate": mc},
            "n": [len(sb), len(sc)],
            "no_sample": no_sample, "detail": detail}


# ---------------------------------------------------------------------------
# capture / asserts：写序列 case 的步间变量与单侧语义断言（t13 工作A）
# ---------------------------------------------------------------------------
# 为什么需要：写路径端点（B-03 建→B-06 改→B-07 停→B-04 删）的响应里必然
# 携带【服务端生成的随机量】（假账号 id 后缀 8hex、rotate 的 device_mid uuid），
# 两侧各生成各的 → 跨侧 L3 值比对在此天然不成立。对拍的职责退为：
#   ① L1 跨侧一致（状态码是契约）；
#   ② 每步【单侧语义断言】（baseline 的断言跑在 baseline 的响应上，
#      candidate 跑在自己的响应上）——验证"契约规定的副作用真实发生"：
#      创建返回的 id 稍后查得到、停用后 enabled 变 false、删除后查不到。
#      这正是 B-07 双写实测captain定案的模式（"{"ok":true}" 与实际落库一致）
#      的 case 化。
#   ③ capture：步间把 "$.ids[0]" 提出来，后续步的 path/body 里 {{ID}} 插值，
#      两侧各插各自的值。未解析变量 → 整 case 报错 FAIL（不静默跳过，
#      绝不带着 "{{ID}}" 字面量去打服务）。
_VAR_RE = re.compile(r"\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}")


def path_get(obj, path):
    """极简 jsonpath：'$.a.b[0].c' → 值；取不到返回哨兵 _MISSING。"""
    cur = obj
    if not path.startswith("$"):
        return _MISSING
    for seg in re.findall(r"\.([A-Za-z0-9_\-]+)|\[(\d+)\]", path):
        key, idx = seg
        if key:
            if not isinstance(cur, dict) or key not in cur:
                return _MISSING
            cur = cur[key]
        else:
            if not isinstance(cur, list) or int(idx) >= len(cur):
                return _MISSING
            cur = cur[int(idx)]
    return cur


class _Missing:
    def __repr__(self):
        return "<missing>"


_MISSING = _Missing()


def apply_vars(obj, vmap):
    """递归插值字符串里的 {{VAR}}；未定义变量 → ValueError（case 级 FAIL）。"""
    if isinstance(obj, str):
        missing = [v for v in _VAR_RE.findall(obj) if v not in vmap]
        if missing:
            raise ValueError("unresolved capture var(s): %s in %r"
                             % (missing, obj))
        return _VAR_RE.sub(lambda m: str(vmap[m.group(1)]), obj)
    if isinstance(obj, dict):
        return {k: apply_vars(v, vmap) for k, v in obj.items()}
    if isinstance(obj, list):
        return [apply_vars(v, vmap) for v in obj]
    return obj


def do_captures(raw_b, raw_c, spec, vmap_b, vmap_c):
    """spec={VAR: path}；从两侧各自的【原始】响应提取，各存各的。"""
    ev = []
    for var, p in spec.items():
        vb, vc = path_get(raw_b, p), path_get(raw_c, p)
        if isinstance(vb, _Missing) or isinstance(vc, _Missing) \
                or isinstance(vb, (dict, list)) or isinstance(vc, (dict, list)):
            ev.append("capture %s←%s 提取失败(b_ok=%s c_ok=%s 标量=%s)"
                      % (var, p, not isinstance(vb, _Missing),
                         not isinstance(vc, _Missing),
                         not isinstance(vb, (dict, list))))
            continue
        vmap_b[var] = vb
        vmap_c[var] = vc
    if ev:
        ev.append("vmap 快照 b=%s c=%s（供失败归因）"
                  % (sorted(vmap_b), sorted(vmap_c)))
    return ev


def eq_strict(v, want):
    """断言用严格相等：JSON 语义的 bool 与数字【永不互判】——
    Python 裸 == 里 True == 1 为真，candidate 若把契约的 1 实现成 true，
    泛 eq 会跟着假绿（"一致地错"里两侧错法不同却都过谓词的形态）。
    int/float 同值可互判（JSON 的 1 与 1.0 是同一数值，非类型契约面）。"""
    if isinstance(v, bool) != isinstance(want, bool):
        return False
    if isinstance(v, (int, float)) and isinstance(want, (int, float)):
        return v == want
    return type_name(v) == type_name(want) and v == want


def eval_asserts(raw, preds, side, vars=None):
    """单侧断言。返回 fail 列表。谓词：eq/in/re/type/len_eq/
    find_by_id(+field+eq/startswith)/find_by_id_missing/
    find_by_prefix_missing(+unless_shared_source)。
    vars=该侧变量（SAME_SOURCE=1 时 unless_shared_source 谓词如实跳过：
    同源自比两侧必然互见假数据，跳过=承认无证据，不是伪造证据）。"""
    vars = vars or {}
    fails = []
    for pd in preds or []:
        pth = pd.get("path", "$")
        v = path_get(raw, pth)
        label = "%s: %s" % (side, json.dumps(pd, ensure_ascii=False))
        if pd.get("unless_shared_source") and vars.get("SAME_SOURCE") == "1":
            continue
        if isinstance(v, _Missing):
            fails.append(label + " → 路径不存在")
            continue
        ok = True
        # 分派按【特异性优先】：组合谓词(find_by_id+eq)绝不能落进泛 eq 分支
        # （引擎哨兵 assert-find-by-id-catches-missing 正是抓这条的）
        if not pd:
            fails.append(label + " → 空谓词 = 配置错误（不静默放行）")
            continue
        if "find_by_id" in pd:
            if not isinstance(v, list):
                ok = False
            else:
                hit = next((it for it in v if isinstance(it, dict)
                            and it.get("id") == pd["find_by_id"]), None)
                if hit is None:
                    ok = False
                elif "field" in pd:
                    f = hit.get(pd["field"], _MISSING)
                    if f is _MISSING:
                        ok = False
                    elif "eq" in pd:
                        ok = eq_strict(f, pd["eq"])
                    elif "startswith" in pd:
                        ok = isinstance(f, str) and f.startswith(pd["startswith"])
        elif "find_by_id_missing" in pd:
            ok = isinstance(v, list) and not any(
                isinstance(it, dict) and it.get("id") == pd["find_by_id_missing"]
                for it in v)
        elif "find_by_prefix_missing" in pd:
            # 共享数据源污染探测（{{OTHER}} 侧假数据前缀不得出现在本侧列表）
            ok = isinstance(v, list) and not any(
                isinstance(it, dict) and str(it.get("id", "")).startswith(pd["find_by_prefix_missing"])
                for it in v)
        elif "len_eq" in pd:
            # list 长度或 dict 键数（D-02 实跑抓出：$.paths 是 dict，旧版只认
            # list 导致两侧同红——ASSERT 自身也须防假红/假绿）
            ok = isinstance(v, (list, dict)) and len(v) == pd["len_eq"]
        elif "eq" in pd:
            ok = eq_strict(v, pd["eq"])
        elif "in" in pd:
            ok = any(eq_strict(v, c) for c in pd["in"])
        elif "re" in pd:
            ok = isinstance(v, str) and bool(re.search(pd["re"], v))
        elif "startswith" in pd:
            ok = isinstance(v, str) and v.startswith(pd["startswith"])
        elif "type" in pd:
            ok = type_name(v) == pd["type"]
        else:
            ok = False    # 无判定谓词 = 配置错误，不得静默充绿
        if not ok:
            fails.append(label + " → 不成立(实得 %s)" % _clip(v if not isinstance(v, (dict, list)) else type_name(v)))
    return fails


# ---------------------------------------------------------------------------
# 差异引擎
# ---------------------------------------------------------------------------
def diff_structure(a, b, path="$", out=None, limit=400):
    """L2：逐位置类型树 diff（在原始数据上跑）。"""
    if out is None:
        out = []
    if len(out) >= limit:
        return out
    ta, tb = type_name(a), type_name(b)
    if ta != tb:
        d = {"path": path, "kind": "type", "baseline": ta, "candidate": tb}
        # 类型漂移附上标量原值形态，供豁免清单【精确匹配】
        # （ADR-002 的 enabled 豁免只应命中 str "True"→bool true 这一形态，
        #   不得顺手豁免 bool→int 之类从未被批准过的漂移）
        if ta in ("str", "int", "float", "bool", "null"):
            d["baseline_value"] = _clip(a)
        if tb in ("str", "int", "float", "bool", "null"):
            d["candidate_value"] = _clip(b)
        out.append(d)
        return out
    if ta == "object":
        for k in sorted(set(a) - set(b)):
            out.append({"path": f"{path}.{k}", "kind": "missing_in_candidate"})
        for k in sorted(set(b) - set(a)):
            out.append({"path": f"{path}.{k}", "kind": "missing_in_baseline"})
        for k in sorted(set(a) & set(b)):
            diff_structure(a[k], b[k], f"{path}.{k}", out, limit)
    elif ta == "array":
        if len(a) != len(b):
            out.append({"path": path, "kind": "length",
                        "baseline": len(a), "candidate": len(b)})
        for i in range(min(len(a), len(b))):
            diff_structure(a[i], b[i], f"{path}[{i}]", out, limit)
    return out


def diff_values(a, b, path="$", out=None, limit=400):
    """L3：归一化后逐值 diff。"""
    if out is None:
        out = []
    if len(out) >= limit:
        return out
    ta, tb = type_name(a), type_name(b)
    if ta != tb:
        d = {"path": path, "kind": "value-type",
             "baseline": ta, "candidate": tb}
        # L3 的 value-type 与 L2 一样必须携带两侧原值：形态锚定型豁免
        # （ADR-002 要求 baseline_value=="True"）在 classify 里比对
        # d.get("baseline_value")——缺此键则 str(None)="None" 永不相等，
        # 用户已批准的形态漂移在 L3 层【永远无法命中豁免】→ 假红。
        # （19120 实跑抓出，2026-10-16；补值字段不放松任何匹配——值仍
        #   须逐项相等才豁免，见哨兵 l3-value-type-values-complete-*）
        if ta in ("str", "int", "float", "bool", "null"):
            d["baseline_value"] = _clip(a)
        if tb in ("str", "int", "float", "bool", "null"):
            d["candidate_value"] = _clip(b)
        out.append(d)
        return out
    if ta == "object":
        for k in sorted(set(a) & set(b)):
            diff_values(a[k], b[k], f"{path}.{k}", out, limit)
    elif ta == "array":
        for i in range(min(len(a), len(b))):
            diff_values(a[i], b[i], f"{path}[{i}]", out, limit)
    elif a != b:
        out.append({"path": path, "kind": "value",
                    "baseline": _clip(a), "candidate": _clip(b)})
    return out


def _clip(v):
    if isinstance(v, str) and len(v) > 200:
        return v[:200] + f"…(+{len(v) - 200} chars)"
    return v


# ---------------------------------------------------------------------------
# 差异豁免清单（allowlist）
# ---------------------------------------------------------------------------

def _compiled_entry(a: dict):
    return path_segs(a["path"]), a


def _case_match(case_id, spec):
    """case 匹配：'*' 全配；精确名；或步骤 case（'PARENT#step0-x'）归属父 case。
    spec 可为字符串或字符串列表。"""
    if spec in ("*", None):
        return True
    specs = spec if isinstance(spec, list) else [spec]
    return any(case_id == s or case_id.startswith(s + "#") for s in specs)


def classify_diffs(diffs, allowlist, case_id):
    """值级差异可被豁免；结构级（type/missing/length/value-type）默认不可 ——
    除非豁免条目显式 covers_structure=true 且批准人非空。审计痕迹进报告。"""
    failures, allowed = [], []
    entries = [(_compiled_entry(a), a) for a in allowlist
               if isinstance(a, dict) and not a.get("requires_check")]
    # requires_check 条目是【case 级强制门】（requires_check_gate 处理），
    # 不参与差异豁免匹配——防止把"结构验证必须过"误用成"结构差异可豁免"。
    for d in diffs:
        hit = None
        for (pat, a), raw in entries:
            if not _case_match(case_id, raw.get("case")):
                continue
            if not match_segs(pat, path_segs(d["path"])):
                continue
            structural = d["kind"] in ("type", "value-type", "missing_in_candidate",
                                       "missing_in_baseline", "length")
            if structural and not (raw.get("covers_structure")
                                   and raw.get("approved_by")):
                continue
            if "baseline" in raw and str(d.get("baseline")) != str(raw["baseline"]):
                continue
            if "candidate" in raw and str(d.get("candidate")) != str(raw["candidate"]):
                continue
            if ("baseline_value" in raw and
                    str(d.get("baseline_value")) != str(raw["baseline_value"])):
                continue
            if ("candidate_value" in raw and
                    str(d.get("candidate_value")) != str(raw["candidate_value"])):
                continue
            hit = raw
            break
        if hit:
            allowed.append({"diff": d, "allowlist_id": hit.get("id"),
                            "why": hit.get("why"),
                            "approved_by": hit.get("approved_by")})
        else:
            failures.append(d)
    return failures, allowed


# ---------------------------------------------------------------------------
# 请求执行
# ---------------------------------------------------------------------------

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Response:
    __slots__ = ("status", "headers", "body", "transport", "elapsed")

    def __init__(self):
        self.status = None
        self.headers = {}
        self.body = None
        self.transport = None
        self.elapsed = None

    def parsed(self):
        ct = self.headers.get("content-type", "") or ""
        if self.body is None:
            return None, "no-body"
        if "application/json" in ct:
            try:
                return json.loads(self.body.decode("utf-8")), "json"
            except Exception:
                return None, "json-parse-fail"
        if "text/event-stream" in ct:
            frames = []
            for block in self.body.decode("utf-8", "replace").split("\n\n"):
                for ln in block.splitlines():
                    if ln.startswith("data: "):
                        d = ln[6:]
                        if d.strip() == "[DONE]":
                            frames.append({"__done__": True})
                        else:
                            try:
                                frames.append(json.loads(d))
                            except Exception:
                                frames.append({"__raw__": d})
            return {"__sse_frames__": frames}, "sse"
        return None, "non-json"


def execute(case, base_url, env, timeout):
    r = Response()
    url = base_url.rstrip("/") + case["path"]
    if case.get("query"):
        url += "?" + case["query"]
    headers = expand(dict(case.get("headers", {})), env)
    data = None
    body = expand(case.get("json"), env)
    if body is not None:
        data = json.dumps(body, ensure_ascii=False).encode("utf-8")
        headers.setdefault("Content-Type", "application/json")
    elif case.get("raw_body") is not None:
        data = expand(case["raw_body"], env).encode("utf-8")
        headers.setdefault("Content-Type", "application/json")
    # ---- 未解析占位符 = 配置错误，整 case 硬 FAIL（绝不把 "${KEY}" 字面量
    # 发出去换来"两侧一致 401"的假绿——worktree env 漏配事故的治本防呆）----
    probe = url + json.dumps(headers, ensure_ascii=False) + (
        data.decode("utf-8", "replace") if data else "")
    unresolved = sorted(set(re.findall(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}", probe)))
    if unresolved:
        raise ValueError("unresolved env placeholder(s) %s — env-file 缺键? "
                         "(path=%s)" % (unresolved, case["path"]))
    req = urllib.request.Request(url, data=data,
                                 method=case.get("method", "GET"))
    for k, v in headers.items():
        req.add_header(k, v)
    opener = urllib.request.build_opener(NoRedirect,
                                         urllib.request.ProxyHandler({}))
    t0 = time.monotonic()
    try:
        with opener.open(req, timeout=timeout) as resp:
            r.status = resp.getcode()
            r.headers = {k.lower(): v for k, v in resp.getheaders()}
            r.body = resp.read()
    except urllib.error.HTTPError as e:
        r.status = e.code
        r.headers = {k.lower(): v for k, v in (e.headers or {}).items()}
        try:
            r.body = e.read()
        except Exception:
            r.body = b""
    except urllib.error.URLError as e:
        reason = e.reason
        if isinstance(reason, socket.timeout) or isinstance(reason, TimeoutError):
            r.transport = "timeout"
        elif isinstance(reason, ConnectionError):
            r.transport = type(reason).__name__
        elif isinstance(reason, OSError):
            r.transport = "OSError"
        else:
            r.transport = type(reason).__name__
    except socket.timeout:
        r.transport = "timeout"
    except Exception as e:  # noqa: BLE001
        r.transport = type(e).__name__
    r.elapsed = time.monotonic() - t0
    return r


# ---------------------------------------------------------------------------
# 单端点对拍
# ---------------------------------------------------------------------------

HEADER_KEYS_DEFAULT = ["content-type", "location"]


def _pair_once(case, baseline_url, candidate_url, env, normalizer,
               allowlist, timeout, header_keys, sentinel=None):
    """跑一遍 baseline/candidate，返回判定字典。

    两种 case 形态：
      单请求 case：直接对拍响应。
      序列 case（case["steps"]，ADR-003 写行为对拍）：按序把每一步
      （写步+读步）双打两个服务，逐步对拍后聚合成一个判定——
      任何一步不一致即整体不一致；写步两侧执行的是【同一操作序列】，
      所以「只写列不写 data」这类缺陷会在后续读步显形。
    """
    steps = case.get("steps") or [dict(case)]
    is_seq = len(steps) > 1
    # 自动侧别变量 {{SIDE}}：b/c——同一服务自比时两侧假数据（如 lifecycle 的
    # 建号 token）互不碰撞（相同 token 会被去重成 count:0，正是本轮实弹抓出的
    # 设计缺陷）；跨服务对拍两侧独立库，同样无害。
    # {{OTHER}}=对侧标记：读步断言【本侧列表不得出现对侧假账号前缀】——
    # candidate 误连共享库（"一致地错"的共享错误源形态）时两侧各自看得到
    # 对侧残留而 L2/L3 依旧同构全绿，唯此谓词定罪（ASSERT 层的 status-pin 时刻）。
    _same = "1" if str(baseline_url).rstrip("/") == str(candidate_url).rstrip("/") else "0"
    vmap = {"baseline": {"SIDE": "b", "OTHER": "c", "SAME_SOURCE": _same},
            "candidate": {"SIDE": "c", "OTHER": "b", "SAME_SOURCE": _same}}
    step_results = []
    for i, st in enumerate(steps):
        one = dict(st)
        one.setdefault("id", "%s#step%d-%s" % (case["id"], i, st.get("label", "req")))
        if is_seq:
            # 步间变量：两侧各插各自的 capture 值；未解析 → 整 case 报错 FAIL
            one_b = apply_vars(one, vmap["baseline"])
            one_c = apply_vars(one, vmap["candidate"])
        else:
            one_b = one_c = one
        sr = _pair_once_single(one, baseline_url, candidate_url, env,
                               normalizer, allowlist, timeout, header_keys,
                               sentinel=sentinel, case_b=one_b, case_c=one_c,
                               want_raw=bool(st.get("capture") or st.get("asserts")))
        afails = []
        raw = sr.pop("_raw", None)
        rb_raw = (raw or {}).get("baseline")
        rc_raw = (raw or {}).get("candidate")
        if st.get("capture"):
            afails += do_captures(rb_raw, rc_raw,
                                  apply_vars(st["capture"], vmap["baseline"]),
                                  vmap["baseline"], vmap["candidate"])
        # asserts 用【本侧插值后】的副本：谓词里的 {{ID}}/{{SIDE}} 各解各值
        if one_b.get("asserts"):
            afails += eval_asserts(rb_raw, one_b["asserts"], "baseline",
                                   vmap["baseline"])
        if one_c.get("asserts"):
            afails += eval_asserts(rc_raw, one_c["asserts"], "candidate",
                                   vmap["candidate"])
        # 按【键存在】而非真值判定：配了 "asserts":[] 空清单是配置错误，
        # 不得静默跳过（静默 = 又一种"一致地错"：两侧都"通过"了空断言）
        if "capture" in st or "asserts" in st:
            if not (st.get("capture") or st.get("asserts")):
                afails.append("配置错误：capture/asserts 键存在但全为空清单"
                              "（空断言不配比不配更危险——它伪装成已覆盖）")
            sr["ASSERT"] = {"pass": not afails, "fails": afails[:20]}
        sr["step"] = i
        sr["label"] = st.get("label", "req")
        step_results.append(sr)
    if len(step_results) == 1:
        return step_results[0]
    # 聚合
    agg = {"baseline": {"status": "sequence"}, "candidate": {"status": "sequence"},
           "body_kind": "sequence(%d)" % len(step_results)}
    all_sent = [e for s in step_results for e in (s.get("sentinel", {}).get("events") or [])]
    if all_sent:
        agg["sentinel"] = {"hits": len(all_sent), "events": all_sent[:80],
                           "note": "序列各步的敏感值命中汇总（同单请求语义）"}

    def worst(key, sub):
        # 只统计【有该层判定的步】；缺层的步不参与（否则 KeyError/None 误洗或误红）
        vals = [s[key][sub] for s in step_results if key in s and sub in s[key]]
        if False in vals:
            return False
        if vals and all(v is None for v in vals):
            return None
        return True
    agg["L1"] = {"pass": worst("L1", "pass"),
                 "steps": [{"label": s["label"], "pass": s["L1"]["pass"]} for s in step_results]}
    agg["L2"] = {"pass": worst("L2", "pass"),
                 "diffs": [dict(d, step=s["label"]) for s in step_results
                           for d in (s["L2"].get("diffs") or [])][:60],
                 "allowed": [a for s in step_results for a in (s["L2"].get("allowed") or [])][:20]}
    agg["L3"] = {"pass": worst("L3", "pass"),
                 "diffs": [dict(d, step=s["label"]) for s in step_results
                           for d in (s["L3"].get("diffs") or [])][:60],
                 "allowed": [a for s in step_results for a in (s["L3"].get("allowed") or [])][:20],
                 "canonical_equal_raw": all(
                     bool(s.get("L3", {}).get("canonical_equal_raw")) for s in step_results)}
    agg["headers"] = {"pass": all(s["headers"]["pass"] for s in step_results),
                      "diffs": [d for s in step_results for d in s["headers"]["diffs"]]}
    if any("ASSERT" in s for s in step_results):
        agg["ASSERT"] = {"pass": worst("ASSERT", "pass"),
                         "fails": [dict(f=f, step=s["label"])
                                   for s in step_results
                                   for f in (s.get("ASSERT", {}).get("fails") or [])][:30]}
    if any("ORDER" in s for s in step_results):
        pv = [s["ORDER"]["pass"] for s in step_results if "ORDER" in s]
        agg["ORDER"] = {"pass": False if False in pv else (None if all(p is None for p in pv) else True),
                        "no_sample": all(p is None for p in pv),
                        "steps": [{"label": s["label"], "pass": s.get("ORDER", {}).get("pass")}
                                  for s in step_results if "ORDER" in s]}
    agg["normalize_trace"] = {"steps": [{"label": s["label"],
                                         "trace": s.get("normalize_trace")}
                                        for s in step_results][:12]}
    return agg


def _pair_once_single(case, baseline_url, candidate_url, env, normalizer,
                      allowlist, timeout, header_keys, sentinel=None,
                      case_b=None, case_c=None, want_raw=False):
    rb = execute(case_b or case, baseline_url, env, timeout)
    time.sleep(0.04)
    rc = execute(case_c or case, candidate_url, env, timeout)

    out = {"baseline": {"status": rb.status, "transport": rb.transport},
           "candidate": {"status": rc.status, "transport": rc.transport}}

    # ---- L1：两侧一致 +（若 case 声明 status）绝对状态码正确 ----
    # "两侧一致地错"不是对拍绿的理由：status pin 把契约实测状态码钉进判定
    # （worktree env 漏配事故复盘：全体 401 时旧 L1 全绿，只有 ORDER 报警）。
    l1 = (rb.transport == rc.transport) and (rb.status == rc.status)
    want_status = case.get("status")
    if want_status is not None:
        st_set = want_status if isinstance(want_status, list) else [want_status]
        l1 = l1 and (rb.status in st_set) and (rc.status in st_set)
    out["L1"] = {"pass": l1,
                 "detail": None if l1 else {
                     "baseline": out["baseline"], "candidate": out["candidate"],
                     **({"expected_status": want_status} if want_status is not None else {})}}

    # ---- 响应头子集（MIME 主干 + Location；server/date 属实现细节不比） ----
    hb = {k: v for k, v in rb.headers.items()}
    hc = {k: v for k, v in rc.headers.items()}
    hdr_diffs = []
    for k in header_keys:
        vb, vc = hb.get(k), hc.get(k)
        if k == "content-type" and vb and vc:
            vb, vc = vb.split(";")[0].strip(), vc.split(";")[0].strip()
        if vb != vc:
            hdr_diffs.append({"header": k, "baseline": vb, "candidate": vc})
    out["headers"] = {"pass": not hdr_diffs, "diffs": hdr_diffs}

    jb, kind_b = rb.parsed()
    jc, kind_c = rc.parsed()
    raw_jb, raw_jc = jb, jc          # capture/asserts 用【sentinel 前】的原始值
    if want_raw:
        out["_raw"] = {"baseline": raw_jb, "candidate": raw_jc}
        # 仅内部传递，聚合前一律 pop，不落报告

    # ---- ORDER 断言（CR-35）：在归一化/sentinel 之前，用【原始】解析数组 ----
    specs = case.get("order_keys")
    if specs:
        if isinstance(specs, dict):
            specs = [specs]
        checks = [order_check(jb, jc, sp) for sp in specs]
        pv = [c["pass"] for c in checks]
        overall = False if False in pv else (None if all(p is None for p in pv) else True)
        out["ORDER"] = {"pass": overall, "checks": checks,
                        "no_sample": overall is None}

    # ---- 敏感值硬防线：进 L2/L3/报告【之前】拦截（captain 指令） ----
    sent_events = []
    if sentinel is not None:
        if jb is not None:
            jb, ev_b = sentinel.scan(jb)
            for e in ev_b:
                e["side"] = "baseline"
            sent_events += ev_b
        if jc is not None:
            jc, ev_c = sentinel.scan(jc)
            for e in ev_c:
                e["side"] = "candidate"
            sent_events += ev_c
        if sent_events:
            out["sentinel"] = {
                "hits": len(sent_events),
                "events": sent_events[:60],
                "note": "命中位置已替换为 sha256 指纹：等值判定保留（同值同指纹"
                        "→可比对；异值→指纹不同→照样 FAIL），明文零落盘。"
                        "此标注本身即 captain 要求的『此处曾命中敏感值，已脱敏』。"}

    structured = (kind_b in ("json", "sse")) and (kind_c in ("json", "sse"))
    if kind_b == "json" and kind_c == "sse" or kind_b == "sse" and kind_c == "json":
        # 一边 SSE 一边 JSON：结构不同族，按 body_kind 差异进 L3 明细
        structured = False

    # ---- ST-DOM：ADR-006 结构等价层（case kind="html-dom" 才启用）----
    if case.get("kind") == "html-dom":
        ct_b = rb.headers.get("content-type", "") or ""
        ct_c = rc.headers.get("content-type", "") or ""
        html_ok = ("text/html" in ct_b and "text/html" in ct_c
                   and rb.body is not None and rc.body is not None)
        if html_ok:
            bnodes = extract_dom(rb.body.decode("utf-8", "replace"))
            cnodes = extract_dom(rc.body.decode("utf-8", "replace"))
            dom_diffs = diff_dom(bnodes, cnodes)
            out["ST-DOM"] = {
                "pass": not dom_diffs,
                "nodes": [len(bnodes), len(cnodes)],
                "ids": [sum(1 for n in bnodes if n["id"]),
                        sum(1 for n in cnodes if n["id"])],
                "classes": [len({c for n in bnodes for c in n["classes"]}),
                            len({c for n in cnodes for c in n["classes"]})],
                "data_attrs": [len([1 for n in bnodes for _ in n["data"]]),
                               len([1 for n in cnodes for _ in n["data"]])],
                "resources": [dom_resources(bnodes), dom_resources(cnodes)],
                "diff_count": len(dom_diffs),
                "diffs": dom_diffs[:60]}
        else:
            out["ST-DOM"] = {
                "pass": False,
                "detail": "not comparable as HTML: baseline(%s) candidate(%s) body?"
                          % (ct_b or "-", ct_c or "-")}

    if structured:
        # L2：结构对齐层（pre-sort by 唯一锚）后、原始值上比类型树
        ab, tr_pre_b = normalizer.align(jb)
        ac, tr_pre_c = normalizer.align(jc)
        struct_raw = diff_structure(ab, ac)
        s_fail, s_allow = classify_diffs(struct_raw, allowlist, case["id"])
        # L3：对齐 + 值归一化后再逐值比
        tb2, tc2 = [], []
        nb = normalizer._walk(ab, ["$"], tb2, "values")
        nc = normalizer._walk(ac, ["$"], tc2, "values")
        value_raw = diff_values(nb, nc)
        v_fail, v_allow = classify_diffs(value_raw, allowlist, case["id"])
        # header 差异并入 L2（响应头子集属结构面契约）
        l2_diffs = s_fail + [{"path": f"$header.{d['header']}",
                              "kind": "header", "baseline": d["baseline"],
                              "candidate": d["candidate"]} for d in hdr_diffs]
        out["L2"] = {"pass": not l2_diffs,
                     "diff_count_total": len(struct_raw),
                     "diffs": l2_diffs[:60],
                     "allowed": s_allow[:20]}
        l3_blocked = bool(s_fail) or bool(hdr_diffs)
        out["L3"] = {"pass": (not l3_blocked) and not v_fail,
                     "canonical_equal_raw": canonical_json(jb) == canonical_json(jc),
                     "diff_count_total": len(value_raw),
                     "diffs": (v_fail[:60] + [{"path": d["path"],
                                                "kind": "carried-from-L2:" + d["kind"]}
                                               for d in s_fail[:60]]),
                     "allowed": v_allow[:20]}
        out["normalize_trace"] = {"pre_align": (tr_pre_b + tr_pre_c)[:30],
                                  "value_norm": (tb2 + tc2)[:50]}
        out["body_kind"] = "json" if kind_b == "json" else "sse"
    else:
        def textify(r):
            if r.body is None:
                return None
            return re.sub(r"\s+", " ", r.body.decode("utf-8", "replace")).strip()
        tb2, tc2 = textify(rb), textify(rc)
        # 非 JSON 体同样过硬防线：内嵌密钥形态直接指纹化（HTML 注释里躺 token
        # 是老服务真实存在过的形态，不能因为是 text/html 就放行）
        if sentinel is not None:
            for side in ("baseline", "candidate"):
                t = tb2 if side == "baseline" else tc2
                if not t:
                    continue
                hits = []
                for fname, frx, _anchored in sentinel.value_forms:
                    m = frx.search(t)
                    if m:
                        hits.append((fname, m.span()))
                if hits:
                    sent_events += [{"path": "$body(text)", "detector": "value-form",
                                     "match": fname, "side": side,
                                     "action": "fingerprinted-inline",
                                     "fingerprint": sentinel._fp(t[s0:s1])}
                                    for fname, (s0, s1) in hits]
                    if side == "baseline":
                        tb2 = re.sub(r"eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}",
                                     lambda m: sentinel._fp(m.group(0)), tb2)
                    else:
                        tc2 = re.sub(r"eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}",
                                     lambda m: sentinel._fp(m.group(0)), tc2)
            if sent_events:
                out["sentinel"] = {"hits": len(sent_events), "events": sent_events[:60],
                                   "note": "非 JSON 体内嵌密钥形态已指纹化（内联替换，"
                                           "两侧同子串→同指纹，等值判定保留）"}
        note = f"parse:{kind_b}/{kind_c}"
        if not (rb.transport or rc.transport) and rb.body is not None and rc.body is not None:
            eq = tb2 == tc2
            out["L2"] = {"pass": None if not hdr_diffs else False,
                         "detail": "non-JSON body：键/类型不可导，L2 n-a（此类 case 预期应设为 L1+L3）",
                         "diffs": hdr_diffs}
            out["L3"] = {"pass": eq if not hdr_diffs else False,
                         "detail": note if eq else {"first_diff_index": next(
                             (i for i in range(min(len(tb2), len(tc2)))
                              if tb2[i] != tc2[i]), min(len(tb2), len(tc2))),
                             "baseline_len": len(tb2), "candidate_len": len(tc2)}}
            out["body_kind"] = kind_b if kind_b != "json" else "mixed"
        else:
            out["L2"] = {"pass": None, "detail": "one side transport-error"}
            out["L3"] = {"pass": None, "detail": "one side transport-error"}
            out["body_kind"] = note
    return out


def compare_case(case, baseline_url, candidate_url, env, normalizer,
                 allowlist, timeout, header_keys, retries=2, sentinel=None):
    """跑一对服务直到（最多）通过，或穷尽重试后判失败。

    FLAKY 语义：首轮不一致但后续某轮一致 → verdict=pass + flaky=true，
    全部轮次明细进报告供人工审。这是对「baseline 是被 cron/用户流量
    持续写入的共享生产服务」的诚实处理：不静默重试掩盖，也不因外部
    并发写误判契约失败。系统性差异（每轮都不同）必然穷尽重试后 FAIL。

    【写序列禁重试】mutating case（建号/toggle/删除/改设置）重放=再执行一遍
    真实状态变更：二次 create 触发去重语义漂移、失败时库里多留假账号、
    rotate 二次改指纹——FLAKY 对写序列根本不成立。强制单轮。
    """
    if "mutating" in case.get("tags", []):
        retries = 0
    # expect 前置解析：重试只为【预期层级】未满足而进行（observed 层级
    # 的漂移不再空耗三轮——同时保留 ORDER/ASSERT 自动入列防呆：配了
    # order_keys/asserts 却没写进 expect 的人为疏漏，由引擎补上）
    expect = [x.strip() for x in re.split(r"[+,]", case.get("expect", "L1+L2"))]
    if case.get("order_keys") and "ORDER" not in expect:
        expect.append("ORDER")
    if any((st.get("asserts") or st.get("capture"))
           for st in (case.get("steps") or [])) and "ASSERT" not in expect:
        expect.append("ASSERT")

    def _lvl_retry_worthy(a, lvl):
        lv = a.get(lvl, {})
        p = lv.get("pass")
        if lvl in ("L2", "L3"):
            return p is not False          # n/a 不拦；False 才继续重试
        if lvl == "ORDER":
            return p is True or (p is None and lv.get("no_sample"))
        return p is True

    attempts = []
    final = None
    for i in range(1 + max(0, retries)):
        if i:
            time.sleep(0.3)
        a = _pair_once(case, baseline_url, candidate_url, env,
                       normalizer, allowlist, timeout, header_keys,
                       sentinel=sentinel)
        attempts.append(a)
        ok_now = (all(_lvl_retry_worthy(a, lvl) for lvl in expect)
                  and (case.get("kind") != "html-dom"
                       or a.get("ST-DOM", {}).get("pass") is True))
        if ok_now:
            final = a
            break
    if final is None:
        final = attempts[0]
    flaky = len(attempts) > 1
    recovered = flaky and final is not attempts[0]

    unmet = []
    for lvl in expect:
        lv = final.get(lvl, {})
        p = lv.get("pass")
        if lvl == "ORDER" and p is None and lv.get("no_sample"):
            continue   # 双侧无样本 → 可见 skip（报告 ORDER:n/a(no-sample)），不判罪也不充绿
        if p is not True:      # False 与 None 都不算满足预期（缺证据不判过）
            unmet.append(lvl)
    verdict = "pass" if not unmet else "fail"

    # ---- 豁免≠放行：requires_check 门（ADR-006 机制化）----
    # 带 requires_check="html-dom-equivalence" 的豁免条目匹配本 case 时，
    # ST-DOM 必须 PASS——哪怕字节级差异已被豁免/未纳入 expect。
    blocked, gate_why = requires_check_gate(case["id"], final, allowlist)
    if blocked:
        verdict = "fail"
        unmet = (unmet or []) + ["requires_check:html-dom"]

    # html-dom case 的字节级 L3 若不在 expect 中 → 明确标注为 observed
    # （仍如实计算与记录，只是不判；同源自测时通常仍为 True）
    observed_l3 = (case.get("kind") == "html-dom" and "L3" not in expect)

    res = {"id": case["id"], "method": case.get("method", "GET"),
           "path": case["path"], "expect": "+".join(expect),
           "kind": case.get("kind", "http"),
           "tags": case.get("tags", []), "note": case.get("note", ""),
           "verdict": verdict, "flaky": flaky, "recovered": recovered,
           "attempts": len(attempts)}
    for lvl in ("L1", "L2", "L3"):
        res[lvl] = final.get(lvl, {"pass": None})
    if observed_l3:
        res["L3"]["observed"] = True   # 记录但降级：ADR-006 字节漂移不判
    if "ST-DOM" in final:
        res["ST-DOM"] = final["ST-DOM"]
    if "ORDER" in final:
        res["ORDER"] = final["ORDER"]
    if "ASSERT" in final:
        res["ASSERT"] = final["ASSERT"]
    if blocked:
        res["gate"] = gate_why
    res["headers"] = final.get("headers", {})
    res["baseline"] = final.get("baseline", {})
    res["candidate"] = final.get("candidate", {})
    res["body_kind"] = final.get("body_kind")
    if "normalize_trace" in final:
        res["normalize_trace"] = final["normalize_trace"]
    if final.get("sentinel"):
        res["sentinel"] = final["sentinel"]
    if unmet:
        res["unmet"] = unmet
    if len(attempts) > 1:
        res["earlier_attempts"] = [
            {"L1": a["L1"]["pass"], "L2": a["L2"]["pass"], "L3": a["L3"]["pass"]}
            for a in attempts]
    return res


# ---------------------------------------------------------------------------
# 引擎自测（离线漂移注入：证明裁判抓得住真差异、不冤枉假差异）
# ---------------------------------------------------------------------------

FIXTURE_BASE = {
    "id": "chatcmpl-aaa", "object": "chat.completion", "created": 1791000000,
    "model": "GLM-5.3",
    "choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello"},
                 "finish_reason": "stop"}],
    "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
}

FIXTURE_RULES = [
    {"path": "$.id", "replace": "<resp-id>",
     "why": "服务端随机响应 id，无业务语义"},
    {"path": "$.created", "replace": "<timestamp>",
     "why": "秒级创建时刻，同一输入不同秒调用必然不同"},
    {"path": "$.choices[*].message.content", "replace": "<llm-text>",
     "why": "真实上游 LLM 文本不保证跨调用确定性；其结构面（键/类型/存在性）由 L2 原始数据比对守卫"},
]


def _fx_resp(status=200, obj=None, transport=None):
    r = Response()
    if transport:
        r.transport = transport
        return r
    r.status = status
    r.headers = {"content-type": "application/json"}
    r.body = json.dumps(obj if obj is not None else FIXTURE_BASE,
                        ensure_ascii=False).encode()
    return r


def run_engine_selftest(allow_entries=None):
    norm = Normalizer(FIXTURE_RULES)
    checks = []
    allow_entries = allow_entries or []
    adr = [a for a in allow_entries if a.get("id") == "ADR-002-enabled-laohao"]

    def judge(rb, rc, rules=None, allow=None, case="FIXTURE"):
        jb, _ = rb.parsed()
        jc, _ = rc.parsed()
        n = Normalizer(rules if rules is not None else FIXTURE_RULES)
        struct = diff_structure(jb, jc) if jb is not None and jc is not None else []
        s_fail, s_allow = classify_diffs(struct, allow or [], case)
        if jb is None or jc is None:
            return {"L1": rb.status == rc.status and rb.transport == rc.transport,
                    "L2": None, "L3": None}
        nb, _ = n.normalize(jb)
        nc, _ = n.normalize(jc)
        vals = diff_values(nb, nc)
        v_fail, v_allow = classify_diffs(vals, allow or [], case)
        return {"L1": rb.status == rc.status and rb.transport == rc.transport,
                "L2": not s_fail, "L3": not s_fail and not v_fail,
                "s": s_fail, "v": v_fail, "a": s_allow + v_allow}

    harmless = copy.deepcopy(FIXTURE_BASE)
    harmless.update({"id": "chatcmpl-bbb", "created": 1791000999})
    harmless["choices"][0]["message"]["content"] = "Hello there!"

    # 1) 无害漂移（随机id/时间戳/LLM文本）→ 归一化后必须全绿（零假阳性）
    j = judge(_fx_resp(obj=FIXTURE_BASE), _fx_resp(obj=harmless))
    checks.append(("catches-nothing-on-harmless-drift", j["L1"] and j["L2"] and j["L3"], j))

    # 2) 状态码漂移 → L1 红
    j = judge(_fx_resp(), _fx_resp(status=201))
    checks.append(("L1-catches-status-drift", not j["L1"], j))

    # 3) int→str 类型漂移（脏数据同类）→ L2 红（类型树逐位置）
    bad = copy.deepcopy(FIXTURE_BASE)
    bad["usage"]["completion_tokens"] = "5"
    j = judge(_fx_resp(), _fx_resp(obj=bad))
    checks.append(("L2-catches-bool-str-type-drift", not j["L2"], j))

    # 4) bool→str 真例（enabled true vs "True"）→ L2 红
    a1 = {"accounts": [{"id": "x", "enabled": True}]}
    b1 = {"accounts": [{"id": "x", "enabled": "True"}]}
    d = diff_structure(a1, b1)
    checks.append(("L2-catches-enabled-dirty-data",
                   any(x["kind"] == "type" and x["path"].endswith("enabled") for x in d), d))

    # 5) 缺键 → L2 红
    bad = copy.deepcopy(FIXTURE_BASE)
    del bad["model"]
    j = judge(_fx_resp(), _fx_resp(obj=bad))
    checks.append(("L2-catches-missing-key", not j["L2"], j))

    # 6) 多键（未声明字段）→ L2 红，归一化不能吞键
    bad = copy.deepcopy(FIXTURE_BASE)
    bad["surprise"] = {"nested": 1}
    j = judge(_fx_resp(), _fx_resp(obj=bad))
    checks.append(("L2-catches-undeclared-key", not j["L2"], j))

    # 7) 值漂移且无规则 → L3 红
    bad = copy.deepcopy(FIXTURE_BASE)
    bad["usage"]["total_tokens"] = 999
    j = judge(_fx_resp(), _fx_resp(obj=bad))
    checks.append(("L3-catches-unruled-value-drift", j["L2"] and not j["L3"], j))

    # 8) 数组乱序（同集合同长度，仅顺序不同）：无排序规则 → L3 红（值错位）；
    #    加 sort_by=index 规则后 → 对齐 → L3 绿。长度不同的乱序属结构差异，
    #    L2 必须报 length —— 那是特性不是 bug，不放这里测。
    base_two = copy.deepcopy(FIXTURE_BASE)
    base_two["choices"] = [
        {"index": 0, "message": {"role": "assistant", "content": "a"}, "finish_reason": "stop"},
        {"index": 1, "message": {"role": "assistant", "content": "b"}, "finish_reason": "stop"}]
    bad = copy.deepcopy(base_two)
    bad["choices"] = list(reversed(bad["choices"]))
    plain = judge(_fx_resp(obj=copy.deepcopy(base_two)), _fx_resp(obj=copy.deepcopy(bad)))
    ruled = judge(_fx_resp(obj=copy.deepcopy(base_two)), _fx_resp(obj=bad),
                  rules=FIXTURE_RULES + [{"path": "$.choices", "sort_by": "index",
                                          "why": "choices 顺序无契约意义（按 index 锚定）"}])
    checks.append(("array-order-caught-then-ruled",
                   (not plain["L3"]) and ruled["L3"], {"plain": not plain["L3"], "ruled": ruled["L3"]}))

    # 9) transport 差异 → L1 红
    j = judge(_fx_resp(), _fx_resp(transport="timeout"))
    checks.append(("L1-catches-transport-drift", not j["L1"], j))

    # 10) allowlist：值级豁免生效 + 审计痕迹保留
    d_val = [{"path": "$.usage.total_tokens", "kind": "value",
              "baseline": 15, "candidate": 999}]
    al = [{"id": "EX-1", "case": "FIXTURE", "path": "$.usage.total_tokens",
           "baseline": 15, "candidate": 999, "why": "示例豁免",
           "approved_by": "captain"}]
    f, a = classify_diffs(d_val, al, "FIXTURE")
    checks.append(("allowlist-exempts-value-diff",
                   not f and len(a) == 1 and a[0]["allowlist_id"] == "EX-1",
                   {"failures": f, "allowed": a}))

    # 11) allowlist 不得默认豁免结构差异
    d_struct = [{"path": "$.model", "kind": "type", "baseline": "str", "candidate": "int"}]
    f, a = classify_diffs(d_struct, al, "FIXTURE")
    checks.append(("allowlist-blocks-structure-diff-by-default", f and not a,
                   {"failures": f, "allowed": a}))

    # 12) 显式 covers_structure 豁免须同时有批准人，否则不生效
    al2 = al + [{"id": "EX-2", "case": "*", "path": "$.model",
                 "covers_structure": True, "why": "Go 侧统一类型", "approved_by": ""}]
    f, a = classify_diffs(d_struct, al2, "FIXTURE")
    checks.append(("structure-exempt-requires-approver", f and not a,
                   {"failures": f, "allowed": a}))

    # 13) 空 allowlist（默认）→ 一切差异报错
    f, a = classify_diffs(d_val, [], "FIXTURE")
    checks.append(("empty-allowlist-defaults-to-red", len(f) == 1 and not a,
                   {"failures": len(f), "allowed": len(a)}))

    # 14) SSE 帧解析
    r = _fx_resp(obj=None)
    r.headers = {"content-type": "text/event-stream"}
    r.body = b'data: {"x":1}\n\ndata: [DONE]\n\n'
    parsed, kind = r.parsed()
    checks.append(("sse-frames-parsed",
                   kind == "sse" and parsed == {"__sse_frames__": [{"x": 1}, {"__done__": True}]},
                   parsed))

    # 15) 键序不同但内容相同 → canonical 后必须绿（防假阳性）
    a_json = json.loads('{"b":1,"a":2}')
    b_json = json.loads('{"a":2,"b":1}')
    checks.append(("key-order-insensitive",
                   not diff_structure(a_json, b_json) and not diff_values(a_json, b_json),
                   None))

    # 16) ADR-002 真实豁免条目端到端（读 allowlist.json 的实际内容）：
    #     str "True" → bool true 的类型漂移必须先被 L2 抓住（红），
    #     再被批准条目命中转 allowed（不是消失，是带审计痕迹放行）
    py_side = {"accounts": [{"id": "a1", "enabled": True},
                            {"id": "laohao-bigmodel-2420b3c2", "enabled": "True"}]}
    go_side = {"accounts": [{"id": "a1", "enabled": True},
                            {"id": "laohao-bigmodel-2420b3c2", "enabled": True}]}
    d = diff_structure(py_side, go_side)
    f_wo, a_wo = classify_diffs(d, [], "AD-accounts")
    f_w, a_w = classify_diffs(d, allow_entries, "AD-accounts")
    if adr:
        checks.append(("ADR-002-exemption-hits-with-audit",
                       bool(f_wo) and not a_wo and not f_w and len(a_w) == 1
                       and a_w[0]["allowlist_id"] == "ADR-002-enabled-laohao",
                       {"diff": d, "without": [len(f_wo)], "with": [len(f_w), len(a_w)]}))
        # 17) 过度豁免守卫：bool→int 这类未批准的形态漂移不得被 ADR-002 条目吞掉
        other = [{"path": "$.accounts[0].enabled", "kind": "type",
                  "baseline": "bool", "candidate": "int",
                  "baseline_value": True, "candidate_value": 0}]
        f2, a2 = classify_diffs(other, allow_entries, "AD-accounts")
        checks.append(("ADR-002-exemption-no-overmatch", len(f2) == 1 and not a2,
                       {"failures": len(f2), "allowed": len(a2)}))
        # ---- 17b) L3 value-type 豁免链三红线（captain 01:30 裁定，1f39731 基线）----
        # diff_values 的 value-type 现已携带两侧原值 → ADR-002 形态锚定在
        # L3 层同样可命中（修复 harness.py:929 缺值字段的裁判缺陷）；
        # 但值字段是【精确锚】不是洗白通道——同型不同值/异型值必须仍红。
        l3_true = diff_values({"accounts": [{"enabled": "True"}]},
                              {"accounts": [{"enabled": True}]})
        f3w, a3w = classify_diffs(l3_true, allow_entries, "AD-accounts")
        item = l3_true[0] if l3_true else {}
        checks.append(("l3-redline1-approved-shape-exempts-green",
                       item.get("kind") == "value-type"
                       and item.get("baseline_value") == "True"
                       and item.get("candidate_value") is True
                       and not f3w and len(a3w) == 1
                       and a3w[0]["allowlist_id"] == "ADR-002-enabled-laohao",
                       {"diff": item, "failures": len(f3w), "allowed": len(a3w)}))
        l3_false = diff_values({"accounts": [{"enabled": "True"}]},
                               {"accounts": [{"enabled": False}]})
        f3f, a3f = classify_diffs(l3_false, allow_entries, "AD-accounts")
        checks.append(("l3-redline2-different-value-still-red",
                       len(f3f) == 1 and not a3f,
                       {"diff": l3_false[0] if l3_false else None,
                        "failures": len(f3f), "allowed": len(a3f)}))
        l3_one = diff_values({"accounts": [{"enabled": "1"}]},
                             {"accounts": [{"enabled": True}]})
        f3o, a3o = classify_diffs(l3_one, allow_entries, "AD-accounts")
        checks.append(("l3-redline3-other-shape-still-red",
                       len(f3o) == 1 and not a3o,
                       {"diff": l3_one[0] if l3_one else None,
                        "failures": len(f3o), "allowed": len(a3o)}))
    else:
        checks.append(("ADR-002-entry-absent-default-red",
                       bool(f_wo), "allowlist 无该条目 → 该类型漂移必须 FAIL（默认一切报错）"))

    # 18) 位置对齐守卫：两侧账号【同集合不同顺序】，加 $.accounts sort_by=id
    #     后 L2/L3 必须绿；不加排序则错位账号会被比出假差异
    mis_b = {"accounts": [{"id": "z1", "enabled": True, "quota": 5},
                          {"id": "a2", "enabled": False, "quota": 9}]}
    mis_c = {"accounts": [{"id": "a2", "enabled": False, "quota": 9},
                          {"id": "z1", "enabled": True, "quota": 5}]}
    n_plain = Normalizer([])
    ab, _ = n_plain.align(mis_b)
    ac, _ = n_plain.align(mis_c)
    plain_fail = bool(diff_structure(ab, ac)) or bool(diff_values(ab, ac))
    n_sorted = Normalizer([{"path": "$.accounts", "sort_by": "id",
                            "why": "自测：唯一锚对齐位置"}])
    sb, _ = n_sorted.align(mis_b)
    sc, _ = n_sorted.align(mis_c)
    sorted_ok = (not diff_structure(sb, sc)) and (not diff_values(sb, sc))
    checks.append(("pre-sort-aligns-by-unique-anchor",
                   plain_fail and sorted_ok,
                   {"plain_failures": plain_fail, "sorted_clean": sorted_ok}))

    # ---- 19) sentinel 敏感值硬防线专项（captain 指令）----
    sent = Sentinel()
    # 19a) 字段名命中 → 指纹化，且【等值判定保留】：两侧同值脱敏后仍相等
    body_a = {"admin_key": "TEST" + "notarealkey0123456789abcdefg",
              "gateway_key": "sk-abcdefghij123456", "enabled": True}
    body_b = {"admin_key": "TEST" + "notarealkey0123456789abcdefg",
              "gateway_key": "sk-abcdefghij123456", "enabled": True}
    ma, eva = sent.scan(body_a)
    mb, evb = sent.scan(body_b)
    checks.append(("sentinel-fieldname-masked-equality-kept",
                   len(eva) == 2 and ma == mb
                   and isinstance(ma["enabled"], bool)
                   and str(ma["admin_key"]).startswith("<secret:")
                   and "TEST" not in json.dumps(ma),
                   {"events": len(eva), "masked_admin_key_form": ma["admin_key"][:16]}))
    # 19b) 两侧【不同】密钥值 → 指纹不同 → 值级仍 FAIL（脱敏不洗白差异）
    body_c = {"admin_key": "totally-different-value-xx"}
    mc, _ = sent.scan(body_c)
    checks.append(("sentinel-different-secret-still-red",
                   mc["admin_key"] != ma["admin_key"],
                   {"a": ma["admin_key"], "c": mc["admin_key"]}))
    # 19c) 值形态命中（无敏感字段名）：JWT 三段式与 32hex
    body_d = {"note": "eyJhbGciOiJIUzI1NiIsIl9hbCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.aBcDeFgHiJkLmNoPqRsTuV",
              "plan_id": "0123456789abcdef0123456789abcdef"}
    md_, evd = sent.scan(body_d)
    forms = {e["match"] for e in evd}
    checks.append(("sentinel-value-form-catch",
                   len(evd) == 2 and "jwt-3seg" in forms and "hex32" in forms
                   and "eyJhbGci" not in json.dumps(md_),
                   {"forms": sorted(forms)}))
    # 19d) 白名单：safe_paths 声明豁免【该路径】——其余路径照常拦（防线不放开）
    sent_w = Sentinel({"safe_paths": ["$.plan_id"]})
    mw2, evw = sent_w.scan(body_d)
    checks.append(("sentinel-safelist-exempts-declared-path-only",
                   len(evw) == 1 and mw2["plan_id"] == "0123456789abcdef0123456789abcdef"
                   and str(mw2["note"]).startswith("<secret:"),
                   {"events": evw}))
    # 19e) 非字符串值命中字段名：不动值（防 L2 类型污染），事件 logged-only
    body_e = {"password": 123456, "token": None, "secret": {"nested": "eyJfZy9h.bbbb.cccc"}}
    me_, eve = sent.scan(body_e)
    kinds = {(e["action"], e["path"].split(".")[-1]) for e in eve}
    checks.append(("sentinel-nonstring-logged-not-mutated",
                   me_["password"] == 123456 and me_["token"] is None
                   and ("logged-only(non-string)", "password") in kinds
                   and isinstance(me_["secret"], dict)
                   and str(me_["secret"]["nested"]).startswith("<secret:"),
                   {"events": eve}))
    # 19f) 数组内嵌命中
    body_f = [{"items": ["sk-abcdefghijklmnop123", "hello"]}]
    mf, evf = sent.scan(body_f)
    checks.append(("sentinel-list-scan",
                   len(evf) == 1 and str(mf[0]["items"][0]).startswith("<secret:")
                   and mf[0]["items"][1] == "hello",
                   {"events": evf}))

    return [{"check": n, "pass": bool(ok), "detail": redact_tree(_clip_detail(det), {})}
            for n, ok, det in checks]


# ---------------------------------------------------------------------------
# DOM 验证器引擎自测（5 项防假绿 + 资源子项 + requires_check 门）
# 纯离线：构造 HTML → extract_dom → diff_dom，不触网。
# ---------------------------------------------------------------------------

DOM_SAMPLE = """<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <title>zcode-hub · 演示</title>
  <!-- 一个注释：允许差异面 -->
  <link href="https://cdn.jsdelivr.net/npm/geist@1.0.0/dist/fonts/geist-sans/style.css" rel="stylesheet">
  <link href="/static/css/app.css?v=2.5.1" rel="stylesheet">
</head>
<body class="admin-body">
  <div id="admin-header" data-active="accounts" class="hdr sticky">
    <span class="brand">zcode-hub</span>
    <button id="btn-refresh" class="btn btn-primary">刷新</button>
  </div>
  <main id="acct-list" class="panel">
    <table><thead><tr><th>名称</th><th>状态</th></tr></thead>
    <tbody><tr><td>老号</td><td>active</td></tr></tbody></table>
  </main>
  <script src="/static/js/auth.js?v=2.5.1"></script>
  <script>const t = `row-${a.id}-ok`; document.title = t;</script>
</body>
</html>"""


def run_dom_selftest():
    """返回 [(name, pass, detail)]；每项是 ADR-006 验证器的防假绿断言。"""
    out = []
    base = extract_dom(DOM_SAMPLE)

    # 1) 同一份 HTML 对自身 → 全等价（零假阳性）
    same = extract_dom(DOM_SAMPLE)
    d1 = diff_dom(base, same)
    out.append(("dom-self-identical", not d1,
                {"nodes": len(base), "diffs": d1[:3]}))

    # 2) 删 1 个 id → 必报（零假阴性）
    v2 = DOM_SAMPLE.replace('<button id="btn-refresh"', "<button")
    d2 = diff_dom(base, extract_dom(v2))
    out.append(("dom-catch-missing-id",
                any(x["kind"] == "id" and x["baseline"] == "btn-refresh"
                    for x in d2), d2[:4]))

    # 3) 改 1 个 class 名 → 必报
    v3 = DOM_SAMPLE.replace('class="btn btn-primary"', 'class="btn btn-PRIM3"')
    d3 = diff_dom(base, extract_dom(v3))
    out.append(("dom-catch-class-change",
                any(x["kind"] in ("class-missing", "class-extra") for x in d3), d3[:4]))

    # 4) 改 1 处可见文本 → 必报
    v4 = DOM_SAMPLE.replace("刷新", "重新加载")
    d4 = diff_dom(base, extract_dom(v4))
    out.append(("dom-catch-text-change",
                any(x["kind"] == "text" for x in d4), d4[:4]))

    # 5) 空白/注释/属性顺序变化 → 不报（"允许差异"清单生效）
    v5 = DOM_SAMPLE.replace(
        '<div id="admin-header" data-active="accounts" class="hdr sticky">',
        '<div class="sticky hdr" data-active="accounts"    id="admin-header">\n\n  ')
    v5 = v5.replace("<!-- 一个注释：允许差异面 -->", "<!-- 换了个注释 -->")
    d5 = diff_dom(base, extract_dom(v5))
    out.append(("dom-ignore-whitespace-comment-attrorder", not d5, d5[:4]))

    # 6) 资源版本串漂移 → 必报（ADR-006 第 5 条不变量独立子项）
    v6 = DOM_SAMPLE.replace("/static/css/app.css?v=2.5.1", "/static/css/app.css?v=9.9.9")
    d6 = diff_dom(base, extract_dom(v6))
    out.append(("dom-catch-resource-version",
                any(x["kind"].startswith("resource-") for x in d6), d6[:4]))

    # 7) 缺一个元素节点（结构漂移）→ 必报 node-count + 级联
    v7 = DOM_SAMPLE.replace('<span class="brand">zcode-hub</span>', "")
    d7 = diff_dom(base, extract_dom(v7))
    out.append(("dom-catch-node-removed",
                any(x["kind"] == "node-count" for x in d7), d7[:4]))

    # 8) script 内 JS 模板文本改动 → 必报（脚本源码属等价面）
    v8 = DOM_SAMPLE.replace("row-${a.id}-ok", "row-${a.id}-XX")
    d8 = diff_dom(base, extract_dom(v8))
    out.append(("dom-catch-js-template-text",
                any(x["kind"] == "text" for x in d8), d8[:4]))

    # 9) requires_check 门：ST-DOM fail 时，即使其它层全过也必须整体 FAIL
    fake_allow = [{"id": "ADR-006-ui-login-page", "case": "UI-login-page",
                   "path": "$body-bytes", "requires_check": "html-dom-equivalence",
                   "why": "测试", "approved_by": "用户"}]
    res_pass_dom = {"ST-DOM": {"pass": True}, "L1": {"pass": True},
                    "L2": {"pass": None}, "L3": {"pass": False}}
    res_fail_dom = {"ST-DOM": {"pass": False}, "L1": {"pass": True},
                    "L2": {"pass": None}, "L3": {"pass": True}}
    b1, _ = requires_check_gate("UI-login-page", res_pass_dom, fake_allow)
    b2, why2 = requires_check_gate("UI-login-page", res_fail_dom, fake_allow)
    b3, _ = requires_check_gate("OTHER-case", res_fail_dom, fake_allow)
    out.append(("dom-requires-check-gate",
                (not b1) and b2 and not b3,
                {"ST-DOM过→放行": not b1, "ST-DOM挂→强制FAIL": b2,
                 "其他case不受影响": not b3}))

    # 10) 带 requires_check 的条目不得充当 diff 豁免（classify_diffs 层验证）
    d_val = [{"path": "$.x", "kind": "value", "baseline": 1, "candidate": 2}]
    f10, a10 = classify_diffs(d_val, fake_allow, "UI-login-page")
    out.append(("requires-check-not-a-waiver", len(f10) == 1 and not a10,
                {"failures": len(f10), "allowed": len(a10)}))

    # 11) class 多空格归一：class="a  b" 与 "a b" 等价
    v11 = DOM_SAMPLE.replace('class="hdr sticky"', 'class="hdr   sticky"')
    d11 = diff_dom(base, extract_dom(v11))
    out.append(("dom-class-multispace-normalized", not d11, d11[:4]))

    # ---- 12-14) ORDER 断言（CR-35 盲区守卫）：证明机制非永真摆设 ----
    acc_spec = {"path": "$.accounts", "by": "id"}
    accs_a = {"accounts": [{"id": "z1-aaaaaaaa", "enabled": True, "quota": 5},
                           {"id": "a2-bbbbbbbb", "enabled": False, "quota": 9}]}
    accs_reordered = {"accounts": list(reversed(accs_a["accounts"]))}
    oc_ok = order_check(accs_a, accs_a, acc_spec)
    oc_swap = order_check(accs_a, accs_reordered, acc_spec)
    out.append(("order-identical-pass", oc_ok["pass"] is True, oc_ok))
    # 核心哨兵：同内容不同序——pre-sort 规则下 L2/L3 必绿，ORDER 必须红。
    nrm = Normalizer([{"path": "$.accounts", "sort_by": "id",
                       "why": "对齐演示"}])
    al_b, _ = nrm.align(accs_a)
    al_c, _ = nrm.align(accs_reordered)
    l2_clean = not diff_structure(al_b, al_c)
    l3_clean = not diff_values(al_b, al_c)
    out.append(("order-catches-reorder-despite-sort",
                l2_clean and l3_clean and oc_swap["pass"] is False,
                {"L2绿": l2_clean, "L3绿": l3_clean,
                 "ORDER红": oc_swap["pass"] is False,
                 "first_diff": oc_swap.get("detail", {}).get("first_diff_index")}))
    oc_missing = order_check({"accounts": "not-a-list"}, accs_a, acc_spec)
    out.append(("order-absent-path-fails-not-passes",
                oc_missing["pass"] is False, oc_missing))

    # ---- 15-17) ORDER 方向模式（CR-37：monitoring.entries 新在前=降序契约）----
    dir_spec = {"path": "$.entries", "by": "ts", "direction": "desc"}
    desc_b = {"entries": [{"ts": 300.0}, {"ts": 200.0}, {"ts": 100.0}]}
    desc_c = {"entries": [{"ts": 999.0}, {"ts": 50.0}]}          # 内容不同但同为降序
    oc_dir_ok = order_check(desc_b, desc_c, dir_spec)
    out.append(("order-direction-passes-on-different-content-same-direction",
                oc_dir_ok["pass"] is True, oc_dir_ok))
    asc_c = {"entries": [{"ts": 1.0}, {"ts": 2.0}, {"ts": 3.0}]}  # 候选旧在前
    oc_dir_bad = order_check(desc_b, asc_c, dir_spec)
    out.append(("order-direction-catches-reversed-candidate",
                oc_dir_bad["pass"] is False
                and oc_dir_bad["per_side"]["candidate"] is False, oc_dir_bad))
    oc_empty = order_check({"entries": []}, {"entries": []}, dir_spec)
    out.append(("order-empty-both-sides-visible-skip",
                oc_empty["pass"] is None and oc_empty.get("no_sample") is True
                and "NEVER silently passed" in str(oc_empty.get("detail")),
                oc_empty))
    oc_half = order_check(desc_b, {"entries": []}, dir_spec)
    out.append(("order-one-side-empty-is-defect-fail",
                oc_half["pass"] is False, oc_half))

    # ---- 18-23) capture/asserts/嵌套方向（工作A/B 机制非摆设证明）----
    # 18) 插值：两侧各插各自的变量
    iv = apply_vars({"path": "/admin/api/accounts/{{ID}}/enabled",
                     "json": {"ids": ["{{ID}}"]}}, {"ID": "abc-123"})
    out.append(("capture-interpolation-per-side",
                iv["path"].endswith("/abc-123/enabled") and iv["json"]["ids"] == ["abc-123"],
                iv))
    # 19) 未定义变量 → 必须炸（绝不带 "{{ID}}" 字面量打服务）
    try:
        apply_vars({"path": "/x/{{UNDEF}}"}, {"ID": "1"})
        raised = False
    except ValueError:
        raised = True
    out.append(("capture-undefined-var-hard-fails", raised, {"raised": raised}))
    # 20) capture 提取：$.ids[0] 两侧各得其所
    vb2 = {"count": 1, "ids": ["d-aaaa"]}
    vc2 = {"count": 1, "ids": ["d-bbbb"]}
    m_b, m_c = {}, {}
    ev20 = do_captures(vb2, vc2, {"ID": "$.ids[0]"}, m_b, m_c)
    out.append(("capture-extraction-per-side",
                not ev20 and m_b["ID"] == "d-aaaa" and m_c["ID"] == "d-bbbb",
                {"b": m_b, "c": m_c, "ev": ev20}))
    # 21) 断言判罪：假账号建了却查不到（find_by_id 缺失）必须 FAIL
    accs_wo = {"accounts": [{"id": "x1", "enabled": True}]}
    f21 = eval_asserts(accs_wo, [{"path": "$.accounts",
                                  "find_by_id": "d-aaaa", "field": "enabled",
                                  "eq": False}], "baseline")
    f21ok = eval_asserts({"accounts": [{"id": "d-aaaa", "enabled": False}]},
                         [{"path": "$.accounts", "find_by_id": "d-aaaa",
                           "field": "enabled", "eq": False},
                          {"path": "$.accounts", "find_by_id_missing": "ghost"}],
                         "candidate")
    out.append(("assert-find-by-id-catches-missing",
                len(f21) == 1 and f21ok == [], {"bad": f21, "good": f21ok}))
    # 21b) eq 严格类型：candidate 把契约 1 写成 true 必须红（Python True==1 陷阱）
    f21b = eval_asserts({"count": True}, [{"path": "$.count", "eq": 1}], "c")
    f21b2 = eval_asserts({"count": 1}, [{"path": "$.count", "eq": 1}], "c")
    f21b3 = eval_asserts({"count": True}, [{"path": "$.count", "eq": True}], "c")
    out.append(("assert-eq-strict-bool-vs-int",
                len(f21b) == 1 and f21b2 == [] and f21b3 == [],
                {"true_vs_1": len(f21b), "1_vs_1": len(f21b2), "true_vs_true": len(f21b3)}))
    # 21c) 共享库污染探测：对侧前缀出现在本侧列表 → 红；未出现 → 绿；
    #      同源自比（SAME_SOURCE=1）带 unless_shared → 如实 skip 不算罪也不算证
    contamin = {"accounts": [{"id": "liche-lc-dummy-c-abc12345"}]}
    clean = {"accounts": [{"id": "liche-lc-dummy-b-abc12345"}]}
    pd_p = [{"path": "$.accounts",
             "find_by_prefix_missing": "liche-lc-dummy-c",
             "unless_shared_source": True}]
    f21c_bad = eval_asserts(contamin, pd_p, "baseline", {"SAME_SOURCE": "0"})
    f21c_ok = eval_asserts(clean, pd_p, "baseline", {"SAME_SOURCE": "0"})
    f21c_skip = eval_asserts(contamin, pd_p, "baseline", {"SAME_SOURCE": "1"})
    out.append(("assert-shared-source-contamination-detect",
                len(f21c_bad) == 1 and f21c_ok == [] and f21c_skip == [],
                {"cross": len(f21c_bad), "clean": len(f21c_ok), "self": len(f21c_skip)}))
    # 22) 断言值罪：eq 不成立即报（不是只查键存在）
    f22 = eval_asserts({"ok": True}, [{"path": "$.ok", "eq": False}], "baseline")
    out.append(("assert-eq-catches-value", len(f22) == 1, f22))
    # 22b) len_eq 三态（D-02 实跑抓出的洞固化为哨兵）：dict 键数/list 长度/错值
    f22b = eval_asserts({"paths": {"a": 1, "b": 2}}, [{"path": "$.paths", "len_eq": 2}], "c")
    f22b2 = eval_asserts({"paths": {"a": 1, "b": 2}}, [{"path": "$.paths", "len_eq": 3}], "c")
    f22b3 = eval_asserts({"xs": [1, 2]}, [{"path": "$.xs", "len_eq": 2}], "c")
    out.append(("assert-len-eq-dict-list-wrong",
                f22b == [] and len(f22b2) == 1 and f22b3 == [],
                {"dict-ok": f22b, "dict-wrong": f22b2, "list-ok": f22b3}))
    # 23) 嵌套方向（recent_results 契约面）：违规必报 + 全空可见 skip
    nest_spec = {"path": "$.accounts[*].recent_results", "by": "at",
                 "direction": "asc"}
    nest_ok = order_check(
        {"accounts": [{"id": "a", "recent_results": [{"at": 1.0}, {"at": 2.0}]}]},
        {"accounts": [{"id": "a", "recent_results": [{"at": 5.0}, {"at": 6.0}]}]},
        nest_spec)
    nest_bad = order_check(
        {"accounts": [{"id": "a", "recent_results": [{"at": 9.0}, {"at": 2.0}]}]},
        {"accounts": [{"id": "a", "recent_results": [{"at": 5.0}, {"at": 6.0}]}]},
        nest_spec)
    nest_empty = order_check({"accounts": [{"id": "a", "recent_results": []}]},
                             {"accounts": [{"id": "a", "recent_results": []}]},
                             nest_spec)
    nest_halfok = order_check(
        {"accounts": [{"id": "a", "recent_results": [{"at": 1.0}, {"at": 2.0}]}]},
        {"accounts": [{"id": "a", "recent_results": []}]},   # 新实例无历史=合法
        nest_spec)
    out.append(("order-nested-direction-modes",
                nest_ok["pass"] is True and nest_bad["pass"] is False
                and nest_empty["pass"] is None and nest_empty["no_sample"]
                and nest_halfok["pass"] is True,
                {"ok": nest_ok["pass"], "bad": nest_bad["pass"],
                 "empty": nest_empty["pass"], "half-empty": nest_halfok["pass"]}))
    # 24) 未解析 env 占位符必须炸在发送之前（worktree env 漏配假绿事故防回归：
    #     绝不允许把 "Bearer ${KEY}" 字面量发出去换"两侧一致 401"的全绿）
    try:
        execute({"path": "/x", "headers": {"Authorization": "Bearer ${NO_SUCH}"}},
                "http://127.0.0.1:1", {}, 1)
        raised24 = False
    except ValueError as e:
        raised24 = "unresolved env placeholder" in str(e)
    out.append(("missing-env-placeholder-hard-fails-before-send", raised24,
                {"raised": raised24}))

    return out


def _clip_detail(det):
    try:
        s = json.dumps(det, ensure_ascii=False, default=str)
    except Exception:
        s = repr(det)
    return s[:400]


# ---------------------------------------------------------------------------
# 报告
# ---------------------------------------------------------------------------

def write_md(path: Path, report: dict):
    s = report["summary"]
    L = ["# 对拍报告（conformance report）", ""]
    L.append(f"- baseline: `{s['baseline']}`")
    L.append(f"- candidate: `{s['candidate']}`"
             + ("　⚠ **自测模式**（candidate==baseline：同一服务对拍自身，验证裁判零假阳性）"
                if s["self_mode"] else ""))
    L.append(f"- 时间：{s['ts']}")
    L.append(f"- **结论：{s['pass']}/{s['total']} 通过，{s['fail']} 失败**"
             + ("　✅ 自测通过" if s["pass"] == s["total"] and s["fail"] == 0 else "　❌ 存在未通过项"))
    L.append(f"- 判定层级统计：L1 ✓{s['by_level']['L1']['pass']}/✗{s['by_level']['L1']['fail']}"
             f"　L2 ✓{s['by_level']['L2']['pass']}/✗{s['by_level']['L2']['fail']}"
             f"/n-a {s['by_level']['L2']['na']}　L3 ✓{s['by_level']['L3']['pass']}"
             f"/✗{s['by_level']['L3']['fail']}/n-a {s['by_level']['L3']['na']}")
    L.append("")
    if report.get("engine_selftest") is not None:
        es = s.get("engine_selftest", {})
        L.append(f"## 引擎自测（离线漂移注入 ×{es.get('total')}）："
                 f"{es.get('pass')}/{es.get('total')} 通过")
        L.append("")
        for e in report["engine_selftest"]:
            L.append(f"- [{'x' if e['pass'] else ' '}] `{e['check']}`"
                     + ("" if e["pass"] else f" — {e['detail']}"))
        L.append("")
    L.append("## 归一化规则（每条必须给理由；只作用于值与顺序，不碰键集合与类型）")
    L.append("")
    for r in report["normalization_rules"]:
        act = (f"replace→`{r['replace']}`" if "replace" in r else
               f"regex→`{r.get('regex')}` 替换为 `{r.get('with')}`" if "regex" in r else
               f"sort_by=`{r.get('sort_by')}`")
        L.append(f"- `{r['path']}`（{act}）")
        L.append(f"  - **理由**：{r.get('why', '（未写理由 —— 不合格）')}")
    L.append("")
    entries = report["allowlist"].get("entries", [])
    L.append(f"## 已知差异豁免清单：{len(entries)} 条（默认空，一切差异默认报错）")
    for a in entries:
        rc = a.get("requires_check")
        L.append(f"- `{a.get('id')}` case=`{a.get('case')}` path=`{a.get('path')}` "
                 f"结构豁免={bool(a.get('covers_structure'))}"
                 + (f" **requires_check=`{rc}`（豁免≠放行：{rc} 不过则整体 FAIL）**"
                    if rc else "")
                 + f" — {a.get('why')}"
                 f"（批准：**{a.get('approved_by')}**）")
    if not entries:
        L.append("- （空）")
    L.append("")
    sh = s.get("sentinel_hits", 0)
    L.append(f"## 敏感值硬防线（sentinel）：本轮命中 {sh} 处")
    L.append("")
    if sh:
        L.append("以下位置的响应值命中「疑似密钥形态」，**已替换为 sha256 指纹**——")
        L.append("等值判定能力保留（同值同指纹可比对；异值指纹不同照样 FAIL），明文零落盘：")
        L.append("")
        for r in report["cases"]:
            se = r.get("sentinel") or {}
            for e in (se.get("events") or [])[:20]:
                L.append(f"- `{r['id']}` [{e.get('side')}] `{e['path']}` "
                         f"检测器={e['detector']} 命中={e['match']} "
                         f"动作={e['action']} 指纹={e.get('fingerprint','-')}")
    else:
        L.append("- （无命中：本轮所有响应体不含已知敏感字段名/密钥值形态）")
    L.append("")
    L.append("## 逐端点结论")
    L.append("")
    L.append("| id | 方法 | 路径 | 预期 | 结论 | L1 | L2 | L3 | ORDER | ST-DOM | 重试轮 | 备注 |")
    L.append("|---|---|---|---|---|---|---|---|---|---|---|---|")
    for r in report["cases"]:
        def m(lvl):
            p = r.get(lvl, {}).get("pass")
            if lvl == "L3" and r.get("L3", {}).get("observed"):
                return "obs" + ("✓" if p is True else "✗" if p is False else "")
            return "✓" if p is True else "✗" if p is False else "n/a"
        note = ("FLAKY" if r.get("flaky") else "") + \
               ("" if not r.get("unmet") else f" unmet={','.join(r['unmet'])}") + \
               (" " + r.get("note", ""))
        L.append(f"| `{r['id']}` | {r['method']} | `{r['path']}` | {r['expect']} "
                 f"| **{r['verdict']}** | {m('L1')} | {m('L2')} | {m('L3')} "
                 f"| {m('ORDER') if 'ORDER' in r else '—'} "
                 f"| {m('ST-DOM') if 'ST-DOM' in r else '—'} "
                 f"| {r.get('attempts', 1)} | {note.strip()} |")
    L.append("")
    dom_cases = [r for r in report["cases"] if "ST-DOM" in r]
    if dom_cases:
        L.append("## 页面结构等价（ST-DOM · ADR-006 正式判据；L3 obs = 字节仅记录不判）")
        L.append("")
        L.append("| case | ST-DOM | 节点 B/C | id B/C | class(去重) B/C | data-* B/C | 资源数 | 结构差异 |")
        L.append("|---|---|---|---|---|---|---|---|")
        for r in dom_cases:
            sd = r["ST-DOM"]
            if "nodes" not in sd:
                L.append(f"| `{r['id']}` | ✗ | 不可比 | — | — | — | — | — |")
                continue
            nb, nc = sd["nodes"]
            ib, ic = sd["ids"]
            cb, cc = sd["classes"]
            db, dc = sd["data_attrs"]
            nr = len(sd.get("resources", [[], []])[0])
            L.append(f"| `{r['id']}` | {'✓' if sd['pass'] else '✗'} "
                     f"| {nb}/{nc} | {ib}/{ic} | {cb}/{cc} | {db}/{dc} "
                     f"| {nr} | {sd.get('diff_count', 0)} |")
        L.append("")
        L.append("资源引用清单（两侧集合完全一致是 ADR-006 第 5/6 条不变量）：")
        for r in dom_cases:
            sd = r["ST-DOM"]
            for side, urls in zip(("baseline", "candidate"), sd.get("resources", [])):
                L.append(f"- `{r['id']}` [{side}]: " + ", ".join(f"`{u}`" for u in urls))
        L.append("")
    L.append("## 差异明细（仅未通过项）")
    for r in report["cases"]:
        if r.get("verdict") == "pass" and not r.get("flaky"):
            continue
        blocks = []
        if r.get("flaky"):
            blocks.append(f"- FLAKY：重试 {r.get('attempts')} 轮后通过；各轮 "
                          f"{json.dumps(r.get('earlier_attempts'), ensure_ascii=False)}")
        for lvl in ("L1", "L2", "L3"):
            dd = r.get(lvl, {})
            for d in (dd.get("diffs") or [])[:10]:
                blocks.append(f"- [{lvl}/{r['id']}] `{d.get('path')}` "
                              f"{d.get('kind')}: baseline=`{d.get('baseline', dd.get('baseline'))}` "
                              f"candidate=`{d.get('candidate', dd.get('candidate'))}`")
        if r.get("error"):
            blocks.append(f"- ERROR: {r['error']}")
        if r.get("gate"):
            blocks.append(f"- 🚫 GATE: {r['gate']}（豁免≠放行）")
        sd = r.get("ST-DOM", {})
        for d in (sd.get("diffs") or [])[:20]:
            blocks.append(f"- [ST-DOM/{r['id']}] `{d.get('path')}` {d.get('kind')}: "
                          f"baseline=`{d.get('baseline')}` candidate=`{d.get('candidate')}`")
        if blocks:
            L.append(f"### `{r['id']}` ({r['method']} {r['path']})")
            L += blocks
            L.append("")
    L.append("## 豁免命中（审计痕迹）")
    hits = []
    for r in report["cases"]:
        for lvl in ("L2", "L3"):
            for it in (r.get(lvl, {}).get("allowed") or []):
                hits.append(f"- `{r['id']}` [{lvl}] → allowlist `{it.get('allowlist_id')}`："
                            f"{json.dumps(it.get('diff'), ensure_ascii=False)} — "
                            f"{it.get('why', '')}（批准 {it.get('approved_by', '')}）")
    L += hits if hits else ["- （无）"]
    L.append("")
    path.write_text("\n".join(L), encoding="utf-8")


# ---------------------------------------------------------------------------
# 主程序
# ---------------------------------------------------------------------------

def main(argv=None):
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--baseline", default="http://127.0.0.1:9120")
    ap.add_argument("--candidate", default=None, help="缺省=baseline（自测模式）")
    ap.add_argument("--cases", default=str(HERE / "cases.json"))
    ap.add_argument("--rules", default=str(HERE / "normalize.rules.json"))
    ap.add_argument("--allowlist", default=str(HERE / "allowlist.json"))
    ap.add_argument("--env-file",
                    default=str(HERE.parents[2] / "zcode2api" / ".env"))
    ap.add_argument("--timeout", type=float, default=20.0)
    ap.add_argument("--out-dir", default=str(HERE / "latest"))
    ap.add_argument("--include-tag", action="append", default=[],
                    help="追加运行含该 tag 的 case（默认只跑 safe：只读/负例/404 锚）")
    ap.add_argument("--filter", default=None, help="只跑 id 含子串的 case")
    ap.add_argument("--retries", type=int, default=2,
                    help="失败时额外重试轮数（区分外部并发写导致的 FLAKY 与系统性差异）")
    ap.add_argument("--no-sentinel", action="store_true",
                    help="关闭敏感值硬防线（仅调试用；正式对拍绝不开）")
    ap.add_argument("--engine-selftest", action="store_true",
                    help="离线夹具漂移自测（不触网），验证裁判判定可信")
    ap.add_argument("--dom-selftest", action="store_true",
                    help="DOM 验证器 11 项防假绿自测（ADR-006），不触网；"
                         "--engine-selftest 时自动附带")
    args = ap.parse_args(argv)

    candidate = args.candidate or args.baseline
    ef = Path(args.env_file)
    if not ef.is_file():
        # worktree 等非常规深度：向上找 zcode2api/.env 兄弟目录（主仓路径优先）
        d = HERE
        for _ in range(6):
            d = d.parent
            if (d / "zcode2api" / ".env").is_file():
                ef = d / "zcode2api" / ".env"
                break
    env = load_dotenv(ef)
    rules = json.loads(Path(args.rules).read_text(encoding="utf-8"))
    normalizer = Normalizer(rules)
    allow_doc = json.loads(Path(args.allowlist).read_text(encoding="utf-8"))
    allowlist = allow_doc.get("entries", [])
    cases_doc = json.loads(Path(args.cases).read_text(encoding="utf-8"))
    header_keys = cases_doc.get("headers_compared", HEADER_KEYS_DEFAULT)
    sentinel = None if args.no_sentinel else Sentinel(
        json.loads(Path(HERE / "sentinel.json").read_text(encoding="utf-8"))
        if (HERE / "sentinel.json").is_file() else None)

    # ---- 服务可达性门禁（Catherine 2026-10-16 假红实战）----
    # 若候选没起，每个 case 都会走满重试后判「每轮均不一致」，输出 49 条
    # L1:FAIL + flaky=49 —— 这个形态与【真实契约破坏】几乎无法区分，会把
    # 排查者引去查代码。实测发生一次（合并操作后候选被停，19120 curl→000）。
    # 故在开跑前显式探针两侧，任一不通即【拒绝开跑】并指名是哪一侧，
    # 而不是产出一份看似"全面崩塌"的报告。
    if not (args.engine_selftest or args.dom_selftest):
        def _probe(base: str, label: str):
            import urllib.error
            import urllib.request as _u
            try:
                req = _u.Request(base.rstrip("/") + "/v1/models", method="GET")
                _u.urlopen(req, timeout=5)
                return None
            except urllib.error.HTTPError:
                return None  # HTTP 层有响应即视为"在听"（401/404 都算活着）
            except Exception as e:  # noqa: BLE001
                return f"{label} {base} 不可达：{e}"

        probe_errors = [x for x in (_probe(args.baseline, "baseline"),
                                    _probe(candidate, "candidate")) if x]
        if probe_errors:
            print("== zhipucode2api conformance harness ==")
            print(f"baseline  = {args.baseline}")
            print(f"candidate = {candidate}")
            for pe in probe_errors:
                print(f"!! 服务未就绪：{pe}")
            print()
            print("拒绝开跑：一侧或两侧服务未就绪。若强行开跑，每个 case 都会走满"
                  "重试后报『每轮均不一致』，产出 49 条 L1:FAIL + flaky=49 —— 该形态"
                  "与真实契约破坏无法区分，会误导排查方向。")
            print("排查：ss -tlnp | grep ':PORT ' 确认监听；再 curl <base>/v1/models。")
            return 2

    run_tags = set(["safe"]) | set(args.include_tag)
    selected = [c for c in cases_doc["cases"]
                if run_tags & set(c.get("tags", []))
                and (not args.filter or args.filter in c["id"])]

    engine_results = (run_engine_selftest(allow_entries=allowlist)
                      if args.engine_selftest else None)
    dom_results = (run_dom_selftest()
                   if (args.dom_selftest or args.engine_selftest) else None)
    if dom_results is not None:
        engine_results = (engine_results or []) + [
            {"check": n, "pass": bool(ok), "detail": _clip_detail(det)}
            for n, ok, det in dom_results]

    results = []
    for case in selected:
        try:
            results.append(compare_case(case, args.baseline, candidate, env,
                                        normalizer, allowlist, args.timeout,
                                        header_keys, retries=max(0, args.retries),
                                        sentinel=sentinel))
        except Exception as e:  # noqa: BLE001 裁判不因单 case 崩溃
            results.append({"id": case["id"], "method": case.get("method", "GET"),
                            "path": case["path"], "expect": case.get("expect", ""),
                            "verdict": "fail", "error": f"{type(e).__name__}: {e}",
                            "L1": {"pass": False}, "L2": {"pass": False},
                            "L3": {"pass": False}})

    summary = {
        "baseline": args.baseline, "candidate": candidate,
        "self_mode": candidate == args.baseline,
        "total": len(results),
        "pass": sum(1 for r in results if r.get("verdict") == "pass"),
        "fail": sum(1 for r in results if r.get("verdict") == "fail"),
        "flaky": sum(1 for r in results if r.get("flaky")),
        "sentinel_hits": sum(r.get("sentinel", {}).get("hits", 0) for r in results),
        "order_context": (
            "self-compare: engine-consistency only (NOT cross-service proof)"
            if candidate == args.baseline
            else ("cross-service: ORDER PASS = candidate order/direction matches legacy"
                  if any("ORDER" in r for r in results) else "n/a")),
        "by_level": {lvl: {
            "pass": sum(1 for r in results if r.get(lvl, {}).get("pass") is True),
            "fail": sum(1 for r in results if r.get(lvl, {}).get("pass") is False),
            "na": sum(1 for r in results if r.get(lvl, {}).get("pass") is None),
        } for lvl in ("L1", "L2", "L3", "ORDER", "ST-DOM", "ASSERT")},
        "ts": time.strftime("%Y-%m-%d %H:%M:%S%z"),
    }
    if engine_results is not None:
        summary["engine_selftest"] = {
            "total": len(engine_results),
            "pass": sum(1 for e in engine_results if e["pass"]),
            "fail": sum(1 for e in engine_results if not e["pass"])}

    report = redact_tree({"summary": summary, "engine_selftest": engine_results,
                          "normalization_rules": rules, "allowlist": allow_doc,
                          "cases": results}, env)
    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=1),
                                         encoding="utf-8")
    write_md(out_dir / "report.md", report)

    print("== zhipucode2api conformance harness ==")
    print(f"baseline  = {args.baseline}")
    print(f"candidate = {candidate}"
          + ("    [SELFTEST: same service]" if summary["self_mode"] else ""))
    if engine_results is not None:
        es = summary["engine_selftest"]
        print(f"-- engine selftest (offline drift injection): {es['pass']}/{es['total']} pass")
        for e in engine_results:
            print(f"   [{'OK' if e['pass'] else 'XX'}] {e['check']}"
                  + ("" if e["pass"] else f"   {e['detail']}"))
    print(f"-- cases (tags ⊆ {sorted(run_tags)}): {summary['total']}  "
          f"pass={summary['pass']}  fail={summary['fail']}  flaky={summary['flaky']}"
          f"  sentinel-hits={summary['sentinel_hits']}")
    if summary.get("order_context", "").startswith("self-compare"):
        print("   ⚠ 语境提醒（CR-37/Hilkiah）：candidate==baseline 为自比模式——ORDER/ST-DOM"
              " 的 PASS 只证明【引擎自洽性】（哨兵不永红、归一化不吞单侧断言），"
              "【不能】当作'跨服务顺序/结构等价已验证'的证据；后者仅在"
              " candidate≠baseline 的正式对拍运行中产生。")
    for r in results:
        line = f"   [{r.get('verdict','?'):4}] {r['id']:<28}"
        for lvl in ("L1", "L2", "L3"):
            p = r.get(lvl, {}).get("pass")
            line += f" {lvl}:{'PASS' if p is True else 'FAIL' if p is False else 'n/a'}"
        if "ORDER" in r:
            p = r["ORDER"].get("pass")
            if p is None and r["ORDER"].get("no_sample"):
                line += " ORDER:n/a(no-sample)"
            else:
                line += f" ORDER:{'PASS' if p is True else 'FAIL' if p is False else 'n/a'}"
        if "ASSERT" in r:
            p = r["ASSERT"].get("pass")
            line += f" ASSERT:{'PASS' if p is True else 'FAIL' if p is False else 'n/a'}"
        if "ST-DOM" in r:
            p = r["ST-DOM"].get("pass")
            line += f" ST-DOM:{'PASS' if p is True else 'FAIL' if p is False else 'n/a'}"
            if r.get("L3", {}).get("observed"):
                line += " (L3-observed)"
        if r.get("recovered"):
            line += "  (FLAKY-pass: 首轮漂移、重试轮一致)"
        elif r.get("flaky"):
            line += "  (每轮均不一致：重试穷尽，非偶发)"
        if r.get("unmet"):
            line += "  unmet=" + ",".join(r["unmet"])
        print(line)
    print(f"-- report: {out_dir / 'report.md'}")
    bad = (summary["fail"] > 0
           or (engine_results is not None and summary["engine_selftest"]["fail"] > 0))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
