"""zcode2api 黑盒测试 · 可指向任意 base URL 的公共夹具。

来源与纪律（t4 契约）：
  - 本目录测试由 zcode2api/tests/integration/ 的 6 个黑盒测试**改造**而来：
    断言语义一律照抄原测试（那是验证意图）；只允许两类改动：
      ① 指向哪个服务：ASGITransport/进程内 fixture → 环境变量 Z2A_BASE_URL；
      ② 密钥从哪里读：写死 "Bearer zcode" → Z2A_ADMIN_KEY 或 .env（不回显）。
  - 原测试大量使用进程内能力（fresh_app.set_setting / seed_account /
    mock.state.* / app 内部对象直改）。这些能力在 HTTP 层**不存在**。
    对应测试要么用真实管理 API 等价重建前置条件（并加安全门），要么
    显式 skip 并在 README 记录「无法用于对拍的原因 X」——不悄悄删。

环境变量：
  Z2A_BASE_URL            被测服务地址，如 http://127.0.0.1:9120（必填才有意义）
  Z2A_ADMIN_KEY           后台密钥；缺省时从 Z2A_ENV_FILE 读 ZCODE_ADMIN_KEY 键名
  Z2A_ENV_FILE            .env 路径（默认 zcode2api/.env）
  Z2A_ALLOW_MUTATION=1    允许管理写操作（设 gateway_key / 清监控 / 起 flow）
                          ⚠ 打共享生产服务(9120)会临时影响真实用户，默认关
  Z2A_ALLOW_REAL_UPSTREAM=1  允许产生真实上游调用（会耗账号额度），默认关
安全红线：任何 fixture 不得打印密钥值；异常信息一律脱敏。
"""
from __future__ import annotations

import os
import re
from pathlib import Path

import httpx
import pytest

CONFORMANCE_DIR = Path(__file__).resolve().parent.parent
DEFAULT_ENV_FILE = CONFORMANCE_DIR.parents[2] / "zcode2api" / ".env"


def _load_env_file(path: Path) -> dict:
    env = {}
    if path.is_file():
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, _, v = line.partition("=")
            env[k.strip()] = v.strip().strip('"').strip("'")
    return env


def _redact(text: str) -> str:
    """异常/日志里的密钥兜底脱敏。"""
    secret = os.environ.get("__Z2A_ADMIN_KEY_REDACTOR__", "")
    return text.replace(secret, "${ZCODE_ADMIN_KEY}") if secret else text


@pytest.fixture(scope="session")
def base_url() -> str:
    url = os.environ.get("Z2A_BASE_URL", "").strip()
    if not url:
        pytest.skip("Z2A_BASE_URL 未设置（本套测试必须指向一个运行中的服务，"
                    "老服务 http://127.0.0.1:9120 或新服务任一地址）")
    return url.rstrip("/")


@pytest.fixture(scope="session")
def admin_key() -> str:
    key = os.environ.get("Z2A_ADMIN_KEY", "").strip()
    if not key:
        env_file = Path(os.environ.get("Z2A_ENV_FILE", str(DEFAULT_ENV_FILE)))
        key = _load_env_file(env_file).get("ZCODE_ADMIN_KEY", "")
    if not key:
        pytest.skip("后台密钥不可得（Z2A_ADMIN_KEY 与 env 文件中的 ZCODE_ADMIN_KEY 均为空）")
    # 把真实值藏进环境变量供 _redact 兜底，测试代码里只经由 fixture 使用
    os.environ["__Z2A_ADMIN_KEY_REDACTOR__"] = key
    return key


@pytest.fixture(scope="session")
def admin_headers(admin_key) -> dict:
    return {"Authorization": "Bearer " + admin_key}


@pytest.fixture(scope="session")
def client(base_url) -> httpx.Client:
    # trust_env=False：绝不让系统代理（mihomo 等）污染对拍结果——
    # 走代理测回环 = 假阳性经典来源（memory: 并发测速假阳性/容器出站排查）。
    with httpx.Client(base_url=base_url, timeout=30.0, trust_env=False) as c:
        yield c


def need_mutation():
    if os.environ.get("Z2A_ALLOW_MUTATION", "") != "1":
        pytest.skip("该测试需要管理写操作（会临时改变被测服务状态）。"
                    "设 Z2A_ALLOW_MUTATION=1 且只指向隔离实例时运行")


def need_real_upstream():
    if os.environ.get("Z2A_ALLOW_REAL_UPSTREAM", "") != "1":
        pytest.skip("该测试会产生真实上游调用（消耗真实账号额度/产生流量）。"
                    "设 Z2A_ALLOW_REAL_UPSTREAM=1 时运行")
