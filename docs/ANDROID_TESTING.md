# Android prototype testing

Install the debug APK on Android 7.0/API 24 or newer, arm64 or x86_64.
The APK is debug-signed for testing; preserve its signing key to update an existing
installation. Use `MULTIDNS_DEBUG_KEYSTORE=/absolute/path/to/debug.keystore`
or `multidns.debugKeystore` in the ignored `android/local.properties` file to
select the retained debug signing key explicitly for direct Gradle builds too.
The debug store uses the standard Android debug credentials; never commit it.
A different Actions runner can generate a different debug key, in
which case uninstalling the previous APK is necessary before installation.

Version 0.2.0 replaces the screens with a native card layout, persistent bottom
navigation, one connection toggle, expandable advanced settings, and a visual
ordered routing-rule editor. Disconnect closes the VPN interface immediately and
cancels startup discovery/probes. Reconnect creates a new engine. A notification
Disconnect action uses the same shutdown path. Saved profiles and the signing
certificate are retained.

Live payload rates are shown instead of raw JSON. **Profile options → Copy diagnostics** retains access to the full counters
and per-resolver state without including the authentication key. Update the
server as well to enable bounded read long-polls and larger replies. Start with
window 8 for comparison, then try 16; see [performance tuning](PERFORMANCE.md).

Version 0.1.1 fixes a native-library loading defect when connecting. The HEV
library is built without its upstream JNI registration for
`hev.htproxy.TProxyService`; MultiDNS supplies its own JNI bridge. The build
script checks both packaged ABIs with `scripts/check-android-native.py` so that
an APK containing the incompatible loader fails validation. Java/linkage errors
during startup are reported in the connection status instead of escaping the
adapter thread. This does not catch native signals such as SIGSEGV.

When rebuilding an existing 0.1.0 checkout, use
`CLEAN_ANDROID=1 bash scripts/build-android.sh`. NDK incremental builds can reuse
the old shared library when a source file is removed from its link inputs.

1. Deploy the Linux server and delegate its tunnel domain using NS and A/AAAA.
2. Add a server profile with the domain and matching random authentication key.
3. Enter the candidate resolvers. Choose **Profile options → Test resolver latency**.
   This runs two authenticated probes per endpoint with up to 16 concurrent workers.
4. For manual grouping, open **Organize resolvers**, long press a resolver card, and drag it
   into a group or spare pool. Text input and automatic partitioning are also available.
5. For startup grouping, enable automatic groups and set members/group and maximum
   groups. The candidate pool includes both entered groups and additional spares.
6. Connect and accept Android's VPN permission. Open a site or transfer a file.
7. Configure split routing and app include/exclude selection, then reconnect.
8. Scan ranges while connected or disconnected. Filter TXT and NS->A results, copy
   IPs separated by newline, or export the full matching list to a file.

## Group policy

With size 2 and maximum 5 groups, the fastest ten healthy candidates form five
groups. If eleven candidates are healthy, one remains a spare. If only one is
healthy, one one-member group is formed. If none pass, connection fails explicitly.
The fastest candidates are spread across groups before backup members are assigned.
While connected, health is refreshed every ten seconds and automatic groups are
reformed. Group changes preserve stream IDs and sequence state.

Adaptive scheduling uses RTT plus outstanding work for exchanges below 16 KiB.
Bulk streams use smooth weighted selection from estimated useful payload rate.
Three actual failures exclude a resolver; a canceled race is not counted as loss.
For interactive traffic, members slower than both 200 ms and four times the best
member's RTT are omitted from that race. Bulk races retain healthy backup members.

## Routing precedence

App selection happens first. Excluded apps bypass the VPN and therefore all rules.
Included traffic follows the first matching destination rule, then the default action.
Rules can combine domain suffix/exact domain, IP CIDR, port, and TCP/UDP protocol.
DNS replies observed through SOCKS UDP port 53 populate a bounded TTL name map.
Shared destination IPs and encrypted external DNS limit domain attribution; use
IP rules when exact name attribution is unavailable. This is not TLS interception.

## Scanner persistence

Results are stored in SQLite instead of an unbounded in-memory list. The screen
shows the best 150 matches; copying/exporting uses the entire matching result set.
Clipboard output is limited to 500 KB to avoid Android binder size failures; export
to a file for larger lists. Ranges are normalized, merged, and lazily enumerated.
Pause/resume retains a contiguous completion watermark, so interrupted probes are
retried. Resume requires unchanged scan inputs and profile settings. IPv6 ranges
are exhaustive; very large prefixes are not silently sampled.

## Validation boundary

Go integration tests use local authoritative DNS endpoints, failed paths, and TCP/
UDP destinations. They do not establish performance through public recursive DNS.
A packaged APK and passing lint do not establish VPN behavior on a physical phone.
Device checks include screen-off operation, background scans, app selection,
network transitions, split routing, and battery use. These must be tested on target
devices with a real delegated domain and server.

## Isolated emulator lifecycle test

`SmokeInstrumentation` is packaged only in the test APK. It replaces the emulator's
saved profile with a public fixture and must run on a disposable emulator, not a
phone containing user profiles. Grant notifications and the ACTIVATE_VPN app-op
with adb, install the app and test APK, and launch:

```bash
adb shell am instrument -w -e target HOST_LAN_IP \
  org.multidns.app.test/org.multidns.app.SmokeInstrumentation
```

The fixture server must listen on host UDP/TCP 15353, serve `t.test`, and use key
`device-smoke-test-key-not-production`. A TCP echo endpoint must listen on host
port 18080. The profile reaches the DNS fixture through emulator address
`10.0.2.2`; `HOST_LAN_IP` is the echo destination as seen from the server.
The test checks a byte-verified 16 KiB transfer through the VPN, three actual UI
Connect/Disconnect cycles, disappearance of the VPN network, cancellation during
automatic discovery, and captures each redesigned screen. Screenshots are saved
under the target app's external files directory in `smoke-screenshots`.
