"""网关 API Key 鉴权 · base-URL 可指向版。

原文件：zcode2api/tests/integration/test_gateway_auth.py
改动纪律：断言的状态码语义逐条照抄；进程内 fixture（fresh_app.set_setting /
seed_account）换成真实管理 API（PUT /admin/api/settings）等价重建，
并加 Z2A_ALLOW_MUTATION 安全门（gateway_key 写生产会让真实用户瞬间 401）。

原断言映射：
  test_no_key_configured_allows_anonymous   → 保留（未配置放行 = 生产现状）
  test_missing_key_rejected_401             → 保留（需先配 key，门控）
  test_wrong_key_rejected_403               → 保留（门控）
  test_bearer_and_x_api_key_both_accepted   → 保留（需池内可服务账号 → 真实上游，双门控）
  test_models_endpoint_gated_too            → 保留（门控）
seed_account 在原测试里是为保证「池非空所以能走到 200」的前置；黑盒形态用
「被测实例已有可服务账号」作前置（候选验收时 DB 复制自生产即满足）。
"""
from __future__ import annotations

import pytest

from conftest import need_mutation, need_real_upstream

_GATEWAY_KEY = "sk-test-gateway-0001"
_MESSAGES_BODY = {"model": "GLM-5.2",
                  "messages": [{"role": "user", "content": "hi"}]}


@pytest.fixture
def gateway_key_configured(client, admin_headers):
    """用管理 API 配置网关 key，收尾恢复原值（对应原 set_setting 链）。"""
    need_mutation()
    original = client.get("/admin/api/settings", headers=admin_headers)
    assert original.status_code == 200, "GET /admin/api/settings 失败：管理面不可用"
    settings = original.json()
    resp = client.put("/admin/api/settings", headers=admin_headers,
                      json={"admin_key": settings["admin_key"],
                            "gateway_key": _GATEWAY_KEY,
                            "quota_refresh_interval":
                                settings["quota_refresh_interval"]})
    assert resp.status_code in (200, 204), f"配置 gateway_key 失败: {resp.status_code}"
    try:
        status = client.get("/admin/api/status", headers=admin_headers).json()
        assert status.get("gateway_key_set") is True, "写入后 key 未生效"
        yield _GATEWAY_KEY
    finally:
        client.put("/admin/api/settings", headers=admin_headers,
                   json={"admin_key": settings["admin_key"],
                         "gateway_key": settings["gateway_key"],
                         "quota_refresh_interval":
                             settings["quota_refresh_interval"]})


class TestGatewayAuth:
    def test_no_key_configured_allows_anonymous(self, client, admin_headers):
        """未配置 key 时保持放行（现有部署默认形态，回归锁定）。"""
        status = client.get("/admin/api/status", headers=admin_headers).json()
        assert status["gateway_key_set"] is False
        # 原测试打 POST /v1/messages 验证放行；黑盒共享池下那会消耗真实额度，
        # 改用同一闸门的 GET /v1/models（放行语义相同、零上游成本）。
        res = client.get("/v1/models")
        assert res.status_code != 401 and res.status_code != 403

    def test_missing_key_rejected_401(self, client, gateway_key_configured):
        res = client.post("/v1/messages", json=_MESSAGES_BODY)
        assert res.status_code == 401

    def test_wrong_key_rejected_403(self, client, gateway_key_configured):
        res = client.post("/v1/messages", json=_MESSAGES_BODY,
                          headers={"x-api-key": "sk-wrong"})
        assert res.status_code == 403

    def test_bearer_and_x_api_key_both_accepted(self, client, admin_headers,
                                                gateway_key_configured):
        need_real_upstream()  # 200 需真实上游往返
        for headers in ({"Authorization": "Bearer " + gateway_key_configured},
                        {"x-api-key": gateway_key_configured}):
            res = client.post("/v1/chat/completions", json=_MESSAGES_BODY,
                              headers=headers)
            assert res.status_code == 200, headers

    def test_models_endpoint_gated_too(self, client, gateway_key_configured):
        """/v1/models 与 /v1/messages 同闸门。"""
        assert client.get("/v1/models").status_code == 401
        res = client.get("/v1/models", headers={"x-api-key": gateway_key_configured})
        assert res.status_code == 200
