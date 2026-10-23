# 0.3.8 upload demand and ordered repair

The learned receive ceiling is separate from the current admission limit. Active
uploads retain three quarters of the shared wire budget for write/foreground
requests. Reads already on the wire drain with their original physical leases;
no counters are artificially returned. Once upload demand stops, receive-only
transfers can use the full learned ceiling again. The total 512-credit budget,
resolver ceilings and foreground reserve are unchanged.

One pool-level 20 ms timer handles a fully blocked queue's cooldown expiry.
Eligible waiters are signaled by admission/release/control events. There is no
per-chunk periodic polling timer. Cancellation removes its waiter and selects
another; a timer callback never schedules more work after client cancellation.

Upload writers expose the current unacknowledged prefix through a local atomic
sequence hint. A delayed backup for that prefix alone may use existing foreground
reserve ahead of queued bulk. Other backups preserve the unique-work-first rule;
queued foreground requests retain priority. The hint adds no wire bytes and
changes neither server ACK semantics nor negotiated reorder windows.

Short data retry deadlines are not reachability deadlines. Data failures still
back off/reroute immediately, while unavailable status requires at least one
second without authenticated progress (or three measured RTTs if longer).

---

# Version 0.3.7 transport additions

An additive `opOpen.Seq` capability bit 4 negotiates a 128-chunk downstream
cache/prefetch window. Older peers retain 64. Header sizes, authentication, XOR,
chunk payload limit and key/profile format are unchanged. The new server admits
at most 512 handlers / 384 read handlers; the adaptive client retains 512 total
physical credits and grows its RX budget from 160 toward 320 under healthy
sustained demand. Legacy servers keep the old RX ceiling. Uploaded bytes still
fit in a DNS QNAME; no promise of linear scaling with resolver count is implied.

The server reads TCP data in bounded batches, copies immutable DNS chunks,
tracks a monotone ACK deletion watermark, and signals per-sequence read waiters.
ACKs release separate producer credit, leaving unrelated missing-data readers
asleep. Close/EOF/error wakes outstanding waiters. Registration refcounts remove
expired waiters, and buffering remains bounded by the negotiated cache window.

Circuit recovery reduces a failed path's window and clears stale pacing/loss
history, with a two-second growth grace and exponential retry. Stale completion
and probe episodes cannot overwrite newer recovery/progress. Physical abandoned
request losses still feed direction-specific/global congestion control without
asserting an outage. TCP frame completion has its own bounded deadline; a caller
can cancel without truncating a shared stream. Interactive hedges can use
reserved credits ahead of queued bulk, while queued foreground work has priority.

The main-page reachable count is separate from quality colors. Group layout
continues to express active sets/preferences and standby capacity; adaptive
admission remains shared across active groups.

---

# Prototype contract

One integrated prototype: authenticated, unencrypted DNS transport; independent
reliable TCP streams; UDP associations; 100 resolvers; explicit racing groups;
striped or affinity scheduling; Android profiles, app selection, destination rules,
and incremental resolver scanning. No claim of compatibility with ITOM configuration.

Wire requests identify client session, stream, sequence, cumulative receive ACK,
operation, and transmission nonce. Uploads are idempotent by stream/sequence;
download polls request a particular immutable chunk. The server never dequeues a
different chunk for a duplicate poll. Stream IDs are never reused within a session.
Closed streams remain tombstoned until five-minute idle cleanup, longer than
the two-minute accepted request timestamp window. Bounded queues exert
backpressure. DNS transaction IDs are independent of tunnel identifiers.

DNS uses lowercase Base32 query labels and binary payload in Base64 TXT strings.
TXT is the data carrier in v1. Scanner NS->A means NS hostname lookup then A lookup;
it is not an NS data carrier. Additional carriers require a later protocol extension.
Base64 TXT costs space but avoids recursive parser differences with arbitrary bytes.

Each group races matching authenticated requests. The first valid response wins;
late responses cannot consume data twice. A retry rotates groups. The default is
striping; fixed affinity maps a stream to a group until retry. Adaptive scheduling
uses group preferences while admitting work to available healthy paths. Resolver endpoint health
is measured independently of the race; cancellation is not a timeout.

Source references examined: SlipGate bf2df191a5da31cd447886c83568f676ebfee342;
MasterDnsVPN acbf1c61f90786f41b975d2e2f616afbce292b29;
SlipNet 552ab5078d90ae84294fcd6ef649b8e023ba2a63.
The transport below is independently implemented; no upstream speed claims apply.

Version 0.1.2 keeps wire framing v1. Rolling windows refill as the contiguous
prefix completes, without waiting for the entire batch. Upload capacity is
calculated from the encoded QNAME budget. Server downloads use up to 768 payload
bytes, reduced for longer domains to fit a 1232-byte EDNS reply. A missing chunk
can wait up to 80 ms on a broadcast notification before returning WAIT. UDP
answers exceeding the advertised limit are truncated for TCP retry; the chunk
itself remains unchanged. Download polls have a 96-request sublimit within the
128-request client budget, leaving capacity for uploads and control traffic.

## Version 0.2.0

The authenticated frame layout remains v1. `opOpen.Seq` bit 0 requests selective
queued upload ACKs. A supporting server returns one capability byte (`1`) after
the OK status. Old servers ignore that sequence field and return an empty OK
payload, so the client falls back without sending new operations. Old clients
send sequence 0 and receive legacy committed-write acknowledgements.

For negotiated streams, a write ACK accepts data into the bounded 64-chunk
reorder queue. Operation 8 (`opFlush`) with `Seq=next upload sequence` acknowledges
only after the full preceding prefix has been written to the destination socket.
The client sends this barrier before `Conn.Write` returns. This permits selective
recovery around lost packets without losing the last queued bytes on close.
Half-close continues to wait for all preceding writes. A single writer per stream
performs destination I/O outside the stream mutex, keeping reads/close responsive
when a destination stalls. Tiny destination TCP reads coalesce for at most 2 ms;
UDP datagram framing is unchanged.

Adaptive streams begin at the saved base window and widen after 4 KiB of useful
traffic, bounded by `base * active group count` and 64. Disabling adaptive mode
retains the configured fixed window. Shared budgets remain 128 DNS exchanges,
96 read exchanges, and 120 combined data exchanges, reserving eight positions
for control. Only an idle stream's head read keeps polling; future WAIT positions
are retried after ordered progress. Healthy paths use an RTT-based timeout with
jitter/long-poll allowance, a 300 ms floor and the configured ceiling.

UDP and TCP fallback both multiplex transactions over reusable per-resolver
sockets. Writes have cancellation/deadline bounds; network changes invalidate
in-flight socket creation and close old sockets. No socket-map lock covers a
dial or Android protection callback. Server UDP buffers request 1 MiB; TCP reuse
is permitted with a 30-second idle timeout.

Android lifecycle ownership stays on the service main thread. Disconnect removes
callbacks and closes the TUN before asynchronous engine shutdown/thread joining.
Startup checks cannot publish a VPN after cancellation. The Go engine's startup
mutex covers only state publication, and stopping an engine is terminal; a new
engine is created when reconnecting.


## 0.3.0 optional extensions and scheduling

Header and authentication sizes remain 30 + 8 bytes. The high bit of the operation
byte requests XOR payload obfuscation; authenticate the complete header/ciphertext
before removing the flag and unmasking. Plain v1 remains the default. This option
requires a supporting server; it is not a confidentiality mechanism.

`opOpen.Seq >> 8` requests a smaller immutable download chunk size, clamped to
32..server capacity. Low bits retain queued ACK and extended upload-window
negotiation. A legacy server ignores the extra bits and falls back to its usual
chunk size/TCP truncation recovery. Op 9 is a stateless authenticated parameter
probe: Seq requests 16..768 response bytes, which are deterministically generated
from nonce/request data using SHA-256 blocks; it opens no destination stream.
An older server rejects it, so discovery produces no applicable parameters.

Adaptive primary admission uses one shared FIFO queue per operation/priority,
selecting a free healthy path at admission time. Short data/head reads have
priority, bulk leaves an interactive credit, and waiting writes reserve half of
read concurrency. A path's configured TX/RX ceilings and learned combined limit
all apply. Data RTT inflation above a recent minimum plus the delay target cuts
capacity before loss; timeouts retain the earlier burst-safe multiplicative cut.
Primary selection can move outside the preferred group. One-member groups send
one unique attempt; paired groups can use a delayed spare-capacity hedge whose
timer follows the actually selected primary. A busy backup is skipped rather
than creating another pinned queue. Packet loss triggers selective ARQ with a
fresh nonce; sequence/byte identity and the final upstream flush barrier remain.

In 0.3.0, each selectively accepted upload chunk immediately frees its worker,
while the next-minus-contiguous-prefix position must still fit the separately
negotiated reorder bound. Slow or lost chunks retain their ARQ identity without
idling all fast-path workers. Write returns only after the flush barrier commits
its full prefix to the destination socket.


## 0.3.1 admission and capacity repair

0.3.0's strict short-request preference could indefinitely postpone bulk, while
its delay controller misclassified successful read long-poll wait as congestion.
The shared selector now chooses eligible work in arrival order once its wait is
50 ms; young bulk can be overtaken by short requests before then. Availability is
checked before ordering, so an RX credit bound does not prevent TX progress.
Queued primaries also precede speculative hedges. Combined path/RX/TX ceilings
and control reservations remain; no hard extra bulk-credit reservation applies.

Only successful write ACKs feed the delay controller. It uses smoothed RTT and
jitter, requires sustained inflation and retains cut timing guards. Idle success
can restore capacity to startup from shared backlog, rather than looking only at
the obsolete per-path primary queue. Loss cuts, delay cuts and idle recoveries
are reported independently. Wire protocol and server behavior are unchanged.
