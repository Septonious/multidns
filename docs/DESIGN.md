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
striping; affinity maps a stream to a group until retry. Resolver endpoint health
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
