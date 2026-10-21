package core

import (
	"context"
	"fmt"
	"github.com/miekg/dns"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsolatedLossDoesNotFlapReachableResolver(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	for range 8 {
		c.recordStart(addr)
		c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
		c.recordStart(addr)
		c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
		h := c.healthSnapshot()[0]
		if !h.Healthy {
			t.Fatalf("live resolver marked down after isolated loss: %+v", h)
		}
	}
	if h := c.healthSnapshot()[0]; h.Timeouts != 8 || h.Recoveries != 0 {
		t.Fatalf("incorrect health counters: %+v", h)
	}
}

func TestLargePoolUsesMoreThanEightPathBudget(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.Groups = nil
	for i := 0; i < 20; i++ {
		p.Groups = append(p.Groups, Group{Resolvers: []string{fmt.Sprintf("127.0.0.1:%d", 12000+i)}})
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.adaptiveMu.Lock()
	for _, g := range c.groups {
		c.pathLocked(g.Resolvers[0]).limit = 64
	}
	c.adaptiveMu.Unlock()
	if capacity := c.dataCapacity(opWrite); capacity < 480 {
		t.Fatalf("20 paths share only %d credits", capacity)
	}
}

// Three errors must actually open the circuit when all authenticated progress stops.
func TestQuietFailedPathIsRemovedAndAuthenticatedRecoveryRestoresIt(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	c.adaptiveMu.Lock()
	c.pathLocked(addr).lastSuccess = time.Now().Add(-time.Second)
	c.pathLocked(addr).failureSince = time.Now().Add(-time.Second)
	c.adaptiveMu.Unlock()
	for range 3 {
		c.recordStart(addr)
		c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
	}
	h := c.healthSnapshot()[0]
	if h.Healthy || h.HealthDowns != 1 {
		t.Fatalf("dead path retained: %+v", h)
	}
	c.adaptiveMu.Lock()
	available := c.pathAvailableLocked(addr, opWrite, true)
	c.adaptiveMu.Unlock()
	if available {
		t.Fatal("dead path still admits new work")
	}
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	h = c.healthSnapshot()[0]
	if !h.Healthy || h.Recoveries != 1 || h.Timeouts != 3 {
		t.Fatalf("recovery lost counters: %+v", h)
	}
}

func TestLostHealthProbesDoNotEraseConcurrentDataProgress(t *testing.T) {
	p, server := testProfile(t)
	p.Adaptive = true
	p.TimeoutMS = 100
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		f, _ := extract(m, p.Domain, p.Key)
		if f.Op == opProbe {
			return
		}
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	c.TestLatency(context.Background(), nil)
	h := c.healthSnapshot()[0]
	if !h.Healthy || h.HealthDowns != 0 {
		t.Fatalf("two lost probes overrode recent payload ACK: %+v", h)
	}
}

func TestGlobalReadCreditIsReservedBeforeChoosingPath(t *testing.T) {
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
	c.leasedReads = cap(c.readSlots)
	if c.pathAvailableLocked(addr, opRead, true) {
		t.Fatal("unsent read can occupy a resolver behind the global RX cap")
	}
	if !c.pathAvailableLocked(addr, opWrite, true) {
		t.Fatal("global RX cap incorrectly blocks upload")
	}
}

func TestBackgroundHealthUsesRecentDataWithoutProbingEveryBusyResolver(t *testing.T) {
	p, server := testProfile(t)
	p.Adaptive = true
	var probes atomic.Int64
	addr := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
		f, _ := extract(m, p.Domain, p.Key)
		if f.Op == opProbe {
			probes.Add(1)
		}
		server.ServeDNS(w, m)
	}))
	p.Groups = []Group{{Resolvers: []string{addr}}}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	c.testLatency(context.Background(), nil, true)
	if probes.Load() != 0 {
		t.Fatal("background probes duplicate recent authenticated payload progress")
	}
	c.TestLatency(context.Background(), nil)
	if probes.Load() != 2 {
		t.Fatal("explicit latency test skipped the resolver")
	}
}
