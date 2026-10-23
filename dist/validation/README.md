# 0.3.8 validation

Final product race suite passed (core51.505s/mobile1.039s), followed by vet.
Android build/lint (0 errors/18 warnings), two Robolectric screen tests,
instrumentation compilation, original signing identity, both native ABIs,
16 KiB ZIP/ELF alignment and all seven checksums passed. Old/new, new/old and
new/new CLI pairs passed authenticated startup and byte-verified256KiB HTTP
uploads/downloads. No physical Oppo or native VPN lifecycle run on this host.

Upload-only control:20paths, four512KiB sends +26idle connections +app echo.
upload-before:2.87Mbps (p95118.8/max615.4ms),170timeouts.
upload-release:3.91/3.48Mbps (p95111.4/135.2,max144.8/169.9ms),0/1timeouts.
Use the precise transfer duration/2MiB useful total to convert to Mbps;
the benchmark's MB/s display is rounded.

Duplex SOCKS:20paths in pairs, four8MiB downloads alongside four1MiB uploads,
eight idle connections and byte-verified transfers/application echoes.
Before:7.646/1.124 and7.072/0.9794Mbps down/up,3/7downs,max1080/2573ms.
Final release:10.10/2.277Mbps,p95176.2/max436ms,zero downs.
head-benchmarks is an earlier same-policy check before version metadata change:
9.296/1.957Mbps,p95207.2/max920.2ms,zero downs. It is retained to show tail
variability. The500ms target is not a demonstrated hard bound, and these
client+simulated-server measurements on a two-CPU host are not phone speeds.

intermediate/ retains partial policies, including20downs and a timed-out
repeated duplex run. Its old benchmark b.Fatal background phase printed
contradictory FAIL/PASS; the final harness returns phase errors to the parent.
Do not count that PASS line as success. Later passing runs do not erase it.
See docs/PERFORMANCE.md for scope and remaining limits. Prior release evidence
is archived in0.3.7/, including its original intermediates and screenshots.
