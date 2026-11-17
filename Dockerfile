# AgentRemote platform: core (business) + gateway (WSS ingress), one image.
#
# Build:  docker build -t yiuk-platform:local .
# Run:    two containers from this image (see docs/development.md §6.3)
#
# 一个镜像两个角色，用 YIUK_BINARY 选：
#   yiuk-platform-gateway  对外 WSS，把帧原样转给 core（默认）
#   yiuk-platform-core     只有业务，不开对外端口
#
# The entrypoint renders platform.yaml from YIUK_* environment variables when no
# config file is mounted. See docs/development.md §6.3 for the full variable list.

ARG GO_VERSION=1.27.3

# ---- build ----
FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src
# 纯go语言，在linux上
ENV CGO_ENABLED=0 GOOS=linux
# 依赖包下载缓存
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# 两个角色，同一个镜像可以跑任意一个。编译网关和核心业务，二进制文件放在 /out 目录
RUN go build -trimpath -ldflags='-s -w' -o /out/yiuk-platform-core ./cmd/platform-core \
    && go build -trimpath -ldflags='-s -w' -o /out/yiuk-platform-gateway ./cmd/platform-gateway

# ---- runtime ----
FROM alpine:3.22

# openssl: 可选的自签名证书
# ca-certificates: 保障出站 TLS 可用性
# 创建用户Yiuk
RUN apk add --no-cache ca-certificates openssl netcat-openbsd \
    && addgroup -S -g 10001 yiuk \
    && adduser -S -u 10001 -G yiuk -h /var/lib/yiuk-platform -s /sbin/nologin yiuk \
    && mkdir -p /var/lib/yiuk-platform/certs \
    && chown -R yiuk:yiuk /var/lib/yiuk-platform
# 把编译好的二进制文件和入口脚本拷贝到镜像中，授予执行权限
COPY --from=build /out/yiuk-platform-core /usr/local/bin/yiuk-platform-core
COPY --from=build /out/yiuk-platform-gateway /usr/local/bin/yiuk-platform-gateway
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/yiuk-platform-core /usr/local/bin/yiuk-platform-gateway \
    /usr/local/bin/docker-entrypoint.sh
# 镜像名称和描述信息
LABEL org.opencontainers.image.title="yiuk-platform" \
    org.opencontainers.image.description="AgentRemote remote-assist platform (WSS gateway + business core)" \
    org.opencontainers.image.licenses="UNLICENSED"
# 运行环境变量
ENV YIUK_LISTEN=:29564 \
    YIUK_HEALTHCHECK_PORT=29564 \
    YIUK_BINARY=yiuk-platform-gateway
# 工作目录和挂载到宿主机的卷，平台状态在内存中，只有配置、证书和日志在磁盘上
WORKDIR /var/lib/yiuk-platform
VOLUME ["/var/lib/yiuk-platform"]

# 平台状态在内存中，只有配置、证书和日志在磁盘上
EXPOSE 29564
STOPSIGNAL SIGTERM

USER yiuk

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD nc -z 127.0.0.1 "${YIUK_HEALTHCHECK_PORT}" || exit 1
# ENTRYPOINT 脚本会根据 YIUK_BINARY 选择运行网关还是核心业务
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
