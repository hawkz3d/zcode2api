#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""ADR-003 双写不变量检查器 —— 只读 accounts.db，验证 enabled 的列/数据一致性。

背景（captain 实测裁定，docs/contract/adr-003-enabled-write-semantics.md）：
  老服务对 enabled 是【双写】：accounts.enabled 列（INTEGER 0/1）与
  data JSON 里的 enabled 字段始终同步（建号/停用/启用/重复停用/改名 五路验证）。
  Go 候选的 SetEnabled 必须复刻该语义 —— 尤其要抓「只写列不写 data」缺陷：
  该缺陷会让 API（读 data）在停用后仍显示 true，与库状态自相矛盾。

两判据：
  硬不变量（语义一致）：bool_semantic(列 enabled) == bool_semantic(data.enabled)
      —— laohao 脏值 "True" 语义=True，列=1 语义=True → 语义一致，不算违反双写；
  形态分布（类型审计）：data.enabled 的值类型清单（bool/str/int）
      —— 脏值驻留是【读层规范化】话题（ADR-002），本脚本只报告不定罪。

模式：
  --audit                     全账号双写不变量 + 形态分布（基线体检，默认）
  --after-disabled --account ID  断言：列=0 且 data.enabled 语义 False（写行为对拍后跑）
  --after-enabled   --account ID  断言：列=1 且 data.enabled 语义 True
  只读连接（mode=ro）；WAL 库可安全并发读，绝不写。

对拍流程中的位置（captain 终裁 1/2）：
  隔离实例上：harness 跑 SEQ-toggle-roundtrip（API 层看到状态翻转）
  → 本脚本 --after-disabled/--after-enabled 验证列+data 双双变化（写等价）
  → 再 GET /admin/api/accounts 断言读出一致性（防「读 data 写列」矛盾）。
"""
import argparse
import json
import sqlite3
import sys
from pathlib import Path

def bool_semantic(v):
    """把列 INTEGER 与 data JSON 里任意形态的值映射到语义布尔；无法判定返回 None。"""
    if isinstance(v, bool):
        return v
    if isinstance(v, (int, float)):
        return v == 1 if v in (0, 1) else None
    if isinstance(v, str):
        s = v.lower()
        if s in ("true", "1"):
            return True
        if s in ("false", "0"):
            return False
        return None
    return None


def load_env(path):
    env = {}
    p = Path(path)
    if p.is_file():
        for line in p.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                k, _, v = line.partition("=")
                env[k.strip()] = v.strip().strip('"').strip("'")
    return env


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--db", default=None, help="accounts.db 路径（默认按 --env-file 的 ZCODE_DATA_DIR 解析）")
    ap.add_argument("--env-file", default="/opt/zcode2api/.env")
    ap.add_argument("--base-dir", default="/opt/zcode2api",
                    help="ZCODE_DATA_DIR 为相对路径时的解析基准")
    ap.add_argument("--audit", action="store_true", default=False)
    ap.add_argument("--after-disabled", action="store_true")
    ap.add_argument("--after-enabled", action="store_true")
    ap.add_argument("--account", default=None)
    args = ap.parse_args(argv)

    db = args.db
    if not db:
        env = load_env(args.env_file)
        dd = env.get("ZCODE_DATA_DIR", "data")
        dd = dd if Path(dd).is_absolute() else str(Path(args.base_dir) / dd)
        db = str(Path(dd) / "accounts.db")
    dbp = Path(db)
    if not dbp.is_file():
        print("FAIL: DB 不存在: %s" % dbp)
        return 2

    conn = sqlite3.connect("file:%s?mode=ro" % dbp, uri=True)
    conn.row_factory = sqlite3.Row
    rows = conn.execute("SELECT id, enabled, data FROM accounts").fetchall()
    conn.close()

    accounts = []
    for r in rows:
        try:
            data = json.loads(r["data"]) if r["data"] else {}
        except Exception:
            data = {"__parse_error__": True}
        accounts.append({"id": r["id"], "col_enabled": r["enabled"],
                         "data_enabled": data.get("enabled", "<absent>")})

    hard_fail, report = [], []
    for a in accounts:
        bs_col = bool_semantic(a["col_enabled"])
        bs_data = bool_semantic(a["data_enabled"])
        tv = type(a["data_enabled"]).__name__
        line = ("id=%-32s 列=%-4r(语义=%s)  data.enabled=%-8r(类型=%s, 语义=%s)"
                % (a["id"], a["col_enabled"], bs_col, a["data_enabled"], tv, bs_data))
        if bs_col is None or bs_data is None or bs_col != bs_data:
            hard_fail.append(line)
            line += "   ← 双写不变量违反!"
        report.append(line)

    print("== db_check · %s（只读 mode=ro） ==" % dbp)
    for l in report:
        print("  " + l)
    dist = {}
    for a in accounts:
        dist[type(a["data_enabled"]).__name__] = dist.get(type(a["data_enabled"]).__name__, 0) + 1
    print("  data.enabled 类型分布:", dist, "（脏值驻留=读层规范化话题 ADR-002，此处仅审计）")

    rc = 0
    if args.audit or not (args.after_disabled or args.after_enabled):
        print("  审计结论: 硬不变量违反 %d 条 %s" % (len(hard_fail), "→ FAIL" if hard_fail else "→ 全部语义一致 PASS"))
        rc = 1 if hard_fail else 0

    if args.account and (args.after_disabled or args.after_enabled):
        acc = next((a for a in accounts if a["id"] == args.account), None)
        if acc is None:
            print("  FAIL: 账号不存在 %s" % args.account)
            return 2
        bs_col = bool_semantic(acc["col_enabled"])
        bs_data = bool_semantic(acc["data_enabled"])
        want = (False if args.after_disabled else True)
        ok_col, ok_data = bs_col is want, bs_data is want
        print("  专项判据 account=%s 期望 enabled=%s：列语义=%s(%s) data语义=%s(%s)"
              % (args.account, want, bs_col, "✓" if ok_col else "✗",
                 bs_data, "✓" if ok_data else "✗"))
        if ok_col and ok_data:
            print("  PASS: 列与 data 双双翻转（双写等价）")
        else:
            print("  FAIL: 「只写列不写 data」或半写 —— 正是本检查要抓的缺陷")
            rc = 1
    return rc


if __name__ == "__main__":
    sys.exit(main())
