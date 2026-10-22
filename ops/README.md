# Go Operations Agent

`ops/server` contains a Go service that manages a relay process on a server. It is separate from the Windows device client and remote execution agent described in [`../docs/product-requirements.md`](../docs/product-requirements.md).

## Capabilities and protocol

- Reports relay process status and host CPU/memory metrics.
- Starts, stops, and restarts the configured relay child process.
- Reads, validates, and atomically saves the relay YAML configuration. Saving does not hot-reload a running relay; restart it to apply changes.
- Uses TLS 1.2 or newer for the management connection. The relay's own control protocol is separate and does not use TLS.
- Authenticates management access with the relay host password and rate-limits login attempts by source IP.

The management protocol uses a 4-byte big-endian length prefix followed by a UTF-8 JSON message, with a 2 MiB frame limit. It is not HTTP/HTTPS.

## Build

From the repository root:

```sh
go build -o ops-agent ./ops/server/cmd/ops-agent
```

## Configuration

`ops/server/ops-agent.yaml` is a template. Copy it to a protected runtime directory and update paths for the target host:

```yaml
listen: ":29564"
tls_cert: "/etc/yiuk-ops/server.crt"
tls_key: "/etc/yiuk-ops/server.key"
relay_binary: "/opt/yiuk/relay"
relay_config: "/opt/yiuk/config.yaml"
relay_working_dir: "/opt/yiuk"
```

Restrict the management port to trusted operator addresses using the host firewall and cloud security group. Keep the TLS private key and relay configuration readable only by the service account. Use distinct random relay host and player passwords.
