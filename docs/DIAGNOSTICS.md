# Diagnostics and resolver settings (0.3.3)

## Upload changes

Transport settings add **Loaded RTT budget**, default 500 ms. This is the
controller's approximate two-way target; path failure, remote destinations and
VPN buffering can exceed it. The existing added-delay target remains respected.
Automatic TX can grow to 64, RX to 32; manual limits and per-flow sharing still
bound actual exchanges. Tuning now tests TX 48/64 too and marks a confirmed
latency-only repeat in its detailed report. Packet loss does not trigger retrying
a higher concurrency recommendation.

New JSON/debug fields:

- upload_payload_bytes: maximum payload fitting a DNS upload question, respecting
  domain length and explicit upload_mtu. No frame/authentication overhead changed.
- loaded_rtt_limit_ms: configured target, not measured application RTT.
- recent_loss_percent: failures among the last 128 observed data exchanges;
  canceled attempts are excluded. It is not a raw packet-capture loss percentage.
- paced_queries / pacing_wait_ms: paced uploads and smoothed local pacing delay.
  This delay holds shared credits but occurs before starting the DNS network timer.
- capacity_budget_cuts: strong cuts when write ACK timing exceeds the loaded
  budget, independent of inflated variance. Existing loss/delay counters remain.

## General settings and counters


Update the server before installing the matching APK. Existing saved profiles and
authentication keys remain valid. Open **Diagnostics** in the bottom navigation.

## Find optimal parameters

1. Disconnect the VPN and stop scanner jobs. Keep the phone on the connection you
   intend to use for the tunnel; avoid other transfers during the measurement.
2. Select **Find optimal parameters**. Each candidate is tested independently.
   Progress names the resolver and current phase. You can cancel at any point.
3. Review recommendations and **View parameter test details**. Evidence includes
   DNS MTU, upload payload bytes, worker count, accepted/sent replies, p95 RTT and
   achieved payload rate for every trial. **Copy parameter test report** exports
   the JSON evidence without the server key.
4. **Apply found parameters to resolvers** appears after successful tests. It
   merges successful results into the same unchanged profile and enables adaptive
   scheduling. Failed candidates keep their previous settings. Cancellation
   disables Apply. Reconnect to activate the saved parameters.

The test checks authenticated TXT replies at DNS message budgets 512, 768, 900,
1024 and 1232 bytes, then searches for a reliable full-length upload QNAME payload
(with smaller payload fallbacks). Separate TX/RX concurrency sweeps use 1, 2, 4,
8, 12, 16, 24 and 32 simultaneous exchanges, plus 48/64 for TX. A sweep stops on loss or excess p95
latency; it requires a 10% rate improvement to recommend more workers. Short
trials estimate usable limits at that moment, not a permanent optimum. Re-test
on another mobile network or after a significant change in resolver behavior.
Tests can take several minutes for a large/unreachable pool. Results remain in
memory until the activity closes; save parameters or copy the report before
leaving the app. No profile changes occur during measurement.

## Manual settings

The Diagnostics settings card lists active and spare resolvers. Select an address
for its DNS MTU, upload payload bytes, TX workers and RX workers. Zero uses the
automatic default. Worker limits apply in adaptive mode and limit concurrent DNS
exchanges shared by all streams, rather than creating OS threads. They are upper
bounds; learned congestion capacity may be lower. Combined learned capacity does
not exceed the larger recommended TX/RX limit when both are configured.

MTU is the UDP DNS message budget, including headers, question, TXT framing,
transport framing and authentication. It is not the VPN interface MTU. The QNAME
limit independently bounds upload payload capacity. To preserve immutable ARQ
chunks across paths, a client uses the smallest configured pool payload limits
for its stream chunk sizes, and advertises each resolver's own DNS MTU. Thus one
small-MTU candidate can reduce pool chunk size; remove an unsuitable spare or
reset its settings when comparing performance. This conservative choice allows a
chunk to move to another resolver without changing its sequence or contents.

**Connection and transport settings** opens the profile editor for the base
window, adaptive scheduling, automatic group size/count, maximum DNS timeout,
added network-delay target and optional XOR obfuscation. The delay target is
congestion-controller guidance, not a guarantee on application ping. The default
is 30 ms above the recent minimum observed upload-ACK RTT, with a jitter allowance. **Reset resolver
limits to automatic** removes saved per-path overrides. Reconnect after changes.

For independent paths, put one resolver per group; if automatic groups are
selected, also set resolvers per group to 1. Group preferences never pin queued
adaptive work to an unavailable or saturated resolver.

## Counters

Live counters reset when a connection is started. The debug view provides:

- Global resolver attempts/replies, logical ARQ retries, backup hedge attempts,
  timeouts, DNS errors, invalid authenticated replies, socket errors, server WAIT
  replies, physical network changes, stream count and shared admission queue.
- Per resolver: attempts, replies, ARQ retransmissions, backups, timeouts, DNS
  errors, invalid replies, socket errors and the most recent error description.
- Socket connections/reconnections, path health recoveries and DNS TCP fallbacks.
  A silent UDP failure need not recreate a socket: recovery and reconnection are
  separate events. Reconnection counts new sockets after an earlier socket for
  the same resolver/transport, including rebinds after a network change.
- DNS RTT, minimum RTT, RTT variation, average/peak local admission delay, learned
  capacity, leased wire capacity, active operations and saved limits.
- Accepted upload/download bytes. Retransmitted payload can be counted again;
  use the dashboard's application byte counters for end-to-end transfer totals.

0.3.2 shares directional window budgets across active bulk flows and grants the
next available wire credit to the eligible flow with the fewest credits already
leased. At equal credit counts, older bulk work wins over fresh short work after
50 ms. This is an admission policy, not a guarantee of total queue delay.
Controls use healthy fast paths across groups; group membership still defines
bulk backup candidates. Data/control backups have separate global counters.
Successful read long-polls never trigger delay cuts; sustained smoothed upload
ACK delay with jitter allowance still does. Shared backlog can recover startup
capacity after a loss episode.

`tcp_fallback_failures` counts unsuccessful TCP download fallbacks.
`read_cooldown_ms` shows remaining time before reads can use that path again.
Failed large replies back off for 2..30 seconds; tiny probes and successful
uploads do not revive the download path. Upload eligibility is preserved.
A reachable authenticated resolver does not prove successful large downloads.

A request in the shared admission queue has no resolver ownership. Per-resolver
`queued` remains available for legacy path admission; `admission_queued` is the
meaningful backlog for adaptive primary work. Queue delay is assigned to the
resolver eventually chosen. RTT excludes that delay. Service-rate JSON fields
are per-exchange estimates, not aggregate measured link throughput.

The first observed transport failure quarantines a path. Waiting work immediately
uses other healthy paths. Already-sent requests detect failure through a response,
socket error or adaptive retransmission timeout; silent failure cannot be detected
before observation. ARQ preserves chunk identity and retries elsewhere. A single
canary can retry after 300 ms; protected authenticated health probes run every two
seconds and allow recovered paths to rejoin. Delayed in-flight replies retain
wire credits until response, expiry or reset. A recovered resolver resumes unique
work without rebuilding destination connections.

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
