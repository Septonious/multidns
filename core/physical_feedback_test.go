package core

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestAbandonedReadLossBacksOffWithoutDeclaringResolverOffline(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive, p.TimeoutMS = true, 100
	addr := testDNS(t, dns.HandlerFunc(func(dns.ResponseWriter, *dns.Msg) {}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	c.adaptiveMu.Lock()
	c.pathLocked(addr).limit, c.pathLocked(addr).rx.limit = 32, 24
	c.adaptiveMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	m, _ := query(frame{Op: opRead, Session: c.session, Nonce: freshNonce()}, p)
	c.recordStart(addr)
	_, _, done := c.exchangeDNSTracked(ctx, m, addr, "udp", opRead)
	c.recordEnd(addr, 10*time.Millisecond, 0, opRead, ctx.Err(), true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("abandoned physical request never drained")
	}
	until := time.Now().Add(time.Second)
	for {
		h := c.healthSnapshot()[0]
		if h.AbandonedTimeouts == 1 {
			if !h.Healthy || h.HealthDowns != 0 || h.Failures != 0 || h.Timeouts != 0 || h.RXCapacity >= 24 {
				t.Fatalf("incorrect cancellation feedback: %+v", h)
			}
			break
		}
		if time.Now().After(until) {
			t.Fatal("physical timeout was ignored")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCancelledBlockedTCPFrameFinishesWithoutClosingSharedSocket(t *testing.T) {
	p, _ := testProfile(t)
	p.TimeoutMS = 500
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn, peer := net.Pipe()
	defer peer.Close()
	s := &resolverSocket{conn: conn, network: "tcp", pending: map[uint16]*pendingDNS{}, done: make(chan struct{}), write: make(chan struct{}, 1), serial: make(chan struct{}, 1)}
	c.sockets["tcp|fixture"] = s
	go s.readLoop()
	m, _ := query(frame{Op: opProbe, Session: c.session, Nonce: freshNonce()}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err, done := c.exchangeDNSTracked(ctx, m, "fixture", "tcp", opProbe)
	if err == nil {
		t.Fatal("blocked caller did not cancel")
	}
	select {
	case <-s.done:
		t.Fatal("caller cancellation closed shared socket")
	default:
	}
	select {
	case <-done:
		t.Fatal("unsent frame released physical credit early")
	default:
	}
	served := make(chan error, 1)
	go func() {
		wire := &dns.Conn{Conn: peer}
		for range 2 {
			request, e := wire.ReadMsg()
			if e != nil {
				served <- e
				return
			}
			f, e := extract(request, p.Domain, p.Key)
			if e != nil {
				served <- e
				return
			}
			f.Data = []byte{statusOK}
			if e = wire.WriteMsg(response(request, f, p.Key)); e != nil {
				served <- e
				return
			}
		}
		served <- nil
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("physical frame did not drain")
	}
	next, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err, _ = c.exchangeDNSTracked(next, m, "fixture", "tcp", opProbe); err != nil {
		t.Fatal(err)
	}
	if err = <-served; err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
		t.Fatal("healthy TCP socket was not reusable")
	default:
	}
}

func TestSlowFailedProbeCannotEraseNewerAuthenticatedProgress(t *testing.T) {
	p, _ := testProfile(t)
	p.TimeoutMS = 700
	entered := make(chan struct{})
	var once sync.Once
	addr := dualDNS(t, dns.HandlerFunc(func(dns.ResponseWriter, *dns.Msg) { once.Do(func() { close(entered) }) }))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	done := make(chan struct{})
	go func() { c.TestLatency(context.Background(), nil); close(done) }()
	<-entered
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("probe did not finish")
	}
	h := c.healthSnapshot()[0]
	if !h.Healthy || h.HealthDowns != 0 {
		t.Fatalf("old slow probe erased progress: %+v", h)
	}
}

func TestLateAuthenticatedReplyPreservesReachabilityWithoutCountingGoodput(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 500
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		time.Sleep(60 * time.Millisecond)
		f, e := extract(m, p.Domain, p.Key)
		if e != nil {
			return
		}
		f.Data = append([]byte{statusOK}, []byte("late payload")...)
		w.WriteMsg(response(m, f, p.Key))
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 0, opProbe, nil, false)
	m, _ := query(frame{Op: opRead, Session: c.session, Stream: 1, Nonce: freshNonce()}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	c.recordStart(addr)
	_, _, done := c.exchangeDNSTracked(ctx, m, addr, "udp", opRead)
	c.recordEnd(addr, 10*time.Millisecond, 0, opRead, ctx.Err(), true)
	<-done
	until := time.Now().Add(time.Second)
	for {
		h := c.healthSnapshot()[0]
		if h.LateReplies == 1 {
			if !h.Healthy || h.Failures != 0 || h.HealthDowns != 0 || c.Stats().Downloaded != 0 || c.Stats().LateReplies != 1 {
				t.Fatalf("late success misclassified: %+v", h)
			}
			break
		}
		if time.Now().After(until) {
			t.Fatal("valid late response was ignored")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInvalidOrPreOutageLateReplyCannotRestoreHealth(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 0, opProbe, nil, false)
	f := frame{Op: opRead, Session: c.session, Stream: 1, Nonce: freshNonce()}
	m, _ := query(f, p)
	f.Data = []byte{statusOK, 1}
	started := time.Now().Add(-40 * time.Millisecond)
	c.adaptiveMu.Lock()
	c.markPathDownLocked(addr, time.Now())
	c.adaptiveMu.Unlock()
	c.recordLateDNSReply(addr, opRead, started, m, response(m, f, "invalid-signing-key"))
	if c.Stats().LateReplies != 0 {
		t.Fatal("invalid late MAC counted as progress")
	}
	c.recordLateDNSReply(addr, opRead, started, m, response(m, f, p.Key))
	if c.healthSnapshot()[0].Healthy {
		t.Fatal("pre-outage late success reopened circuit")
	}
}
