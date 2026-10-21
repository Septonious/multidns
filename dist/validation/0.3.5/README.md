# 0.3.5 validation

Current release evidence lives here; older evidence is archived by version.
`race-exact-release.txt` is the passing serialized full suite of the exact
release source (49.194 s core), followed by `vet-exact-release.txt`. Earlier
`race-release.txt` and `fallback-final.txt` (two repeats) additionally cover
TCP serial-credit cancellation and the final failed-probe-pair optimization. Android build/lint, native UI tests, signing identity, 16 KiB
alignment, Linux builds, checksums and old/new compatibility are retained here.

`transitions-baseline.txt` and `transitions-release.txt` compare identical SOCKS
fixtures, with SHA-verified upload/download, application echoes, singles/pairs,
8/20 paths, downstream loss/queueing and quiet recovery. See docs/PERFORMANCE.md
for the full table, including loss and uncertainty. Results are local, not
physical phone or production resolver measurements.

`intermediate/` intentionally retains failed candidates, timing outliers,
intermittent complete-suite and isolated test failures, and earlier passes.
A later pass does not identify a cause or erase those failures. No native VPN
lifecycle or physical Oppo run is available on this host.
