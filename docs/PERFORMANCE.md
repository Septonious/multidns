# Performance and tuning

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

- `queries`: resolver exchange attempts, including duplicate group members.
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
