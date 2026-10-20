package core

import (
	"context"
	"fmt"
	"github.com/miekg/dns"
	"testing"
	"time"
)

func TestSparseLossDoesNotHalveAnOtherwiseFastPath(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	path := c.pathLocked(addr)
	path.limit = 32
	path.txRTT = 45
	for range 128 {
		path.observeOutcome(false)
	}
	c.updateCapacityLocked(addr, 0, opWrite, context.DeadlineExceeded, 300*time.Millisecond)
	if path.limit < 28 {
		t.Fatalf("isolated loss collapsed useful capacity: %d", path.limit)
	}
	for range 64 {
		path.observeOutcome(true)
	}
	path.lastCut = time.Now().Add(-time.Second)
	before := path.limit
	c.updateCapacityLocked(addr, 0, opWrite, context.DeadlineExceeded, 300*time.Millisecond)
	if path.limit > before/2 {
		t.Fatal("sustained loss did not cause strong backoff")
	}
}

func TestLoadedRTTGuardOverridesInflatedVariance(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	path := c.pathLocked(addr)
	path.limit = 32
	path.minRTT = 40
	path.baselineAt = time.Now()
	path.txRTT = 300
	path.txVariance = 200
	c.updateCapacityLocked(addr, 100, opWrite, nil, 300*time.Millisecond)
	if path.limit > 16 || path.budgetCuts != 1 {
		t.Fatal("variance masked a breach of the loaded RTT budget")
	}
}

func TestUploadUsesFullNegotiatedWindowWithinPoolBudget(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.adaptiveMu.Lock()
	c.groups = nil
	for i := 0; i < 8; i++ {
		addr := fmt.Sprintf("127.0.0.1:%d", 9900+i)
		c.groups = append(c.groups, Group{Resolvers: []string{addr}})
		c.pathLocked(addr).limit = 64
	}
	c.adaptiveMu.Unlock()
	s := &Conn{client: c, writeLimit: 256}
	s.traffic.Store(1 << 20)
	if w := s.window(opWrite); w < 200 || w > 256 {
		t.Fatalf("negotiated upload budget not usable: %d", w)
	}
	s.writeLimit = 64
	if w := s.window(opWrite); w > 64 {
		t.Fatalf("legacy reorder budget exceeded: %d", w)
	}
}

func TestPacingCancellationDoesNotLeakWireCredits(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.adaptiveMu.Lock()
	for _, addr := range p.ResolverPool() {
		path := c.pathLocked(addr)
		path.nextSend = time.Now().Add(time.Second)
		path.txRTT = 100
	}
	c.adaptiveMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.race(ctx, frame{Op: opWrite, Stream: 1, Bulk: true, Data: []byte("cancel")}, 0); err == nil {
		t.Fatal("paced write ignored cancellation")
	}
	until := time.Now().Add(time.Second)
	for {
		c.adaptiveMu.Lock()
		flight := 0
		for _, p := range c.paths {
			flight += p.flight
		}
		flows := len(c.flowFlight)
		c.adaptiveMu.Unlock()
		if flight == 0 && flows == 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("pacing leaked capacity: %d wire / %d flows", flight, flows)
		}
		time.Sleep(time.Millisecond)
	}
	if c.Stats().Queries != 0 {
		t.Fatal("canceled paced write was transmitted")
	}
}

func TestUploadRTOUsesWriteACKsInsteadOfReadWait(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 1500
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.adaptiveMu.Lock()
	c.health[addr].RTTMS = 140
	c.health[addr].RTTVarianceMS = 30
	path := c.pathLocked(addr)
	path.txRTT = 40
	path.txChecked = time.Now()
	path.txVariance = 2
	c.adaptiveMu.Unlock()
	if got := c.exchangeTimeout(addr, opWrite); got != 160*time.Millisecond {
		t.Fatalf("read wait inflated upload recovery: %v", got)
	}
	c.adaptiveMu.Lock()
	path.txVariance = 80
	c.adaptiveMu.Unlock()
	if got := c.exchangeTimeout(addr, opWrite); got < 400*time.Millisecond {
		t.Fatalf("write jitter allowance lost: %v", got)
	}
	c.adaptiveMu.Lock()
	c.health[addr].Healthy = true
	path.lastFailure = time.Now()
	path.txChecked = time.Now().Add(-time.Second)
	c.adaptiveMu.Unlock()
	if got := c.exchangeTimeout(addr, opWrite); got < 300*time.Millisecond {
		t.Fatalf("recovery reused a stale pre-failure write clock: %v", got)
	}
	c.adaptiveMu.Lock()
	c.health[addr].Healthy = false
	c.adaptiveMu.Unlock()
	if got := c.exchangeTimeout(addr, opWrite); got != 1500*time.Millisecond {
		t.Fatalf("unknown/failing path did not keep configured timeout: %v", got)
	}
}

func TestUDPWriteUsesDirectionalTimeoutOnTheWire(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 1500
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.recordStart(addr)
	c.adaptiveMu.Lock()
	c.health[addr].RTTMS = 500
	c.health[addr].RTTVarianceMS = 1
	path := c.pathLocked(addr)
	path.txRTT = 40
	path.txChecked = time.Now()
	path.txVariance = 2
	c.adaptiveMu.Unlock()
	message, err := query(frame{Op: opWrite, Session: c.session, Stream: 1, Data: []byte("lost")}, p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err, done := c.exchangeDNSTracked(ctx, message, addr, "udp", opWrite)
	if err == nil || ctx.Err() != nil {
		t.Fatalf("UDP write used read timeout instead of its 160ms ACK clock: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("wire credit was retained past the directional timeout")
	}
}

func TestSparseLossKeepsFreshUploadClockButNoProgressInvalidatesIt(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	path := c.pathLocked(addr)
	path.txRTT = 40
	path.txChecked = time.Now()
	c.updateCapacityLocked(addr, 0, opRead, context.DeadlineExceeded, 200*time.Millisecond)
	c.updateCapacityLocked(addr, 0, opWrite, context.DeadlineExceeded, 160*time.Millisecond)
	if !path.lastFailure.IsZero() {
		t.Fatal("isolated loss discarded fresh upload progress")
	}
	path.txChecked = time.Now().Add(-time.Second)
	c.updateCapacityLocked(addr, 0, opWrite, context.DeadlineExceeded, 160*time.Millisecond)
	if !path.lastFailure.After(path.txChecked) {
		t.Fatal("stalled upload retained a stale ACK clock")
	}
}

func TestClusteredLossEscalatesMildCutWithoutWaitingForCooldown(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	path := c.pathLocked(addr)
	path.limit = 32
	path.txRTT = 40
	path.txChecked = time.Now()
	for range 128 {
		path.observeOutcome(false)
	}
	c.updateCapacityLocked(addr, 0, opWrite, context.DeadlineExceeded, 160*time.Millisecond)
	if path.limit < 28 {
		t.Fatal("first sparse loss did not take a mild cut")
	}
	c.updateCapacityLocked(addr, 0, opWrite, context.DeadlineExceeded, 160*time.Millisecond)
	c.updateCapacityLocked(addr, 0, opWrite, context.DeadlineExceeded, 160*time.Millisecond)
	if path.limit > 14 || path.lastCutMild {
		t.Fatal("loss burst waited through the mild-loss cooldown")
	}
}
