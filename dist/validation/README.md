# 0.3.6 validation

`race-final.txt` is the complete serialized race suite of final Go source
(core 42.370 s, mobile 1.079 s), followed by `vet-final.txt`. Current Android
build/lint, native UI rendering, signing identity, 16 KiB alignment, artifact
checksums and old/new CLI compatibility evidence live here. Native VPN lifecycle
and the physical Oppo are not exercised on this host.

`admission-before.txt` / `admission-final.txt` measure the same release/fair-queue
operation with 20 paths and 256 waiters. Median 20.28 us -> 5.393 us. This is a
scheduler operation, not total CPU or a fourfold bandwidth claim.

`transitions-before.txt`, `transitions-control-repeat.txt`, and
`transitions-final.txt` compare identical SHA-verified SOCKS download/upload/quiet
traffic with application echoes, idle streams, 20 paths, singles/pairs and
sparse downstream loss/queueing. Final four cases: upload 2.91–3.59 Mbps,
maximum echo 121.5–159.7 ms, no health-downs. The repeated unchanged baseline
also has an 8.35 s echo / 28 health-down outlier. Cause is not established;
a later good run cannot erase it or prove an RTT guarantee.

`intermediate/` retains rejected directional-ranking/deficit candidates and
release-only intermediates, including regressions. They are not the shipped
transport. The final release preserves placement and concurrency bounds and
reduces admission work instead. See docs/PERFORMANCE.md for context and the
user's separate phone measurements. Earlier release evidence is archived by
version, including the previous `0.3.5/intermediate/` record.
