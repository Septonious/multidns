package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestLightSummaryCountsUniqueResolverStreamsWithoutDebugSerialization(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDiagnostics(false)
	addr := p.Groups[0].Resolvers[0]
	c.recordStart(addr)
	c.adaptiveMu.Lock()
	one := c.leaseFlowLocked(addr, opWrite, true, 1)
	read := c.leaseFlowLocked(addr, opRead, false, 1)
	two := c.leaseFlowLocked(addr, opWrite, true, 2)
	c.adaptiveMu.Unlock()
	check := func(want int) {
		t.Helper()
		var data struct {
			Resolvers []struct {
				Endpoint string `json:"endpoint"`
				Streams  int    `json:"streams"`
			}
			Debug bool `json:"debug_enabled"`
		}
		if err := json.Unmarshal([]byte(c.SummaryJSON()), &data); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range data.Resolvers {
			if row.Endpoint == addr {
				found = true
				if row.Streams != want {
					t.Fatalf("stream count %d want %d", row.Streams, want)
				}
			}
		}
		if !found {
			t.Fatal("resolver missing")
		}
		if data.Debug {
			t.Fatal("debug enabled by default")
		}
		var raw map[string]any
		json.Unmarshal([]byte(c.SummaryJSON()), &raw)
		if _, ok := raw["timeouts"]; ok {
			t.Fatal("light summary exported detailed counters")
		}
	}
	check(2)
	one()
	check(2)
	read()
	check(1)
	two()
	check(0)
	c.SetDiagnostics(true)
	if !c.DiagnosticsEnabled() {
		t.Fatal("diagnostic switch failed")
	}
}

func TestResolverCardStateDistinguishesUnknownLossAndConfirmedOutage(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	state := func() string {
		var data struct {
			Resolvers []resolverSummary `json:"resolvers"`
		}
		json.Unmarshal([]byte(c.SummaryJSON()), &data)
		for _, r := range data.Resolvers {
			if r.Endpoint == addr {
				return r.State
			}
		}
		return ""
	}
	if state() != "yellow" {
		t.Fatal("untested path was shown as reachable")
	}
	c.recordStart(addr)
	c.recordEnd(addr, 40*time.Millisecond, 110, opWrite, nil, false)
	if state() != "green" {
		t.Fatal("authenticated path is not green")
	}
	c.recordStart(addr)
	c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
	if state() != "yellow" {
		t.Fatal("isolated loss should be degraded, not offline")
	}
	c.adaptiveMu.Lock()
	c.pathLocked(addr).lastSuccess = time.Now().Add(-time.Second)
	c.pathLocked(addr).failureSince = time.Now().Add(-time.Second)
	c.adaptiveMu.Unlock()
	for range 3 {
		c.recordStart(addr)
		c.recordEnd(addr, 160*time.Millisecond, 0, opWrite, context.DeadlineExceeded, false)
	}
	if state() != "red" {
		t.Fatal("confirmed outage is not red")
	}
}

func TestYellowCardExplainsDirectionalIssueWithoutImposingStreamCap(t *testing.T) {
	p, _ := testProfile(t)
	p.Adaptive = true
	c, err := NewClient(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	addr := p.Groups[0].Resolvers[0]
	c.adaptiveMu.Lock()
	c.health[addr] = &ResolverHealth{Endpoint: addr, Healthy: true, RTTMS: 44}
	path := c.pathLocked(addr)
	path.limit, path.lastIssue = 64, time.Now()
	leases := []func(){}
	for i := range 12 {
		leases = append(leases, c.leaseFlowLocked(addr, opWrite, true, uint32(i+1)))
	}
	if !c.pathAvailableLocked(addr, opWrite, true) {
		t.Error("healthy low-latency path arbitrarily capped at 12 streams")
	}
	c.adaptiveMu.Unlock()
	defer func() {
		for _, release := range leases {
			release()
		}
	}()
	check := func(want string) {
		t.Helper()
		var data struct {
			Resolvers []resolverSummary `json:"resolvers"`
		}
		if err := json.Unmarshal([]byte(c.SummaryJSON()), &data); err != nil {
			t.Fatal(err)
		}
		for _, row := range data.Resolvers {
			if row.Endpoint == addr {
				if row.Status != want || row.State != "yellow" || row.Streams != 12 {
					t.Fatalf("unexpected resolver card: %+v, want %s", row, want)
				}
				return
			}
		}
		t.Fatal("resolver card missing")
	}
	check("Recent loss")
	c.recordTCPFallback(addr, context.DeadlineExceeded, false)
	check("Download limited")
	c.adaptiveMu.Lock()
	path.lastIssue, path.readRetryAt = time.Time{}, time.Time{}
	c.health[addr].RTTMS = c.writeRTTLimit() + 10
	c.adaptiveMu.Unlock()
	check("High latency")
}
