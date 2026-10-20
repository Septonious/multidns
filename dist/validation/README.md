# MultiDNS 0.3.4 validation — 21 October 2026

Final source checks:

- `go-race-release-final.txt`: full Go race suite, core + mobile pass.
- `go-vet-release-final.txt`: successful vet; no output is expected.
- `failover-release.txt`: three race-instrumented repetitions of real resolver
  outage/redistribution, stable reachability, probe cancellation and recovery.
- `automatic-tests.txt`: lightweight summaries and automatic-control regressions.
- `compatibility-final.txt`: old/new clients and servers authenticate and transfer
  byte-verified 32 KiB HTTP uploads/downloads in all three combinations.
- `android-build-final.txt`, `android-package-final.txt`: two native ABIs, APK
  assembly and lint. `ui-render.txt` and `ui-tests.xml`: native Robolectric views
  at 360 dp and 320 dp / 130% font, live cards, debug switch/copy and navigation.
- `apk-signature.txt`: retained debug certificate SHA-256
  `28d3a8d227f63d209bbf4575c128a2546e38e7265e95ec2243886ef5bfbc9c54`.
  Package version is 0.3.4, code 11. Native/ELF/ZIP logs verify both ABIs and
  16 KiB alignment. `artifact-checksums.txt` verifies all seven build artifacts.

Native Android view previews use fixture data, not screenshots from the phone:
[Servers](ui/servers.png), [Connected](ui/connected.png),
[Live resolvers](ui/resolvers-live.png), [Latency tests](ui/diagnostics.png),
[Scanner](ui/scanner.png), [Routing](ui/routing.png),
[Narrow / large font](ui/diagnostics-narrow-font-130.png).

Controlled performance data is in `benchmark-results.json`, `pool-*.txt`, and
`upload-release-final.txt`, with setup and comparisons in
[`docs/PERFORMANCE.md`](../../docs/PERFORMANCE.md). Final six cases verified all
payloads and 229 application echoes, maximum 261.3 ms. This is not a promise of
500 ms worst-case RTT on the phone or Internet.

Intermediate candidates/failures are retained under `intermediate/`. In
particular, a 1024-credit candidate caused severe latency regressions and is not
shipped. A mixed-traffic race run timed out before its targeted recheck and the
final complete suite passed. No physical Oppo, ICMP policy, or live native VPN
lifecycle execution was available here. The updated device instrumentation was
compiled, not run. Earlier release evidence is archived in versioned folders.

`apk-compaction.txt` verifies that a fresh package removes incremental ZIP gaps
(20,499,716 -> 13,922,524 bytes) while every APK entry remains byte-identical.
The retained signature, all ABIs and native alignment were verified again.
