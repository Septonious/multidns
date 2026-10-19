# MultiDNS testing prototype

Go DNS tunnel and Android VPN with resolver groups, live scanning, profiles,
and app/destination split routing. Experimental wire protocol v1; incompatible
with other DNS tunnels. Payload is not encrypted: use application TLS where needed.
All transport frames have an 8-byte truncated HMAC-SHA256 authentication tag.

## Local development

Use the existing checkout in an isolated task; no additional worktree is needed.
Go 1.25+ is required. `go test -race ./...` runs the integration and unit tests.
`go build ./cmd/...` builds all command-line tools.

## Server

Delegate `t.example.com` using `NS t.example.com -> ns.example.com` and an A/AAAA
record pointing `ns.example.com` at the server. Allow UDP/TCP 53. Generate a key
with `openssl rand -hex 32` and pass it in the `MULTIDNS_KEY` environment variable.

```
multidns-server -listen :53 -domain t.example.com
multidns-client -profile profile.json -listen 127.0.0.1:1080
```

The server and profile must use the same key. No key is shipped in the APK.
The CLI provides a SOCKS5 endpoint; Android wraps it in a VpnService.
Resolver groups duplicate requests within a group and stripe work across groups.
Each stream uses independent sequence numbers and a bounded retransmission window.
Resolver failover preserves destination connections while server stream state lives.
Switching independent servers creates a new session.

## Split routing

Profiles contain an ordered `rules` list. First match wins; fallback is `default_action`.
Rules can match domain suffix, exact domain, IP CIDR, port, and TCP/UDP protocol.
Actions: `direct`, `tunnel`, `block`. Domain rules use SOCKS hostnames or captured DNS
mappings; encrypted external DNS can hide names, so IP rules remain the fallback.
Android app selection is VPN-wide: include only selected apps, or exclude selected apps.
Excluded apps bypass all destination rules because their traffic never enters the VPN.
This is the explicit precedence model, not a promise of undocumented ITOM behavior.

## Builds

The validated local builds from source checkpoint `f4db08b` are saved in [`dist/`](dist/):
the Android debug APK and Linux amd64/arm64 server, client, and scanner binaries.
Verify downloads with `sha256sum -c SHA256SUMS` from that directory. Build and
signature evidence is in `dist/validation/`.

The GitHub Actions `Prototype builds` workflow tests Go, builds Linux amd64/arm64
server and CLI binaries, builds a debug-signed Android APK, and uploads artifacts.
Native source dependencies and Go mobile tooling are pinned by the build script.
Debug APKs are for testing and are not Play Store releases.
