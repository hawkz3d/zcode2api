# 切换前基线快照（2026-10-15 19:25:58）

## 服务状态
```
state: active
pid: 505437
listener: 1
```

## 账号清单（只记 id/name/provider/status/enabled，不记凭证）
```json
[
  {
    "id": "oauth-login-ea92aa60",
    "name": "老号·抢包(JWT)",
    "provider": "zai",
    "status": "active",
    "enabled": true,
    "mode": "jwt"
  },
  {
    "id": "oauth-apikey-c5c641a1",
    "name": "老号·智谱Key",
    "provider": "zai",
    "status": "active",
    "enabled": true,
    "mode": "apiKey"
  },
  {
    "id": "oauth-login-8523aba4",
    "name": "xin号·抢包(JWT)",
    "provider": "zai",
    "status": "active",
    "enabled": true,
    "mode": "jwt"
  },
  {
    "id": "oauth-apikey-8808fbac",
    "name": "xin号·智谱Key",
    "provider": "zai",
    "status": "active",
    "enabled": true,
    "mode": "apiKey"
  },
  {
    "id": "laohao-bigmodel-2420b3c2",
    "name": "老号·BigModelKey",
    "provider": "bigmodel",
    "status": "active",
    "enabled": "True",
    "mode": "apiKey"
  },
  {
    "id": "xinhao-2000w-02f71f86",
    "name": "xin号·BigModelKey",
    "provider": "bigmodel",
    "status": "active",
    "enabled": true,
    "mode": "apiKey"
  }
]

账号总数: 6
```

## 网关可用性
```
GET /v1/models → HTTP 200
GET /admin/login → HTTP 200
GET /admin/api/status → HTTP 200
```

## 数据快照（只记计数与 schema 指纹，不导数据）
```
accounts: 6 行
meta: 3 行
accounts 列: id,provider,name,mode,status,enabled,created_at,data
schema 指纹: d34097dd671ac089
```
