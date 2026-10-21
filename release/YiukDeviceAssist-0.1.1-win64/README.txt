Yiuk Device Assist 0.1.1
========================

Start yiuk-device-assist.exe on the device owner computer.

Settings > Platform Service:
  server address : host:port of the platform service (TLS)
  player password: platform player password (8+ characters)

Self-signed servers: the first connection shows the certificate SHA-256
fingerprint. Choose "trust this certificate" only when it is your own server.
The client pins that fingerprint and stops retrying on any mismatch instead of
reconnecting in a loop.

Settings > Agent Integration: "Copy Agent Configuration" copies the MCP JSON
for your Agent. yiuk-mcp.exe stays beside the Qt client; the client never
copies MCP files into other directories.

The package requires Windows x64.
