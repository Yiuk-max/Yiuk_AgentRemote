#!/bin/sh
# Container entrypoint for the Yiuk Switchboard platform server.
#
# Two ways to configure the server:
#
#   1. Mount a config file and point YIUK_CONFIG at it (used verbatim):
#        -v /host/platform.yaml:/etc/yiuk/platform.yaml:ro \
#        -e YIUK_CONFIG=/etc/yiuk/platform.yaml
#
#   2. Let the entrypoint render /var/lib/yiuk-platform/platform.yaml from the
#      YIUK_* environment variables below; it is rewritten on every start.
#
# Environment:
#   YIUK_CONFIG              path to platform.yaml
#   YIUK_LISTEN              listen address, default ":29564"
#   YIUK_PLAYER_PASSWORD     player password (>= 8 chars, 16+ recommended)
#   YIUK_PLAYER_PASSWORD_FILE  read the password from a file (docker secret)
#   YIUK_TLS_CERT            server certificate path, default "$DATA_DIR/certs/server.crt"
#   YIUK_TLS_KEY             server key path, default "$DATA_DIR/certs/server.key"
#   YIUK_ALLOW_INSECURE      "true" disables TLS (local tests only)
#   YIUK_TLS_SELF_SIGNED     "true" generates a throwaway cert when none exists
#   YIUK_TLS_HOST            CN/SAN for that cert, default "yiuk-platform"
#   YIUK_TLS_SELF_SIGNED_DAYS  validity in days, default 825
#   YIUK_LOG_FILE            log file path, default "$DATA_DIR/platform.log"
#
# All other platform limits (sessions, tasks, output caps, TTLs, ...) fall back
# to the built-in defaults; mount a full platform.yaml to change them.
set -eu

DEFAULT_CONFIG=/var/lib/yiuk-platform/platform.yaml
CONFIG=${YIUK_CONFIG:-$DEFAULT_CONFIG}
DATA_DIR=$(dirname "$CONFIG")

log() { printf 'yiuk: %s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

# Emit a YAML double-quoted scalar so paths and secrets with ': # " \' stay valid.
yaml() {
    printf '"%s"' "$(printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')"
}

read_password() {
    password=
    if [ -n "${YIUK_PLAYER_PASSWORD_FILE:-}" ]; then
        [ -r "${YIUK_PLAYER_PASSWORD_FILE}" ] \
            || die "YIUK_PLAYER_PASSWORD_FILE is not readable: ${YIUK_PLAYER_PASSWORD_FILE}"
        password=$(tr -d '\r\n' < "${YIUK_PLAYER_PASSWORD_FILE}")
    elif [ -n "${YIUK_PLAYER_PASSWORD:-}" ]; then
        password=$(printf '%s' "${YIUK_PLAYER_PASSWORD}" | tr -d '\r\n')
    fi
}

render_config() {
    read_password
    [ -n "$password" ] \
        || die "no config at $CONFIG: mount one and set YIUK_CONFIG, or set YIUK_PLAYER_PASSWORD / YIUK_PLAYER_PASSWORD_FILE"

    listen=${YIUK_LISTEN:-:29564}
    cert=${YIUK_TLS_CERT:-$DATA_DIR/certs/server.crt}
    key=${YIUK_TLS_KEY:-$DATA_DIR/certs/server.key}
    log_file=${YIUK_LOG_FILE:-$DATA_DIR/platform.log}

    insecure=false
    case "${YIUK_ALLOW_INSECURE:-false}" in
    true | 1 | yes | on) insecure=true ;;
    *) insecure=false ;;
    esac

    tls_block=
    if [ "$insecure" = true ]; then
        log "warning: YIUK_ALLOW_INSECURE is on, the listener is plaintext; use this only for local tests"
    else
        if [ ! -f "$cert" ] || [ ! -f "$key" ]; then
            case "${YIUK_TLS_SELF_SIGNED:-false}" in
            true | 1 | yes | on)
                host=${YIUK_TLS_HOST:-yiuk-platform}
                mkdir -p "$(dirname "$cert")" "$(dirname "$key")"
                openssl req -x509 -newkey rsa:2048 -nodes -sha256 \
                    -days "${YIUK_TLS_SELF_SIGNED_DAYS:-825}" \
                    -keyout "$key" -out "$cert" \
                    -subj "/CN=$host" \
                    -addext "subjectAltName=DNS:$host,IP:127.0.0.1" >/dev/null 2>&1 \
                    || die "could not generate a self-signed certificate at $cert"
                chmod 600 "$key"
                log "generated a throwaway self-signed certificate (CN=$host); clients must trust its SHA-256 fingerprint"
                ;;
            *)
                die "no TLS certificate at $cert / $key: mount a trusted pair there, or set YIUK_TLS_SELF_SIGNED=true for a throwaway one"
                ;;
            esac
        fi
        tls_block="  tls_cert: $(yaml "$cert")
  tls_key: $(yaml "$key")"
    fi

    mkdir -p "$DATA_DIR" || die "cannot create $DATA_DIR (mount a writable volume or run as the owning uid)"
    {
        echo "platform:"
        echo "  listen: $(yaml "$listen")"
        if [ -n "$tls_block" ]; then
            printf '%s\n' "$tls_block"
        fi
        echo "  allow_insecure: $insecure"
        echo "  player_password: $(yaml "$password")"
        echo "  log_file: $(yaml "$log_file")"
    } >"$CONFIG.tmp" || die "cannot write $CONFIG.tmp (read-only volume?)"
    chmod 600 "$CONFIG.tmp"
    mv "$CONFIG.tmp" "$CONFIG"
    log "rendered $CONFIG from environment (listen=$listen tls=$([ "$insecure" = true ] && echo off || echo on))"
}

if [ -n "${YIUK_CONFIG:-}" ]; then
    [ -f "$CONFIG" ] || die "YIUK_CONFIG=$CONFIG does not exist (unset YIUK_CONFIG to render one from the YIUK_* variables)"
    log "using mounted config $CONFIG"
else
    render_config
fi

exec /usr/local/bin/yiuk-platform -config "$CONFIG" "$@"
