"""GW-011 /v1/chat/completions · base-URL 可指向版。

原文件：zcode2api/tests/integration/test_gateway_openai.py
改动纪律：可指向的断言逐字保留；依赖 mock 上游固定文本/固定 usage/上游载荷
（mock.state.calls）的断言显式 skip + 记录原因，不悄悄删。
"""
from __future__ import annotations

import pytest

from conftest import need_mutation, need_real_upstream


class TestChatCompletions:
    def test_invalid_payload_rejected(self, client):
        """缺 model → 400 + error.type=invalid_request_error。
        网关校验层，零上游，可自由双指。"""
        res = client.post("/v1/chat/completions",
                          json={"messages": [{"role": "user", "content": "hi"}]})
        assert res.status_code == 400
        assert res.json()["error"]["type"] == "invalid_request_error"

    def test_nonstream_basic_real_upstream(self, client):
        """非流式最小契约：object/chat.completion 结构、choices[0].message、
        finish_reason、usage 计数为正整数。原断言锁定 mock 固定值
        （'Hello from mock upstream'、prompt_tokens==10、model 映射 GLM-5.3-Flash），
        真上游只能保留【结构语义】：object/finish_reason/usage 键与类型、content 非空。
        模型名映射（glm-5.3-flash→GLM-5.3-Flash）仍是契约，保留断言。"""
        need_real_upstream()
        res = client.post("/v1/chat/completions",
                          json={"model": "glm-5.3-flash",
                                "messages": [{"role": "user", "content": "say ok"}]})
        assert res.status_code == 200
        data = res.json()
        assert data["object"] == "chat.completion"
        assert data["model"] == "GLM-5.3-Flash"
        assert data["choices"][0]["message"]["content"]
        assert data["choices"][0]["finish_reason"] == "stop"
        assert isinstance(data["usage"]["prompt_tokens"], int)
        assert data["usage"]["prompt_tokens"] > 0

    def test_system_message_reaches_upstream_as_system(self):
        pytest.skip("无法用于对拍：断言观测的是网关发给【上游】的载荷 "
                    "mock.state.calls[-1][3]（system 块位置），HTTP 黑盒不可见。"
                    "原因：上游载荷观测能力（需受控 mock 环境）")
        raise AssertionError

    def test_max_tokens_clamped_to_upstream_limit(self):
        pytest.skip("无法用于对拍：max_tokens 钳制结果只能从上游载荷验证 "
                    "（payload['max_tokens']==131072）。原因：同上。"
                    "候选可用受控 mock 套件或抓包验证")
        raise AssertionError

    def test_max_tokens_string_form_clamped(self):
        pytest.skip("无法用于对拍：同 max_tokens 钳制（上游载荷观测）。"
                    "原因：上游载荷观测能力")
        raise AssertionError

    def test_max_tokens_floor_and_passthrough(self):
        pytest.skip("无法用于对拍：同上。原因：上游载荷观测能力")
        raise AssertionError

    def test_stream_returns_openai_chunks(self, client):
        """流式协议形态（chunk object 类型、首帧 delta.role、[DONE] 终止、
        finish 唯一 stop）为契约；原断言中的 'chunk-0' 文本是 mock 产物 →
        改为内容非空聚合。真上游门控。"""
        need_real_upstream()
        res = client.post("/v1/chat/completions",
                          json={"model": "glm-5.3-flash", "stream": True,
                                "messages": [{"role": "user", "content": "say ok"}]})
        assert res.status_code == 200
        assert res.headers["content-type"].startswith("text/event-stream")
        lines = [ln for ln in res.text.splitlines() if ln.startswith("data: ")]
        assert lines[-1] == "data: [DONE]"
        import json as _json
        chunks = [_json.loads(ln[6:]) for ln in lines[:-1]]
        assert all(c["object"] == "chat.completion.chunk" for c in chunks)
        assert chunks[0]["choices"][0]["delta"]["role"] == "assistant"
        text = "".join(c["choices"][0]["delta"].get("content") or "" for c in chunks)
        assert text
        finishes = [c["choices"][0]["finish_reason"] for c in chunks
                    if c["choices"][0]["finish_reason"]]
        assert finishes == ["stop"]

    def test_no_account_returns_503(self):
        pytest.skip("无法用于对拍（指向共享实例时）：前置=空账号池（原测试用 "
                    "create_app() 空池进程内构造）。生产服务必有账号。"
                    "新服务用空库起独立实例即可恢复该断言（error.type="
                    "no_available_account）。原因：进程内空池构造")
        raise AssertionError

    def test_gateway_key_required(self, client, admin_headers):
        """配 key 后无凭证 401；凭证对但无可用账号 → 503（鉴权在调度之前）。
        503 分支需要空池 → 只在空池隔离实例成立；此处保留 401 分支（门控），
        503 断言随 no_account 测试一起记录为【隔离环境恢复】。"""
        need_mutation()
        settings = client.get("/admin/api/settings", headers=admin_headers).json()
        client.put("/admin/api/settings", headers=admin_headers,
                   json={"admin_key": settings["admin_key"],
                         "gateway_key": "sk-gw-test",
                         "quota_refresh_interval": settings["quota_refresh_interval"]})
        try:
            res = client.post("/v1/chat/completions",
                              json={"model": "glm-5.3",
                                    "messages": [{"role": "user", "content": "hi"}]})
            assert res.status_code == 401
        finally:
            client.put("/admin/api/settings", headers=admin_headers,
                       json={"admin_key": settings["admin_key"],
                             "gateway_key": settings["gateway_key"],
                             "quota_refresh_interval": settings["quota_refresh_interval"]})
