package core

import (
	"context"
	"testing"
	"time"
)

func TestSuccessfulLongPollDoesNotReduceCapacity(t *testing.T) {
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
	c.updateCapacityLocked(addr, 100, opWrite, nil, 40*time.Millisecond)
	for range 12 {
		c.pathLocked(addr).lastCut = time.Time{}
		// A successful download can include up to 80 ms of server waiting.
		c.updateCapacityLocked(addr, 100, opRead, nil, 110*time.Millisecond)
	}
	if got := c.pathLocked(addr).limit; got < 8 {
		t.Fatalf("valid long-polls collapsed capacity from 8 to %d", got)
	}
}
func TestSharedQueuedIdlePollRestoresCollapsedCapacity(t *testing.T) {
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
	path.limit = 2
	path.lastCut = time.Now().Add(-3 * time.Second)
	c.admissions = append(c.admissions, &admission{bulk: true, op: opRead, started: time.Now()})
	c.updateCapacityLocked(addr, 0, opRead, nil, 120*time.Millisecond)
	if path.limit <= 2 {
		t.Fatal("shared backlog was ignored; successful idle reply could not restore capacity")
	}
}
func TestAgedBulkAdmissionIsNotBlockedByFreshInteractivePolls(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.Groups = p.Groups[:1]
	p.Groups[0].Resolvers = p.Groups[0].Resolvers[:1]
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	c.pathLocked(addr).limit = 4
	leases := []func(){}
	for range 4 {
		leases = append(leases, c.leasePathLocked(addr, opWrite, false))
	}
	c.adaptiveMu.Unlock()
	defer func() {
		for _, release := range leases {
			release()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, release, err := c.acquireAnyPath(ctx, []string{addr}, opRead, true)
		if release != nil {
			release()
		}
		result <- err
	}()
	until := time.Now().Add(100 * time.Millisecond)
	for {
		c.adaptiveMu.Lock()
		if len(c.admissions) > 0 {
			c.admissions[0].started = time.Now().Add(-time.Second)
			break
		}
		c.adaptiveMu.Unlock()
		if time.Now().After(until) {
			t.Fatal("bulk work did not enqueue")
		}
		time.Sleep(time.Millisecond)
	}
	fresh := &admission{op: opRead, started: time.Now()}
	c.admissions = append(c.admissions, fresh)
	c.adaptiveMu.Unlock()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				c.adaptiveMu.Lock()
				fresh.started = time.Now()
				c.adaptiveMu.Unlock()
			}
		}
	}()
	leases[0]()
	leases = leases[1:]
	if err := <-result; err != nil {
		t.Fatalf("aged bulk starved behind continuously fresh interactive work: %v", err)
	}
}

func TestWriteDelayControllerIgnoresJitterButStillRespondsToPersistentQueue(t *testing.T) {
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
	for i := 0; i < 64; i++ {
		sample := []time.Duration{35, 55, 40, 70}[i%4] * time.Millisecond
		c.updateCapacityLocked(addr, 100, opWrite, nil, sample)
	}
	path := c.pathLocked(addr)
	if path.delayCuts != 0 {
		t.Fatal("normal write jitter was treated as sustained congestion")
	}
	reduced := false
	for range 64 {
		previous, cuts := path.limit, path.delayCuts
		c.updateCapacityLocked(addr, 100, opWrite, nil, 200*time.Millisecond)
		if path.delayCuts > cuts && path.limit < previous {
			reduced = true
		}
	}
	if !reduced {
		t.Fatal("persistent upload delay did not produce an actual capacity reduction")
	}

}

func TestBlockedOlderReadDoesNotPreventWriteAdmission(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.Groups[0].Resolvers = p.Groups[0].Resolvers[:1]
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	c.pathLocked(addr).limit = 2
	releaseRead := c.leasePathLocked(addr, opRead, false)
	older := &admission{op: opRead, started: time.Now().Add(-time.Second)}
	writer := &admission{op: opWrite, bulk: true, started: time.Now()}
	c.admissions = append(c.admissions, older, writer)
	chosen := c.nextAdmissionLocked()
	c.adaptiveMu.Unlock()
	releaseRead()
	if chosen != writer {
		t.Fatal("unavailable older RX operation blocked the free upload credit")
	}
}

func TestRepliesStartedBeforeCutCannotImmediatelyRegrowCapacity(t *testing.T) {
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
	path.limit = 4
	path.lastCut = time.Now()
	for range 32 {
		c.updateCapacityLocked(addr, 100, opRead, nil, 80*time.Millisecond)
	}
	if path.limit != 4 {
		t.Fatal("old in-flight replies immediately undid the congestion cut")
	}
}
