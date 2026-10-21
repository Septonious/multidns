package core

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A saturated pool can have hundreds of admitted stream workers waiting for
// a physical lease. Measure actual flow/path release and fair-queue selection,
// including their mutex/map costs, without DNS or timing noise from sockets.
func BenchmarkAdmissionRelease(b *testing.B) {
	c := &Client{Profile: Profile{Window: 8}, ctx: context.Background(), dataSlots: make(chan struct{}, 512), readSlots: make(chan struct{}, 160)}
	c.health = map[string]*ResolverHealth{}
	for i := 0; i < 20; i++ {
		addr := fmt.Sprintf("192.0.2.%d:53", i+1)
		c.groups = append(c.groups, Group{Resolvers: []string{addr}})
		c.pathLocked(addr).limit = 64
	}
	for i := 0; i < 256; i++ {
		c.admissions = append(c.admissions, &admission{wake: make(chan struct{}, 1), stream: uint32(i%32 + 1), op: opWrite, bulk: true, started: time.Now().Add(-time.Second)})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.adaptiveMu.Lock()
		release := c.leaseFlowLocked(c.groups[i%20].Resolvers[0], opWrite, true, uint32(i%32+1))
		c.adaptiveMu.Unlock()
		release()
	}
	b.StopTimer()
	if c.leasedData != 0 || len(c.flowFlight) != 0 {
		b.Fatal("flow/path credits leaked")
	}
}
