# Performance and tuning

## Version 0.3.3: upload ACK clock, pacing and loss tolerance

The user's 0.3.2 phone measurement improved to 4.78 Mbps down / 0.60 Mbps up,
87 ms idle ping and 189/201 ms download/upload-loaded ping. Shared admission was
empty; resolver queue EWMA was about 1–25 ms. The four fast 31.131 paths recorded
529 timeouts in 30806 attempts (~1.72%), yet repeatedly halved useful capacity.
These are user measurements of 0.3.2, not results for 0.3.3.

Changes retain the wire format and 0.3.0+ server compatibility:

- Upload losses now use a write-only ACK clock. The UDP implementation previously
  hardcoded the read timeout for every operation. Known healthy writes now use
  max(160 ms,2*ACK_RTT+max(25 ms,4*ACK_variation)), bounded by the profile timeout.
  Read recovery retains an 80 ms server-wait allowance and a 200 ms floor. Unknown
  paths retain the configured timeout. Idle/stalled write clocks expire; a failed
  read or isolated lost write does not discard contemporaneous upload progress.
- Sparse loss (<=4% of the last 128 observed data outcomes, with at least 64 samples
  and acceptable ACK RTT) cuts capacity by about 1/8, at most once per 500 ms.
  Unknown/high loss still halves capacity. Three losses within one ACK clock
  can escalate a mild cut once, before its cooldown expires. This avoids treating
  a hard resolver capacity limit as independent random loss. Initial canaries, quarantine, ARQ,
  packet identity and physical wire-credit retention remain intact. Canceled
  attempts are excluded from these outcome samples.
- Payload ACKs grow by one credit per capacity-sized success batch, rather than
  two. Auto capacity can reach 64; per-path RX remains 32. Configured TX/RX limits
  still apply. A single sustained upload can use the full negotiated 256-position
  reorder budget within the shared 240-data-exchange budget, instead of a 128 cap.
- Bulk writes are paced by ACK_RTT/capacity (minimum 100 microseconds), with leases
  retained while waiting. Short writes and controls bypass pacing. Canceled
  paced sends release credits and cannot leave an unbounded future-send queue.
- The default loaded_rtt_limit_ms is 500 (range 100..2000). The controller halves
  capacity when smoothed write ACK RTT exceeds (budget-80)/2; growing variance
  cannot mask this guard. Existing latency_target_ms remains a softer per-path
  queue target. This approximation cannot bound destination RTT, VPN-stack
  buffering or every loss/failover outlier; final phone measurement is required.
- A paired group's backup count no longer grows when its primary is admitted on
  another group's resolver. This avoids an unintended third copy of a chunk.
- Tuning probes TX up to 64, RX up to 32. A fully received but latency-inflated
  concurrency trial is repeated once; persistent delay or packet loss still stops
  growth. Both attempts are retained in the report, with repeated=true.

The new benchmark isolates upload: four concurrent 128 KiB sinks return SHA-256
only after receiving each complete payload. An application echo and 26 idle TCP
sessions share eight 35..90 ms DNS paths. Every 71st data query is deliberately
lost per resolver. The queued case adds 8 ms for each concurrent request above 8.
Controls incur the path delay too; echo includes Write/Flush/Read application I/O.
Each row records one byte-verified 512 KiB upload batch, not phone throughput.

Quiet sequential comparison on this two-CPU workspace (one baseline batch and
 two final batches per case):

| Queued | Version | Upload time (s) | Upload (Mbps) | Echo p95 (ms) | Echo p99 (ms) | Slowest echo (ms) | Samples | Retries |
|---|---|---:|---:|---:|---:|---:|---:|---:|
| No | 0.3.2 | 11.41 | 0.368 | 236.6 | 434.6 | 461.3 | 79 | 86 |
| No | 0.3.3 | 3.71 | 1.130 | 191.6 | 295.7 | 303.1 | 28 | 78 |
| No | 0.3.3 | 5.00 | 0.839 | 197.8 | 260.6 | 384.3 | 35 | 75 |
| Yes | 0.3.2 | 11.81 | 0.355 | 240.8 | 557.5 | 715.4 | 78 | 88 |
| Yes | 0.3.3 | 6.80 | 0.617 | 343.8 | 443.1 | 781.7 | 38 | 113 |
| Yes | 0.3.3 | 6.12 | 0.685 | 189.7 | 262.2 | 349.1 | 39 | 78 |

The final code improved completion time by2.3–3.1x without the simulated queue,
and1.7–1.9x with it. Final p99 stayed below444 ms; one of140 application echoes
reached781.7 ms during recovery. This is explicitly not a guaranteed500 ms
worst-case bound. Earlier candidates and their failed/slow runs are retained too;
do not cite an earlier candidate's425 ms maximum as the final build's maximum.
All payload digests matched. No0.3.3 phone throughput was measured here.

Regression checks cover sparse loss versus clustered loss, hard ACK-budget cuts
that cannot be masked by variance, directional on-wire timeout, expiration of
stale clocks after stopped progress, full negotiated upload-window use and
cancellation without credit leaks. The hard8-concurrent-request resolver fixture
passes three race-instrumented repetitions after adding burst escalation; an
intermediate version exceeded its retry bound (26 versus25) and is retained.
The tuner fixture injects one fully received latency spike;0.3.2 stops atRX1,
while0.3.3 confirms it and still discovers the2..4-worker usable range.

## Version 0.3.2: many-stream queueing and group-independent controls

The user's 0.3.1 snapshot showed 333 shared waiting requests, about 400 ms
smoothed local queue delay and peaks around 2.28 seconds, despite fast DNS RTTs.
Phone results were 3.53/0.74 and 3.74/0.55 Mbps, with upload-loaded latency
657/870 ms and download-loaded latency 304/1308 ms. These are user measurements,
not measurements of this update.

Changing the same eight resolvers from pairs to single-member groups does not
multiply primary bandwidth: adaptive admission already uses the full pool.
Previously controls remained pinned to their original group; single-member
groups could therefore delay a flush/handshake on one slow path. Adaptive
controls now choose at most two healthy low-cost paths across groups, sending
a delayed backup rather than immediately duplicating every control packet.
Bulk backup policy remains group-based; one-member groups do not hedge data.

Every busy stream previously offered a window sized for the whole resolver pool.
TX/RX windows now divide usable directional capacity between active bulk flows,
with two refill positions per flow. Completed readers age out after one second;
idle connection heads do not retain bulk window shares. Admission selects the
eligible flow with the fewest wire leases; at equal counts, old bulk requests
beat fresh short requests after 50 ms. This prevents one older large transfer
from filling the queue ahead of every new request. It does not promise that
application loaded RTT stays below 50 ms or remove all device/server buffering.

Idle head reads back off to 160 ms between repeated WAIT responses. Local writes
wake the wait promptly; busy reads retain immediate refill. Failed authenticated
TCP download fallbacks quarantine only that path's reads for 2, 4, 8, 16, then
30 seconds. Tiny health probes and successful uploads cannot clear the cooldown.
After expiry, a read can test recovery; a validated TCP success clears its streak.
This addresses a path with 60 TCP fallbacks, 26 reconnections and only 20,755 B
of accepted downloads in the supplied snapshot, without disabling useful uploads.
New diagnostics split data/control backups and expose failed TCP fallbacks and
remaining read cooldown. No wire format or cryptographic overhead was added.

One sequential quiet comparison on this two-CPU workspace (one measured batch):

| Group members | Version | 8 echo transfers (s) | App echo p95 (ms) | Echo samples | Peak local queue (ms) | ARQ retries |
|---|---|---:|---:|---:|---:|---:|
| 1 | 0.3.1 | 42.91 | 11411.0 | 9 | 13296.0 | 27 |
| 1 | 0.3.2 | 8.57 | 317.3 | 43 | 360.4 | 0 |
| 2 | 0.3.1 | 32.60 | 8306.0 | 8 | 8296.0 | 44 |
| 2 | 0.3.2 | 9.42 | 335.5 | 45 | 953.9 | 30 |

All transfers were byte-verified. The baseline collected few interactive samples
because each waited much longer. Retries remain possible under load; one updated
paired run still recorded 30, so this is not a claim of eliminating retransmission.
Raw logs are retained, including earlier exploratory runs with build contention.

Controlled results and limitations are recorded in `dist/validation/summary.json`.
The many-stream benchmark verifies eight concurrent 64 KiB echo transfers beside
26 idle connections and application-level echo, across eight heterogeneous DNS
paths. Echo latency includes both tunnel directions, admission and application
I/O; it is not a control probe or a prediction of the Oppo's Speedtest results.

## Version 0.3.1: repair 0.3.0 starvation and capacity collapse

The user's real 0.3.0 link became unusable despite successful authentication.
Its snapshot showed 162 queued requests, seven paths at capacity two (one at
three), and per-path admission peaks above 33 seconds. Even a resolver with zero
timeouts collapsed to two credits. This is a client regression; the earlier
controlled throughput comparisons did not cover this failure condition and do
not establish real-link usability.

Three reproductions fail on 0.3.0 and pass after the fix:

- Nonempty reads can include the server's deliberate 80 ms long-poll. Mixing
  those timings with immediate write ACKs falsely cut capacity from eight to
  two. Reads no longer drive delay cuts; sustained smoothed write ACK delay,
  with jitter allowance, still reduces capacity under actual delay inflation.
- Idle-poll recovery checked `path.waiting`, although adaptive primary work waits
  in `client.admissions`. Successful idle replies now inspect the shared backlog,
  recovering toward the startup limit while respecting configured ceilings and
  loss cooldown. They do not grow indefinitely on empty polls.
- Fresh short requests could continuously preempt eligible bulk work. Selection
  now honors the oldest eligible operation after 50 ms of waiting. Blocked reads
  do not block free TX room, and hedge attempts cannot bypass queued primaries.
  A short-lived cached selection reduces repeated scans during shared wakeups.
  Hard bulk-credit reservation is removed; bounded priority handles short flows.

New regression coverage starts 26 idle streams, forcibly collapses all eight
path budgets to two and runs concurrent byte-verified TLS HTTPS uploads/downloads
while injecting successful read delays. Groups of one and two are both checked,
along with fixed-scheduling fallback. A separate test verifies write jitter does
not cut capacity but persistent write delay still does. Capacity-loss cuts,
capacity-delay cuts, idle capacity recoveries and oldest queued age are exposed
in diagnostics. Full raw results are in `dist/validation`.

These are regression checks, not a phone throughput claim. No physical Oppo or
production-link measurement is available here. The unchanged 0.3.0 server protocol
is compatible with the repaired APK. Disabling adaptive scheduling is a temporary
workaround in 0.3.0; install the hotfix before enabling it again.

## Version 0.3.0: selective upload credits and loaded application latency

The user reported 3.42 Mbit/s download, 0.40 Mbit/s upload, 121 ms unloaded ping,
and 1028/974 ms loaded ping. Their snapshot had 399 physical timeouts in 84,160
resolver attempts (~0.47%) and 27 logical retries in 70,250 exchanges (~0.038%).
All eight paths were full, with 294 additional locally queued requests. Loss
alone does not explain that backlog or loaded application RTT.

Changes:

- Upload workers now refill on each selective acceptance ACK, independently of
  the contiguous prefix. An earlier slow/lost chunk no longer idles otherwise
  free workers. The separate negotiated 64/256 reorder bound and final committed
  flush barrier preserve ordering, bounded memory and successful Write semantics.
- Adaptive primaries enter one shared FIFO queue per operation/priority. At
  admission, work selects a healthy path with room, including outside its original
  group. Interactive data/head reads have priority, bulk leaves one interactive
  credit, and waiting uploads reserve half the available read concurrency.
- A reserved credit already occupied by an interactive read does not block the
  remaining upload credit. Busy backups are skipped instead of queuing another
  speculative attempt. Single-member groups keep one unique attempt after global
  selection; a paired group's hedge delay follows its actual chosen primary.
- Useful data-exchange RTT above a recent minimum plus the delay target reduces
  path concurrency before loss. Existing loss feedback and wire-credit retention
  remain. Speculative windows are capped at 128 upload workers/64 read positions,
  with the negotiated upload reorder limit enforced independently.
- The first observed failure quarantines a resolver and unowned queued work
  redistributes. Single canaries after cooldown and two-second authenticated health
  checks restore recovered paths. Silent failures still need a timeout/error to
  become observable. Socket diagnostics now preserve the underlying read error.
- A new Diagnostics tab provides per-path counters, errors, queue delay and manual
  settings. Full-size authenticated MTU/TX/RX tests produce evidence and explicitly
  applied per-resolver recommendations. See [Diagnostics](DIAGNOSTICS.md).
- Optional XOR payload obfuscation adds no bytes; authentication remains enabled.
  It does not add TLS or claim confidentiality. Application HTTPS retains its TLS.

Final controlled comparison, two runs each, same host and fixtures:

| Workload | 0.2.2 | 0.3.0 |
| --- | --- | --- |
| Asymmetric 256 KiB upload | 29.605 / 12.030 s | 7.840 / 8.381 s |
| Asymmetric 256 KiB download | 1.679 / 1.731 s | 1.663 / 1.499 s |
| Four simultaneous upload/echo transfers | 7.582 / 7.545 s | 2.406 / 3.616 s |
| Loaded **application** echo p95 | 614.5 / 856.3 ms | 75.63 / 71.29 ms |

The asymmetric median upload improves 2.57x in these runs, but the old version's
upload variance is large. Four-stream median transfer time improves 2.51x; its
median application echo p95 falls about 90%. Unlike earlier control-only Probe
measurements, this echo passes through real tunnel data admission during load.
This still does not establish phone speed or Speedtest loaded RTT. The asymmetric
fixture remains subject to capacity-discovery loss (47/49 logical upload retries
in the new runs versus 34/7 old); a new loaded run also had 17 retries. Discovery
is not loss-free. Tuner recommendations and the user's next real-link test are
needed to choose stable per-resolver limits.

The mixed pool uses eight resolvers at 44..204 ms, a hard eight-request data
capacity and fourteen idle streams. The loaded application fixture uses four
one-member groups, four 32 KiB uploads with byte-verified echo readback, and a
separate short echo stream. DNS base delay is 20 ms, with 8 ms added per request
above eight in flight. Both benchmark sources are tracked; raw final runs,
intermediate runs and machine-readable measurements are in `dist/validation`.
XOR is off for comparative benchmarks; separate race tests verify obfuscation,
MTU bounds, corruption rejection, tuner results, cancellation, selective progress,
independent-path failure/recovery and byte-exact transfers. The full race suite,
vet and old/new interoperability checks passed. No physical Oppo measurement is
available from this environment.

Earlier versions below are historical descriptions and limits.

## Version 0.2.2: useful parallelism and wire occupancy

The user's 0.2.1 snapshot shows 47 logical retries in 71,894 attempts (0.065%),
225 physical timeouts in 113,740 resolver attempts (0.198%), no DNS/authentication
errors, and all 26 socket errors on 94.25.113.230. Throughput was 1.25 Mbit/s down
and 0.27 Mbit/s up with four pairs; probe RTT was 54 ms. Those are user-reported
measurements. The counters do not establish the socket error's underlying errno,
or an independent sustained capacity for each of the eight addresses.

Several client limits wasted useful capacity even with low logical loss:

- Bulk scheduling treated payload/time for a single reply as aggregate resolver
  capacity. It now weights that service rate by learned parallelism and queue
  depth, and adds the capacity of members carrying independent bulk work.
- Adaptive data requests select a primary and delay the backup. Bulk primaries
  are weighted across members; short data operations prefer lower expected latency.
  Controls still race immediately. A failing primary starts the backup early;
  idle read hedges allow the server's 80 ms wait. Healthy traffic avoids sending
  two copies of every chunk while retaining fallback.
- Canceling a race does not cancel a query already running on a recursor. The
  socket now retains its transaction until a matching reply, expiry, or reset.
  Adaptive path and shared budgets remain occupied for that wire lifetime.
  Diagnostics add `wire_in_flight`, the leased resolver budget: it includes
  admission to shared slots and canceled wire requests, and can exceed active
  RPC `in_flight`.
- Adaptive windows follow aggregate learned credits after warm-up, capped at 64
  download chunks and 256 upload chunks on a supporting server. Open bit 1
  negotiates the larger upload reorder bound; old peers retain 64. The existing
  queued-ACK/flush delivery guarantee is preserved. No v1 header changes.
- Download prefetch includes unread application buffers in its 64-chunk credit
  bound and wakes when the application consumes a chunk. Adaptive shared limits
  are 256 total / 240 data / 160 reads, leaving 16 control positions. Fixed mode
  retains its prior 128 / 120 / 96 limits and configured stream window.
- Idle head reads re-evaluate placement after WAIT. Queued successful idle polls
  can restore a collapsed path toward its startup budget, at most one credit per
  second; they cannot grow it above that startup level. Losses from requests sent
  before an earlier cut do not repeatedly halve the new budget.

`goodput_bytes_per_second` and directional rate fields remain **per-exchange
service estimates**, not measured aggregate resolver bandwidth. `hedges` counts
delayed backup resolver exchange attempts. `peer_upload_window` reports the largest negotiated
upload bound observed in this client session: 256 confirms the updated server;
64 indicates fallback. `transport_version` identifies this transport build.

### Mixed-path measurement

`BenchmarkAsymmetricPool` checks a 262,144-byte HTTP payload through four pairs,
44/54, 44/84, 46/157, and 51/204 ms delays, eight simultaneous data requests per
resolver, fourteen idle TCP streams, and one path initially reduced to two credits.
Upload is verified by a SHA-256 response; download is compared byte for byte.
Concurrent authenticated probes measure loaded control latency.

| Direction | 0.2.1 baseline | 0.2.2 median (3 runs) | Relative payload rate |
|---|---:|---:|---:|
| Upload | 23.854 s | 14.287 s | 1.67× |
| Download | 3.191 s | 2.081 s | 1.53× |

The final repeated run's probe p95 values ranged from 46–76 ms. An earlier updated
upload took 21.489 s with a 313 ms probe p95; that outlier is retained in
`performance-first-after.txt`. The clean 5×2 benchmark measured 2.243 → 1.629 s
upload and 0.544 → 0.396 s download, each with zero retries. These are short fixture
measurements; the baseline mixed-pool table has one run, not a statistical claim
of a universal speed multiplier.

Raw before/after results and diagnostic snapshots are in `dist/validation`.
They are controlled tests, not a prediction of phone speeds. Earlier prototypes
that did not improve throughput were discarded. Different public resolver IPs
may still share infrastructure, upstream bandwidth, or per-domain rate limits.
The fixture models in-flight limits rather than proving any provider's actual
limits. Compare the same pool before changing candidates.

The race tests use 1 MiB DNS socket buffers, matching the production listener.
The large backpressure and correlated-loss fixtures allow 20/30 seconds under the
race detector on this two-CPU host; their byte-integrity and progress assertions
remain intact. These time bounds are test deadlines, not client timeout defaults.

Install both matching APK and server, enable adaptive scheduling, and start with
base window 8. Keep the existing resolver pool for the first comparison. Preserve
the server environment file and install the APK as an update to keep profiles.

## Historical 0.2.1: retries and resolver congestion

Low probe latency does not establish a resolver's sustained capacity. The previous
client enlarged stream windows without reducing requests to an overloaded resolver.
Many streams could also fill a shared path independently. Adaptive mode now shares
an admission budget across all streams for each resolver. It starts at up to eight
data exchanges, grows additively on useful payload, and halves after timeout or
SERVFAIL bursts (minimum two, maximum 32). Downloads leave upload capacity available;
control traffic retains its separate shared reservation. Waiting for admission does
not consume global slots or run the DNS response timeout. Canceled race members
release their admission without being classified as loss. Fixed scheduling remains
available when adaptive mode is disabled.

The scheduler accounts for queued requests and learned capacity. RTT variance now
increases the retransmission allowance when necessary, retaining the previous
four-RTT margin, 300 ms floor and configured timeout ceiling. Health probes preserve
bulk loss counters and loaded RTT estimates. DNS question matching accepts case
normalization, and a header-only truncated reply can trigger TCP fallback. Only a
fully authenticated TCP answer can complete that RPC.

Server read long-polls have a 192-request sublimit within the existing 256-handler
budget. A saturated server sends an authenticated WAIT reply rather than silently
dropping a request. Android 12+ follows the system's best matching physical network;
older versions consistently prefer validated Ethernet/Wi-Fi over standby cellular.
Merely receiving a callback for another available network no longer blindly selects
it. Real changes still rebind sockets while preserving the tunnel session.

### Capacity-limited regression

An identical fixture was run against `a346812` (0.2.0) and this version: one racing
pair, base window 32, adaptive scheduling, 25 ms request delay, eight simultaneous
data requests allowed per resolver, and a byte-verified 32,768-byte TCP echo.
Requests over that capacity are deliberately dropped.

| Version | Completion | Logical retries | Requests dropped by fixture |
|---|---:|---:|---:|
| 0.2.0 | 16.120 s | 942 | 1903 |
| 0.2.1 final run | 2.692 s | 1 | 35 |

The recorded repeated runs varied: the new capacity fixture took 2.692–13.026 s
with 1–9 retries. Final clean-path medians were 2.231 s upload / 0.468 s download
with zero retries; the baseline medians were 2.325 s / 0.786 s. Other recorded
runs had longer times and additional timeouts. Keep the raw variability in mind;
these short fixtures do not establish a reliable speed multiplier. The final
correlated-loss echo recovered all 86,016 bytes with exactly 40 logical retries
for the injected loss, and loaded probes p50 41 ms / p95 45 ms (118 samples).

These are controlled measurements, not a diagnosis of the user's specific network
or a promise of phone speed. Small discovery losses are expected as capacity grows.
The original multi-group clean-path benchmark and correlated-loss echo are retained
in `dist/validation/`; compare throughput, loaded RTT and errors together. Packet
loss cannot be removed by client tuning, and different IPs may share one rate limit.

Use adaptive scheduling, base window 8, the same resolver groups and no background
scan for the first phone comparison. Install the matching APK and server update.
The APK now uses a dark charcoal/mint theme throughout, including native dialogs
and system bars. The signing certificate is unchanged, preserving saved profiles.

## Historical 0.2.0: lifecycle, load handling and sustained traffic

The base window saved in existing profiles remains unchanged. In adaptive mode,
useful traffic gradually widens the per-stream window after 4 KiB, bounded by the
base window times active group count and 64. Fixed-window mode remains available
by disabling adaptive scheduling. Default shared limits stay at 128 exchanges,
96 download exchanges and 120 data exchanges; eight positions remain available
for connection control. Larger per-stream windows do not raise that global cap.

Only an idle stream's head read continues polling. Future WAIT positions wait for
ordered progress. A 32-position stream with a racing pair used 10 DNS requests
in a measured idle 500 ms interval. Healthy paths use an RTT-based retransmission
timeout with jitter allowance, a 300 ms floor and the configured timeout ceiling.

Supporting clients and servers negotiate selective queued upload ACKs, then use
a flush barrier before a successful Write returns. This recovers missing packets
without holding every later packet at an acknowledgement barrier and preserves
delivery on close. Destination writes run outside the stream mutex, so stalled
TCP destinations do not block read/close. Tiny destination TCP writes coalesce
for at most 2 ms. UDP framing remains intact. TCP fallback reuses a multiplexed
connection instead of opening one for every truncated response; 24 full-size
replies were verified across two connections with a network reset in between.
Socket creation never holds the shared map lock across protection/dial callbacks;
reset invalidates in-progress old-network dials. DNS writes honor deadlines.

### 0.1.2 → 0.2.0 controlled comparison

Baseline is `62d8474` (0.1.2). Both versions use the **same** benchmark with adaptive
mode enabled: five groups of two endpoints, base window 8, 40 ms delay plus 40 ms
on every eighth sequence, a 500 ms timeout and a byte-verified 73,728-byte HTTP
transfer. These adaptive results are a new comparison; the earlier 0.1.1→0.1.2
measurements below used fixed windows and are historical.

| Direction | 0.1.2 median | 0.2.0 median | Relative payload rate |
|---|---:|---:|---:|
| Upload | 6.745 s | 2.355 s | 2.86× |
| Download | 1.029 s | 0.548 s | 1.88× |

Three one-transfer runs per direction are retained in `dist/validation/`, with
raw timings, request/retry counts and machine-readable medians. The larger
prefetch window can send more speculative requests at the end of a download;
these are bounded and do not continue as full-window idle polling. One download
run had four retries and took 1.219 s. Results vary with host scheduling and are
not predictions or guarantees for public resolvers or the user's phone.

A separate 40 ms path test drops both initial raced requests for every nineteenth
selected upload sequence. An 86,016-byte echo completed with byte-for-byte
integrity; concurrent authenticated probes measured p50 42 ms / p95 109 ms across
110 samples. This is an intentionally correlated-loss fixture, not a measured
public-network loss rate. Bounds, duplicate operations, delayed consumers,
network resets, TCP truncation fallback, blocked destinations, and cancellation
are also checked under the Go race detector.

Keep the same resolver groups for the first phone comparison. Use adaptive mode
and base window 8, with background scans stopped. Compare sustained transfers,
loaded latency and retries before raising the base window to 16. Resolver IPs can
share an upstream/rate limit; adding addresses is not guaranteed additive capacity.

## Historical 0.1.2 changes


Version 0.1.2 addresses transport bottlenecks found after the first successful
Android connection. The user's initial measurement was 2.71 Mbps down and
0.15 Mbps up with five racing groups of two resolvers and 38.8–48.1 ms probe RTT.
Those are user-reported results, not a benchmark from this workspace.

## What changed

- Uploads and downloads use rolling windows rather than full-batch barriers.
- Upload payload size uses the available authenticated Base32 QNAME budget,
  replacing the fixed 80-byte cap. The name is still bounded to 254 characters.
- Server download payloads grow from 640 to at most 768 bytes, reduced when
  needed so the encoded reply for the tunnel domain fits 1232 bytes.
- Server reads wait up to 80 ms for the requested chunk before returning WAIT.
  Notifications wake all duplicate readers without consuming another chunk.
- At most 96 of the 128 client request slots can be occupied by download polls;
  uploads and connection control can proceed while streams wait for data.
- Server writes combine a contiguous buffered upload prefix into one write.
- Upload and download payload-rate estimates are separate. Control/probe bytes
  no longer count as useful payload, and idle waits do not inflate network RTT.

The authenticated wire format remains v1. Old and new endpoints can interoperate,
but both endpoints should be updated to obtain the full improvement.

## Reading the counters

- `exchanges`: logical attempts, including valid WAIT polls. The dashboard shows
  `100 * retries / exchanges`; this is not physical packet-loss percentage.
- `queries`: resolver exchange attempts, including duplicate group members.
- `timeouts`, `dns_errors`, `invalid_replies`, `socket_errors`: uncanceled failures
  per resolver attempt. Two members can fail in one logical retry, or one can fail
  while the other succeeds. Counts need not match logical retries.
- `network_changes`: socket rebinds requested by the network layer.
- Per-resolver diagnostics retain cumulative error counters, last error, RTT
  variance, learned `capacity`, and requests `queued` for admission. Health probes
  do not erase this evidence; their own test traffic is outside transfer counters.
- `replies`: valid authenticated responses observed before race cancellation.
- `retries`: failed transport exchanges that need another attempt. Cancellation
  while shutting down is not counted as a failed exchange.
- `waits`: valid server replies saying an operation/data is not yet ready.
- `uploaded` / `downloaded`: delivered stream payload bytes, not DNS wire bytes.

Before 0.1.2, `retries` also counted server WAIT replies. That made idle polling
look like packet loss. A two-member racing group normally sends two queries for
one logical exchange; a query/reply ratio near 2 is not a 50% loss measurement.

## Controlled measurements

`BenchmarkTunnelTransfer` uses five groups of two loopback DNS endpoints, a
40 ms delay per DNS request, another 40 ms on every eighth sequence, window 8,
a 500 ms exchange timeout, and a 73,728-byte HTTP upload or download. It verifies
the transferred data. Three one-transfer runs are used for each direction.

The baseline is source checkpoint `bf6e726` (0.1.1). Its existing test helpers
are generalized to testing.TB and the same benchmark file is copied into the
baseline snapshot. The median timing and request counts are recorded below.
These synthetic tests do not predict absolute speeds through public resolvers.

| Direction | 0.1.1 median | 0.1.2 median | Relative payload rate | DNS requests before → after |
|---|---:|---:|---:|---:|
| Upload | 10.46 s | 7.44 s | 1.41× | 2548 → 1677 |
| Download | 1.29 s | 1.05 s | 1.23× | 260 → 212 |

Run the current benchmark with:

```bash
go test ./core -run '^$' -bench BenchmarkTunnelTransfer -benchtime=1x -count=3
```

## Phone test procedure

1. Update the server binary and install the matching APK as an update.
2. Preserve the ten candidates and five groups of two for the first comparison.
3. Use stripe mode with adaptive scheduling enabled and window 8. Repeat the
   same speed test on the same Wi-Fi/mobile network, with background scans stopped.
4. Try window 16 and compare sustained upload/download, loaded latency, and
   network retries. Try 32 only if 16 is stable; extra concurrency can overload
   a resolver or trigger rate limiting.
5. Compare against the fastest two resolvers in one group. More IP addresses do
   not guarantee more capacity; multiple addresses can share an upstream service
   or the same rate limit. Racing members provide redundancy rather than additive
   payload capacity.
6. Copy connection diagnostics after each run to preserve the full counters.

Small authenticated probes establish basic reachability/latency. A full transfer
is still needed to test long queries, large replies, loss under load and sustained
rate limits. SlipNet's generic EDNS/DPI badges are only candidate filters.

The client already supported TCP retry on truncated UDP replies before 0.1.2.
The updated server also explicitly honors the advertised UDP response limit.
EDNS(1232) remains preferable; frequent TCP fallback can reduce throughput.
