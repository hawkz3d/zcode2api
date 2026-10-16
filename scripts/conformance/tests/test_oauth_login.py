"""INT-022 后台化 OAuth 登录 · base-URL 可指向版。

原文件：zcode2api/tests/integration/test_oauth_login.py
改动纪律：两条纯 HTTP 可复现断言（未知 flow 幂等 expired；无 key 401）逐字保留；
其余依赖 mock.state.oauth_state 切换、admin_api._login_flows 私有字典、
mock-flow- 前缀、兑换链上游观测（mock.state.calls）→ skip + 原因。
"""
from __future__ import annotations

import pytest


class TestOAuthLoginFlow:
    def test_unknown_flow_expired(self, client, admin_headers):
        """原断言逐字：未知 flow_id 与超时同响应 {\"status\":\"expired\"}（幂等无 404）。"""
        res = client.get("/admin/api/login/poll/nonexistent", headers=admin_headers)
        assert res.status_code == 200
        assert res.json()["status"] == "expired"

    def test_admin_key_required(self, client):
        """原断言逐字：POST /admin/api/login/start 无 key → 401（先于上游 init）。"""
        res = client.post("/admin/api/login/start")
        assert res.status_code == 401

    def test_start_shape_on_real_upstream(self, client, admin_headers):
        """原断言 expires_in==300 与 authorize_url https:// 前缀是真上游也成立的
        契约；flow_id 'mock-flow-' 前缀是 mock 产物（真实为 32hex，实测
        7366a2a2...）→ 该条断言按『mock 产物』记录偏离，保留其余断言。
        ⚠ 会创建真实 OAuth flow（上游 init + 300s 会话）→ mutation+upstream 双门控。"""
        from conftest import need_mutation, need_real_upstream
        need_mutation()
        need_real_upstream()
        res = client.post("/admin/api/login/start", json={"label": "conformance-probe"},
                          headers=admin_headers)
        assert res.status_code == 200
        data = res.json()
        assert data["authorize_url"].startswith("https://")
        assert data["expires_in"] == 300
        assert isinstance(data["flow_id"], str) and data["flow_id"]
        # 原断言 flow_id.startswith("mock-flow-")：mock 产物，真实服务形态不同，
        # 已在文件头与 README 记录，不悄悄删。

    def test_full_flow_ready_pools_account(self):
        pytest.skip("无法用于对拍：oauth_state=ready 由测试进程直改 mock；兑换链"
                    "（z/login→getCustomerInfo→api_keys/copy→入池）观测依赖 "
                    "mock.state.calls；token 脱敏断言依赖 mock 凭证。"
                    "原因：mock 状态注入 + 上游载荷观测")
        raise AssertionError

    def test_flow_ttl_and_reentry_protection(self):
        pytest.skip("无法用于对拍：直改 admin_api._login_flows[fid]['created'] 快进时钟、"
                    "断言私有字典摘除。原因：进程内私有对象操作")
        raise AssertionError

    def test_denied_and_pending_branches(self):
        pytest.skip("无法用于对拍：failed/pending 分支需 mock.state.oauth_state 注入。"
                    "原因：mock 状态注入")
        raise AssertionError
