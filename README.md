# MultiDNS

Go DNS tunnel and Android VPN with resolver groups, live scanning, profiles,
and app/destination split routing. Experimental wire protocol v1; incompatible
with other DNS tunnels. Optional zero-byte XOR obfuscation is not encryption; use application TLS for confidentiality.
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
In adaptive mode healthy fast paths carry controls with a delayed backup; groups
define data backups, and primary data work shares the whole resolver pool. Fixed scheduling retains group duplication.
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

Local builds are saved in [`dist/`](dist/): the Android debug APK and Linux
amd64/arm64 server, client, and scanner binaries. Version 0.3.2 shares request
windows across busy streams, schedules shared credits fairly by flow, moves
controls to healthy fast paths independently of group size, and backs off idle
polls. Broken TCP download fallbacks enter a directional cooldown while uploads
remain eligible. The dark Android UI uses text navigation, consistent spacing,
standard chevrons and less idle/status copy. Comprehensive counters and
**Find optimal parameters** remain under Diagnostics. See
[`docs/DIAGNOSTICS.md`](docs/DIAGNOSTICS.md) for settings and counter definitions.
The 0.3.0/0.3.1 server is compatible; update the APK/client for these changes.
Existing profiles, XOR obfuscation and authentication are unchanged.
Verify downloads with `sha256sum -c SHA256SUMS` from that directory. Build and
signature evidence is in `dist/validation/`.

See [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) for tuning, counter definitions,
and controlled before/after measurements. Resolver probe RTT is not a throughput
measurement. Members carry unique work; delayed backup attempts can duplicate it.

The GitHub Actions `Prototype builds` workflow tests Go, builds Linux amd64/arm64
server and CLI binaries, builds a debug-signed Android APK, and uploads artifacts.
Native source dependencies and Go mobile tooling are pinned by the build script.
Debug APKs are for testing and are not Play Store releases.
