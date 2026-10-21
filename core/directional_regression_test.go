package core

import (
	"context"
	"testing"
	"time"
)

func TestDownloadLossKeepsWorkingUploadCapacity(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	c.pathLocked(addr).limit = 48
	c.pathLocked(addr).rx.limit = 24
	c.adaptiveMu.Unlock()
	for range 12 {
		c.recordStart(addr)
		c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
		c.recordStart(addr)
		c.recordEnd(addr, 350*time.Millisecond, 0, opRead, context.DeadlineExceeded, false)
	}
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	path := c.pathLocked(addr)
	if path.limit < 48 || !c.pathAvailableLocked(addr, opWrite, true) {
		t.Fatalf("download loss collapsed reliable upload: limit=%d retry=%v", path.limit, path.retryAt)
	}
	if c.health[addr].HealthDowns != 0 {
		t.Fatal("working upload marked unavailable")
	}
	if path.rx.limit >= 24 || path.rx.lossCuts == 0 {
		t.Fatal("download overload was not controlled")
	}
}

func TestReadDelayGuardCannotBeHiddenByJitter(t *testing.T) {
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
	path.limit = 64
	path.rx = readControl{limit: 32, rtt: 400, minimum: 40, variance: 200, baselineAt: time.Now()}
	for range 4 {
		c.updateCapacityLocked(addr, 768, opRead, nil, 400*time.Millisecond)
	}
	if path.rx.limit >= 32 || path.rx.delayCuts == 0 {
		t.Fatal("high variance masked loaded download latency")
	}
	if path.limit != 64 {
		t.Fatal("download delay unnecessarily removed upload capacity")
	}
}

func TestBurstOfExpiredPacketsIsNotAnInstantResolverOutage(t *testing.T) {
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
	c.adaptiveMu.Unlock()
	for range 12 {
		c.recordStart(addr)
		c.recordEnd(addr, 350*time.Millisecond, 0, opRead, context.DeadlineExceeded, false)
	}
	if !c.healthSnapshot()[0].Healthy {
		t.Fatal("one simultaneous expiry batch caused a false outage")
	}
	c.adaptiveMu.Lock()
	c.pathLocked(addr).failureSince = time.Now().Add(-time.Second)
	c.adaptiveMu.Unlock()
	c.recordStart(addr)
	c.recordEnd(addr, 350*time.Millisecond, 0, opRead, context.DeadlineExceeded, false)
	if c.healthSnapshot()[0].Healthy {
		t.Fatal("sustained no-progress failure was not removed")
	}
}
