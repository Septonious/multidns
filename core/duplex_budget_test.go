package core

import (
	"context"
	"fmt"
	"github.com/miekg/dns"
	"testing"
	"time"
)

// A download-expanded RX pool must not keep borrowing upload credits after a
// direction change. Shrinking eligibility must not release requests still on
// the wire, or permanently reduce the learned download budget.
func TestUploadDemandReclaimsBorrowedRXCreditsWithoutRevokingLeases(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	p.Groups = nil
	for i := 0; i < 20; i++ {
		p.Groups = append(p.Groups, Group{Resolvers: []string{fmt.Sprintf("127.0.0.1:%d", 16000+i)}})
	}
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.rxBudget.Store(320)
	c.adaptiveMu.Lock()
	for _, g := range c.groups {
		path := c.pathLocked(g.Resolvers[0])
		path.limit, path.rx.limit = 64, 32
	}
	var reads []func()
	for i := 0; i < 320; i++ {
		addr := c.groups[i%len(c.groups)].Resolvers[0]
		if !c.pathAvailableLocked(addr, opRead, true) {
			t.Fatalf("download pool stopped at %d", i)
		}
		reads = append(reads, c.leaseFlowLocked(addr, opRead, true, uint32(i+1)))
	}
	addr := c.groups[0].Resolvers[0]
	c.activeUploads.Store(1)
	if c.pathAvailableLocked(addr, opRead, true) {
		t.Fatal("expanded reads retained upload credits")
	}
	if c.leasedReads != 320 || c.leasedData != 320 {
		t.Fatal("demand change revoked physical credits")
	}
	if !c.pathAvailableLocked(addr, opWrite, true) {
		t.Fatal("upload cannot use remaining physical credits")
	}
	write := c.leaseFlowLocked(addr, opWrite, true, 400)
	c.adaptiveMu.Unlock()
	write()
	// A receive reply is still eligible below the demand limit; do not starve
	// destination responses or the upload's application-level acknowledgement.
	limit := c.readAdmissionBudget()
	for len(reads) >= limit {
		release := reads[len(reads)-1]
		reads = reads[:len(reads)-1]
		release()
	}
	c.adaptiveMu.Lock()
	if !c.pathAvailableLocked(addr, opRead, true) {
		t.Fatal("receive progress starved during upload")
	}
	c.adaptiveMu.Unlock()
	c.activeUploads.Store(0)
	if c.readAdmissionBudget() != 320 || c.readBudget() != 320 {
		t.Fatal("download borrowing did not resume")
	}
	for _, release := range reads {
		release()
	}
	c.adaptiveMu.Lock()
	defer c.adaptiveMu.Unlock()
	if c.leasedReads != 0 || c.leasedData != 0 || len(c.flowFlight) != 0 {
		t.Fatal("direction change leaked physical/flow credits")
	}
}

func TestAdmissionResumesAfterCooldownWithoutPacketCompletions(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.adaptiveMu.Lock()
	until := time.Now().Add(60 * time.Millisecond)
	for _, addr := range p.ResolverPool() {
		c.pathLocked(addr).retryAt = until
	}
	c.adaptiveMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, release, err := c.acquireAnyPath(ctx, nil, opWrite, true, 1)
	if err != nil {
		t.Fatal("cooldown expired but no request could resume:", err)
	}
	defer release()
	if time.Now().Before(until) {
		t.Fatal("admitted before cooldown expired")
	}
}

func TestShortARQExpiryDoesNotDeclareAResponsiveResolverUnavailable(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.ResolverPool()[0]
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	c.adaptiveMu.Lock()
	path := c.pathLocked(addr)
	path.lastSuccess = time.Now().Add(-500 * time.Millisecond)
	path.failureSince = time.Now().Add(-400 * time.Millisecond)
	c.adaptiveMu.Unlock()
	for range 4 {
		c.recordStart(addr)
		c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
	}
	if h := c.healthSnapshot()[0]; !h.Healthy || h.HealthDowns != 0 {
		t.Fatal("short ARQ expiry became a reachability outage")
	}
	c.adaptiveMu.Lock()
	path.lastSuccess = time.Now().Add(-2 * time.Second)
	c.adaptiveMu.Unlock()
	c.recordStart(addr)
	c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
	if h := c.healthSnapshot()[0]; h.Healthy || h.HealthDowns != 1 {
		t.Fatal("confirmed silence did not remove resolver")
	}
}

// A missing prefix must have a bounded repair opportunity even while later
// unique chunks are queued. Only the head is allowed to use foreground reserve.
func TestUploadHeadRepairEscapesQueuedBulk(t *testing.T) {
	for _, head := range []bool{false, true} {
		t.Run(fmt.Sprint("head-", head), func(t *testing.T) {
			p, _ := testProfile(t)
			p.Adaptive, p.TimeoutMS = true, 200
			primary := testDNS(t, dns.HandlerFunc(func(dns.ResponseWriter, *dns.Msg) {}))
			backup := testDNS(t, dns.HandlerFunc(func(w dns.ResponseWriter, m *dns.Msg) {
				f, _ := extract(m, p.Domain, p.Key)
				f.Data = []byte{statusOK}
				w.WriteMsg(response(m, f, p.Key))
			}))
			p.Groups = []Group{{Resolvers: []string{primary, backup}}}
			c, err := NewClient(p, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.rxBudget.Store(1)
			c.adaptiveMu.Lock()
			c.health[primary] = &ResolverHealth{Healthy: true, RTTMS: 1, Endpoint: primary}
			c.health[backup] = &ResolverHealth{Healthy: true, RTTMS: 100, Endpoint: backup}
			read := c.leaseFlowLocked(primary, opRead, true, 99)
			c.admissions = []*admission{{wake: make(chan struct{}, 1), stream: 99, op: opRead, bulk: true, started: time.Now()}}
			c.adaptiveMu.Unlock()
			defer read()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = c.race(ctx, frame{Op: opWrite, Stream: 1, Bulk: true, Data: []byte("repair"), uploadHead: func() bool { return head }}, 0)
			if head && err != nil {
				t.Fatal("head repair stayed behind bulk:", err)
			}
			if !head && err == nil {
				t.Fatal("non-head bulk bypassed unique queued work")
			}
			c.adaptiveMu.Lock()
			c.admissions = nil
			c.adaptiveMu.Unlock()
		})
	}
}
