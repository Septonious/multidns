# Prototype contract

One integrated prototype: authenticated, unencrypted DNS transport; independent
reliable TCP streams; UDP associations; 100 resolvers; explicit racing groups;
striped or affinity scheduling; Android profiles, app selection, destination rules,
and incremental resolver scanning. No claim of compatibility with ITOM configuration.

Wire requests identify client session, stream, sequence, cumulative receive ACK,
operation, and transmission nonce. Uploads are idempotent by stream/sequence;
download polls request a particular immutable chunk. The server never dequeues a
different chunk for a duplicate poll. Stream IDs are never reused within a session.
Closed streams remain tombstoned for the session lifetime. Bounded queues exert
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
