"""GW-009 连接级失败重试 · base-URL 可指向版。

原文件：zcode2api/tests/integration/test_gateway_connect_fail.py
结论：**整文件无法用于对拍**（原因见下），保留文件以显式记录，不悄悄删。

不可重指向原因：
  1. 「首次连接失败」场景由 mock 上游的 connect_fail_first 注入
     （mock.state.sequences[前缀] = ["connect_fail_first"]），真实上游无法
     按凭证前缀注入连接级断连；
  2. 断言观测点全部是进程内对象：fresh_app.list_accounts("zai") 的
     status=="cooling"、last_error 含「连接失败」、mock.state.calls 计数；
  3. seed_account 直接改全局 store，HTTP 层无等价（POST /admin/api/accounts
     会真建账号 + 触发真实上游额度刷新与自动领取，不能拿生产开玩笑）。

候选（Go 版）验证该行为的正确路径：受控 mock 套件（独立实例 + 独立库 +
mock 上游），等价场景在 docs/testing/04 §2 故障注入矩阵中有契约描述。
harness 对拍侧的等价锁定：本行为的【外部可见结果】（冷却账号不被选中、
故障转移后 200）在真实流量中不可构造，故由受控套件负责，对拍负责
「同一输入同一响应形态」的端点面。
"""
from __future__ import annotations

import pytest


class TestConnectFailFailover:
    def test_first_connect_fail_fails_over(self):
        pytest.skip("无法用于对拍：mock 场景注入（connect_fail_first）+ 进程内"
                    "store 断言 + mock.state.calls 观测。原因见模块 docstring")
        raise AssertionError
