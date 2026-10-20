# Performance and tuning

## Version 0.2.1: retries and resolver congestion

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
