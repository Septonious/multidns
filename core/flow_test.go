package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdmissionSharesCreditsBetweenFlows(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	c.pathLocked(addr).limit = 8
	release := c.leaseFlowLocked(addr, opWrite, true, 1)
	old := &admission{stream: 1, op: opWrite, bulk: true, started: time.Now().Add(-time.Second)}
	fresh := &admission{stream: 2, op: opWrite, started: time.Now()}
	c.admissions = []*admission{old, fresh}
	if c.nextAdmissionLocked() != fresh {
		t.Error("fresh flow waited behind an already serviced bulk flow")
	}
	c.adaptiveMu.Unlock()
	release()
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if len(c.flowFlight) != 0 {
		t.Error("flow lease leaked")
	}
	if c.nextAdmissionLocked() != old {
		t.Error("aged bulk did not regain its fair turn after wire release")
	}
	c.admissions = nil
}

func TestWindowsSharePoolInsteadOfMultiplyingIt(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.adaptiveMu.Lock()
	for _, a := range p.ResolverPool() {
		c.pathLocked(a).limit = 8
	}
	c.adaptiveMu.Unlock()
	c.activeUploads.Store(8)
	sumTX, sumRX := 0, 0
	for i := uint32(1); i <= 8; i++ {
		c.markReader(i)
	}
	for i := uint32(1); i <= 8; i++ {
		s := &Conn{client: c, id: i, writeLimit: 256}
		s.traffic.Store(1 << 20)
		sumTX += s.window(opWrite)
		sumRX += s.window(opRead)
	}
	if sumTX > c.dataCapacity(opWrite)+24 || sumRX > c.dataCapacity(opRead)+24 {
		t.Fatalf("windows multiply pool budget: tx=%d rx=%d", sumTX, sumRX)
	}
	c.forgetReader(1)
	if c.readerCount() != 7 {
		t.Fatal("closed reader retained pool share")
	}
	c.adaptiveMu.Lock()
	for id := range c.activeReaders {
		c.activeReaders[id] = time.Now().Add(-2 * time.Second)
	}
	c.adaptiveMu.Unlock()
	if c.readerCount() != 1 {
		t.Fatal("idle readers retained bulk shares")
	}
}

func TestFailedDownloadFallbackDoesNotDisableUploadOrReviveOnProbe(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.recordTCPFallback(addr, errors.New("TCP refused"), false)
	c.recordEnd(addr, 40*time.Millisecond, 0, opProbe, nil, false)
	c.adaptiveMu.Lock()
	if c.pathAvailableLocked(addr, opRead, true) {
		t.Error("tiny probe revived a broken download path")
	}
	if !c.pathAvailableLocked(addr, opWrite, true) {
		t.Error("download failure disabled working upload")
	}
	c.pathLocked(addr).readRetryAt = time.Now().Add(-time.Millisecond)
	if !c.pathAvailableLocked(addr, opRead, true) {
		t.Error("cooldown did not allow a recovery read")
	}
	c.adaptiveMu.Unlock()
	c.recordTCPFallback(addr, nil, false)
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if c.pathLocked(addr).readFailures != 0 || !c.pathLocked(addr).readRetryAt.IsZero() {
		t.Error("successful large response did not clear cooldown")
	}
	if c.health[addr].TCPFallbackFailures != 1 {
		t.Error("fallback failures lost")
	}
}

func TestControlEscapesDeadIndependentGroup(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first, second := p.Groups[0].Resolvers[0], p.Groups[0].Resolvers[1]
	c.adaptiveMu.Lock()
	c.groups = []Group{{Resolvers: []string{first}}, {Resolvers: []string{second}}}
	c.health[first] = &ResolverHealth{Endpoint: first}
	c.health[second] = &ResolverHealth{Endpoint: second}
	c.health[first].Healthy = false
	c.health[second].Healthy = true
	c.health[second].RTTMS = 40
	c.adaptiveMu.Unlock()
	targets, _ := c.controlTargets([]string{first})
	if len(targets) != 1 || targets[0] != second {
		t.Fatalf("control stayed pinned to dead group: %v", targets)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.rpc(ctx, frame{Op: opProbe}, 0); err != nil {
		t.Fatalf("global control recovery: %v", err)
	}
}

func TestPoolWindowRespectsDirectionalWorkerLimits(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.ResolverSettings = map[string]ResolverSettings{}
	for _, addr := range p.ResolverPool() {
		p.ResolverSettings[addr] = ResolverSettings{TXWorkers: 1, RXWorkers: 1}
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.dataCapacity(opWrite) != 2 || c.dataCapacity(opRead) != 2 {
		t.Fatal("window budget ignored configured directional limits")
	}
}
