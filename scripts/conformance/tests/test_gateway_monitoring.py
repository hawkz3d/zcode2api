"""GW-013 请求监控 · base-URL 可指向版。

原文件：zcode2api/tests/integration/test_gateway_monitoring.py
改动纪律：断言语义照抄；进程内 reqlog.clear() 换成 POST /admin/api/monitoring/clear
（同为「清空」语义，门控）。依赖「隔离池只 seed mon-1」的条目内容断言在共享生产池
不可复现 → 显式 skip 并记录原因（README §改造映射），不悄悄删。
"""
from __future__ import annotations

import pytest

from conftest import need_mutation, need_real_upstream

_MSG = {"model": "GLM-5.3-Flash", "max_tokens": 8,
        "messages": [{"role": "user", "content": "你好世界"}]}


class TestMonitoringRecording:
    def test_monitoring_page_served(self, client):
        """原测试同款断言：页面可达且含「请求监控」。零副作用，无需门控。"""
        res = client.get("/admin/monitoring")
        assert res.status_code == 200
        assert "请求监控" in res.text

    def test_clear_endpoint(self, client, admin_headers):
        need_mutation()
        res = client.post("/admin/api/monitoring/clear", headers=admin_headers)
        assert res.status_code == 200
        res = client.get("/admin/api/monitoring", headers=admin_headers)
        assert res.json()["entries"] == []

    def test_messages_success_recorded(self, client, admin_headers):
        """清空后一条真实对话 → 监控应出现 endpoint=messages / ok=True / status=200
        / t_first,t_total 非空 / preview 含请求文本 的条目（字段断言逐字照抄原测试；
        仅 account=="mon-1" 一条因原前置是 mock 隔离池无法在共享池复现 → 见下）。"""
        need_mutation()
        need_real_upstream()
        assert client.post("/admin/api/monitoring/clear",
                           headers=admin_headers).status_code == 200
        res = client.post("/v1/messages", json=_MSG)
        assert res.status_code == 200
        entries = client.get("/admin/api/monitoring",
                             headers=admin_headers).json()["entries"]
        mine = [e for e in entries if e["preview"] and "你好世界" in e["preview"]]
        assert mine, "本次对话未被记录"
        e = mine[-1]
        assert e["endpoint"] == "messages"
        assert e["model"] == "GLM-5.3-Flash"
        assert e["ok"] is True and e["status"] == 200
        assert e["t_first"] is not None and e["t_total"] is not None
        assert "你好世界" in e["preview"]

    def test_messages_account_name_assertion(self):
        pytest.skip("无法用于对拍：原断言 account=='mon-1' 的前置是 mock 隔离池"
                    "（seed_account 只放 mon-1）。共享池 round-robin 选号不可指定；"
                    "隔离受控环境（独立库+mock 上游）可恢复该断言。原因：进程内注入能力")
        raise AssertionError  # 不可达；保留条目以示断言存在过

    def test_chat_completions_tokens_recorded(self):
        pytest.skip("无法用于对拍：input_tokens==10/output_tokens==5 是 mock 上游"
                    "固定 usage；真实上游 token 数随生成变化。原因：mock 数值前置")
        raise AssertionError

    def test_upstream_4xx_recorded(self):
        pytest.skip("无法用于对拍：not_found 场景需 mock.state.sequences 注入。"
                    "原因：mock 场景控制")
        raise AssertionError

    def test_no_account_error_recorded(self):
        pytest.skip("无法用于对拍：需 create_app() 空池进程内构造。"
                    "隔离新实例（空库）可恢复。原因：进程内 app 构造")
        raise AssertionError
