"""GW-010 上游错误处理与冷却 · base-URL 可指向版。

原文件：zcode2api/tests/integration/test_gateway_risk_cooldown.py
结论：**整文件无法用于对拍**（原因逐条），保留文件显式记录。

共同不可重指向原因：场景全部依赖 mock.state.sequences 注入
（risk_control_3012 / rate_limited / server_error）；断言观测
fresh_app.list_accounts 的 Status/cooling_until/risk_strikes/last_error
（进程内模型对象与属性方法 is_selectable()/is_cooling()）；重试参数
monkeypatch app.settings（RETRY_429_TIMES 等）与内部函数 _parse_retry_after、
_sleep。真实上游不可按指令返回 3012/429/500，也绝不能拿真实账号试风控
（3012 会**真的禁用账号**——这是文件头契约描述的行为）。

逐条映射：
  TestRiskControlBan.test_3012_*          mock 场景 + 进程内状态断言
  Test429Retry.test_429_*                 mock 场景 + settings monkeypatch +
                                          counters 计数（mock 内部）
  Test5xxRetry.test_5xx_*                 同上
  TestCooldownBehavior.*                  直改进程内 Status/cooling_until +
                                          CaptchaManager._gate_open 私有方法
  TestBillingRefreshDebounce.*            进程内时间拨快 + mock.state.calls
  TestAdminEndpointsRespectCooldown.*     refresh/claim 管理写路径 + 冷却前置
                                          （真实调用会打真实 billing；且冷却
                                          状态无法在无 mock 的环境构造）
  test_parse_retry_after                  纯函数单测（app.routes.gateway 私有），
                                          不属黑盒面

候选（Go 版）验证路径：受控 mock 套件按 docs/testing/04 §2 故障注入矩阵逐场景
复跑原断言语义。对拍侧：这些分支的【响应形态】（429 透传/503 错误体）在真实
流量偶发时由 harness expensive 类端点观测，不作为切换门禁。
"""
from __future__ import annotations

import pytest


@pytest.mark.parametrize("name", [
    "test_3012_bans_account_and_fails_over",
    "test_ban_recoverable_by_manual_enable",
    "test_repeated_bans_accumulate",
    "test_429_retries_then_fails_over_account_stays_active",
    "test_429_retry_then_success",
    "test_429_wait_uses_capped_retry_after",
    "test_5xx_retries_then_cools",
    "test_5xx_retry_then_success",
    "test_cooldown_blocks_selection_until_expiry",
    "test_success_resets_strikes_and_cleans_state",
    "test_quota_refresh_respects_active_cooldown",
    "test_captcha_refill_gate_follows_account_state",
    "test_success_refresh_debounced",
    "test_refresh_all_skips_cooling",
    "test_refresh_one_skips_cooling",
    "test_claim_skips_cooling",
    "test_refresh_still_works_for_active_accounts",
    "test_parse_retry_after",
])
def test_requires_managed_environment(name):
    pytest.skip("无法用于对拍：%s —— mock 场景注入/进程内状态/内部函数 monkeypatch。"
                "详见模块 docstring 的逐条映射表（保留原测试名以示未删除）" % name)
    raise AssertionError
