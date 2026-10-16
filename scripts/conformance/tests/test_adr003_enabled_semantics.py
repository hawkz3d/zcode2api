"""ADR-003 enabled 双写语义 · 行为等价测试（captain 终裁新增覆盖，非原 6 文件改造）。

来源：docs/contract/adr-003-enabled-write-semantics.md
  老服务对 enabled 双写（列 INTEGER 与 data JSON 恒同步）；Go 候选必须复刻。
  本测试负责 ADR-003 判据 2（读出一致性）的 HTTP 面：
    停用 → API 快照必须显示语义 false（若实现「只写列不写 data」，
    API 读 data 会继续显示 true → 本测试 FAIL，这正是要抓的缺陷）
    启用 → API 快照必须显示语义 true
  判据 1（列=0 且 data.enabled=false 的存储双写）属 DB 面，由
  db_check.py --after-disabled/--after-enabled 在候选 DB 副本上核验
  （操作串联见 README §10；测试进程不碰库）。

纪律（captain 指令）：全程用 zzconf-* 假账号，测后 DELETE 清理；
mutation 门控，绝不指向共享生产 9120。

注：toggle/POST/DELETE 响应形态出自 docs 04 §账号池（优先级 4 文档），
尚未在隔离实例实测；首次运行若状态码/形态与预期不符，按实测修正
【前置与观测通道】，不改本文件三条核心断言（验证意图）。
"""
from __future__ import annotations

import secrets

import pytest

from conftest import need_mutation


class TestEnabledWriteSemantics:
    @pytest.fixture
    def probe_account(self, client, admin_headers):
        need_mutation()
        name = "zzconf-probe-" + secrets.token_hex(4)
        res = client.post("/admin/api/accounts",
                          json={"provider": "zai", "name": name,
                                "secret": "sk-conformance-fake-" + secrets.token_hex(8)},
                          headers=admin_headers)
        assert res.status_code == 200, "POST /admin/api/accounts 假账号入池失败: %s" % res.status_code
        listing = client.get("/admin/api/accounts", headers=admin_headers).json()
        acc = next((a for a in listing["accounts"] if a["name"] == name), None)
        assert acc is not None, "入池后列表查不到假账号"
        yield acc["id"]
        client.delete("/admin/api/accounts/%s" % acc["id"], headers=admin_headers)

    def _api_enabled(self, client, admin_headers, acc_id):
        listing = client.get("/admin/api/accounts", headers=admin_headers).json()
        acc = next(a for a in listing["accounts"] if a["id"] == acc_id)
        return acc["enabled"]

    def test_disable_flips_api_view(self, client, admin_headers, probe_account):
        """判据 2 前半：POST toggle 停用后，API（读 data JSON）必须显示 false。
        抓「只写列不写 data」——列变了而 data 仍旧值时 API 仍显 True。"""
        res = client.post("/admin/api/accounts/%s/toggle" % probe_account,
                          headers=admin_headers)
        assert res.status_code == 200
        assert self._api_enabled(client, admin_headers, probe_account) is False, \
            "停用后 API 仍显示 enabled=True → data JSON 未随列翻转（ADR-003 判据2 违反）"

    def test_reenable_restores_api_view(self, client, admin_headers, probe_account):
        """判据 2 后半：停用再启用后，API 输出与实际状态一致（回到 true，
        且形态是 bool true——候选按 ADR-002 读层规范化）。
        ⚠ 假设 POST toggle 为翻转语义（docs 04「启用/禁用」）；若隔离实测
        证明是定值语义，本测试的第二次调用改为对应通道——前置通道可校准，
        断言（回到 true + 布尔形态）不动。"""
        client.post("/admin/api/accounts/%s/toggle" % probe_account, headers=admin_headers)
        res = client.post("/admin/api/accounts/%s/toggle" % probe_account,
                          headers=admin_headers)
        assert res.status_code == 200
        val = self._api_enabled(client, admin_headers, probe_account)
        assert val is True, "重新启用后 API enabled 应为布尔 true，实际 %r" % (val,)
        assert isinstance(val, bool), "候选按 ADR-002 应输出布尔而非 str，实际类型 %s" % type(val).__name__

    # captain 实测的五路操作中「重复停用」需 toggle 语义实测后补通道：
    # HTTP POST /toggle 若是翻转语义则无法表达「重复停用」，可能需要
    # 带 body 的 set 形态端点。隔离实例首次运行校准后，在此补第三条
    # 断言（enabled 连续停用不产生半翻转状态）。刻意留白 > 写永真断言。
