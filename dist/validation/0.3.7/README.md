# 0.3.7 validation

Full final-product race suite passed (core 51.221 s, mobile 1.080 s), followed
by vet. Additional RX relearning, cancellation/TCP repeats, late-reply and
backpressure repeats passed. Android build, lint (0 errors/18 warnings), two
Robolectric UI tests, instrumentation compilation, original signing identity,
16 KiB ZIP/ELF alignment, both HEV/JNI ABIs and all seven checksums passed.
Old/new, new/old and new/new CLI pairs transferred byte-verified 256 KiB HTTP
uploads/downloads. The physical Oppo and native VPN lifecycle were not tested.

capacity-before/final use identical 36-path SHA-verified sustained traffic:
0.3.6 11.80/2.902 Mbps down/up, p95 109.7/max 1127 ms, zero health-downs;
0.3.7 13.13/2.522 Mbps, p95 159.9/max 775.7 ms, four health-downs.
Upload and status transitions regressed in this sample. Do not claim a
consistent speed or stability improvement from these measurements.

sustained-before/final add deterministic sparse read loss and queueing:
original group-two baseline 8.094/1.656 Mbps, p95 212.1/max 723.8 ms, five downs;
final policy 6.766/2.145 Mbps, p95 228.9/max 1489 ms, zero downs.
The 500 ms target is not a proven hard bound. Global/path feedback and recovery
now cover previously ignored physical outcomes, but remaining tails require
production measurements. See docs/PERFORMANCE.md for fixture scope and limits.

intermediate/ preserves failed full-suite/TCP runs and earlier candidates.
The TCP timing fixture changed 150 to 400 ms with its original zero-timeout
and bounded-connection assertions retained. A later pass does not erase old
failures. Previous release evidence, including its intermediates, is in 0.3.6/.
