"""套餐领取（claim）· base-URL 可指向版。

原文件：zcode2api/tests/integration/test_claim.py（489 行）
改动纪律：能在纯 HTTP 复现的断言（鉴权边界、参数校验、404 形态）逐字保留；
依赖 mock 场景值（mock-claim-plan/已经领取过/验证码桩计数）与进程内 monkeypatch
的断言显式 skip + 记录原因。预览/领取真跑属真实上游写操作，本套不自动触发。
"""
from __future__ import annotations

import pytest


class TestClaim:
    def test_claim_requires_admin_key(self, client):
        """原断言：POST /admin/api/claim 无 key → 401（鉴权先于业务）。"""
        res = client.post("/admin/api/claim", json={"account_ids": ["x"]})
        assert res.status_code == 401

    def test_manual_claim_requires_admin_key(self, client):
        res = client.post("/admin/api/claim/manual",
                          json={"account_id": "x", "captcha_verify_param": "p"})
        assert res.status_code == 401

    def test_claim_preview_requires_admin_key(self, client):
        """（原文件未含此条，但 claim/preview 属同族管理面；补锚非改语义：
        GET 无凭证 → 401 {detail: 缺少鉴权凭证} —— 该形态由 harness 对拍锁定。）"""
        res = client.get("/admin/api/claim/preview")
        assert res.status_code == 401

    def test_preview_scenario_values(self):
        pytest.skip("无法用于对拍：preview[0].plan_id=='mock-claim-plan'、grants 过滤、"
                    "'已经领取过' 等都是 mock 上游固定场景值。真实上游的 preview 内容"
                    "属业务数据（新老服务应各自如实反映上游），其【一致性】由 harness "
                    "AD-preview 类端点结构对拍 + 真实冒烟覆盖。原因：mock 场景值")
        raise AssertionError

    def test_claim_success_headers(self):
        pytest.skip("无法用于对拍：断言观测上游收到的验证码头/region/plan body "
                    "（mock.state.calls）与桩求解计数（进程内 monkeypatch）。"
                    "原因：上游载荷观测 + 进程内桩")
        raise AssertionError

    def test_claim_captcha_retry_and_business_receipts(self):
        pytest.skip("无法用于对拍：3007 重试/invalidate/CaptchaSolveError 回执/"
                    "上游网络错误回执全部依赖验证码桩与 mock 场景注入。"
                    "原因：进程内桩 + mock 场景")
        raise AssertionError

    def test_claim_skips_api_key_accounts(self):
        pytest.skip("无法用于对拍（共享实例）：会真的向全部 JWT 账号发起领取"
                    "（真实写上游）。隔离受控环境+mock 可恢复。原因：真实写操作")
        raise AssertionError


class TestManualClaim:
    def test_manual_claim_unknown_account_404(self, client, admin_headers):
        """原断言逐字：未知账号 → 404。参数校验先于上游调用，零成本零副作用。"""
        res = client.post("/admin/api/claim/manual",
                          json={"account_id": "no-such-account-zz",
                                "captcha_verify_param": "p"},
                          headers=admin_headers)
        assert res.status_code == 404

    def test_manual_claim_without_param_rejected(self, client, admin_headers):
        """原断言：缺 captcha_verify_param → outcome.ok False + '缺少验证码参数'。
        ⚠ 需要一个存在的 account_id；且该「参数先于验证码」的校验顺序在生产服务
        未经实测证实（若服务端先解验证码再校验参数，会触发真实求解）→
        保守起见按 mutation 门控，只在隔离实例跑。"""
        from conftest import need_mutation
        need_mutation()
        accounts = client.get("/admin/api/accounts", headers=admin_headers).json()["accounts"]
        jwt_acc = next((a for a in accounts if a["mode"] == "jwt"), None)
        if jwt_acc is None:
            pytest.skip("被测实例无 JWT 账号")
        res = client.post("/admin/api/claim/manual",
                          json={"account_id": jwt_acc["id"]},
                          headers=admin_headers)
        outcome = res.json()["outcomes"][0]
        assert outcome["ok"] is False
        assert "缺少验证码参数" in outcome["message"]

    def test_captcha_config_endpoint(self, client, admin_headers):
        """原断言逐字（enabled True / scene 'mock-scene' / region 'sgp' /
        prefix 'mockpre'）为 mock 桩配置值，指向真实服务时 captcha 配置来自
        真实云端 → 具体值不可双指。保留【结构断言】：四键存在 + 类型正确；
        具体值一致性由 harness 对拍端点清单收编后逐字节锁定。"""
        res = client.get("/admin/api/claim/captcha-config", headers=admin_headers)
        assert res.status_code == 200
        cfg = res.json()
        assert isinstance(cfg["enabled"], bool)
        assert isinstance(cfg["scene_id"], str)
        assert isinstance(cfg["region"], str)
        assert isinstance(cfg["prefix"], str)


class TestAutoClaimOnPoolEntry:
    def test_pool_entry_auto_claim_internal(self):
        pytest.skip("无法用于对拍：auto_claim_all_plans 直调、admin_api._login_flows/"
                    "monkeypatch spy、asyncio.sleep 观察后台任务——全部进程内能力。"
                    "原因：进程内私有对象与调度观测")
        raise AssertionError

    def test_fingerprint_rotation(self, client, admin_headers):
        """原断言（rotate 返回新 device_mid 且落库）是真实【写库】行为，会改变
        真实账号的设备身份（影响正在跑的抢包/对话指纹一致性）→ 绝不允许打
        共享生产实例；Z2A_ALLOW_MUTATION 门控下仅隔离实例可跑。"""
        from conftest import need_mutation
        need_mutation()
        listing = client.get("/admin/api/accounts", headers=admin_headers).json()
        acc = next((a for a in listing["accounts"] if a.get("fingerprint")), None)
        if acc is None:
            pytest.skip("无带指纹账号")
        old_mid = acc["fingerprint"]["device_mid"]
        res = client.post("/admin/api/accounts/%s/fingerprint/rotate" % acc["id"],
                          headers=admin_headers)
        assert res.status_code == 200
        new_mid = res.json()["fingerprint"]["device_mid"]
        assert new_mid != old_mid
        after = client.get("/admin/api/accounts", headers=admin_headers).json()
        cur = next(a["fingerprint"]["device_mid"] for a in after["accounts"]
                   if a["id"] == acc["id"])
        assert cur == new_mid
