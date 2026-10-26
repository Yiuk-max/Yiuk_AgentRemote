# Yiuk Switchboard platform server (cmd/platform).
#
# Build:  docker build -t yiuk-platform:local .
# Run:    docker run -d --name yiuk-platform -p 29564:29564 \
#           -e YIUK_PLAYER_PASSWORD='<16+ random chars>' \
#           -e YIUK_TLS_SELF_SIGNED=true \
#           -v yiuk-platform-data:/var/lib/yiuk-platform \
#           yiuk-platform:local
#
# Or:     docker compose up -d --build   (see docker-compose.yml)
#
# The image only contains the server binary and a small entrypoint that renders
# platform.yaml from YIUK_* environment variables when no config file is
# mounted. See docs/development.md §6.1 for the full variable list.

ARG GO_VERSION=1.27.3

# ---- build ----
FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src
# The platform server is pure Go: no cgo, no runtime deps beyond the OS.
ENV CGO_ENABLED=0 GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN go build -trimpath -ldflags='-s -w' -o /out/yiuk-platform ./cmd/platform

# ---- runtime ----
FROM alpine:3.22

# openssl: optional throwaway self-signed certificate generation.
# ca-certificates: keeps outbound TLS usable for anything running in the image later.
RUN apk add --no-cache ca-certificates openssl \
    && addgroup -S -g 10001 yiuk \
    && adduser -S -u 10001 -G yiuk -h /var/lib/yiuk-platform -s /sbin/nologin yiuk \
    && mkdir -p /var/lib/yiuk-platform/certs \
    && chown -R yiuk:yiuk /var/lib/yiuk-platform

COPY --from=build /out/yiuk-platform /usr/local/bin/yiuk-platform
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/yiuk-platform /usr/local/bin/docker-entrypoint.sh

LABEL org.opencontainers.image.title="yiuk-platform" \
      org.opencontainers.image.description="Yiuk Switchboard remote-assist platform server (TLS relay, sessions, command tasks)" \
      org.opencontainers.image.licenses="UNLICENSED"

ENV YIUK_LISTEN=:29564 \
    YIUK_HEALTHCHECK_PORT=29564

WORKDIR /var/lib/yiuk-platform
VOLUME ["/var/lib/yiuk-platform"]

# Platform state is in memory; only the config, certs and log live on disk.
EXPOSE 29564
STOPSIGNAL SIGTERM

USER yiuk

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD nc -z 127.0.0.1 "${YIUK_HEALTHCHECK_PORT}" || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
