# Resolvers and diagnostics (0.3.8)

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

UDP paths can learn up to 64 total in-flight exchanges and 32 reads. TX and RX
have independent loss/delay feedback. A timed-out download briefly pauses new
reads on that path; reliable uploads remain eligible. Full download replies
feed a separate smoothed-delay guard with room for the server's 80 ms wait.
Bulk download requests are paced as well as uploads. Empty WAITs are not a
congestion sample; an old collapsed startup floor can recover cautiously when
real queued work is starved.

Global data credits remain bounded at 512, with at most 160 reads. Eight global
credits and one position on larger UDP windows remain available to interactive
work. Real wire leases survive cancellation until completion or timeout. A
waiting unique chunk has no resolver owner, so removing a failed path spreads
waiting work over the remaining pool. A single opportunistic delayed backup can
use a different live resolver regardless of group size, without overtaking
queued unique work. Groups do not multiply physical resolver capacity.

SOCKS uploads retain a continuous selective-ACK pipeline across local TCP reads.
The source queue is bounded to three 32 KiB blocks plus the negotiated reorder
window. No per-buffer flush is added; half-close remains the final server-commit
barrier. The public Conn.Write API retains its existing flush guarantee.

### UDP/53 preference and TCP/53 fallback

Startup E2E probes try UDP first, then TCP if UDP is unavailable. The chosen
transport is learned only from an authenticated tunnel reply. Occasional lost
UDP data packets with other recent UDP progress use ARQ across the pool and do
not force TCP. Truncated UDP answers still use TCP without changing the preferred
transport for small requests.

TCP uses protected reusable connections, with separate control, download and
four bounded upload connections. Each connection admits one physical request
at a time to support sequential DNS TCP frontends; one download long-poll and
at most four upload requests can be assigned per TCP resolver. TCP preference
gets a UDP canary about every ten seconds, even while active. Authenticated UDP
recovery resets the timing clock. Old TCP replies cannot undo a newer UDP recovery.
The scanner also discovers TCP-only authenticated resolvers and reports their
actual exchange RTT, excluding the initial UDP failure delay.

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
- Current transport (UDP or TCP) and unique stream count on outstanding assignments.
  Several chunks or directions of the same stream count once. Striping can
  place one stream on multiple resolvers; counts need not sum to global streams.
- Green: reachable/active. Yellow: checking, standby, **Recent loss**,
  **Download limited** (a temporary RX cooldown), or **High latency**. Red:
  confirmed unavailable. A recent error is not a confirmed outage.

There is no ten-stream limit. A 44 ms path can legitimately carry twelve streams
while a recent lost exchange makes its card yellow. These are unique stream IDs
with outstanding physical requests, not twelve pinned TCP connections. New work
is governed by learned request capacity, directional limits, physical requests
in flight and cooldowns. A failed or capacity-limited path cannot collect queued
work beyond admission limits; already-sent work drains or retries elsewhere.

One lost exchange briefly pauses the affected direction for 25..100 ms and
redistributes waiting work. It does not toggle reachability. Three consecutive failures with
no authenticated progress for max(150 ms,3*RTT), spanning at least another
150 ms failure episode, open the circuit; a successful authenticated exchange
restores it. A batch of simultaneous expirations does not count as a sustained
outage. Idle health probes require two failed maintenance cycles to mark a
previously reachable resolver down. Cancellation cannot publish a false failed
probe result. Already-sent requests rely on ARQ timeouts
or observed errors; silent failure cannot be detected instantly.

Busy paths use recent authenticated traffic for health instead of repeatedly
opening extra probe sockets. Idle, spare and failed paths retain background
recovery checks. Speculative future reads expire after 350 ms without turning
that expiry into a connection error; the idle head remains available.

The dashboard counts an explicit `reachable` field, independently of a card's
color or label. High latency, recent loss and a temporary download cooldown do
not make a responding resolver unresponsive. Checking/confirmed outages remain
excluded. Grouping descriptions refer to the active pool and standby failover;
groups do not create additional independent bandwidth.

Confirmed outage retries back off from 500 ms to at most 8 seconds. Authenticated
recovery retains cumulative diagnostics, clears old congestion/pacing history,
starts with at most four physical requests and holds growth for two seconds.
Replies from before the outage cannot restore capacity; late failures from
before recovery cannot restart the failure episode. Failed probes predating
newer authenticated progress cannot erase it.

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
(`health_downs`), recoveries, capacity cuts and directional cooldowns. `transport`, `transport_switches`,
`udp_timeouts`, `udp_socket_errors`, `tx_capacity`, `rx_capacity`,
`rx_capacity_loss_cuts` and `rx_capacity_delay_cuts` expose the independent
controllers and transport fallback.
`assigned_streams` is the resolver's unique outstanding stream count.
`queue_ms` covers shared admission; `slot_wait_ms` covers pre-send credit/pacing
wait after selection. RTT excludes both local waits. `data_budget`,
`data_in_flight`, `read_in_flight` and `active_uploads` expose global activity.

`session_seconds` permits session-average rates including idle time. Per-resolver
`goodput_bytes_per_second` and upload/download rate fields are per-exchange
service estimates, not aggregate measured link throughput. Recent upload loss covers
128 observed TX outcomes and excludes canceled races. Timeouts count attempts,
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

### New 0.3.7 fields

- `read_budget`: current learned global RX request budget (initial 160, up to
  320 with the new server); `peer_read_window`: negotiated per-connection cache
  window (64 legacy, 128 new). Shared data requests remain capped at 512.
- `late_replies`, global and per resolver: authenticated replies after a data
  caller stopped waiting. They update delay and reachability after request/nonce
  validation, but do not add delivered application bytes or grow the global RX
  budget. Pre-outage replies cannot reopen the circuit.
- `abandoned_timeouts`, both global and per resolver: actual physical timeouts
  after a data caller stopped waiting, excluding stale outage epochs/shutdown.
  These feed congestion control but do not increment consecutive reachability
  failures or health-down transitions. They are distinct from normal `timeouts`.
- `recovery_ms` and `retry_ms`: remaining recovery growth grace and assignment
  cooldown. These appear only in the optional detailed resolver report.

The RX budget grows by 16 only with substantial occupancy, payload progress and
low recent physical loss; persistent delay or substantial aggregate loss withdraws credits. The global
loss cut requires a full 256-outcome sample with at least 8% loss; ordinary
per-path loss remains handled by individual controllers and ARQ.
Smaller path limits remain authoritative. A configured short timeout remains
a hard ceiling, including in TCP mode. Normal snapshots remain slim and debug
capture stays disabled by default.

### 0.3.8 upload demand and reachability

`read_budget` is the learned receive ceiling. `read_admission_budget` is the
current receive eligibility limit: during active uploads it is at most one
quarter of the shared data budget (128 of 512 on a large pool). Existing
physical reads retain their credits until completion; the limiter cannot revoke
them. After uploads finish, the learned receive budget becomes available again.
A small response/read floor remains, with existing per-path and fairness limits.

Only the oldest unacknowledged upload chunk may use foreground-reserved room
for its delayed backup while bulk is queued. This applies to SOCKS streaming
and direct Conn.Write. It does not raise total/per-path credits or remove pacing
from the primary. Foreground queued work retains priority.

Fast ARQ timeouts keep rerouting work immediately, but three expirations can only
open the displayed unavailable circuit after at least one second without
authenticated progress (longer for a high known RTT). Congestion still reduces
capacity and can show a yellow warning sooner. High latency itself remains a
quality state, separate from reachability. Detailed capture stays off by default.
