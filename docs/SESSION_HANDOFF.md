# Version 0.2.1 — 20 October 2026

Current authorized work: investigate very high retry counts, fix/optimize transport,
apply a complete dark theme, and deliver matching APK/Linux binaries to GitHub.
Previous release was a346812 / 0.2.0. Prior session-stop instructions below remain
historical; user has authorized continued development, builds and publication.

- Shared per-resolver adaptive admission: initial up to 8 data exchanges, additive
  growth on useful payload, multiplicative decrease on timeout/SERVFAIL bursts,
  bounds 2..32. Download admission leaves room for upload; control bypasses this
  data limit and retains shared reserved slots. Queued admission does not start
  exchange timers. Scheduler includes queue depth and actual learned capacity.
- RTT variance can increase retransmission allowance while preserving the old
  four-RTT safety margin, configured ceiling and 300 ms floor. Health probes keep
  loaded-path estimates and cumulative error evidence instead of replacing them.
- Case-normalized DNS questions accepted; header-only TC responses trigger fully
  authenticated TCP fallback. Strict session/stream/sequence/nonce/HMAC checks stay.
- Server reserves control/upload room against read long-polls and replies with
  authenticated WAIT under saturation, replacing silent drops.
- Android 12+ uses best matching physical network callbacks. Earlier Android uses
  deterministic validated Ethernet/Wi-Fi/cellular preference. Available standby
  networks no longer blindly trigger rebinds. Real network changes preserve streams.
- Diagnostics distinguish logical exchanges/retries from per-member timeouts, DNS
  errors, invalid replies and socket errors; include rebind count, per-path queue,
  capacity and last failure. Dashboard shows retry percentage and major causes.
- Entire Android UI uses charcoal surfaces/mint accents, dark native dialogs,
  fields/spinners and system bars. Version 0.2.1 / versionCode 5. Retained signing
  certificate matches the installed 0.1.2/0.2.0 lineage; profiles are preserved.
- Capacity fixture: 32 KiB verified echo, one pair/window 32/25 ms delay, max eight
  in-flight data requests per resolver. Before: 16.120s / 942 logical retries /
  1903 dropped requests; final after: 2.692s / 1 retry / 35 drops. This reproduces
  overload, not the user's unknown production loss rate. Adaptive must be enabled.
- Go race/vet and network regressions passed; matching old/new endpoints transfer
  authenticated byte-verified 32 KiB HTTP uploads/downloads. Android build/lint,
  both JNI ABIs, 16 KiB ELF/ZIP alignment, signature/certificate and SHA checks
  are recorded in dist/validation. Previous evidence is under validation/0.2.0.
- Timing varied across runs (capacity 2.692–13.026s / 1–9 retries); all raw
  evidence retained. Final clean-path medians: upload 2.231s, download 0.468s,
  zero retries. Final loss echo p50 41ms / p95 45ms, all bytes preserved and 40
  logical retries matching injected loss. Do not turn these into phone guarantees.
- No physical Oppo runtime test or production network measurement was available.
  Do not claim phone throughput/RTT or actual retry cause from synthetic fixtures.

Production: t.atmosl.ink delegated via ns2.atmosl.ink to 136.148.220.253. Preserve
/etc/multidns/server.env and systemd public-IP listen override. No production key
or SSH access here. User's checkout may be detached: use explicit remote/branch:
  cd ~/multidns
  git pull --ff-only origin main
  (cd dist && sha256sum -c SHA256SUMS)
  sudo install -m 755 dist/multidns-server-linux-amd64 /usr/local/bin/multidns-server.new
  sudo mv -f /usr/local/bin/multidns-server.new /usr/local/bin/multidns-server
  sudo systemctl restart multidns
  sudo systemctl --no-pager --full status multidns

Build with retained toolchain and signing key as described in earlier notes.
Always set ANDROID_USER_HOME=/workspace/toolchains/android-user and
MULTIDNS_DEBUG_KEYSTORE=/workspace/toolchains/android-user/debug.keystore.
Certificate SHA256: 28d3a8d227f63d209bbf4575c128a2546e38e7265e95ec2243886ef5bfbc9c54.
Do not inspect/commit the private store, credentials, or server environment file.
No delegated agents were used; current developer instructions prohibit spawning
unless user/AGENTS/skill explicitly requests it.

Earlier notes below are historical.

# Version 0.2.0 — 20 October 2026

Current authorized work: fix Disconnect, redesign the entire Android UI, optimize
transport, and deliver an APK plus Linux binaries. This release implements those
changes. Historical session-stop instructions below are superseded by this request.

- Disconnect removes callbacks and closes TUN before asynchronous engine cleanup.
  Startup is cancelable; a stopped engine cannot resurrect a VPN. The same path
  handles the notification action and permission revocation. Stats/scan display
  snapshots run outside the UI thread.
- Native Android UI: warm neutral/teal cards, bottom navigation, one connection
  control, live rates, expandable settings, and a visual ordered routing editor.
- Transport: adaptive window scaling to 64; idle head-only polling; eight reserved
  control slots; bounded destination writer outside stream mutex; 2 ms read
  coalescing; negotiated selective queued ACKs with flush barrier; reused UDP/TCP
  sockets; cancellation/deadline bounds; old-network dial generation checks.
- Wire header stays v1. Supporting endpoints negotiate queued ACK bit 0 in Open
  sequence/response and use operation 8 for delivery barriers. Old endpoints work.
- APK: `dist/multidns-prototype-debug.apk`, version 0.2.0 / versionCode 4. Certificate
  matches the previously installed 0.1.2 APK. All six Linux tools rebuilt CGO-free.
- Retained signing key is outside Git. Direct Gradle builds select it through
  `MULTIDNS_DEBUG_KEYSTORE` or ignored `android/local.properties` entry
  `multidns.debugKeystore`. Do not fall back to another home directory's debug key.
- Go race tests/vet, Android build/lint, both JNI ABIs, all six ELF LOAD alignments,
  16 KiB APK ZIP alignment, signature/certificate and SHA-256 checks passed.
- Byte-verified HTTP transfers pass new/old, old/new, and new/new client/server.
- Final synthetic medians versus 62d8474: 2.86x upload, 1.88x download; adaptive
  mode, base window 8, 5x2 endpoints, 40 ms + periodic jitter, 73,728-byte payload.
  These are not public-resolver or Oppo performance claims.
- Correlated-loss echo test verifies payload with loaded probe p50 42 ms / p95
  109 ms. More detailed evidence and raw timings are in `dist/validation`.
- Device runtime verification remains pending: API 35/28 software emulators did
  not finish Android framework startup. A compiled custom device regression test
  covers actual VPN transfer, repeated UI disconnect, startup cancel and screens.
  No runtime APK test success is claimed. No physical phone was available.

The user's production setup remains t.atmosl.ink delegated via ns2.atmosl.ink to
136.148.220.253. Preserve `/etc/multidns/server.env` and the systemd public-IP listen
drop-in. No production key or SSH access exists in this workspace. Updating the
server binary and installing this signed APK as an update preserves configuration.

Earlier notes below are historical.

# Session handoff — 19 October 2026

## Throughput follow-up: version 0.1.2

The user confirmed 0.1.1 connects successfully on the Oppo. With ten resolvers
in five two-member groups, the user measured 2.71 Mbps down and 0.15 Mbps up;
small authenticated probes were 38.8–48.1 ms. These are user-reported results.

Version 0.1.2 removes full-batch barriers, computes upload QNAME capacity, reserves
upload/control capacity against idle polls, refreshes stream ACKs on retries,
uses bounded server long-polls and larger EDNS-budgeted replies, coalesces queued
upload writes, and schedules bulk reads/writes using separate payload-rate
estimates. Server WAIT replies now have a separate counter from transport
retries. The Android dashboard shows payload rates and offers a copy-diagnostics
button. See docs/PERFORMANCE.md and dist/validation for measurements and checks.
The replacement still needs testing on the user's actual phone/network.

## Android connection crash follow-up

The user's server at `136.148.220.253` serves `t.atmosl.ink`, delegated through
`ns2.atmosl.ink`. The user verified authenticated probes and HTTPS transfers
directly and through both 1.1.1.1 and 8.8.8.8 from the VPS. The authentication key
remains on the user's server and has not been shared with this workspace.

The first APK crashed on connection on an Oppo Find X9 Ultra after VPN consent.
Inspection found HEV's upstream JNI_OnLoad trying to register methods against
`hev.htproxy.TProxyService`, a class not present in MultiDNS. Version 0.1.1 builds
the HEV C API without that registration, preloads the bridge inside the guarded
connection worker, and catches Java/linkage failures on the adapter thread.
The new packaged-native check rejects the old APK. Successful connection with
the replacement APK on the user's phone is still pending.

## Publication update

GitHub write access was restored in the continuation session. All four original
checkpoints through `f4db08b` were pushed to `main`, and the first Actions run
started: https://github.com/Septonious/multidns/actions/runs/39322975678.
The locally validated APK, Linux binaries, checksums, and validation evidence are
being retained in `dist/` in Git because the release API is unavailable from the
execution environment. The earlier GitHub blocker below is historical.
Physical-device and public-recursive-path testing remain outstanding.

The user asked to save the workspace and end this session safely so they can
publish the workspace and continue in a new session. Do not resume development
or attempt another push until a new instruction arrives.

## Project and requested behavior

Project checkout: `/workspace/multidns`, local branch `work`, remote
`https://github.com/Septonious/multidns.git`. The remote was initially empty.
The user authorized checkpoint commits, GitHub pushes, and Actions builds.

Implemented prototype: authenticated unencrypted DNS TXT transport; independent
TCP streams; UDP datagrams and SOCKS5; resolver racing groups; striping/affinity;
adaptive interactive/bulk selection; health exclusion/recovery; automatic grouping
with partial groups and spares; Android VPN; encrypted profiles; app include/exclude
selection; ordered domain/IP/port/protocol routing; observed-DNS name mapping;
drag-and-drop resolver cards; live scanner with TXT/NS->A filtering, SQLite results,
newline export and pause/resume; concurrent manual authenticated RTT test with VPN off.

Automatic grouping means members/group and maximum groups. Eleven healthy
candidates at size 2/count 5 produce five groups and one spare; one candidate
produces one partial group. Automatic startup probes and periodic health checks
keep the live path independent from the saved candidate pool.

## Validation

- `go test -race ./...` passed, including HTTP through SOCKS, SOCKS UDP association,
  socket rebinding without stream reset, failed resolver paths, duplicate operations,
  concurrent streams, routing, authentication, scanner range parsing, automatic
  group edge cases, and adaptive policy selection.
- `go vet ./...` passed.
- The complete `scripts/build-android.sh` succeeded locally: generated Go AAR,
  both native ABIs, debug APK, and Android lint (localization warnings remain).
- `scripts/build-binaries.sh` built Linux amd64 and arm64 server/client/scanner.
- No physical Android device, real delegated server, or public-recursive-path
  throughput testing has been performed. The APK has not been installed on a device.
- Only TXT is a tunnel carrier in v1; NS->A is a scanner test, not an NS carrier.
- This is an independent prototype, not an upstream MasterDnsVPN fork and not
  wire-compatible with SlipNet, VayDNS, MasterDnsVPN, or ITOM configuration.
- Payload framing/encoding and scheduling require real-path performance tuning.

## GitHub blocker

Both push attempts returned HTTP 403:
`Permission to Septonious/multidns.git denied to Septonious`.
The GitHub plugin exposed no callable tools in this session. The GitHub CLI's
injected credential failed its status check; Git read access nevertheless works
through the platform proxy. Never print/extract credentials or ask for a token in chat.

All checkpoints are LOCAL ONLY. `.github/workflows/build.yml` is saved, but no
Actions run occurred and no GitHub artifact links exist. After write/workflow
access is restored, inspect local/remote state before pushing `HEAD:main`; preserve
any new remote work. Workflow triggers on pushes to main or workflow_dispatch.

## Files retained in the workspace

- Source, local Git history, docs, deploy templates, build scripts: this checkout.
- APK and Linux binaries: `/workspace/multidns/dist/` (ignored by Git, retained on disk).
- Go mobile AAR: `android/app/libs/multidns.aar` (ignored).
- Native dependency source: `build-deps/hev` at pinned public commits (ignored).
- Reference repositories: `/workspace/references/{MasterDnsVPN,slipgate,SlipNet}`.
- Toolchains and caches: `/workspace/toolchains/`.
- Standalone source archive and Git bundle: `/workspace/multidns-source.tar.gz`
  and `/workspace/multidns-checkpoint.bundle`, created at shutdown.
- Root handoff `/workspace/SESSION_HANDOFF.md` records the final commit and artifact paths.

## Tool activation

`/usr/bin/go` is an unrelated application. Use the installed Go toolchain explicitly.
The system Java runtime has no javac; use the retained JDK.

```bash
export PATH=/workspace/toolchains/go/bin:/workspace/toolchains/gopath/bin:/workspace/toolchains/jdk-21.0.12.1/bin:$PATH
export GOCACHE=/workspace/toolchains/cache
export GOPATH=/workspace/toolchains/gopath
export JAVA_HOME=/workspace/toolchains/jdk-21.0.12.1
export ANDROID_HOME=/workspace/toolchains/android
export ANDROID_USER_HOME=/workspace/toolchains/android-user
export GRADLE_USER_HOME=/workspace/toolchains/gradle-cache
cd /workspace/multidns
```

Use this existing checkout in the isolated environment; do not create a worktree.
For Java downloads in this environment, use the platform's existing proxy and
trusted CA store `/etc/ssl/certs/java/cacerts`. Java does not automatically honor
HTTPS_PROXY: set the corresponding Java proxy host/port properties from that URL
without printing credentials. Do not disable TLS or checksum checks. Public GitHub
Actions runners generally do not need these cloud-specific proxy settings.

No application service needs to remain running across publication. Server startup
requires the user's actual delegated domain and matching authentication key; do not
invent them. Read `README.md`, `docs/DESIGN.md`, and `docs/ANDROID_TESTING.md` before
continuing. Workspace publication/fresh-task restoration has not been verified.
