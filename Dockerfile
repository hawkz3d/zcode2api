# syntax=docker/dockerfile:1
#
# zhipucode2api —— 多阶段构建
#
# 设计要点：
#   1. 构建阶段用 CGO_ENABLED=0 产出【完全静态】二进制。驱动 modernc.org/sqlite
#      是纯 Go 实现，故无需 glibc；实测 `ldd` 输出 "not a dynamic executable"。
#   2. 运行阶段用 alpine 而非 scratch —— 因为验证码求解器是外部 node 进程，
#      需要 node 运行时（见下方 solver 层说明）。
#   3. 页面 / 静态资源 / 身份块全部经 go:embed 编入二进制，运行阶段【无需】任何
#      模板或资源文件，只需数据目录与 .env。
#
# 镜像分层（solver 独立层，其源码【不】进入本仓库）：
#   ┌─ builder      golang:1.26-alpine        编译静态二进制
#   ├─ solver       node:22-alpine            node + happy-dom + solver.js
#   └─ runtime      alpine:3.20               二进制 + node 运行时 + solver
#
# solver.js 及其 node_modules 是【外部黑盒依赖】，通过构建上下文之外的
# `captcha_node/` 目录或构建参数引入 —— 见 README「容器化」一节。

# ─────────────────────────── 构建阶段 ───────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /src

# 先只复制依赖清单，让 go mod download 的层可缓存（源码改动不触发重下）
COPY go.mod go.sum* ./
RUN go mod download

# 再复制源码并静态编译
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w" \
        -o /out/zhipucode2api ./cmd/zhipucode2api

# ─────────────────────── solver 构建阶段 ────────────────────────
# 本阶段把验证码求解器及其依赖装成一个独立层。
#
# 🔴 重要：solver.js 是外部黑盒依赖，【不在本仓库内】。构建时由调用方通过
#    `--build-context captcha=../zcode2api/captcha_node` 或把该目录复制到
#    构建上下文后传入。若未提供，本阶段仍可构建，但镜像内无求解器 ——
#    此时网关照常工作（挑战分支直通上游应答），只是不重试，启动日志会注明。
FROM node:22-alpine AS solver

WORKDIR /opt/captcha

# 独立的依赖清单（只含 solver 的运行时依赖，不含仓库自身依赖）
COPY docker/solver-package.json ./package.json
RUN npm install --omit=dev --no-audit --no-fund && npm cache clean --force

# solver.js 由【额外的构建上下文】注入（BuildKit 的 --build-context）。
# 若调用方未提供该上下文，docker 会因为 COPY 失败而报错 —— 这是有意的
# fail-loud 行为：宁可构建失败，也不要产出一个"看起来正常但求解器缺失"
# 的镜像。要构建无求解器的镜像，请用 `--target runtime-minimal`（见文末）。
COPY --from=captcha ./solver.js /opt/captcha/solver.js

# ─────────────── 精简变体：不含验证码求解器 ───────────────
# 注意：本 stage 必须【在 runtime 之前】定义 —— docker 的默认 target 是文件中
# 最后一个 stage。runtime（含求解器）放最后，保证 `docker build .` 即得完整镜像。
# 适用于：不需要 captcha 挑战重试的部署，或求解器由外部服务提供。
# 网关照常服务；收到挑战时直通上游应答而不重试（启动日志会注明 solver 未装配）。
FROM alpine:3.20 AS runtime-minimal

COPY --from=builder /out/zhipucode2api /usr/local/bin/zhipucode2api

RUN addgroup -S -g 10001 z2a && adduser -S -u 10001 -G z2a z2a \
 && mkdir -p /data && chown z2a:z2a /data

USER z2a
WORKDIR /data

ENV ZCODE_HOST=0.0.0.0 \
    ZCODE_PORT=9120 \
    ZCODE_DATA_DIR=/data

EXPOSE 9120

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:9120/v1/models >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/zhipucode2api"]
CMD ["serve"]

# ─────────────────────────── 运行阶段 ───────────────────────────
FROM alpine:3.20 AS runtime

# node 运行时由 solver 阶段提供（同一 alpine 系，musl 兼容）。
# 🔴 实测坑：node 二进制是 C++ 实现的，从 node:22-alpine 单独复制到裸 alpine
#    会缺 libstdc++.so.6 与 libgcc_s.so.1 —— 表现为执行时报
#    "Error loading shared library libstdc++.so.6" 加大量符号解析失败，
#    solver 完全不可用（而镜像构建本身成功，属静默失效）。故必须补装这两个库。
RUN apk add --no-cache libstdc++ libgcc
COPY --from=solver /usr/local/bin/node /usr/local/bin/node
COPY --from=solver /opt/captcha /opt/captcha

# 网关二进制
COPY --from=builder /out/zhipucode2api /usr/local/bin/zhipucode2api

# 非 root 运行
RUN addgroup -S -g 10001 z2a && adduser -S -u 10001 -G z2a z2a \
 && mkdir -p /data && chown z2a:z2a /data /opt/captcha

USER z2a
WORKDIR /data

ENV ZCODE_HOST=0.0.0.0 \
    ZCODE_PORT=9120 \
    ZCODE_DATA_DIR=/data \
    ZCODE_NODE_PATH=/usr/local/bin/node \
    ZCODE_SOLVER_PATH=/opt/captcha/solver.js

EXPOSE 9120

# 健康检查用 /v1/models —— 该端点在未配置 gateway_key 时免鉴权；
# 若部署时配了 key，健康检查会收到 401（仍证明进程在听，属预期）。
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:9120/v1/models >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/zhipucode2api"]
CMD ["serve"]
