#!/usr/bin/env bash
# Deploy the AgentRemote platform server on a Linux host.
#
# Usage:
#   sudo ./deploy/install-platform.sh <tls-cert> <tls-key>
#
# The script builds both binaries (core + gateway) from the current checkout,
# installs them under /opt/yiuk-platform, generates a random player password, and
# starts two systemd services. It never overwrites an existing platform.yaml, so
# re-running keeps the current password.
#
# core 只处理业务，gateway 只处理客户端连接；两者通过内网 gRPC 通信。
set -euo pipefail

DEPLOY_DIR=/opt/yiuk-platform
SERVICE_USER=yiukplatform

if [[ $EUID -ne 0 ]]; then
    echo "must run as root" >&2
    exit 1
fi
if [[ $# -lt 2 ]]; then
    echo "usage: $0 <tls-cert> <tls-key>" >&2
    exit 1
fi

CERT_SRC=$1
KEY_SRC=$2
REPO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

id "$SERVICE_USER" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"

mkdir -p "$DEPLOY_DIR/certs"
go build -o "$DEPLOY_DIR/yiuk-platform-core" "$REPO_DIR/cmd/platform-core"
go build -o "$DEPLOY_DIR/yiuk-platform-gateway" "$REPO_DIR/cmd/platform-gateway"
cp "$CERT_SRC" "$DEPLOY_DIR/certs/server.crt"
cp "$KEY_SRC" "$DEPLOY_DIR/certs/server.key"
chmod 644 "$DEPLOY_DIR/certs/server.crt"
chmod 600 "$DEPLOY_DIR/certs/server.key"

if [[ ! -f "$DEPLOY_DIR/platform.yaml" ]]; then
    PLAYER_PW=$(openssl rand -base64 48 | tr -d '/+=' | head -c 48)
    cat > "$DEPLOY_DIR/platform.yaml" <<EOF
platform:
  listen: ":29564"
  tls_cert: "$DEPLOY_DIR/certs/server.crt"
  tls_key: "$DEPLOY_DIR/certs/server.key"
  allow_insecure: false
  player_password: "$PLAYER_PW"
  max_connections: 1024
  max_devices: 1024
  max_sessions: 2048
  max_sessions_per_device: 8
  max_tasks_per_session: 2
  session_timeout_sec: 600
  approval_timeout_sec: 120
  temp_password_ttl_sec: 600
  temporary_password_min_sec: 60
  temporary_password_max_sec: 86400
  heartbeat_timeout_sec: 45
  handshake_timeout_sec: 10
  max_output_bytes: 2097152
  max_download_bytes: 33554432
  max_upload_bytes: 33554432
  max_frame_bytes: 262144
  auth_rate_per_min: 10
  offline_device_keep_sec: 86400
  log_file: "$DEPLOY_DIR/platform.log"
  core_grpc_listen: "127.0.0.1:25670"
  core_grpc_addr: "127.0.0.1:25670"
  gateway_grpc_listen: "127.0.0.1:25671"
  gateway_grpc_addr: "127.0.0.1:25671"
EOF
    echo "generated player password (store it safely, it is not shown again by this script):"
    echo "  $PLAYER_PW"
fi

chown -R "$SERVICE_USER:$SERVICE_USER" "$DEPLOY_DIR"
chmod 600 "$DEPLOY_DIR/platform.yaml"

install -m 644 "$REPO_DIR/deploy/systemd/yiuk-platform-core.service" /etc/systemd/system/yiuk-platform-core.service
install -m 644 "$REPO_DIR/deploy/systemd/yiuk-platform-gateway.service" /etc/systemd/system/yiuk-platform-gateway.service
systemctl daemon-reload
systemctl enable --now yiuk-platform-core.service yiuk-platform-gateway.service
systemctl --no-pager --lines=5 status yiuk-platform-core.service yiuk-platform-gateway.service
