# Resolvers and diagnostics (0.3.4)

## Automatic transport

Android always adapts resolver capacity, TX/RX sharing, pacing, retransmission
timeouts and failover from live traffic. Saved per-resolver overrides from older
versions are ignored and removed from the editor. There is no manual resolver
settings or parameter-discovery menu. CLI profiles retain their older settings
for compatibility.

Upload payload is calculated from the actual DNS name limit and tunnel-domain
length. DNS replies retain the 1232-byte EDNS ceiling. Failed TCP download
fallbacks temporarily remove that path from reads while preserving uploads;
an empty WAIT reply cannot clear this directional cooldown. Immutable chunks
and sequence numbers are preserved when a request moves to another resolver.

A resolver can learn up to 64 total in-flight exchanges, with at most 32 reads.
Global data credits scale from 240 to a bounded 512; reads remain capped at 160.
Credits are reserved in the shared fair queue before assigning a resolver, and
only the selected waiter is awakened when a credit is freed. Per-path pacing
and the loaded RTT controller remain active. A tested 1024-credit candidate
caused severe local latency/CPU regressions and is not shipped.

The default loaded RTT budget is 500 ms. This is feedback guidance, not a
worst-case guarantee on Internet, destination, VPN-stack or recovery latency.

## Disconnected: ICMP + E2E latency

Open **Resolvers** and select **Test latency**. Each candidate receives two
authenticated TXT probes through the configured MultiDNS server, then two ICMP
pings to the resolver IP. ICMP work is limited to four concurrent processes.
Tests can be canceled; connecting cancels any remaining probes.

The card shows ICMP and end-to-end DNS latency separately. A resolver can block
ICMP or Android can make ping unavailable while the DNS tunnel still works.
Only the authenticated E2E result determines tunnel reachability. No HTTP/TLS
speedtest or throughput claim is inferred from either latency measurement.
Previous test results are saved locally for viewing while disconnected.

## Connected: live resolver cards

During a connection, the test interface is replaced by slim resolver cards:

- IP and current estimated DNS transport RTT, preferring fresh upload ACK timing
  so the server's deliberate idle-read wait does not inflate the displayed RTT.
- Current unique stream count on that resolver's outstanding assignments.
  Several chunks or directions of the same stream count once. Striping can
  place one stream on multiple resolvers; counts need not sum to global streams.
- Green: reachable/active. Yellow: checking, standby, recent loss or degraded
  read/delay behavior. Red: confirmed unavailable.

One lost exchange briefly pauses new assignments for 25..100 ms and redistributes
waiting work. It does not toggle reachability. Three consecutive failures with
no authenticated progress for max(150 ms,3*RTT) open the circuit; a successful
authenticated exchange restores it. Already-sent requests rely on ARQ timeouts
or observed errors; silent failure cannot be detected instantly.

Busy paths use recent authenticated traffic for health instead of repeatedly
opening extra probe sockets. Idle, spare and failed paths retain background
recovery checks. Speculative future reads expire after 350 ms without turning
that expiry into a connection error; the idle head remains available.

## Debug capture

**Debug capture** is off by default. Ordinary Android operation serializes a
small summary for live cards and rates, stops activity polling when hidden, and
reduces background service snapshots to once per five seconds. It does not
construct, sort or copy the full resolver diagnostic report on every refresh.
The service reuses one snapshot worker rather than creating a thread per tick.

Enable capture before reproducing a problem, then select **Copy debug report**.
Reports contain no authentication key or destination payloads. Disable capture
after copying. Optional admission/pre-send/pacing timing is collected while
capture is enabled. Essential RTT, loss and capacity feedback, byte totals and
cheap counters remain internal because transport control still needs them.
Counters normally cover the session; optional timing can cover only enabled
capture periods. The last captured report remains available after disconnect.

Debug fields include attempts/replies, TX/RX timeouts, ARQ retries and backups,
DNS/invalid/socket errors, socket connections/reconnections, confirmed outages
(`health_downs`), recoveries, capacity cuts and directional cooldowns.
`assigned_streams` is the resolver's unique outstanding stream count.
`queue_ms` covers shared admission; `slot_wait_ms` covers pre-send credit/pacing
wait after selection. RTT excludes both local waits. `data_budget`,
`data_in_flight`, `read_in_flight` and `active_uploads` expose global activity.

`session_seconds` permits session-average rates including idle time. Per-resolver
`goodput_bytes_per_second` and upload/download rate fields are per-exchange
service estimates, not aggregate measured link throughput. Recent loss covers
128 observed data outcomes and excludes canceled races. Timeouts count attempts,
not entire VPN disconnects. Socket reconnections are distinct from health recovery.

## Wire overhead and TLS

Application HTTPS/TLS encrypts its content before it enters the tunnel. MultiDNS
does not add another TLS layer. Plain HTTP and tunnel metadata have no additional
confidentiality. Optional XOR is obfuscation only and requires the updated server.
It adds **zero bytes**: an authenticated operation flag selects the payload mask.
The existing frame header is 30 bytes and the existing authentication tag is
8 bytes. DNS name uploads use Base32; TXT downloads use unpadded Base64, plus DNS
framing. XOR does not change those sizes or remove existing application TLS bytes.
Authentication remains enabled, including over the obfuscation flag/ciphertext.
A separate nonce-derived mask avoids exposing the authentication key through
known plaintext; repeating the mask still does not make XOR a secure cipher.
