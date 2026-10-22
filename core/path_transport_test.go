package core

import (
	"bytes"
	"context"
	"github.com/miekg/dns"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func dualDNS(t *testing.T, handler dns.Handler) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", l.Addr().String())
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	tcp, udp := &dns.Server{Listener: l, Handler: handler, MaxTCPQueries: -1}, &dns.Server{PacketConn: pc, Handler: handler}
	go tcp.ActivateAndServe()
	go udp.ActivateAndServe()
	t.Cleanup(func() { tcp.Shutdown(); udp.Shutdown() })
	return l.Addr().String()
}

func TestTCPOnlyResolverStartupTransferAndUDPRecovery(t *testing.T) {
	p, server := testProfile(t)
	var allowUDP atomic.Bool
	addr := dualDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		if _, udp := w.RemoteAddr().(*net.UDPAddr); udp && !allowUDP.Load() {
			return
		}
		time.Sleep(15 * time.Millisecond) // sequential DNS TCP frontend with upstream delay
		server.ServeDNS(w, m)
	}))
	p.Adaptive = true
	p.Automatic = true
	p.Pool = []string{addr}
	p.Groups = []Group{{Resolvers: []string{addr}}}
	p.GroupCount = 1
	p.GroupSize = 1
	// The fixture intentionally adds 15ms service plus up to 80ms server
	// long-poll wait. Leave race-detector scheduling margin while retaining
	// the zero-timeout and bounded-connection assertions below.
	p.TimeoutMS = 400
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, err := c.DialContext(ctx, "tcp", echoTCP(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(7 * time.Second))
	data := bytes.Repeat([]byte("TCP-only DNS tunnel"), 1024)
	sent := make(chan error, 1)
	go func() { _, e := conn.Write(data); sent <- e }()
	out := make([]byte, len(data))
	if _, err = io.ReadFull(conn, out); err != nil {
		t.Fatal(err)
	}
	if err = <-sent; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, out) {
		t.Fatal("corrupt TCP tunnel")
	}
	h := c.healthSnapshot()[0]
	if h.Transport != "tcp" || h.TransportSwitches != 1 || h.Connections > 7 || h.Timeouts != 0 {
		t.Fatalf("TCP not learned/reused: %+v", h)
	}
	allowUDP.Store(true)
	c.adaptiveMu.Lock()
	c.pathLocked(addr).udpProbeAt = time.Time{}
	c.adaptiveMu.Unlock()
	c.testLatency(ctx, nil, true)
	h = c.healthSnapshot()[0]
	if h.Transport != "udp" || h.TransportSwitches != 2 || !h.Healthy {
		t.Fatalf("UDP recovery failed: %+v", h)
	}
}

func TestInvalidTCPReplyCannotSelectTCPTransport(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 100
	addr := dualDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		if _, udp := w.RemoteAddr().(*net.UDPAddr); udp {
			return
		}
		r := new(dns.Msg)
		r.SetReply(m)
		w.WriteMsg(r)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.TestLatency(context.Background(), nil)
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if c.pathLocked(addr).preferTCP || c.health[addr].Healthy {
		t.Fatal("unauthenticated TCP answer promoted transport")
	}
}

func TestOnePostLoadProbeFailureDoesNotRemoveReachablePath(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 100
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	c.adaptiveMu.Lock()
	c.pathLocked(addr).lastSuccess = time.Now().Add(-10 * time.Second)
	c.adaptiveMu.Unlock()
	c.testLatency(context.Background(), nil, true)
	if !c.healthSnapshot()[0].Healthy {
		t.Fatal("single failed maintenance cycle caused outage")
	}
	c.testLatency(context.Background(), nil, true)
	if c.healthSnapshot()[0].Healthy {
		t.Fatal("confirmed idle outage not detected")
	}
}

func TestLateTCPReplyDoesNotUndoSuccessfulUDPCanary(t *testing.T) {
	p, _ := testProfile(t)
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	c.pathLocked(addr).preferTCP = true
	c.adaptiveMu.Unlock()
	old := time.Now()
	c.recordTransportSuccess(addr, pathReply{network: "udp", udpCanary: true})
	c.recordTransportSuccess(addr, pathReply{network: "tcp", udpUnavailable: true, started: old})
	c.recordTransportSuccess(addr, pathReply{network: "tcp", udpUnavailable: true, fallback: true, started: old})
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if c.pathLocked(addr).preferTCP {
		t.Fatal("old TCP response undid authenticated UDP recovery")
	}
}

func TestScannerFindsTCPOnlyAuthenticatedResolver(t *testing.T) {
	p, server := testProfile(t)
	addr := dualDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		if _, udp := w.RemoteAddr().(*net.UDPAddr); udp {
			return
		}
		server.ServeDNS(w, m)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := scanOne(ctx, ScanOptions{Profile: p, TimeoutMS: 100}, addr, nil)
	if !result.Tunnel || !result.TXT || result.Transport != "tcp" || result.RTTMS >= 100 {
		t.Fatalf("TCP-only scan result: %+v", result)
	}
}

func TestCancelledTCPQueryKeepsItsPhysicalSerialCredit(t *testing.T) {
	p, server := testProfile(t)
	p.TimeoutMS = 500
	gate, entered := make(chan struct{}), make(chan struct{})
	var seen atomic.Int64
	var release sync.Once
	defer release.Do(func() { close(gate) })
	addr := dualDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		if seen.Add(1) == 1 {
			close(entered)
			<-gate
		}
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m, err := query(frame{Op: opProbe, Session: c.session, Nonce: freshNonce(), Data: []byte("serial")}, p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		err  error
		done <-chan struct{}
	}
	finished := make(chan result, 1)
	go func() { _, e, d := c.exchangeDNSTracked(ctx, m, addr, "tcp", opProbe); finished <- result{e, d} }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("TCP request not received")
	}
	cancel()
	var first result
	select {
	case first = <-finished:
		if first.err == nil {
			t.Fatal("cancel ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("TCP cancellation blocked")
	}
	nextCtx, nextCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer nextCancel()
	if _, err, _ = c.exchangeDNSTracked(nextCtx, m, addr, "tcp", opProbe); err == nil {
		t.Fatal("second query bypassed occupied TCP serial credit")
	}
	if seen.Load() != 1 {
		t.Fatal("cancelled query released wire credit too early")
	}
	release.Do(func() { close(gate) })
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("late response retained wire credit")
	}
	nextCtx, nextCancel = context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	if _, err, _ = c.exchangeDNSTracked(nextCtx, m, addr, "tcp", opProbe); err != nil {
		t.Fatalf("TCP serial credit leaked: %v", err)
	}
}
