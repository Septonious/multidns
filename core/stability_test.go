package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestIdlePollingStaysBounded(t *testing.T) {
	p, _ := testProfile(t)
	p.Window = 32
	p.Adaptive = true
	p.TimeoutMS = 500
	client, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(8 * time.Second))
	payload := bytes.Repeat([]byte("idle-budget"), 4096)
	done := make(chan error, 1)
	go func() { _, err := conn.Write(payload); done <- err }()
	out := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatal("corrupt payload")
	}
	time.Sleep(400 * time.Millisecond)
	before := client.Stats().Queries
	time.Sleep(500 * time.Millisecond)
	queries := client.Stats().Queries - before
	if queries > 32 {
		t.Fatalf("idle stream kept a bulk polling window alive: %d DNS queries in 500ms", queries)
	}
	t.Logf("idle stream: %d DNS queries per 500ms, window 32 with racing pair", queries)
}

type signaledConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *signaledConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Write(b)
}

func TestBlockedDestinationStillAllowsClose(t *testing.T) {
	server, err := NewServer("t.test", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	destination, reader := net.Pipe()
	defer destination.Close()
	defer reader.Close()
	blocked := &signaledConn{Conn: destination, entered: make(chan struct{})}
	ready := make(chan struct{})
	close(ready)
	stream := &serverStream{conn: blocked, ready: ready, notify: make(chan struct{}), writes: map[uint32][]byte{}, chunks: map[uint32][]byte{}}
	server.streams[streamKey{1, 1}] = stream
	go stream.flushWrites()
	written := make(chan []byte, 1)
	go func() { written <- server.handle(frame{Op: opWrite, Session: 1, Stream: 1, Data: []byte("blocked")}) }()
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("destination write never began")
	}
	closed := make(chan []byte, 1)
	go func() { closed <- server.handle(frame{Op: opClose, Session: 1, Stream: 1}) }()
	select {
	case result := <-closed:
		if result[0] != statusOK {
			t.Fatal("close failed")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("close blocked behind destination write")
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("writer did not release after close")
	}
}

// Real payload is verified while authenticated control traffic shares a lossy,
// delayed DNS path. Each selected upload sequence loses both initial raced
// requests; its retry must recover without duplication or ordering corruption.
func TestLossRecoveryAndLoadedRTT(t *testing.T) {
	p, server := testProfile(t)
	p.Groups = nil
	p.Window = 8
	p.Adaptive = true
	p.TimeoutMS = 700
	var attempts sync.Map
	for i := 0; i < 5; i++ {
		g := Group{Name: fmt.Sprint(i)}
		for j := 0; j < 2; j++ {
			g.Resolvers = append(g.Resolvers, testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
				f, err := extract(m, p.Domain, p.Key)
				if err == nil && f.Op == opWrite && f.Seq%19 == 11 {
					key := uint64(f.Stream)<<32 | uint64(f.Seq)
					value, _ := attempts.LoadOrStore(key, &atomic.Int32{})
					if value.(*atomic.Int32).Add(1) <= 2 {
						return
					}
				}
				time.Sleep(40 * time.Millisecond)
				server.ServeDNS(w, m)
			})))
		}
		p.Groups = append(p.Groups, g)
	}
	client, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	payload := bytes.Repeat([]byte("verified-lossy-stream"), 4096)
	done := make(chan error, 1)
	go func() { _, err := conn.Write(payload); done <- err }()
	probed := make(chan []time.Duration, 1)
	probeCtx, stopProbes := context.WithCancel(ctx)
	defer stopProbes()
	go func() {
		var timings []time.Duration
		for probeCtx.Err() == nil {
			start := time.Now()
			pctx, c := context.WithTimeout(probeCtx, time.Second)
			err := client.Probe(pctx)
			c()
			if err == nil {
				timings = append(timings, time.Since(start))
			}
			select {
			case <-time.After(40 * time.Millisecond):
			case <-probeCtx.Done():
			}
		}
		probed <- timings
	}()
	start := time.Now()
	out := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Log("loss recovery diagnostics:", client.StatsJSON())
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Log("diagnostics:", client.StatsJSON())
	stopProbes()
	timings := <-probed
	if !bytes.Equal(out, payload) {
		t.Fatal("loss recovery corrupted stream")
	}
	if client.Stats().Retries == 0 {
		t.Fatal("loss injection did not exercise retries")
	}
	if len(timings) < 3 {
		t.Fatal("control traffic made insufficient progress under load")
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	p95 := timings[(len(timings)-1)*95/100]
	if p95 > 350*time.Millisecond {
		t.Fatalf("loaded control RTT too high: %s", p95)
	}
	t.Logf("%d bytes echoed in %.3fs; %d retries; loaded probe p50=%s p95=%s (%d samples)", len(payload), elapsed.Seconds(), client.Stats().Retries, timings[len(timings)/2].Round(time.Millisecond), p95.Round(time.Millisecond), len(timings))
}

func TestQueuedACKRequiresFlushAndKeepsCreditBounded(t *testing.T) {
	server, err := NewServer("t.test", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	destination, reader := net.Pipe()
	defer destination.Close()
	defer reader.Close()
	blocked := &signaledConn{Conn: destination, entered: make(chan struct{})}
	ready := make(chan struct{})
	close(ready)
	stream := &serverStream{conn: blocked, ready: ready, notify: make(chan struct{}), writes: map[uint32][]byte{}, chunks: map[uint32][]byte{}, queuedACK: true}
	server.streams[streamKey{1, 1}] = stream
	go stream.flushWrites()
	defer server.handle(frame{Op: opClose, Session: 1, Stream: 1})
	for seq := uint32(0); seq < 64; seq++ {
		if server.handle(frame{Op: opWrite, Session: 1, Stream: 1, Seq: seq, Data: []byte{byte(seq)}})[0] != statusOK {
			t.Fatal("queued write rejected")
		}
	}
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("writer didn't begin")
	}
	if server.handle(frame{Op: opWrite, Session: 1, Stream: 1, Seq: 64, Data: []byte("over limit")})[0] != statusWait {
		t.Fatal("server exceeded 64-chunk credit")
	}
	if server.handle(frame{Op: opFlush, Session: 1, Stream: 1, Seq: 64})[0] != statusWait {
		t.Fatal("flush acknowledged unsent data")
	}
	done := make(chan []byte, 1)
	go func() { data := make([]byte, 64); io.ReadFull(reader, data); done <- data }()
	deadline := time.Now().Add(time.Second)
	for server.handle(frame{Op: opFlush, Session: 1, Stream: 1, Seq: 64})[0] != statusOK {
		if time.Now().After(deadline) {
			t.Fatal("flush didn't complete after delivery")
		}
	}
	data := <-done
	for i, v := range data {
		if v != byte(i) {
			t.Fatal("queue corrupted order")
		}
	}
}

func TestTCPFallbackReusesConnectionAndSurvivesReset(t *testing.T) {
	p, server := testProfile(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	var mu sync.Mutex
	peers := map[string]bool{}
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
			m.SetEdns0(900, false)
		} else {
			mu.Lock()
			peers[w.RemoteAddr().String()] = true
			mu.Unlock()
		}
		server.ServeDNS(w, m)
	})
	tcp := &dns.Server{Listener: listener, Handler: handler}
	udp := &dns.Server{PacketConn: packet, Handler: handler}
	go tcp.ActivateAndServe()
	go udp.ActivateAndServe()
	defer tcp.Shutdown()
	defer udp.Shutdown()
	p.Groups = []Group{{Resolvers: []string{listener.Addr().String()}}}
	p.TimeoutMS = 500
	client, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ready := make(chan struct{})
	close(ready)
	payload := bytes.Repeat([]byte("x"), 768)
	chunks := map[uint32][]byte{}
	for seq := uint32(0); seq < 24; seq++ {
		chunks[seq] = payload
	}
	server.mu.Lock()
	server.streams[streamKey{client.session, 1}] = &serverStream{ready: ready, notify: make(chan struct{}), chunks: chunks, nextRead: 24}
	server.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for seq := uint32(0); seq < 24; seq++ {
		out, err := client.rpc(ctx, frame{Op: opRead, Stream: 1, Seq: seq}, 0)
		if err != nil || !bytes.Equal(out, payload) {
			t.Fatalf("fallback %d: %v", seq, err)
		}
		if seq == 11 {
			client.ResetSockets()
		}
	}
	mu.Lock()
	count := len(peers)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("24 truncated replies used %d TCP connections; expected one before and one after reset", count)
	}
	t.Log("24 full-size replies verified over two reused TCP connections, including network reset")
}

type gatedProtector struct {
	entered, release chan struct{}
	once             sync.Once
}

func (p *gatedProtector) Protect(int) bool {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return true
}
func TestNetworkResetDoesNotBlockBehindSocketProtection(t *testing.T) {
	profile, _ := testProfile(t)
	gate := &gatedProtector{entered: make(chan struct{}), release: make(chan struct{})}
	client, err := NewClient(profile, gate)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		_, err := client.socket(context.Background(), profile.Groups[0].Resolvers[0], "udp")
		done <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("socket didn't enter protection")
	}
	reset := make(chan struct{})
	go func() { client.ResetSockets(); close(reset) }()
	select {
	case <-reset:
	case <-time.After(200 * time.Millisecond):
		close(gate.release)
		t.Fatal("network reset blocked behind protection callback")
	}
	close(gate.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old-network socket was published after reset")
		}
	case <-time.After(time.Second):
		t.Fatal("dial failed to return")
	}
}

func TestBlockedTCPDNSWriteHonorsCancellation(t *testing.T) {
	profile, _ := testProfile(t)
	client, err := NewClient(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, peer := net.Pipe()
	defer peer.Close()
	socket := &resolverSocket{conn: conn, network: "tcp", pending: map[uint16]*pendingDNS{}, done: make(chan struct{}), write: make(chan struct{}, 1)}
	client.sockets["tcp|fixture"] = socket
	message, err := query(frame{Op: opProbe}, profile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = client.exchangeDNS(ctx, message, "fixture", "tcp")
	if err == nil || time.Since(start) > 300*time.Millisecond {
		t.Fatalf("blocked DNS write ignored deadline: %v (%s)", err, time.Since(start))
	}
}
