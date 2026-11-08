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
# Both roles are pure Go: no cgo, no runtime deps beyond the OS.
ENV CGO_ENABLED=0 GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# 两个角色，同一个镜像可以跑任意一个。
RUN go build -trimpath -ldflags='-s -w' -o /out/yiuk-platform-core ./cmd/platform-core \
 && go build -trimpath -ldflags='-s -w' -o /out/yiuk-platform-gateway ./cmd/platform-gateway

# ---- runtime ----
FROM alpine:3.22

# openssl: optional throwaway self-signed certificate generation.
# ca-certificates: keeps outbound TLS usable for anything running in the image later.
RUN apk add --no-cache ca-certificates openssl netcat-openbsd \
    && addgroup -S -g 10001 yiuk \
    && adduser -S -u 10001 -G yiuk -h /var/lib/yiuk-platform -s /sbin/nologin yiuk \
    && mkdir -p /var/lib/yiuk-platform/certs \
    && chown -R yiuk:yiuk /var/lib/yiuk-platform

COPY --from=build /out/yiuk-platform-core /usr/local/bin/yiuk-platform-core
COPY --from=build /out/yiuk-platform-gateway /usr/local/bin/yiuk-platform-gateway
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/yiuk-platform-core /usr/local/bin/yiuk-platform-gateway \
    /usr/local/bin/docker-entrypoint.sh

LABEL org.opencontainers.image.title="yiuk-platform" \
      org.opencontainers.image.description="AgentRemote remote-assist platform (WSS gateway + business core)" \
      org.opencontainers.image.licenses="UNLICENSED"

ENV YIUK_LISTEN=:29564 \
    YIUK_HEALTHCHECK_PORT=29564 \
    YIUK_BINARY=yiuk-platform-gateway

WORKDIR /var/lib/yiuk-platform
VOLUME ["/var/lib/yiuk-platform"]

# Platform state is in memory; only the config, certs and log live on disk.
EXPOSE 29564
STOPSIGNAL SIGTERM

USER yiuk

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD nc -z 127.0.0.1 "${YIUK_HEALTHCHECK_PORT}" || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
