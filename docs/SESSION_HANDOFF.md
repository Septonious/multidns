# Session handoff — 19 October 2026

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
